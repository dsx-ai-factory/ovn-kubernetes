package ovn

import (
	"fmt"
	"net"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"

	ipam "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/allocator/ip"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	libovsdbops "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdb/ops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	ipreserv "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/controller/ipreservation"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	utilerrors "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util/errors"
)

// method/structure shared by all layer 2 network controller, including localnet and layer2 network controllres.

// BaseSecondaryLayer2NetworkController structure holds per-network fields and network specific
// configuration for secondary layer2/localnet network controller
type BaseSecondaryLayer2NetworkController struct {
	BaseSecondaryNetworkController
	// IP reservation controller, for networks that pod allocation done in ovnkube-controller
	ipreservController *ipreserv.Controller
}

// stop gracefully stops the controller, and delete all logical entities for this network if requested
func (oc *BaseSecondaryLayer2NetworkController) stop() {
	klog.Infof("Stop secondary %s network controller of network %s", oc.TopologyType(), oc.GetNetworkName())
	close(oc.stopChan)
	oc.cancelableCtx.Cancel()
	oc.wg.Wait()

	if oc.ipamClaimsHandler != nil {
		oc.watchFactory.RemoveIPAMClaimsHandler(oc.ipamClaimsHandler)
	}
	if oc.netPolicyHandler != nil {
		oc.watchFactory.RemovePolicyHandler(oc.netPolicyHandler)
	}
	if oc.multiNetPolicyHandler != nil {
		oc.watchFactory.RemoveMultiNetworkPolicyHandler(oc.multiNetPolicyHandler)
	}
	if oc.podHandler != nil {
		oc.watchFactory.RemovePodHandler(oc.podHandler)
	}
	if oc.nodeHandler != nil {
		oc.watchFactory.RemoveNodeHandler(oc.nodeHandler)
	}
	if oc.namespaceHandler != nil {
		oc.watchFactory.RemoveNamespaceHandler(oc.namespaceHandler)
	}
}

// cleanup cleans up logical entities for the given network, called from net-attach-def routine
// could be called from a dummy Controller (only has CommonNetworkControllerInfo set)
func (oc *BaseSecondaryLayer2NetworkController) cleanup() error {
	netName := oc.GetNetworkName()
	klog.Infof("Delete OVN logical entities for network %s", netName)
	// delete layer 2 logical switches
	ops, err := libovsdbops.DeleteLogicalSwitchesWithPredicateOps(oc.nbClient, nil,
		func(item *nbdb.LogicalSwitch) bool {
			return item.ExternalIDs[types.NetworkExternalID] == netName
		})
	if err != nil {
		return fmt.Errorf("failed to get ops for deleting switches of network %s: %v", netName, err)
	}

	ops, err = cleanupPolicyLogicalEntities(oc.nbClient, ops, oc.controllerName)
	if err != nil {
		return err
	}

	ops, err = libovsdbops.DeleteQoSesWithPredicateOps(oc.nbClient, ops,
		func(item *nbdb.QoS) bool {
			return item.ExternalIDs[types.NetworkExternalID] == netName
		})
	if err != nil {
		return fmt.Errorf("failed to get ops for deleting QoSes of network %s: %v", netName, err)
	}

	ops, err = libovsdbops.DeleteAddressSetsWithPredicateOps(oc.nbClient, ops,
		func(item *nbdb.AddressSet) bool {
			return item.ExternalIDs[types.NetworkExternalID] == netName
		})
	if err != nil {
		return fmt.Errorf("failed to get ops for deleting address sets of network %s: %v", netName, err)
	}

	_, err = libovsdbops.TransactAndCheck(oc.nbClient, ops)
	if err != nil {
		return fmt.Errorf("failed to deleting switches of network %s: %v", netName, err)
	}

	return nil
}

func (oc *BaseSecondaryLayer2NetworkController) run() error {
	klog.Infof("Starting all the Watchers for network %s ...", oc.GetNetworkName())
	start := time.Now()

	// WatchNamespaces() should be started first because it has no other
	// dependencies, and WatchNodes() depends on it
	if err := oc.WatchNamespaces(); err != nil {
		return err
	}

	// we need to start this before WatchPods so that we can reserve existing IPs reserved
	// for IP reservation before it gets assigned to the Pods
	if config.OVNKubernetesFeature.EnableIPReservation && oc.allocatesPodAnnotation() {
		var switchName string
		if oc.TopologyType() == types.LocalnetTopology {
			switchName = types.OVNLocalnetSwitch
		} else {
			switchName = types.OVNLayer2Switch
		}
		ipresvController, err := ipreserv.NewController(oc.ReconcilableNetInfo, oc.kube, oc.watchFactory,
			oc.lsManager.ForSwitch(oc.GetNetworkScopedName(switchName)), oc.recorder, oc.stopChan)
		if err != nil {
			return err
		}
		oc.ipreservController = ipresvController
	}

	if err := oc.WatchNodes(); err != nil {
		return err
	}

	// when on IC, it will be the NetworkController that returns the IPAMClaims
	// IPs back to the pool
	if oc.allocatesPodAnnotation() && oc.allowPersistentIPs() {
		// WatchIPAMClaims should be started before WatchPods to prevent OVN-K
		// master assigning IPs to pods without taking into account the persistent
		// IPs set aside for the IPAMClaims
		if err := oc.WatchIPAMClaims(); err != nil {
			return err
		}
	}

	if err := oc.WatchPods(); err != nil {
		return err
	}

	if config.OVNKubernetesFeature.EnableIPReservation && oc.allocatesPodAnnotation() {
		// start to allocate IP reservation IPs after pod IP allocation so that it won't
		// re-allocate IPs that already allocated for Pods.
		// Note that the existing IP reservation IPs are reserved when ipreservController is initialized.
		oc.wg.Add(1)
		go func() {
			defer oc.wg.Done()
			// Until we have scale issues in future let's spawn only one thread
			oc.ipreservController.Run(1, oc.stopChan)
		}()
	}

	if config.OVNKubernetesFeature.EnableVirtualIP {
		if err := oc.watchPortBindingTable(); err != nil {
			return err
		}
		if err := oc.WatchVirtualIPs(); err != nil {
			return err
		}
	}
	if config.OVNKubernetesFeature.EnableAdminPolicyBasedRouting {
		if err := oc.WatchAdminPolicyBasedRoutes(); err != nil {
			return err
		}
	}
	if util.IsMultiNetworkPoliciesSupportEnabled() && !oc.IsPrimaryNetwork() {
		// WatchMultiNetworkPolicy depends on WatchPods and WatchNamespaces
		if err := oc.WatchMultiNetworkPolicy(); err != nil {
			return err
		}
	}

	if oc.IsPrimaryNetwork() {
		// WatchNetworkPolicy depends on WatchPods and WatchNamespaces
		if err := oc.WatchNetworkPolicy(); err != nil {
			return err
		}
	}

	// start NetworkQoS controller if feature is enabled
	if config.OVNKubernetesFeature.EnableNetworkQoS {
		err := oc.newNetworkQoSController()
		if err != nil {
			return fmt.Errorf("unable to create network qos controller, err: %w", err)
		}
		oc.wg.Add(1)
		go func() {
			defer oc.wg.Done()
			// Until we have scale issues in future let's spawn only one thread
			oc.nqosController.Run(1, oc.stopChan)
		}()
	}

	if config.OVNKubernetesFeature.EnablePortMirror {
		err := oc.WatchPortMirrors()
		if err != nil {
			return err
		}
	}

	klog.Infof("Completing all the Watchers for network %s took %v", oc.GetNetworkName(), time.Since(start))
	return nil
}

func (oc *BaseSecondaryLayer2NetworkController) initializeLogicalSwitch(switchName string, clusterSubnets []config.CIDRNetworkEntry,
	excludeSubnets []*net.IPNet, clusterLoadBalancerGroupUUID, switchLoadBalancerGroupUUID string) (*nbdb.LogicalSwitch, error) {
	logicalSwitch := nbdb.LogicalSwitch{
		Name:        switchName,
		ExternalIDs: util.GenerateExternalIDsForSwitchOrRouter(oc.GetNetInfo()),
	}

	hostSubnets := make([]*net.IPNet, 0, len(clusterSubnets))
	for _, clusterSubnet := range clusterSubnets {
		subnet := clusterSubnet.CIDR
		hostSubnets = append(hostSubnets, subnet)
		if utilnet.IsIPv6CIDR(subnet) {
			logicalSwitch.OtherConfig = map[string]string{"ipv6_prefix": subnet.IP.String()}
		} else {
			logicalSwitch.OtherConfig = map[string]string{"subnet": subnet.String()}
		}
	}

	if oc.isLayer2Interconnect() {
		err := oc.zoneICHandler.AddTransitSwitchConfig(&logicalSwitch)
		if err != nil {
			return nil, err
		}
	}

	if clusterLoadBalancerGroupUUID != "" && switchLoadBalancerGroupUUID != "" {
		logicalSwitch.LoadBalancerGroup = []string{clusterLoadBalancerGroupUUID, switchLoadBalancerGroupUUID}
	}

	err := libovsdbops.CreateOrUpdateLogicalSwitch(oc.nbClient, &logicalSwitch)
	if err != nil {
		return nil, fmt.Errorf("failed to create logical switch %+v: %v", logicalSwitch, err)
	}

	if err = oc.lsManager.AddOrUpdateSwitch(switchName, hostSubnets, excludeSubnets...); err != nil {
		return nil, err
	}

	if oc.NADToInterConnect() != "" {
		gatewayIPS := make([]*net.IPNet, 0, len(oc.Subnets()))
		for _, subnet := range oc.Subnets() {
			gwIP := util.GetNodeGatewayIfAddr(subnet.CIDR).IP
			gatewayIPS = append(
				gatewayIPS,
				&net.IPNet{IP: gwIP, Mask: util.GetIPFullMask(gwIP)})
		}
		// It is ok if the gateway IPs are already reserved
		err = oc.lsManager.AllocateIPs(switchName, gatewayIPS)
		if err != nil && !ipam.IsErrAllocated(err) {
			return nil, fmt.Errorf("failed to allocate gatewayIPs %v on %s: %w", gatewayIPS, switchName, err)
		}
	}

	return &logicalSwitch, nil
}

func (oc *BaseSecondaryLayer2NetworkController) addUpdateNodeEvent(node *corev1.Node) error {
	if oc.isLocalZoneNode(node) {
		return oc.addUpdateLocalNodeEvent(node)
	}
	return oc.addUpdateRemoteNodeEvent(node)
}

func (oc *BaseSecondaryLayer2NetworkController) addUpdateLocalNodeEvent(node *corev1.Node) error {
	_, present := oc.localZoneNodes.LoadOrStore(node.Name, true)

	if !present {
		// process all pods so they are reconfigured as local
		errs := oc.addAllPodsOnNode(node.Name)
		if errs != nil {
			err := utilerrors.Join(errs...)
			return err
		}
	}

	return nil
}

func (oc *BaseSecondaryLayer2NetworkController) addUpdateRemoteNodeEvent(node *corev1.Node) error {
	_, present := oc.localZoneNodes.Load(node.Name)

	if present {
		err := oc.deleteNodeEvent(node)
		if err != nil {
			return err
		}

		// process all pods so they are reconfigured as remote
		errs := oc.addAllPodsOnNode(node.Name)
		if errs != nil {
			err = utilerrors.Join(errs...)
			return err
		}
	}

	return nil
}

func (oc *BaseSecondaryLayer2NetworkController) deleteNodeEvent(node *corev1.Node) error {
	oc.localZoneNodes.Delete(node.Name)
	return nil
}

func (oc *BaseSecondaryLayer2NetworkController) syncNodes(nodes []interface{}) error {
	for _, tmp := range nodes {
		node, ok := tmp.(*corev1.Node)
		if !ok {
			return fmt.Errorf("spurious object in syncNodes: %v", tmp)
		}

		// Add the node to the foundNodes only if it belongs to the local zone.
		if oc.isLocalZoneNode(node) {
			oc.localZoneNodes.Store(node.Name, true)
		}
	}

	return nil
}

func (oc *BaseSecondaryLayer2NetworkController) syncIPAMClaims(ipamClaims []interface{}) error {
	switchName, err := oc.getExpectedSwitchName(dummyPod())
	if err != nil {
		return err
	}
	return oc.ipamClaimsReconciler.Sync(ipamClaims, oc.lsManager.ForSwitch(switchName))
}

func dummyPod() *corev1.Pod {
	return &corev1.Pod{Spec: corev1.PodSpec{NodeName: ""}}
}
