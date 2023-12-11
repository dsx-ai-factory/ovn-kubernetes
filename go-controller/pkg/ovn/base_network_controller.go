package ovn

import (
	"fmt"
	"math"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	libovsdbclient "github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/ovsdb"
	"github.com/pkg/errors"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/allocator/pod"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	adminpbrapi "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/adminpbr/v1beta1"
	ipreservation "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/ipreservation/v1beta1"
	virtualip "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/virtualip/v1beta1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kubevirt"
	libovsdbops "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdb/ops"
	libovsdbutil "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdb/util"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/metrics"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	addressset "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/address_set"
	lsm "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/logical_switch_manager"
	zoneic "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/zone_interconnect"
	ovnretry "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/retry"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/syncmap"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	kapi "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	ref "k8s.io/client-go/tools/reference"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"
)

// CommonNetworkControllerInfo structure is place holder for all fields shared among controllers.
type CommonNetworkControllerInfo struct {
	client       clientset.Interface
	kube         *kube.KubeOVN
	watchFactory *factory.WatchFactory
	podRecorder  *metrics.PodRecorder

	// event recorder used to post events to k8s
	recorder record.EventRecorder

	// libovsdb northbound client interface
	nbClient libovsdbclient.Client

	// libovsdb southbound client interface
	sbClient libovsdbclient.Client

	// has SCTP support
	SCTPSupport bool

	// has multicast support; set to false for secondary networks.
	// TBD: Changes need to be made to support multicast for secondary networks
	multicastSupport bool

	// Supports OVN Template Load Balancers?
	svcTemplateSupport bool

	// Northbound database zone name to which this Controller is connected to - aka local zone
	zone string
}

// BaseNetworkController structure holds per-network fields and network specific configuration
// Note that all the methods with NetworkControllerInfo pointer receivers will be called
// by more than one type of network controllers.
type BaseNetworkController struct {
	CommonNetworkControllerInfo
	// controllerName should be used to identify objects owned by given controller in the db
	controllerName string

	// network information
	util.NetInfo

	// retry framework for pods
	retryPods *ovnretry.RetryFramework
	// retry framework for nodes
	retryNodes *ovnretry.RetryFramework
	// retry framework for namespaces
	retryNamespaces *ovnretry.RetryFramework
	// retry framework for network policies
	retryNetworkPolicies *ovnretry.RetryFramework

	// pod events factory handler
	podHandler *factory.Handler
	// node events factory handler
	nodeHandler *factory.Handler
	// namespace events factory Handler
	namespaceHandler *factory.Handler

	// A cache of all logical switches seen by the watcher and their subnets
	lsManager *lsm.LogicalSwitchManager

	// An utility to allocate the PodAnnotation to pods
	podAnnotationAllocator *pod.PodAnnotationAllocator

	// A cache of all logical ports known to the controller
	logicalPortCache *portCache

	// Info about known namespaces. You must use oc.getNamespaceLocked() or
	// oc.waitForNamespaceLocked() to read this map, and oc.createNamespaceLocked()
	// or oc.deleteNamespaceLocked() to modify it. namespacesMutex is only held
	// from inside those functions.
	namespaces      map[string]*namespaceInfo
	namespacesMutex sync.Mutex

	// An address set factory that creates address sets
	addressSetFactory addressset.AddressSetFactory

	// topology version of this network. It is first retrieved from network logical entities,
	// and will eventually updated to latest version once topology upgrade is done.
	topologyVersion int

	// network policies map, key should be retrieved with getPolicyKey(policy *knet.NetworkPolicy).
	// network policies that failed to be created will also be added here, and can be retried or cleaned up later.
	// network policy is only deleted from this map after successful cleanup.
	// Allowed order of locking is namespace Lock -> oc.networkPolicies key Lock -> networkPolicy.Lock
	// Don't take namespace Lock while holding networkPolicy key lock to avoid deadlock.
	networkPolicies *syncmap.SyncMap[*networkPolicy]

	// map of existing shared port groups for network policies
	// port group exists in the db if and only if port group key is present in this map
	// key is namespace
	// allowed locking order is namespace Lock -> networkPolicy.Lock -> sharedNetpolPortGroups key Lock
	// make sure to keep this order to avoid deadlocks
	sharedNetpolPortGroups *syncmap.SyncMap[*defaultDenyPortGroups]

	podSelectorAddressSets *syncmap.SyncMap[*PodSelectorAddressSet]

	// stopChan per controller
	stopChan chan struct{}
	// waitGroup per-Controller
	wg *sync.WaitGroup

	// some downstream components need to stop on their own or when the network
	// controller is stopped
	// use a chain of cancelable contexts for this
	cancelableCtx util.CancelableContext

	// List of nodes which belong to the local zone (stored as a sync map)
	// If the map is nil, it means the controller is not tracking the node events
	// and all the nodes are considered as local zone nodes.
	localZoneNodes *sync.Map

	// zoneICHandler creates the interconnect resources for local nodes and remote nodes.
	// Interconnect resources are Transit switch and logical ports connecting this transit switch
	// to the cluster router. Please see zone_interconnect/interconnect_handler.go for more details.
	zoneICHandler *zoneic.ZoneInterconnectHandler

	// AdminPBR is only supported for default network and secondary layer3 network
	adminPBRHandler          *factory.Handler
	adminPBRNodeHandler      *factory.Handler
	adminPBRNamespaceHandler *factory.Handler
	// map of admin pbr policies
	adminPBRStore      sync.Map
	adminPBRRetryQueue workqueue.RateLimitingInterface

	// VirtualIP is only supported by secondary layer2 network
	virtualIPHandler *factory.Handler
	// libovsdb southbound client interface to monitor VIP port bindings
	vipSBClient libovsdbclient.Client
	// map & workqueue for virtualIP operations
	virtualIPs          sync.Map
	virtualIPRetryQueue workqueue.RateLimitingInterface

	// workqueue for IPReserve operation
	ipReserveRetryQueue workqueue.RateLimitingInterface
	ipReserveHandler    *factory.Handler
}

// BaseSecondaryNetworkController structure holds per-network fields and network specific
// configuration for secondary network controller
type BaseSecondaryNetworkController struct {
	BaseNetworkController
	// multi-network policy events factory handler
	policyHandler *factory.Handler
}

// NewCommonNetworkControllerInfo creates CommonNetworkControllerInfo shared by controllers
func NewCommonNetworkControllerInfo(client clientset.Interface, kube *kube.KubeOVN, wf *factory.WatchFactory,
	recorder record.EventRecorder, nbClient libovsdbclient.Client, sbClient libovsdbclient.Client,
	podRecorder *metrics.PodRecorder, SCTPSupport, multicastSupport, svcTemplateSupport bool) (*CommonNetworkControllerInfo, error) {
	zone, err := libovsdbutil.GetNBZone(nbClient)
	if err != nil {
		return nil, fmt.Errorf("error getting NB zone name : err - %w", err)
	}
	return &CommonNetworkControllerInfo{
		client:             client,
		kube:               kube,
		watchFactory:       wf,
		recorder:           recorder,
		nbClient:           nbClient,
		sbClient:           sbClient,
		podRecorder:        podRecorder,
		SCTPSupport:        SCTPSupport,
		multicastSupport:   multicastSupport,
		svcTemplateSupport: svcTemplateSupport,
		zone:               zone,
	}, nil
}

func (bnc *BaseNetworkController) GetLogicalPortName(pod *kapi.Pod, nadName string) string {
	if !bnc.IsSecondary() {
		return util.GetLogicalPortName(pod.Namespace, pod.Name)
	} else {
		return util.GetSecondaryNetworkLogicalPortName(pod.Namespace, pod.Name, nadName)
	}
}

func (bnc *BaseNetworkController) AddConfigDurationRecord(kind, namespace, name string) (
	[]ovsdb.Operation, func(), time.Time, error) {
	if !bnc.IsSecondary() {
		return metrics.GetConfigDurationRecorder().AddOVN(bnc.nbClient, kind, namespace, name)
	}
	// TBD: no op for secondary network for now
	return []ovsdb.Operation{}, func() {}, time.Time{}, nil
}

// createOvnClusterRouter creates the central router for the network
func (bnc *BaseNetworkController) createOvnClusterRouter() (*nbdb.LogicalRouter, error) {
	// Create default Control Plane Protection (COPP) entry for routers
	defaultCOPPUUID, err := EnsureDefaultCOPP(bnc.nbClient)
	if err != nil {
		return nil, fmt.Errorf("unable to create router control plane protection: %w", err)
	}

	// Create a single common distributed router for the cluster.
	macBindingAgeThreshold := "0"
	for _, ipnet := range bnc.Subnets() {
		macBindingAgeThreshold += ";" + ipnet.CIDR.String() + ":" + strconv.Itoa(config.Default.ClusterSubnetsMacBindingAging)
	}

	logicalRouterName := bnc.GetNetworkScopedName(types.OVNClusterRouter)
	logicalRouter := nbdb.LogicalRouter{
		Name: logicalRouterName,
		ExternalIDs: map[string]string{
			"k8s-cluster-router":            "yes",
			types.TopologyVersionExternalID: strconv.Itoa(bnc.topologyVersion),
		},
		Options: map[string]string{
			"always_learn_from_arp_request": "false",
			"mac_binding_age_threshold":     macBindingAgeThreshold,
		},
		Copp: &defaultCOPPUUID,
	}
	if bnc.IsSecondary() {
		logicalRouter.ExternalIDs[types.NetworkExternalID] = bnc.GetNetworkName()
		logicalRouter.ExternalIDs[types.TopologyExternalID] = bnc.TopologyType()
	}
	if bnc.multicastSupport {
		logicalRouter.Options = map[string]string{
			"mcast_relay": "true",
		}
	}

	err = libovsdbops.CreateOrUpdateLogicalRouter(bnc.nbClient, &logicalRouter, &logicalRouter.Options,
		&logicalRouter.ExternalIDs, &logicalRouter.Copp)
	if err != nil {
		return nil, fmt.Errorf("failed to create distributed router %s, error: %v",
			logicalRouterName, err)
	}

	return &logicalRouter, nil
}

// syncNodeClusterRouterPort ensures a node's LS to the cluster router's LRP is created.
// NOTE: We could have created the router port in ensureNodeLogicalNetwork() instead of here,
// but chassis ID is not available at that moment. We need the chassis ID to set the
// gateway-chassis, which in effect pins the logical switch to the current node in OVN.
// Otherwise, ovn-controller will flood-fill unrelated datapaths unnecessarily, causing scale
// problems.
func (bnc *BaseNetworkController) syncNodeClusterRouterPort(node *kapi.Node, hostSubnets []*net.IPNet) error {
	chassisID, err := util.ParseNodeChassisIDAnnotation(node)
	if err != nil {
		return err
	}

	if len(hostSubnets) == 0 {
		hostSubnets, err = util.ParseNodeHostSubnetAnnotation(node, bnc.GetNetworkName())
		if err != nil {
			return err
		}
	}

	// logical router port MAC is based on IPv4 subnet if there is one, else IPv6
	var nodeLRPMAC net.HardwareAddr
	for _, hostSubnet := range hostSubnets {
		gwIfAddr := util.GetNodeGatewayIfAddr(hostSubnet)
		nodeLRPMAC = util.IPAddrToHWAddr(gwIfAddr.IP)
		if !utilnet.IsIPv6CIDR(hostSubnet) {
			break
		}
	}

	switchName := bnc.GetNetworkScopedName(node.Name)
	logicalRouterName := bnc.GetNetworkScopedName(types.OVNClusterRouter)
	lrpName := types.RouterToSwitchPrefix + switchName
	lrpNetworks := []string{}
	for _, hostSubnet := range hostSubnets {
		gwIfAddr := util.GetNodeGatewayIfAddr(hostSubnet)
		lrpNetworks = append(lrpNetworks, gwIfAddr.String())
	}
	logicalRouterPort := nbdb.LogicalRouterPort{
		Name:     lrpName,
		MAC:      nodeLRPMAC.String(),
		Networks: lrpNetworks,
	}
	logicalRouter := nbdb.LogicalRouter{Name: logicalRouterName}

	var gatewayChassis *nbdb.GatewayChassis
	skipPinnedLS := false
	allSkipNADs := util.GetAllNADsSkipPinnedLS(node)
	for _, skipNAD := range allSkipNADs {
		if bnc.HasNAD(skipNAD) {
			skipPinnedLS = true
			break
		}
	}
	if !skipPinnedLS {
		gatewayChassis = &nbdb.GatewayChassis{
			Name:        lrpName + "-" + chassisID,
			ChassisName: chassisID,
			Priority:    1,
		}
	}
	err = libovsdbops.CreateOrUpdateLogicalRouterPort(bnc.nbClient, &logicalRouter, &logicalRouterPort,
		gatewayChassis, &logicalRouterPort.MAC, &logicalRouterPort.Networks)
	if err != nil {
		klog.Errorf("Failed to add gateway chassis %s to logical router port %s, error: %v", chassisID, lrpName, err)
		return err
	}

	if skipPinnedLS {
		// TBD: Can we do it in CreateOrUpdateLogicalRouterPort?
		p := func(item *nbdb.GatewayChassis) bool {
			return item.Name == lrpName+"-"+chassisID
		}
		if err = libovsdbops.DeleteGatewayChassisWithPredicate(bnc.nbClient, logicalRouterPort.Name, p); err != nil {
			klog.Errorf("Failed to delete gateway chassis %s from logical router port %s, error: %v", chassisID, lrpName, err)
			return err
		}
	}

	return nil
}

func (bnc *BaseNetworkController) createNodeLogicalSwitch(nodeName string, hostSubnets []*net.IPNet,
	clusterLoadBalancerGroupUUID, switchLoadBalancerGroupUUID string) error {
	// logical router port MAC is based on IPv4 subnet if there is one, else IPv6
	var nodeLRPMAC net.HardwareAddr
	switchName := bnc.GetNetworkScopedName(nodeName)
	for _, hostSubnet := range hostSubnets {
		gwIfAddr := util.GetNodeGatewayIfAddr(hostSubnet)
		nodeLRPMAC = util.IPAddrToHWAddr(gwIfAddr.IP)
		if !utilnet.IsIPv6CIDR(hostSubnet) {
			break
		}
	}

	logicalSwitch := nbdb.LogicalSwitch{
		Name: switchName,
	}
	if bnc.IsSecondary() {
		logicalSwitch.ExternalIDs = map[string]string{
			types.NetworkExternalID:  bnc.GetNetworkName(),
			types.TopologyExternalID: bnc.TopologyType(),
		}
	}

	var v4Gateway, v6Gateway net.IP
	logicalSwitch.OtherConfig = map[string]string{}
	for _, hostSubnet := range hostSubnets {
		gwIfAddr := util.GetNodeGatewayIfAddr(hostSubnet)
		mgmtIfAddr := util.GetNodeManagementIfAddr(hostSubnet)

		if utilnet.IsIPv6CIDR(hostSubnet) {
			v6Gateway = gwIfAddr.IP

			logicalSwitch.OtherConfig["ipv6_prefix"] =
				hostSubnet.IP.String()
		} else {
			v4Gateway = gwIfAddr.IP
			excludeIPs := mgmtIfAddr.IP.String()
			if config.HybridOverlay.Enabled {
				hybridOverlayIfAddr := util.GetNodeHybridOverlayIfAddr(hostSubnet)
				excludeIPs += ".." + hybridOverlayIfAddr.IP.String()
			}
			logicalSwitch.OtherConfig["subnet"] = hostSubnet.String()
			logicalSwitch.OtherConfig["exclude_ips"] = excludeIPs
		}
	}

	if clusterLoadBalancerGroupUUID != "" && switchLoadBalancerGroupUUID != "" {
		logicalSwitch.LoadBalancerGroup = []string{clusterLoadBalancerGroupUUID, switchLoadBalancerGroupUUID}
	}

	// If supported, enable IGMP/MLD snooping and querier on the node.
	if bnc.multicastSupport {
		logicalSwitch.OtherConfig["mcast_snoop"] = "true"

		// Configure IGMP/MLD querier if the gateway IP address is known.
		// Otherwise disable it.
		if v4Gateway != nil || v6Gateway != nil {
			logicalSwitch.OtherConfig["mcast_querier"] = "true"
			logicalSwitch.OtherConfig["mcast_eth_src"] = nodeLRPMAC.String()
			if v4Gateway != nil {
				logicalSwitch.OtherConfig["mcast_ip4_src"] = v4Gateway.String()
			}
			if v6Gateway != nil {
				logicalSwitch.OtherConfig["mcast_ip6_src"] = util.HWAddrToIPv6LLA(nodeLRPMAC).String()
			}
		} else {
			logicalSwitch.OtherConfig["mcast_querier"] = "false"
		}
	}

	err := libovsdbops.CreateOrUpdateLogicalSwitch(bnc.nbClient, &logicalSwitch, &logicalSwitch.OtherConfig,
		&logicalSwitch.LoadBalancerGroup, &logicalSwitch.ExternalIDs)
	if err != nil {
		return fmt.Errorf("failed to add logical switch %+v: %v", logicalSwitch, err)
	}

	// Connect the switch to the router.
	logicalSwitchPort := nbdb.LogicalSwitchPort{
		Name:      types.SwitchToRouterPrefix + switchName,
		Type:      "router",
		Addresses: []string{"router"},
		Options: map[string]string{
			"router-port": types.RouterToSwitchPrefix + switchName,
			"arp_proxy":   kubevirt.ComposeARPProxyLSPOption(),
		},
	}
	sw := nbdb.LogicalSwitch{Name: switchName}
	err = libovsdbops.CreateOrUpdateLogicalSwitchPortsOnSwitch(bnc.nbClient, &sw, &logicalSwitchPort)
	if err != nil {
		klog.Errorf("Failed to add logical port %+v to switch %s: %v", logicalSwitchPort, switchName, err)
		return err
	}

	if bnc.multicastSupport {
		err = libovsdbops.AddPortsToPortGroup(bnc.nbClient, bnc.getClusterPortGroupName(types.ClusterRtrPortGroupNameBase), logicalSwitchPort.UUID)
		if err != nil {
			klog.Errorf(err.Error())
			return err
		}
	}

	// Add the switch to the logical switch cache
	return bnc.lsManager.AddOrUpdateSwitch(logicalSwitch.Name, hostSubnets)
}

// UpdateNodeAnnotationWithRetry update node's annotation with the given node annotations.
func (cnci *CommonNetworkControllerInfo) UpdateNodeAnnotationWithRetry(nodeName string,
	nodeAnnotations map[string]string) error {
	// Retry if it fails because of potential conflict which is transient. Return error in the
	// case of other errors (say temporary API server down), and it will be taken care of by the
	// retry mechanism.
	resultErr := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		// Informer cache should not be mutated, so get a copy of the object
		node, err := cnci.watchFactory.GetNode(nodeName)
		if err != nil {
			return err
		}

		cnode := node.DeepCopy()
		for k, v := range nodeAnnotations {
			cnode.Annotations[k] = v
		}
		// It is possible to update the node annotations using status subresource
		// because changes to metadata via status subresource are not restricted for nodes.
		return cnci.kube.UpdateNodeStatus(cnode)
	})
	if resultErr != nil {
		return fmt.Errorf("failed to update node %s annotation", nodeName)
	}
	return nil
}

// deleteNodeLogicalNetwork removes the logical switch and logical router port associated with the node
func (bnc *BaseNetworkController) deleteNodeLogicalNetwork(nodeName string) error {
	switchName := bnc.GetNetworkScopedName(nodeName)

	// Remove the logical switch associated with the node
	err := libovsdbops.DeleteLogicalSwitch(bnc.nbClient, switchName)
	if err != nil {
		return fmt.Errorf("failed to delete logical switch %s: %v", switchName, err)
	}

	logicalRouterName := bnc.GetNetworkScopedName(types.OVNClusterRouter)
	logicalRouter := nbdb.LogicalRouter{Name: logicalRouterName}
	logicalRouterPort := nbdb.LogicalRouterPort{
		Name: types.RouterToSwitchPrefix + switchName,
	}
	err = libovsdbops.DeleteLogicalRouterPorts(bnc.nbClient, &logicalRouter, &logicalRouterPort)
	if err != nil {
		return fmt.Errorf("failed to delete router port %s: %w", logicalRouterPort.Name, err)
	}

	return nil
}

func (bnc *BaseNetworkController) addAllPodsOnNode(nodeName string) []error {
	errs := []error{}
	podIndexer := bnc.watchFactory.PodInformer().GetIndexer()
	pods, err := podIndexer.ByIndex(types.CacheIndexPodByNodeName, nodeName)
	if err != nil {
		errs = append(errs, err)
		klog.Errorf("Unable to get all existing pods, existing pods on node %s may not function",
			nodeName)
	} else {
		for _, obj := range pods {
			pod, ok := obj.(*kapi.Pod)
			if !ok {
				continue
			}
			if util.PodCompleted(pod) {
				continue
			}
			klog.V(5).Infof("When adding node %s, adding pod %s/%s/%s to retryPods for network %s", nodeName,
				pod.UID, pod.Namespace, pod.Name, bnc.GetNetworkName())
			err = bnc.retryPods.AddRetryObjWithAddNoBackoff(pod)
			if err != nil {
				errs = append(errs, err)
				klog.Errorf("Failed to add pod %s/%s to retryPods for network %s: %v", pod.Namespace, pod.Name, bnc.GetNetworkName(), err)
			}
		}
	}
	bnc.retryPods.RequestRetryObjs()
	return errs
}

func (bnc *BaseNetworkController) updateL3TopologyVersion() error {
	currentTopologyVersion := strconv.Itoa(types.OvnCurrentTopologyVersion)
	clusterRouterName := bnc.GetNetworkScopedName(types.OVNClusterRouter)
	logicalRouter := nbdb.LogicalRouter{
		Name:        clusterRouterName,
		ExternalIDs: map[string]string{types.TopologyVersionExternalID: currentTopologyVersion},
	}
	err := libovsdbops.UpdateLogicalRouterSetExternalIDs(bnc.nbClient, &logicalRouter)
	if err != nil {
		return fmt.Errorf("failed to generate set topology version, err: %v", err)
	}
	bnc.topologyVersion = types.OvnCurrentTopologyVersion
	klog.Infof("Updated Logical_Router %s topology version to %s", clusterRouterName, currentTopologyVersion)
	return nil
}

func (bnc *BaseNetworkController) updateL2TopologyVersion() error {
	var switchName string

	currentTopologyVersion := strconv.Itoa(types.OvnCurrentTopologyVersion)
	topoType := bnc.TopologyType()
	switch topoType {
	case types.Layer2Topology:
		switchName = bnc.GetNetworkScopedName(types.OVNLayer2Switch)
	case types.LocalnetTopology:
		switchName = bnc.GetNetworkScopedName(types.OVNLocalnetSwitch)
	default:
		return fmt.Errorf("topology type %s is not supported", topoType)
	}
	logicalSwitch := nbdb.LogicalSwitch{
		Name:        switchName,
		ExternalIDs: map[string]string{types.TopologyVersionExternalID: currentTopologyVersion},
	}
	err := libovsdbops.UpdateLogicalSwitchSetExternalIDs(bnc.nbClient, &logicalSwitch)
	if err != nil {
		return fmt.Errorf("failed to generate set topology version, err: %v", err)
	}
	bnc.topologyVersion = types.OvnCurrentTopologyVersion
	klog.Infof("Updated Logical_Switch %s topology version to %s", switchName, currentTopologyVersion)
	return nil
}

// determineOVNTopoVersionFromOVN determines what OVN Topology version is being used.
// If TopologyVersionExternalID key in external_ids column does not exist, it is prior to OVN topology versioning
// and therefore set version number to OvnCurrentTopologyVersion
func (bnc *BaseNetworkController) determineOVNTopoVersionFromOVN() error {
	var topologyVersion int
	var err error

	if !bnc.IsSecondary() {
		topologyVersion, err = bnc.getOVNTopoVersionFromLogicalRouter(types.OVNClusterRouter)
	} else {
		topoType := bnc.TopologyType()
		switch topoType {
		case types.Layer3Topology:
			topologyVersion, err = bnc.getOVNTopoVersionFromLogicalRouter(bnc.GetNetworkScopedName(types.OVNClusterRouter))
		case types.Layer2Topology:
			topologyVersion, err = bnc.getOVNTopoVersionFromLogicalSwitch(bnc.GetNetworkScopedName(types.OVNLayer2Switch))
		case types.LocalnetTopology:
			topologyVersion, err = bnc.getOVNTopoVersionFromLogicalSwitch(bnc.GetNetworkScopedName(types.OVNLocalnetSwitch))
		default:
			return fmt.Errorf("topology type %s not supported", topoType)
		}
	}
	bnc.topologyVersion = topologyVersion
	return err
}

func (bnc *BaseNetworkController) getOVNTopoVersionFromLogicalRouter(clusterRouterName string) (int, error) {
	logicalRouter := &nbdb.LogicalRouter{Name: clusterRouterName}
	logicalRouter, err := libovsdbops.GetLogicalRouter(bnc.nbClient, logicalRouter)
	if err != nil && !errors.Is(err, libovsdbclient.ErrNotFound) {
		return 0, fmt.Errorf("error getting router %s: %v", clusterRouterName, err)
	}
	if errors.Is(err, libovsdbclient.ErrNotFound) {
		// no OVNClusterRouter exists, DB is empty, nothing to upgrade
		return math.MaxInt32, nil
	}
	v, exists := logicalRouter.ExternalIDs[types.TopologyVersionExternalID]
	if !exists {
		klog.Infof("No version string found. The OVN topology is before versioning is introduced. Upgrade needed")
		return 0, nil
	}
	ver, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid OVN topology version string for network %s, err: %v", bnc.GetNetworkName(), err)
	}
	return ver, nil
}

func (bnc *BaseNetworkController) getOVNTopoVersionFromLogicalSwitch(switchName string) (int, error) {
	logicalSwitch := &nbdb.LogicalSwitch{Name: switchName}
	logicalSwitch, err := libovsdbops.GetLogicalSwitch(bnc.nbClient, logicalSwitch)
	if err != nil && !errors.Is(err, libovsdbclient.ErrNotFound) {
		return 0, fmt.Errorf("error getting switch %s: %v", switchName, err)
	}
	if errors.Is(err, libovsdbclient.ErrNotFound) {
		// no switch exists, DB is empty, nothing to upgrade
		return math.MaxInt32, nil
	}
	v := logicalSwitch.ExternalIDs[types.TopologyVersionExternalID]
	ver, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid OVN topology version string for network %s, err: %v", bnc.GetNetworkName(), err)
	}
	return ver, nil
}

// getNamespaceLocked locks namespacesMutex, looks up ns, and (if found), returns it with
// its mutex locked. If ns is not known, nil will be returned
func (bnc *BaseNetworkController) getNamespaceLocked(ns string, readOnly bool) (*namespaceInfo, func()) {
	// Only hold namespacesMutex while reading/modifying oc.namespaces. In particular,
	// we drop namespacesMutex while trying to claim nsInfo.Mutex, because something
	// else might have locked the nsInfo and be doing something slow with it, and we
	// don't want to block all access to oc.namespaces while that's happening.
	bnc.namespacesMutex.Lock()
	nsInfo := bnc.namespaces[ns]
	bnc.namespacesMutex.Unlock()

	if nsInfo == nil {
		return nil, nil
	}
	var unlockFunc func()
	if readOnly {
		unlockFunc = func() { nsInfo.RUnlock() }
		nsInfo.RLock()
	} else {
		unlockFunc = func() { nsInfo.Unlock() }
		nsInfo.Lock()
	}
	// Check that the namespace wasn't deleted while we were waiting for the lock
	bnc.namespacesMutex.Lock()
	defer bnc.namespacesMutex.Unlock()
	if nsInfo != bnc.namespaces[ns] {
		unlockFunc()
		return nil, nil
	}
	return nsInfo, unlockFunc
}

// deleteNamespaceLocked locks namespacesMutex, finds and deletes ns, and returns the
// namespace, locked.
func (bnc *BaseNetworkController) deleteNamespaceLocked(ns string) *namespaceInfo {
	// The locking here is the same as in getNamespaceLocked

	bnc.namespacesMutex.Lock()
	nsInfo := bnc.namespaces[ns]
	bnc.namespacesMutex.Unlock()

	if nsInfo == nil {
		return nil
	}
	nsInfo.Lock()

	bnc.namespacesMutex.Lock()
	defer bnc.namespacesMutex.Unlock()
	if nsInfo != bnc.namespaces[ns] {
		nsInfo.Unlock()
		return nil
	}
	if nsInfo.addressSet != nil {
		// Empty the address set, then delete it after an interval.
		if err := nsInfo.addressSet.SetIPs(nil); err != nil {
			klog.Errorf("Warning: failed to empty address set for deleted NS %s: %v", ns, err)
		}

		// Delete the address set after a short delay.
		// This is so NetworkPolicy handlers can converge and stop referencing it.
		addressSet := nsInfo.addressSet
		go func() {
			select {
			case <-bnc.stopChan:
				return
			case <-time.After(20 * time.Second):
				// Check to see if the NS was re-added in the meanwhile. If so,
				// only delete if the new NS's AddressSet shouldn't exist.
				nsInfo, nsUnlock := bnc.getNamespaceLocked(ns, true)
				if nsInfo != nil {
					defer nsUnlock()
					if nsInfo.addressSet != nil {
						klog.V(5).Infof("Skipping deferred deletion of AddressSet for NS %s: re-created", ns)
						return
					}
				}

				klog.V(5).Infof("Finishing deferred deletion of AddressSet for NS %s", ns)
				if err := addressSet.Destroy(); err != nil {
					klog.Errorf("Failed to delete AddressSet for NS %s: %v", ns, err.Error())
				}
			}
		}()
	}
	delete(bnc.namespaces, ns)

	return nsInfo
}

// WatchNodes starts the watching of the nodes resource and calls back the appropriate handler logic
func (bnc *BaseNetworkController) WatchNodes() error {
	if bnc.nodeHandler != nil {
		return nil
	}

	handler, err := bnc.retryNodes.WatchResource()
	if err == nil {
		bnc.nodeHandler = handler
	}
	return err
}

func (bnc *BaseNetworkController) recordNodeErrorEvent(node *kapi.Node, nodeErr error) {
	if bnc.IsSecondary() {
		// TBD, no op for secondary network for now
		return
	}
	nodeRef, err := ref.GetReference(scheme.Scheme, node)
	if err != nil {
		klog.Errorf("Couldn't get a reference to node %s to post an event: %v", node.Name, err)
		return
	}

	klog.V(5).Infof("Posting %s event for Node %s: %v", kapi.EventTypeWarning, node.Name, nodeErr)
	bnc.recorder.Eventf(nodeRef, kapi.EventTypeWarning, "ErrorReconcilingNode", nodeErr.Error())
}

func (bnc *BaseNetworkController) doesNetworkRequireIPAM() bool {
	return util.DoesNetworkRequireIPAM(bnc.NetInfo)
}

func (bnc *BaseNetworkController) buildPortGroup(hashName, name string, ports []*nbdb.LogicalSwitchPort, acls []*nbdb.ACL) *nbdb.PortGroup {
	externalIds := map[string]string{"name": name}
	if bnc.IsSecondary() {
		externalIds[types.NetworkExternalID] = bnc.GetNetworkName()
	}
	return libovsdbops.BuildPortGroup(hashName, ports, acls, externalIds)
}

func (bnc *BaseNetworkController) getPodNADNames(pod *kapi.Pod) []string {
	if !bnc.IsSecondary() {
		return []string{types.DefaultNetworkName}
	}
	podNadNames, _ := util.PodNadNames(pod, bnc.NetInfo)
	return podNadNames
}

// getClusterPortGroupName gets network scoped port group hash name; base is either
// ClusterPortGroupNameBase or ClusterRtrPortGroupNameBase.
func (bnc *BaseNetworkController) getClusterPortGroupName(base string) string {
	if bnc.IsSecondary() {
		return libovsdbutil.HashedPortGroup(bnc.GetNetworkName()) + "_" + base
	}
	return base
}

// GetLocalZoneNodes returns the list of local zone nodes
// A node is considered a local zone node if the zone name
// set in the node's annotation matches with the zone name
// set in the OVN Northbound database (to which this controller is connected to).
func (bnc *BaseNetworkController) GetLocalZoneNodes() ([]*kapi.Node, error) {
	nodes, err := bnc.watchFactory.GetNodes()
	if err != nil {
		return nil, fmt.Errorf("failed to get nodes: %v", err)
	}

	var zoneNodes []*kapi.Node
	for _, n := range nodes {
		if bnc.isLocalZoneNode(n) {
			zoneNodes = append(zoneNodes, n)
		}
	}

	return zoneNodes, nil
}

// isLocalZoneNode returns true if the node is part of the local zone.
func (bnc *BaseNetworkController) isLocalZoneNode(node *kapi.Node) bool {
	/** HACK BEGIN **/
	// TODO(tssurya): Remove this HACK a few months from now. This has been added only to
	// minimize disruption for upgrades when moving to interconnect=true.
	// We want the legacy ovnkube-master to wait for remote ovnkube-node to
	// signal it using "k8s.ovn.org/remote-zone-migrated" annotation before
	// considering a node as remote when we upgrade from "global" (1 zone IC)
	// zone to multi-zone. This is so that network disruption for the existing workloads
	// is negligible and until the point where ovnkube-node flips the switch to connect
	// to the new SBDB, it would continue talking to the legacy RAFT ovnkube-sbdb to ensure
	// OVN/OVS flows are intact.
	if bnc.zone == types.OvnDefaultZone {
		return !util.HasNodeMigratedZone(node)
	}
	/** HACK END **/
	return util.GetNodeZone(node) == bnc.zone
}

func (bnc *BaseNetworkController) isLayer2Interconnect() bool {
	return config.OVNKubernetesFeature.EnableInterconnect && bnc.NetInfo.TopologyType() == types.Layer2Topology
}

func (bnc *BaseNetworkController) nodeZoneClusterChanged(oldNode, newNode *kapi.Node, newNodeIsLocalZone bool) bool {
	// Check if the annotations have changed. Use network topology and local params to skip unecessary checks

	// NodeIDAnnotationChanged and NodeTransitSwitchPortAddrAnnotationChanged affects local and remote nodes
	if util.NodeIDAnnotationChanged(oldNode, newNode) {
		return true
	}

	if util.NodeTransitSwitchPortAddrAnnotationChanged(oldNode, newNode) {
		return true
	}

	// NodeGatewayRouterLRPAddrAnnotationChanged would not affect local, nor layer3 secondary network
	if !newNodeIsLocalZone && !bnc.IsSecondary() && util.NodeGatewayRouterLRPAddrAnnotationChanged(oldNode, newNode) {
		return true
	}

	return false
}

// WatchAdminPolicyBasedRoutes starts the watching of adminpolicybasedroute resource and calls
// back the appropriate handler logic
func (bnc *BaseNetworkController) WatchAdminPolicyBasedRoutes() (err error) {
	if bnc.adminPBRNamespaceHandler != nil {
		// Called during retry, but WatchAdminPolicyBasedRoutes() has succeeded before, nothing to do
		return nil
	}
	start := time.Now()
	if !bnc.IsSecondary() {
		// delete logical router policies created by egressip since they would block rerouting between pods
		if err := bnc.deleteLogicalRouterPoliciesByPriority(types.DefaultNoRereoutePriority); err != nil {
			if err != libovsdbclient.ErrNotFound {
				return fmt.Errorf("failed to clean up egressip default noreroute policies: %v", err)
			}
		}
		if err := bnc.noRerouteToJoinSubnet(); err != nil {
			return fmt.Errorf("failed to create router policy to skip AdminPBR rules for join switch subnet: %v", err)
		}
	}
	defer func() {
		if err != nil {
			if bnc.adminPBRNamespaceHandler != nil {
				bnc.watchFactory.RemoveNamespaceHandler(bnc.adminPBRNamespaceHandler)
			}
			if bnc.adminPBRNodeHandler != nil {
				bnc.watchFactory.RemoveNodeHandler(bnc.adminPBRNodeHandler)
			}
			if bnc.adminPBRHandler != nil {
				bnc.watchFactory.RemoveAdminPBRHandler(bnc.adminPBRHandler)
			}
			if bnc.adminPBRRetryQueue != nil {
				bnc.adminPBRRetryQueue.ShutDown()
			}
			bnc.adminPBRRetryQueue = nil
			bnc.adminPBRHandler = nil
			bnc.adminPBRNodeHandler = nil
			bnc.adminPBRNamespaceHandler = nil
		}
	}()

	if !bnc.IsSecondary() || bnc.TopologyType() == types.Layer3Topology {
		bnc.adminPBRRetryQueue = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "adminpbr")
	}
	filterAdminPBR := func(obj interface{}) bool {
		apbr, ok := obj.(*adminpbrapi.AdminPolicyBasedRoute)
		if !ok {
			return false
		}
		return bnc.HasNAD(apbr.Spec.NetworkAttachmentName)
	}
	bnc.adminPBRHandler, err = bnc.watchFactory.AddHandlerWithFilterFunc(reflect.TypeOf(&adminpbrapi.AdminPolicyBasedRoute{}), filterAdminPBR, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			apbr, ok := obj.(*adminpbrapi.AdminPolicyBasedRoute)
			if !ok {
				klog.Errorf("Not an AdminPolicyBasedRoute object: %v", apbr)
				return
			}
			bnc.onAdminPBRAddOrUpdate(apbr)
		},
		UpdateFunc: func(old, new interface{}) {
			if bnc.IsSecondary() && bnc.TopologyType() != types.Layer3Topology {
				klog.V(5).Infof("Skipping AdminPBR event since network %s is of topology %s",
					bnc.GetNetworkName(), bnc.TopologyType())
				return
			}
			oldPolicy, ok := old.(*adminpbrapi.AdminPolicyBasedRoute)
			if !ok {
				klog.Errorf("Old object is not an AdminPolicyBasedRoute object: %v", oldPolicy)
				return
			}
			newPolicy, ok := new.(*adminpbrapi.AdminPolicyBasedRoute)
			if !ok {
				klog.Errorf("New object is not an AdminPolicyBasedRoute object: %v", newPolicy)
				return
			}
			// object is marked for deletion, don't do anything
			if !newPolicy.DeletionTimestamp.IsZero() {
				return
			}
			if !reflect.DeepEqual(oldPolicy.Spec, newPolicy.Spec) {
				bnc.onAdminPBRAddOrUpdate(newPolicy)
			}
		},
		DeleteFunc: func(obj interface{}) {
			if bnc.IsSecondary() && bnc.TopologyType() != types.Layer3Topology {
				klog.V(5).Infof("Skipping AdminPBR event since network %s is of topology %s",
					bnc.GetNetworkName(), bnc.TopologyType())
				return
			}
			apbr, ok := obj.(*adminpbrapi.AdminPolicyBasedRoute)
			if !ok {
				klog.Errorf("Not an AdminPolicyBasedRoute object: %v", apbr)
				return
			}
			bnc.onAdminPBRDelete(apbr)
		},
	}, nil, bnc.watchFactory.GetHandlerPriority(factory.AdminPBRType))
	if err != nil {
		return fmt.Errorf("failed to watch for AdminPolicyBasedRoute CRD for network %s", bnc.GetNetworkName())
	}

	if bnc.IsSecondary() && bnc.TopologyType() != types.Layer3Topology {
		klog.V(4).Infof("Skip periodical sync for network %s of topology type %s", bnc.GetNetworkName(), bnc.TopologyType())
		return nil
	}

	bnc.adminPBRNodeHandler, err = bnc.watchFactory.AddNodeHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			bnc.syncAdminPBROnNodeChange(nil, obj)
		},
		UpdateFunc: func(old, new interface{}) {
			bnc.syncAdminPBROnNodeChange(old, new)
		},
		DeleteFunc: func(obj interface{}) {},
	}, nil, 1 /* TBD: set priority */)
	if err != nil {
		return fmt.Errorf("failed to watch for nodes for AdminPolicyBasedRoute for network %s", bnc.GetNetworkName())
	}

	bnc.adminPBRNamespaceHandler, err = bnc.watchFactory.AddNamespaceHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			bnc.syncAdminPBROnNamespaceChange(nil, obj)
		},
		UpdateFunc: func(old, new interface{}) {
			bnc.syncAdminPBROnNamespaceChange(old, new)
		},
		DeleteFunc: func(obj interface{}) {},
	}, nil /* TBD: set priority */)
	if err != nil {
		return fmt.Errorf("failed to watch for namespaces for AdminPolicyBasedRoute for network %s", bnc.GetNetworkName())
	}

	klog.Infof("Bootstrapping existing adminpbrs and cleaning stale adminpbrs for network %s took %v", bnc.GetNetworkName(), time.Since(start))
	go func() {
		ticker := time.NewTicker(types.AdminPBRResyncInterval)
		for {
			select {
			case <-ticker.C:
				bnc.syncAdminPBRPeriodic()
				bnc.syncAddressSetPeriodic()
			case <-bnc.stopChan:
				ticker.Stop()
				bnc.adminPBRRetryQueue.ShutDown()
				return
			}
		}
	}()
	go func() {
		for bnc.retryAdminPBROperations() {
		}
	}()
	return nil
}

// WatchVirtualIPs starts the watching of virtual-ip resources and calls
// back the appropriate handler logic
func (bnc *BaseNetworkController) WatchVirtualIPs() (err error) {
	if bnc.virtualIPHandler != nil {
		// WatchVirtualIPs has succeeded and this is from retry
		return nil
	}
	defer func() {
		if err != nil {
			if bnc.vipSBClient != nil {
				bnc.vipSBClient.Close()
			}
			if bnc.virtualIPHandler != nil {
				bnc.watchFactory.RemoveVirtualIPHandler(bnc.virtualIPHandler)
			}
			if bnc.virtualIPRetryQueue != nil {
				bnc.virtualIPRetryQueue.ShutDown()
			}
			bnc.virtualIPHandler = nil
			bnc.vipSBClient = nil
			bnc.virtualIPRetryQueue = nil
		}
	}()
	start := time.Now()
	if bnc.TopologyType() == types.Layer2Topology {
		bnc.virtualIPRetryQueue = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "virtualIP")
	}
	// filterVirtualIP checks if the virtualIP nad belongs to this controller and
	filterVirtualIP := func(obj interface{}) bool {
		virtIP, ok := obj.(*virtualip.VirtualIP)
		if !ok {
			return false
		}
		return bnc.HasNAD(virtIP.Spec.NetworkAttachmentName)
	}
	// creates corresponding add/update/delete handlers
	bnc.virtualIPHandler, err = bnc.watchFactory.AddHandlerWithFilterFunc(reflect.TypeOf(&virtualip.VirtualIP{}), filterVirtualIP, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			virtIP := obj.(*virtualip.VirtualIP)
			if bnc.TopologyType() != types.Layer2Topology {
				errMsg := fmt.Sprintf("VirtualIP's network-attachment-defintion %s is not %s type",
					virtIP.Spec.NetworkAttachmentName, types.Layer2Topology)
				err = bnc.updateVirtualIPStatusWithRetry(virtIP.Namespace, virtIP.Name, types.OvnK8sStatusFailed,
					[]string{errMsg}, nil, nil, nil)
			} else {
				err = bnc.addVirtualIP(virtIP)
			}
			if err != nil {
				klog.Errorf(err.Error())
				bnc.recordVirtualIPEvent("VirtualIPAddError", err.Error(), virtIP)
			}
		},
		UpdateFunc: func(old, newer interface{}) {
			if bnc.TopologyType() == types.Layer2Topology {
				oldVirtIP := old.(*virtualip.VirtualIP)
				newVirtIP := newer.(*virtualip.VirtualIP)
				// only compare spec changes as we constantly do updates for
				// virtualIP status.
				if !reflect.DeepEqual(oldVirtIP.Spec, newVirtIP.Spec) {
					if err := bnc.deleteVirtualIP(oldVirtIP); err != nil {
						klog.Errorf(err.Error())
					}
					if err := bnc.addVirtualIP(newVirtIP); err != nil {
						klog.Errorf(err.Error())
					}
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			if bnc.TopologyType() == types.Layer2Topology {
				virtIP := obj.(*virtualip.VirtualIP)
				if err := bnc.deleteVirtualIP(virtIP); err != nil {
					klog.Error(err)
				}
			}
		},
	}, nil, bnc.watchFactory.GetHandlerPriority(factory.VirtualIPType))
	if err != nil {
		return err
	}

	if bnc.TopologyType() == types.Layer2Topology {
		go func() {
			ticker := time.NewTicker(types.VirtualIPResyncInterval)
			for {
				select {
				case <-ticker.C:
					bnc.syncVirtualIPsPeriodic()
				case <-bnc.stopChan:
					ticker.Stop()
					bnc.virtualIPRetryQueue.ShutDown()
					return
				}
			}
		}()
		// for virtualIPRetry operations
		go func() {
			for bnc.retryVirtualIPOperations() {
			}
		}()
	}

	klog.Infof("Bootstrapping existing virtualIPs and cleaning stale virtualIPs for network %s took %v", bnc.GetNetworkName(), time.Since(start))
	return nil
}

func (bnc *BaseNetworkController) shouldSkipPinnedLS(node *kapi.Node) bool {
	skip, ok := node.Annotations[util.SkipPinnedLSNodeAnnotationName]
	if !ok {
		return false
	}

	nadNames := strings.Split(skip, ",")
	for _, nadName := range nadNames {
		nadName = strings.TrimSpace(nadName)
		if !bnc.IsSecondary() {
			if nadName == types.DefaultNetworkName {
				return true
			}
		} else {
			if bnc.HasNAD(nadName) {
				return true
			}
		}
	}
	return false
}

func (bnc *BaseNetworkController) skipPinnedLSChanged(oldNode, node *kapi.Node) bool {
	oldSkipPinnedLS := bnc.shouldSkipPinnedLS(oldNode)
	newSkipPinnedLS := bnc.shouldSkipPinnedLS(node)
	return oldSkipPinnedLS != newSkipPinnedLS
}

// WatchIPReservations starts the watching of ipreservation resources and calls
// back the appropriate handler logic, called by non-secondary-layer2/localnet networks
func (bnc *BaseNetworkController) WatchIPReservations() (err error) {
	if bnc.ipReserveHandler != nil {
		// WatchIPReservations has succeeded and this is from retry, nothing to do
		return nil
	}
	start := time.Now()

	// filterIPReservation checks if the ipReservation's NAD belongs to this controller
	filterIPReservation := func(obj interface{}) bool {
		ipResv, ok := obj.(*ipreservation.IPReservation)
		if !ok {
			return false
		}
		return bnc.HasNAD(ipResv.Spec.NetworkAttachmentName)
	}
	bnc.ipReserveHandler, err = bnc.watchFactory.AddHandlerWithFilterFunc(reflect.TypeOf(&ipreservation.IPReservation{}), filterIPReservation,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				resvIPObj := obj.(*ipreservation.IPReservation)
				err := fmt.Errorf("the networkAttachmentName, %s, of IPReservation %s/%s object is not a L2 or Localnet network type",
					resvIPObj.Spec.NetworkAttachmentName, resvIPObj.Namespace, resvIPObj.Name)
				klog.Errorf(err.Error())
				tmpErr := bnc.updateIPReservationStatusWithRetry(resvIPObj.Namespace, resvIPObj.Name, types.OvnK8sStatusFailed,
					[]string{err.Error()}, nil)
				if tmpErr != nil {
					klog.Errorf(tmpErr.Error())
				}
			},
			UpdateFunc: func(old, newer interface{}) {
				oldResvIPObj := old.(*ipreservation.IPReservation)
				newResvIPObj := newer.(*ipreservation.IPReservation)
				if !reflect.DeepEqual(oldResvIPObj.Spec, newResvIPObj.Spec) {
					bnc.recordIPReservationEvent("IPReservationUpdateError", "Updating IPReservation object is not supported",
						oldResvIPObj)
				}
			},
			DeleteFunc: func(obj interface{}) {
			},
		}, nil, 1 /* TBD: set priority */)
	if err != nil {
		return fmt.Errorf("failed to watch for IPReservations CRD for network %s", bnc.GetNetworkName())
	}

	klog.Infof("Bootstrapping existing ipreservations and cleaning stale ipreservations for network %s took %v",
		bnc.GetNetworkName(), time.Since(start))
	return nil
}

func (bnc *BaseNetworkController) ConnectToNetworks(logicalSwitch *nbdb.LogicalSwitch, logicalRouter *nbdb.LogicalRouter,
	layer2ClusterSubnets []config.CIDRNetworkEntry) error {
	var nodeLRPMAC net.HardwareAddr
	var nodeLRPMACFound bool

	klog.Infof("Connect two networks, switch %s with subnets %v connects to router %s ",
		logicalSwitch.Name, layer2ClusterSubnets, logicalRouter.Name)
	lrpNetworks := []string{}
	for _, hostSubnet := range layer2ClusterSubnets {
		gwIfAddr := util.GetNodeGatewayIfAddr(hostSubnet.CIDR)
		lrpNetworks = append(lrpNetworks, gwIfAddr.String())
		if !nodeLRPMACFound && !utilnet.IsIPv6CIDR(hostSubnet.CIDR) {
			nodeLRPMAC = util.IPAddrToHWAddr(gwIfAddr.IP)
			nodeLRPMACFound = true
		}
	}

	// Connect the switch to the router
	logicalSwitchPort := nbdb.LogicalSwitchPort{
		Name:      types.SwitchToRouterPrefix + logicalSwitch.Name,
		Type:      "router",
		Addresses: []string{"router"},
		Options:   map[string]string{"router-port": types.RouterToSwitchPrefix + logicalSwitch.Name},
	}
	err := libovsdbops.CreateOrUpdateLogicalSwitchPortsOnSwitch(bnc.nbClient, logicalSwitch, &logicalSwitchPort)
	if err != nil {
		return fmt.Errorf("failed to add logical port %+v to switch %s: %v", logicalSwitchPort, logicalSwitch.Name, err)
	}

	lrpName := types.RouterToSwitchPrefix + logicalSwitch.Name
	logicalRouterPort := nbdb.LogicalRouterPort{
		Name:     lrpName,
		MAC:      nodeLRPMAC.String(),
		Networks: lrpNetworks,
	}
	err = libovsdbops.CreateOrUpdateLogicalRouterPort(bnc.nbClient, logicalRouter, &logicalRouterPort, nil,
		&logicalRouterPort.MAC, &logicalRouterPort.Networks)
	if err != nil {
		return fmt.Errorf("failed to add logical router port %s, error: %v", lrpName, err)
	}
	return nil
}

func (bnc *BaseNetworkController) DisconnectFromNetworks(logicalSwitch *nbdb.LogicalSwitch, logicalRouter *nbdb.LogicalRouter) error {
	klog.Infof("Disconnect two networks, switch %s disconnects from router %s ",
		logicalSwitch.Name, logicalRouter.Name)

	logicalRouterPort := nbdb.LogicalRouterPort{
		Name: types.RouterToSwitchPrefix + logicalSwitch.Name,
	}
	err := libovsdbops.DeleteLogicalRouterPorts(bnc.nbClient, logicalRouter, &logicalRouterPort)
	if err != nil {
		return fmt.Errorf("failed to delete router port %s: %v", logicalRouterPort.Name, err)
	}

	logicalSwitchPort := nbdb.LogicalSwitchPort{Name: types.SwitchToRouterPrefix + logicalSwitch.Name}
	err = libovsdbops.DeleteLogicalSwitchPorts(bnc.nbClient, logicalSwitch, &logicalSwitchPort)
	if err != nil {
		return fmt.Errorf("failed to delete logical switch port %s from switch %s: %v", logicalSwitchPort.Name, logicalSwitch.Name, err)
	}
	return err
}
