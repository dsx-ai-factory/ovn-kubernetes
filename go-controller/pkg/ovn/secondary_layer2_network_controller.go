package ovn

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/allocator/pod"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	addressset "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/address_set"
	lsm "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/logical_switch_manager"
	zoneinterconnect "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/zone_interconnect"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/syncmap"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"
)

// SecondaryLayer2NetworkController is created for logical network infrastructure and policy
// for a secondary layer2 network
type SecondaryLayer2NetworkController struct {
	BaseSecondaryLayer2NetworkController
}

// NewSecondaryLayer2NetworkController create a new OVN controller for the given secondary layer2 nad
func NewSecondaryLayer2NetworkController(cnci *CommonNetworkControllerInfo, netInfo util.NetInfo) *SecondaryLayer2NetworkController {

	stopChan := make(chan struct{})

	ipv4Mode, ipv6Mode := netInfo.IPMode()
	addressSetFactory := addressset.NewOvnAddressSetFactory(cnci.nbClient, ipv4Mode, ipv6Mode)

	oc := &SecondaryLayer2NetworkController{
		BaseSecondaryLayer2NetworkController: BaseSecondaryLayer2NetworkController{
			BaseSecondaryNetworkController: BaseSecondaryNetworkController{
				BaseNetworkController: BaseNetworkController{
					CommonNetworkControllerInfo: *cnci,
					controllerName:              util.GetClusterScopedName(netInfo.GetNetworkName() + "-network-controller"),
					NetInfo:                     netInfo,
					lsManager:                   lsm.NewL2SwitchManager(),
					logicalPortCache:            newPortCache(stopChan),
					namespaces:                  make(map[string]*namespaceInfo),
					namespacesMutex:             sync.Mutex{},
					addressSetFactory:           addressSetFactory,
					networkPolicies:             syncmap.NewSyncMap[*networkPolicy](),
					sharedNetpolPortGroups:      syncmap.NewSyncMap[*defaultDenyPortGroups](),
					podSelectorAddressSets:      syncmap.NewSyncMap[*PodSelectorAddressSet](),
					stopChan:                    stopChan,
					wg:                          &sync.WaitGroup{},
					localZoneNodes:              &sync.Map{},
					cancelableCtx:               util.NewCancelableContext(),
				},
			},
		},
	}

	if config.OVNKubernetesFeature.EnableInterconnect {
		oc.zoneICHandler = zoneinterconnect.NewZoneInterconnectHandler(oc.NetInfo, oc.nbClient, oc.sbClient, oc.watchFactory)
	}

	if oc.allocatesPodAnnotation() {
		podAnnotationAllocator := pod.NewPodAnnotationAllocator(
			netInfo,
			cnci.watchFactory.PodCoreInformer().Lister(),
			cnci.kube)
		oc.podAnnotationAllocator = podAnnotationAllocator
	}

	// disable multicast support for secondary networks
	// TBD: changes needs to be made to support multicast in secondary networks
	oc.multicastSupport = false

	oc.initRetryFramework()
	return oc
}

// Start starts the secondary layer2 controller, handles all events and creates all needed logical entities
func (oc *SecondaryLayer2NetworkController) Start(ctx context.Context) error {
	klog.Infof("Start secondary %s network controller of network %s", oc.TopologyType(), oc.GetNetworkName())

	start := time.Now()
	defer func() {
		klog.Infof("Starting controller for secondary network %s took %v", oc.GetNetworkName(), time.Since(start))
	}()

	err := oc.syncOVNLogicalEntities()
	if err != nil {
		return err
	}

	if err := oc.Init(); err != nil {
		return err
	}

	return oc.run(ctx)
}

func (oc *SecondaryLayer2NetworkController) run(ctx context.Context) error {
	return oc.BaseSecondaryLayer2NetworkController.run()
}

// Cleanup cleans up logical entities for the given network, called from net-attach-def routine
// could be called from a dummy Controller (only has CommonNetworkControllerInfo set)
func (oc *SecondaryLayer2NetworkController) Cleanup(netName string) error {
	klog.Infof("Delete OVN logical entities for network %s", netName)
	return oc.BaseSecondaryLayer2NetworkController.cleanup(types.Layer2Topology, netName)
}

func (oc *SecondaryLayer2NetworkController) Init() error {
	switchName := util.GetClusterScopedName(oc.GetNetworkScopedName(types.OVNLayer2Switch))

	_, err := oc.initializeLogicalSwitch(switchName, oc.Subnets(), oc.ExcludeSubnets())
	if err != nil {
		return err
	}
	// for layer 2 network which is to be connected to a layer3 network, reserve its first IP in the subnet
	// as gatewayIP
	if oc.NADToInterConnect() != "" {
		for _, hostSubnet := range oc.Subnets() {
			var ipMask net.IPMask
			gatewayIPnet := util.GetNodeGatewayIfAddr(hostSubnet.CIDR)
			// this IP may already be in the list of excluded subnets configured in the NAD.
			// ErrAllocated will be returned, ignore this error.
			if utilnet.IsIPv6(gatewayIPnet.IP) {
				ipMask = net.CIDRMask(128, 128)
			} else {
				ipMask = net.CIDRMask(32, 32)
			}
			_ = oc.lsManager.AllocateIPs(switchName, []*net.IPNet{{IP: gatewayIPnet.IP, Mask: ipMask}})
		}
	}
	return nil
}

func (oc *SecondaryLayer2NetworkController) StartInterConnect(icInfo *util.InterConnectInfo) error {
	layer2ClusterSubnets := oc.Subnets()
	switchName := util.GetClusterScopedName(oc.GetNetworkScopedName(types.OVNLayer2Switch))
	logicalSwitch := &nbdb.LogicalSwitch{Name: switchName}
	logicalRouter, ok := icInfo.LogicalEntityToConnect.(*nbdb.LogicalRouter)
	if !ok {
		// configuration error, no retry
		klog.Errorf("Inter-connect error: network %s can only connect to layer 3 network", oc.GetNetworkName())
		return nil
	}
	return oc.ConnectToNetworks(logicalSwitch, logicalRouter, layer2ClusterSubnets)
}

func (oc *SecondaryLayer2NetworkController) StopInterConnect(icInfo *util.InterConnectInfo) error {
	logicalRouter, ok := icInfo.LogicalEntityToConnect.(*nbdb.LogicalRouter)
	if !ok {
		// configuration error, no retry
		klog.Errorf("Inter-connect error: network %s can only connect to layer 3 network", oc.GetNetworkName())
		return nil
	}
	switchName := util.GetClusterScopedName(oc.GetNetworkScopedName(types.OVNLayer2Switch))
	logicalSwitch := &nbdb.LogicalSwitch{Name: switchName}
	return oc.DisconnectFromNetworks(logicalSwitch, logicalRouter)
}

func (oc *SecondaryLayer2NetworkController) Stop() {
	klog.Infof("Stoping controller for secondary network %s", oc.GetNetworkName())
	oc.BaseSecondaryLayer2NetworkController.stop()
}

func (oc *SecondaryLayer2NetworkController) initRetryFramework() {
	oc.BaseSecondaryLayer2NetworkController.initRetryFramework()
}
