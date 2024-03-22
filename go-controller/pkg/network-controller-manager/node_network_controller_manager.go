package networkControllerManager

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	nad "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/network-attach-def-controller"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/node"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	kexec "k8s.io/utils/exec"
)

// nodeNetworkControllerManager structure is the object manages all controllers for all networks for ovnkube-node
type nodeNetworkControllerManager struct {
	name          string
	dpuName       string
	ovnNodeClient *util.OVNNodeClientset
	Kube          kube.Interface
	watchFactory  factory.NodeWatchFactory
	stopChan      chan struct{}
	recorder      record.EventRecorder
	hostType      string
	pfMACs        []string

	//defaultNodeNetworkController nad.BaseNetworkController

	// net-attach-def controller handle net-attach-def and create/delete secondary controllers
	// nil in dpu-host mode
	nadController *nad.NetAttachDefinitionController
}

// NewNetworkController create secondary node network controllers for the given NetInfo
func (ncm *nodeNetworkControllerManager) NewNetworkController(nInfo util.NetInfo) (nad.NetworkController, error) {
	if _, ok := nInfo.(*util.DefaultNetInfo); ok {
		return node.NewDefaultNodeNetworkController(ncm.newCommonNetworkControllerInfo(), nInfo)
	}

	topoType := nInfo.TopologyType()
	switch topoType {
	case ovntypes.Layer3Topology, ovntypes.Layer2Topology:
		return node.NewSecondaryNodeNetworkController(ncm.newCommonNetworkControllerInfo(), nInfo), nil
	case ovntypes.LocalnetTopology:
		return node.NewSecondaryLocalnetNodeNetworkController(ncm.newCommonNetworkControllerInfo(), nInfo), nil
	}
	return nil, fmt.Errorf("topology type %s not supported", topoType)
}

// CleanupDeletedNetworks is used to upgrade OVS interfaces's stale external-ids, and clean up all stale OVS interfaces.
// From the allControllers argument, we are able to get all NADs/networks at the time when ovnkube-node restarts,
// enable us updating the existing OVS interface's stale OVS external-ids.
func (ncm *nodeNetworkControllerManager) CleanupDeletedNetworks(allControllers []nad.NetworkController) error {
	if config.OvnKubeNode.Mode == ovntypes.NodeModeDPUHost {
		return nil
	}
	klog.V(5).Infof("Upgrade OVS interface's external-ids and delete stale ones")
	// Get all OVN-K8S OVS interfaces
	ovsArgs := []string{"external_ids:sandbox!=\"\""}
	ovsIntefaceToExternalIDMap, err := util.GetOVSInterfaceToExternalIDMapFiltered(ovsArgs)
	if err != nil {
		return err
	}
	if len(ovsIntefaceToExternalIDMap) == 0 {
		return nil
	}

	// list Pods and calculate the expected iface-ids.
	// Note: we do this after scanning ovs interfaces to avoid deleting ports of pods that where just scheduled
	// on the node.
	pods, err := ncm.watchFactory.GetPods("")
	if err != nil {
		return fmt.Errorf("failed to get all existing pods %v", err)
	}
	expectedPodUIDs := make(map[string]struct{})
	for _, pod := range pods {
		if pod.Spec.NodeName == ncm.name && !util.PodWantsHostNetwork(pod) {
			// Note: wf (WatchFactory) *usually* returns pods assigned to this node, however we dont rely on it
			// and add this check to filter out pods assigned to other nodes. (e.g when ovnkube master and node
			// share the same process)
			expectedPodUIDs[string(pod.UID)] = struct{}{}
		}
	}

	// assign an invalid network name for stale OVS interfaces
	invalidNetworkName := "invalid/network"
	nadToNetNameMap := map[string]string{}
	var netName, nadName string
	for hostIfaceName, extMap := range ovsIntefaceToExternalIDMap {
		podUID, ok := extMap["iface-id-ver"]
		if !ok {
			continue
		}
		if _, ok = expectedPodUIDs[podUID]; !ok {
			klog.Warningf("Found stale OVS Interface %s with iface-id-ver %s, deleting it", hostIfaceName, podUID)
			netName = invalidNetworkName
		} else {
			// Add both lagacy and new NAD external_ids to the ovs interfaces if either is missing
			nadName1, ok1 := extMap[ovntypes.LegacyNetworkExternalID]
			nadName2, ok2 := extMap[ovntypes.NADExternalID]
			if (!ok1 && !ok2) || (ok1 && ok2) {
				// it is either the OVS interface of the default network, or both external_ids exist, nothing to do
				continue
			}
			// OVS interface have either LegacyNetworkExternalID or NADExternalID
			if ok1 {
				nadName = nadName1
			} else {
				nadName = nadName2
			}
			netName = nadToNetNameMap[nadName]
			if netName == "" {
				// try to find the network name associated with this NAD
				for _, oc := range allControllers {
					if oc.HasNAD(nadName) {
						netName = oc.GetNetworkName()
						nadToNetNameMap[nadName] = netName
						break
					}
				}
				if netName == "" {
					netName = invalidNetworkName
					klog.Warningf("Found OVS interface %s of NAD %s which no longer exists, deleting it", hostIfaceName, nadName)
				}
			}
		}
		if netName == invalidNetworkName {
			ovsArgs = []string{"--if-exists", "--with-iface", "del-port", hostIfaceName}
		} else {
			ovsArgs = []string{"--if-exists", "set", "interface", hostIfaceName}
			// Set other potential missing external-ids to the OVS port
			if _, ok = extMap["ovn_kube_mode"]; !ok {
				ovsArgs = append(ovsArgs, fmt.Sprintf("external_ids:ovn_kube_mode=%s", config.OvnKubeNode.Mode))
				if config.OvnKubeNode.Mode == ovntypes.NodeModeDPU {
					// in order for VFRep interfaces to participate in the healthcheck, add its netdev-name external-ids
					ovsArgs = append(ovsArgs, fmt.Sprintf("external_ids:netdev-name=%s", hostIfaceName))
				}
			}

			// do not remove the legacyNetworkExternalID yet so in case of upgrade failure, we can
			// still fallback to the old version.
			// "--", "--if-exists", "remove", "interface", hostIfaceName, "external_ids", ovntypes.LegacyNetworkExternalID)
			ovsArgs = append(ovsArgs, fmt.Sprintf("external_ids:%s=%s", ovntypes.NetworkExternalID, netName),
				fmt.Sprintf("external_ids:%s=%s", ovntypes.NADExternalID, nadName),
				fmt.Sprintf("external_ids:%s=%s", ovntypes.LegacyNetworkExternalID, nadName))
		}
		_, stderr, err := util.RunOVSVsctl(ovsArgs...)
		if err != nil {
			err = fmt.Errorf("failed to run OVS commands %s for OVS interfaces %s:, stderr: %q, error: %v", ovsArgs, hostIfaceName, stderr, err)
			return err
		}
	}

	return nil
}

// newCommonNetworkControllerInfo creates and returns the base node network controller info
func (ncm *nodeNetworkControllerManager) newCommonNetworkControllerInfo() *node.CommonNodeNetworkControllerInfo {
	return node.NewCommonNodeNetworkControllerInfo(ncm.ovnNodeClient, ncm.ovnNodeClient.AdminPolicyRouteClient, ncm.watchFactory, ncm.recorder, ncm.name, ncm.dpuName, ncm.hostType, ncm.pfMACs)
}

// NewNodeNetworkControllerManager creates a new OVN controller manager to manage all the controller for all networks
func NewNodeNetworkControllerManager(ovnClient *util.OVNClientset, wf factory.NodeWatchFactory, name string, dpuName string,
	eventRecorder record.EventRecorder) (*nodeNetworkControllerManager, error) {
	ncm := &nodeNetworkControllerManager{
		name:    name,
		dpuName: dpuName,
		ovnNodeClient: &util.OVNNodeClientset{
			KubeClient:             ovnClient.KubeClient,
			AdminPolicyRouteClient: ovnClient.AdminPolicyRouteClient,
			PortMirrorClient:       ovnClient.PortMirrorClient,
		},
		Kube:         &kube.Kube{KClient: ovnClient.KubeClient},
		watchFactory: wf,
		stopChan:     make(chan struct{}),
		recorder:     eventRecorder,
	}

	// need to configure OVS interfaces for Pods on secondary networks in the DPU mode
	var err error
	if config.OVNKubernetesFeature.EnableMultiNetwork {
		ncm.nadController, err = nad.NewNetAttachDefinitionController("node-network-controller-manager", ncm, ovnClient.NetworkAttchDefClient, eventRecorder)
	}
	if err != nil {
		return nil, err
	}
	return ncm, nil
}

func (ncm *nodeNetworkControllerManager) getNodeHostType() error {
	if config.OvnKubeNode.Mode == ovntypes.NodeModeDPU && config.OvnKubeNode.RepresentorMeteringNodes != "" {
		node, err := ncm.watchFactory.GetNode(ncm.name)
		if err != nil {
			return fmt.Errorf("error retrieving node %s: %v", ncm.name, err)
		}

		if hostType, exists := node.Labels[config.OvnKubeNode.RepresentorMeteringNodes]; exists {
			ncm.hostType = hostType
		}
	}
	return nil
}

// // initDefaultNodeNetworkController creates the controller for default network
//
//	func (ncm *nodeNetworkControllerManager) initDefaultNodeNetworkController() error {
//		defaultNodeNetworkController, err := node.NewDefaultNodeNetworkController(ncm.newCommonNetworkControllerInfo())
//		if err != nil {
//			return err
//		}
//		// Make sure we only set defaultNodeNetworkController in case of no error,
//		// otherwise we would initialize the interface with a nil implementation
//		// which is not the same as nil interface.
//		ncm.defaultNodeNetworkController = defaultNodeNetworkController
//		return nil
//	}
//
// Start the node network controller manager
func (ncm *nodeNetworkControllerManager) Start(ctx context.Context) (err error) {
	klog.Infof("Starting the node network controller manager, Mode: %s", config.OvnKubeNode.Mode)

	if config.OvnKubeNode.Mode == ovntypes.NodeModeDPU {
		pfMACs, err := util.GetAllDPUHostPFMACAddress()
		if err != nil {
			// TODO(gmoodalbail): should this be fatal error
			return fmt.Errorf("failed to get the MAC address for all the PFs on the host: %v", err)
		}
		ncm.pfMACs = pfMACs
	}

	// Initialize OVS exec runner; find OVS binaries that the CNI code uses.
	// Must happen before calling any OVS exec from pkg/cni to prevent races.
	// Not required in DPUHost mode as OVS is not present there.
	if err = cni.SetExec(kexec.New()); err != nil {
		return err
	}

	err = ncm.watchFactory.Start()
	if err != nil {
		return err
	}

	if err = ncm.getNodeHostType(); err != nil {
		return err
	}

	// make sure we clean up after ourselves on failure
	defer func() {
		if err != nil {
			ncm.Stop()
		}
	}()

	if config.OvnKubeNode.Mode != ovntypes.NodeModeDPUHost {
		// start health check to ensure there are no stale OVS internal ports
		go wait.Until(func() {
			// SDN-1388 During host reboot, VF representors will be removed and added back on DPU side.
			// Do not delete VF representors from br-int accidentally in that temporary state.
			if config.OvnKubeNode.Mode == ovntypes.NodeModeFull {
				checkForStaleOVSInternalPorts()
			}
			ncm.checkForStaleOVSRepresentorInterfaces()
		}, time.Minute, ncm.stopChan)
	}

	//err = ncm.initDefaultNodeNetworkController()
	//if err != nil {
	//	return fmt.Errorf("failed to init default node network controller: %v", err)
	//}
	//err = ncm.defaultNodeNetworkController.Start(ctx)
	//if err != nil {
	//	return fmt.Errorf("failed to start default node network controller: %v", err)
	//}
	//
	// nadController is nil if multi-network is disabled
	if ncm.nadController != nil {
		err = ncm.nadController.Start()
	}

	return err
}

// Stop gracefully stops all managed controllers
func (ncm *nodeNetworkControllerManager) Stop() {
	// stop stale ovs ports cleanup
	close(ncm.stopChan)

	//if ncm.defaultNodeNetworkController != nil {
	//	ncm.defaultNodeNetworkController.Stop()
	//}
	//
	// stop the NAD controller
	if ncm.nadController != nil {
		ncm.nadController.Stop()
	}
}

// checkForStaleOVSRepresentorInterfaces checks for stale OVS ports backed by Repreresentor interfaces,
// derive iface-id from pod name and namespace then remove any interfaces assoicated with a sandbox that are
// not scheduled to the node.
func (ncm *nodeNetworkControllerManager) checkForStaleOVSRepresentorInterfaces() {
	// Get all representor interfaces. these are OVS interfaces that have their external_ids:sandbox, netdev-name
	// and ovn_kube_mode set.
	ovsArgs := []string{"external_ids:sandbox!=\"\"", "external_ids:netdev-name!=\"\"",
		fmt.Sprintf("external_ids:ovn_kube_mode=%s", config.OvnKubeNode.Mode)}
	ovsIntefaceToExternalIDMap, err := util.GetOVSInterfaceToExternalIDMapFiltered(ovsArgs)
	if err != nil {
		klog.Errorf(err.Error())
		return
	}

	if len(ovsIntefaceToExternalIDMap) == 0 {
		return
	}

	// list Pods and calculate the expected iface-ids.
	// Note: we do this after scanning ovs interfaces to avoid deleting ports of pods that where just scheduled
	// on the node.
	pods, err := ncm.watchFactory.GetPods("")
	if err != nil {
		klog.Errorf("Failed to list pods. %v", err)
		return
	}
	expectedPodUIDs := make(map[string]struct{})
	for _, pod := range pods {
		if pod.Spec.NodeName == ncm.name && !util.PodWantsHostNetwork(pod) {
			// Note: wf (WatchFactory) *usually* returns pods assigned to this node, however we dont rely on it
			// and add this check to filter out pods assigned to other nodes. (e.g when ovnkube master and node
			// share the same process)
			expectedPodUIDs[string(pod.UID)] = struct{}{}
		}
	}

	// Remove any stale representor ports
	for hostIfaceName, extMap := range ovsIntefaceToExternalIDMap {
		podUID, ok := extMap["iface-id-ver"]
		if !ok {
			continue
		}
		if _, ok = expectedPodUIDs[podUID]; !ok {
			klog.Warningf("Found stale OVS Interface %s with iface-id-ver %s, deleting it", hostIfaceName, podUID)
			_, stderr, err := util.RunOVSVsctl("--if-exists", "--with-iface", "del-port", hostIfaceName)
			if err != nil {
				klog.Errorf("Failed to delete stale interface %s, stderr: %q, error: %v",
					hostIfaceName, stderr, err)
			}
		}
	}
}

// checkForStaleOVSInternalPorts checks for OVS internal ports without any ofport assigned,
// they are stale ports that must be deleted
func checkForStaleOVSInternalPorts() {
	// Track how long scrubbing stale interfaces takes
	start := time.Now()
	defer func() {
		klog.V(5).Infof("CheckForStaleOVSInternalPorts took %v", time.Since(start))
	}()

	stdout, _, err := util.RunOVSVsctl("--data=bare", "--no-headings", "--columns=name", "find",
		"interface", "ofport=-1")
	if err != nil {
		klog.Errorf("Failed to list OVS interfaces with ofport set to -1")
		return
	}
	if len(stdout) == 0 {
		return
	}
	// Batched command length overload shouldn't be a worry here since the number
	// of interfaces per node should never be very large
	// TODO: change this to use libovsdb
	staleInterfaceArgs := []string{}
	values := strings.Split(stdout, "\n\n")
	for _, val := range values {
		if val == ovntypes.K8sMgmtIntfName || val == ovntypes.K8sMgmtIntfName+"_0" {
			klog.Errorf("Management port %s is missing. Perhaps the host rebooted "+
				"or SR-IOV VFs were disabled on the host.", val)
			continue
		}
		klog.Warningf("Found stale interface %s, so queuing it to be deleted", val)
		if len(staleInterfaceArgs) > 0 {
			staleInterfaceArgs = append(staleInterfaceArgs, "--")
		}

		staleInterfaceArgs = append(staleInterfaceArgs, "--if-exists", "--with-iface", "del-port", val)
	}

	// Don't call ovs if all interfaces were skipped in the loop above
	if len(staleInterfaceArgs) == 0 {
		return
	}

	_, stderr, err := util.RunOVSVsctl(staleInterfaceArgs...)
	if err != nil {
		klog.Errorf("Failed to delete OVS port/interfaces: stderr: %s (%v)",
			stderr, err)
	}
}
