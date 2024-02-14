package node

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	kapi "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

// XDPInfo stores the IP/MAC information used to setup XDP service for a specific NAD
type XDPInfo struct {
	allowedIPs []string
	mac        string
}

// SecondaryNodeNetworkController structure is the object which holds the controls for starting
// and reacting upon the watched resources (e.g. pods, endpoints) for secondary network
type SecondaryLocalnetNodeNetworkController struct {
	SecondaryNodeNetworkController
	// default bridge name, primary DPU node only, used to update XDP open flows
	defaultBridgeName string
	// bridge name for this localnet network
	bridgeName string
	// Some controllers, e.g those needing XDP and on non-primary DPU, need to manage shared gateway
	// other than the NS gateway on the primary DPU.
	gateway Gateway
}

// NewSecondaryLocalnetNodeNetworkController creates a new OVN controller for creating logical network
// infrastructure and policy for secondary localnet network
func NewSecondaryLocalnetNodeNetworkController(cnnci *CommonNodeNetworkControllerInfo, netInfo util.NetInfo) *SecondaryLocalnetNodeNetworkController {
	return &SecondaryLocalnetNodeNetworkController{
		SecondaryNodeNetworkController: SecondaryNodeNetworkController{
			BaseNodeNetworkController: BaseNodeNetworkController{
				CommonNodeNetworkControllerInfo: *cnnci,
				NetInfo:                         netInfo,
				stopChan:                        make(chan struct{}),
				wg:                              &sync.WaitGroup{},
				DoSCheckStopChan:                nil,
			},
		},
	}
}

// get needed XDP information from the Pod annotation
func getPodXDPInfo(pod *kapi.Pod, nadName string) (*XDPInfo, error) {
	var allowedIPs []string
	netAnnotation, err := util.UnmarshalPodAnnotation(pod.Annotations, nadName)
	if err != nil {
		return nil, err
	}
	if !util.SkipIPAMForNAD(pod.Annotations, nadName) {
		podIP := strings.Split(netAnnotation.IPs[0].String(), "/")
		allowedIPs = append(allowedIPs, podIP[0])
	} else {
		psInfo, err := util.GetPortSecurityInfo(pod.Annotations)
		if err != nil {
			return nil, err
		}
		if ipList := psInfo[nadName]; ipList != nil && len(ipList.IPs) > 0 {
			allowedIPs = append(allowedIPs, ipList.IPs...)
		} else {
			return nil, fmt.Errorf("failed to get IPs of NAD %s from %s annotation", nadName, util.PortSecurityInfoAnnotation)
		}
	}
	return &XDPInfo{allowedIPs, netAnnotation.MAC.String()}, nil
}

// addRepPortFunc is the localnet topology specific function called when specific Pod/NAD is added;
// nadName is the real NAD name even for default network
func (nc *SecondaryLocalnetNodeNetworkController) addRepPortFunc(pod *kapi.Pod, nadName string) (any, error) {
	// Configure XDP for this network
	if nc.XDPService() {
		klog.Infof("Setting up XDP service for pod %s/%s NAD %s", pod.Namespace, pod.Name, nadName)
		xdpInfo, err := getPodXDPInfo(pod, nc.GetAnnotationKey(nadName))
		if err != nil {
			return nil, fmt.Errorf("failed to get IPs for pod %s/%s NAD %s", pod.Namespace, pod.Name, nadName)
		}
		gw := nc.gateway.(*gateway)
		// Check if the (localnet) patch port is in place
		gwReady, _ := gw.readyFunc()
		if !gwReady {
			return nil, fmt.Errorf("failed to setup XDP, gateway not ready")
		}
		// If this pod needs Syn-Flooding mitigation on the DPU (to protect DPU cores)
		// by adding a bump-in-the-path kind of service before signalling that pod as ready.
		klog.Infof("Setting up XDP service for %s/%s NAD %s", pod.Namespace, pod.Name, nadName)
		err = nc.setXDPServiceForInterface(xdpInfo, true)
		if err != nil {
			return nil, fmt.Errorf("failed to setup XDP service for pod %s/%s NAD %s: %v", pod.Namespace, pod.Name, nadName, err)
		}
		return xdpInfo, nil
	}
	klog.Infof("XDP not needed for pod %s/%s NAD %s", pod.Namespace, pod.Name, nadName)
	return nil, nil
}

// delRepPortFunc is the localnet topology specific function called when specific Pod/NAD is deleted;
// nadName is the real NAD name even for default network
func (nc *SecondaryLocalnetNodeNetworkController) delRepPortFunc(pod *kapi.Pod, nadName string, anyInfo any) error {
	// Remove XDP xonfigurationfor this network
	if nc.XDPService() {
		if anyInfo != nil {
			xdpInfo := anyInfo.(*XDPInfo)
			klog.Infof("Tearing down XDP service for pod %s/%s for NAD %s", pod.Namespace, pod.Name, nadName)
			err := nc.setXDPServiceForInterface(xdpInfo, false)
			if err != nil {
				return fmt.Errorf("error remving XDP: %v", err)
			}
		} else {
			klog.V(5).Infof("Skipping tearing down XDP service for pod Pod %s/%s for NAD %s as it wasn't setup",
				pod.Namespace, pod.Name, nadName)
		}
	} else {
		klog.V(5).Infof("XDP service not used for pod %s/%s for NAD %s", pod.Namespace, pod.Name, nadName)
	}
	return nil
}

// updateRepPortFunc is the localnet topology specific function called when XDPInfo of specific Pod/NAD is updated;
// nadName is the real NAD name even for default network
// Note, there is a gap when we teardown old and add new; which might have a brief impact
func (nc *SecondaryLocalnetNodeNetworkController) updateRepPortFunc(newPod *kapi.Pod, nadName string, oldAnyInfo any) (any, error) {
	klog.Infof("Updating XDP service for pod %s/%s NAD %s", newPod.Namespace, newPod.Name, nadName)
	if !nc.XDPService() {
		return nil, nil
	}
	var oldXDPInfo, newXDPInfo *XDPInfo
	var err, getNewInfoError error
	if oldAnyInfo != nil {
		oldXDPInfo = oldAnyInfo.(*XDPInfo)
	}
	newXDPInfo, getNewInfoError = getPodXDPInfo(newPod, nc.GetAnnotationKey(nadName))
	// even if we failed to get new XDP info, continue to delete the old one
	if oldXDPInfo != nil && newXDPInfo == nil {
		klog.Infof("Tearing down XDP service for pod %s/%s for NAD %s", newPod.Namespace, newPod.Name, nadName)
		err = nc.setXDPServiceForInterface(oldXDPInfo, false)
		if err != nil {
			return nil, fmt.Errorf("failed to tear down XDP service for pod %s/%s NAD %s: %v", newPod.Namespace, newPod.Name, nadName, err)
		}
	} else if oldXDPInfo != nil && newXDPInfo != nil {
		if !(util.IsStringListEqual(oldXDPInfo.allowedIPs, newXDPInfo.allowedIPs) && oldXDPInfo.mac == newXDPInfo.mac) {
			if err = nc.UpdateXDPServiceForInterface(oldXDPInfo, newXDPInfo); err != nil {
				return nil, fmt.Errorf("failed to update XDP for pod %s/%s: %v", newPod.Namespace, newPod.Name, err)
			}
		}
	} else if oldXDPInfo == nil && newXDPInfo != nil {
		klog.Infof("Setting up XDP service for %s/%s NAD %s", newPod.Namespace, newPod.Name, nadName)
		err = nc.setXDPServiceForInterface(newXDPInfo, true)
		if err != nil {
			return nil, fmt.Errorf("failed to setup XDP service for pod %s/%s NAD %s: %v", newPod.Namespace, newPod.Name, nadName, err)
		}
	}
	return newXDPInfo, getNewInfoError
}

// Start starts the default controller; handles all events and creates all needed logical entities
func (nc *SecondaryLocalnetNodeNetworkController) Start(ctx context.Context) error {
	var err error
	klog.Infof("Start secondary node network controller of network %s", nc.GetNetworkName())

	if config.OvnKubeNode.Mode != types.NodeModeDPUHost {
		err = nc.updateLocalnetOvnBridgeMapping(true)
		if err != nil {
			return fmt.Errorf(err.Error())
		}
	}

	if config.OvnKubeNode.Mode == types.NodeModeDPU {
		// If this NAD is backed by a XDP service, initialize the shared XDP gateway on the controller.
		// Currently only supported for localnet NAD.
		if nc.XDPService() {
			klog.Infof("XDP is configured on network %s", nc.GetNetworkName())
			if nc.bridgeName == "" {
				klog.Warningf("XDP bridge for network %s is not configured, skip this node...", nc.GetNetworkName())
				return nil
			} else {
				// need to get default bridge name for primary DPU, used to update XDP openflows
				if config.OvnKubeNode.IsPrimaryDPU {
					_, nc.defaultBridgeName, err = getGatewayNextHops()
					if err != nil {
						return fmt.Errorf("failed to get default bridge on this node %s", nc.name)
					}
				}
				if nc.gateway == nil {
					gw, err := nc.initGatewayDPUXDP()
					if err != nil {
						return fmt.Errorf("failed initializing XDP for network %s: %v", nc.GetNetworkName(), err)
					}
					nc.gateway = gw
					klog.Infof("Initialized XDP for NAD %s", nc.GetNetworkName())
				}
			}
		}

		nc.enableDoSChecker()
		handler, err := nc.watchPodsDPU(nc.addRepPortFunc, nc.delRepPortFunc, nc.updateRepPortFunc)
		if err != nil {
			return err
		}
		nc.podHandler = handler
	}
	return nil
}

// Stop gracefully stops the controller
func (nc *SecondaryLocalnetNodeNetworkController) Stop() {
	nc.SecondaryNodeNetworkController.Stop()

	if config.OvnKubeNode.Mode != types.NodeModeDPUHost {
		err := nc.updateLocalnetOvnBridgeMapping(false)
		if err != nil {
			klog.Errorf(err.Error())
		}
	}

	if config.OvnKubeNode.Mode == types.NodeModeDPU {
		// Currently, only XDP uses nc.gateway, so we can assume XDP if	this is set.
		if nc.gateway != nil {
			klog.Infof("Removing XDP for network %s", nc.GetNetworkName())
			err := nc.cleanGatewayDPUXDP(nc.gateway.(*gateway))
			if err != nil {
				klog.Infof("Failed to remove XDP config for network %s: %v", nc.GetNetworkName(), err)
			}
			klog.Infof("Destroyed XDP config for network %s", nc.GetNetworkName())
		}
	}
}

// Cleanup cleans up node entities for the given secondary network
func (nc *SecondaryLocalnetNodeNetworkController) Cleanup(netName string) error {
	return nil
}

func (nc *SecondaryLocalnetNodeNetworkController) updateLocalnetOvnBridgeMapping(toAdd bool) error {
	bridgeName := ""
	if toAdd {
		// ngn-localnet-bridge-mappings exernal_ids is in the form of "<network_prefix1>:<br1>,<network_prefix2>:<br2>...".
		// It sets all the possible localnet networks and associated bridge names on this node.
		stdout, stderr, err := util.RunOVSVsctl("--if-exists", "get", "Open_vSwitch", ".",
			"external_ids:ngn-localnet-bridge-mappings")
		if err != nil {
			klog.Warningf("Failed to get ngn-localnet-bridge-mappings from Open_vSwitch table stderr:%s (%v)", stderr, err)
			return nil
		}

		// find the entry that has a complete match. this is to disambiguate two entries that
		// have the same prefix, but different OVS bridges.
		bridgeMapConfs := strings.Split(stdout, ",")
		for _, bridgeMapConf := range bridgeMapConfs {
			maps := strings.Split(bridgeMapConf, ":")
			if len(maps) == 2 && nc.GetNetworkName() == maps[0] {
				bridgeName = maps[1]
				break
			}
		}

		// now find partial match, if required
		if bridgeName == "" {
			for _, bridgeMapConf := range bridgeMapConfs {
				maps := strings.Split(bridgeMapConf, ":")
				if len(maps) == 2 && strings.HasPrefix(nc.GetNetworkName(), maps[0]) {
					bridgeName = maps[1]
					break
				}
			}
		}

		if bridgeName == "" {
			klog.V(5).Infof("Localnet network %s is not needed on this node %s", nc.GetNetworkName(), nc.name)
			return nil
		}
		klog.V(5).Infof("Set bridge %s for localnet network %s", bridgeName, nc.name)
		nc.bridgeName = bridgeName
	}

	// ovn-bridge-mappings maps a physical network name to a local ovs bridge
	// that provides connectivity to that network. It is in the form of physnet1:br1,physnet2:br2.
	// Note that there may be multiple ovs bridge mappings, be sure not to override
	// the mappings for the other physical network
	networkName := nc.GetNetworkScopedName(types.LocalNetBridgeName)
	stdout, stderr, err := util.RunOVSVsctl("--if-exists", "get", "Open_vSwitch", ".",
		"external_ids:ovn-bridge-mappings")
	if err != nil {
		return fmt.Errorf("failed to get ovn-bridge-mappings stderr:%s (%v)", stderr, err)
	}

	bridgeMap := map[string]string{}
	bridgeMappings := strings.Split(stdout, ",")
	for _, bridgeMapping := range bridgeMappings {
		m := strings.Split(bridgeMapping, ":")
		if len(m) == 2 {
			bridgeMap[m[0]] = m[1]
		}
	}

	bridge, ok := bridgeMap[networkName]
	if toAdd {
		if ok && bridge == bridgeName {
			return nil
		}
		bridgeMap[networkName] = bridgeName
	} else {
		if !ok {
			return nil
		}
		delete(bridgeMap, networkName)
	}

	if len(bridgeMap) == 0 {
		return nil
	}

	mapString := ""
	for networkName, bridge = range bridgeMap {
		if len(mapString) != 0 {
			mapString += ","
		}
		mapString = mapString + networkName + ":" + bridge
	}

	_, stderr, err = util.RunOVSVsctl("set", "Open_vSwitch", ".",
		fmt.Sprintf("external_ids:ovn-bridge-mappings=%s", mapString))
	if err != nil {
		return fmt.Errorf("failed to set ovn-bridge-mappings %s, stderr:%s (%v)", mapString, stderr, err)
	}
	return nil
}

func (nc *SecondaryLocalnetNodeNetworkController) NADToInterConnect() string {
	return ""
}

func (nc *SecondaryLocalnetNodeNetworkController) StartInterConnect(icInfo *util.InterConnectInfo) error {
	panic("unexpected call for secondary localnet Node Network Controller")
}

func (nc *SecondaryLocalnetNodeNetworkController) StopInterConnect(icInfo *util.InterConnectInfo) error {
	panic("unexpected call for secondary localnet Node Network Controller")
}
