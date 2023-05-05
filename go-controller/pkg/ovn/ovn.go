package ovn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"sync"
	"time"

	ctypes "github.com/containernetworking/cni/pkg/types"
	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	libovsdbclient "github.com/ovn-org/libovsdb/client"
	hocontroller "github.com/ovn-org/ovn-kubernetes/go-controller/hybrid-overlay/pkg/controller"
	cnitypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdbops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/metrics"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	addressset "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/address_set"
	svccontroller "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/controller/services"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/controller/unidling"
	corev1listers "k8s.io/client-go/listers/core/v1"

	lsm "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/logical_switch_manager"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/subnetallocator"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/sbdb"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	adminpbrapi "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/adminpbr/v1beta1"

	virtualip "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/virtualip/v1beta1"
	utilnet "k8s.io/utils/net"

	egressqoslisters "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/egressqos/v1/apis/listers/egressqos/v1"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	ref "k8s.io/client-go/tools/reference"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const (
	egressFirewallDNSDefaultDuration time.Duration = 30 * time.Minute
)

// ACL logging severity levels
type ACLLoggingLevels struct {
	Allow string `json:"allow,omitempty"`
	Deny  string `json:"deny,omitempty"`
}

// namespaceInfo contains information related to a Namespace. Use oc.getNamespaceLocked()
// or oc.waitForNamespaceLocked() to get a locked namespaceInfo for a Namespace, and call
// nsInfo.Unlock() on it when you are done with it. (No code outside of the code that
// manages the oc.namespaces map is ever allowed to hold an unlocked namespaceInfo.)
type namespaceInfo struct {
	util.NetNameInfo

	sync.RWMutex

	// addressSet is an address set object that holds the IP addresses
	// of all pods in the namespace.
	addressSet           addressset.AddressSet
	nodeHostNetPodsCache map[string]map[string][]net.IP

	// map from NetworkPolicy name to networkPolicy. You must hold the
	// namespaceInfo's mutex to add/delete/lookup policies, but must hold the
	// networkPolicy's mutex (and not necessarily the namespaceInfo's) to work with
	// the policy itself.
	networkPolicies map[string]*networkPolicy

	// routingExternalGWs is a slice of net.IP containing the values parsed from
	// annotation k8s.ovn.org/routing-external-gws
	routingExternalGWs gatewayInfo

	// routingExternalPodGWs contains a map of all pods serving as exgws as well as their
	// exgw IPs
	// key is <namespace>_<pod name>
	routingExternalPodGWs map[string]gatewayInfo

	multicastEnabled bool

	// If not empty, then it has to be set to a logging a severity level, e.g. "notice", "alert", etc
	aclLogging ACLLoggingLevels

	// Per-namespace port group default deny UUIDs
	portGroupIngressDenyName string // Port group Name for ingress deny rule, without network prefix
	portGroupEgressDenyName  string // Port group Name for egress deny rule, without network prefix
}

// multihome controller
type OvnMHController struct {
	client       clientset.Interface
	kube         kube.Interface
	watchFactory *factory.WatchFactory
	wg           *sync.WaitGroup
	stopChan     chan struct{}
	identity     string
	podRecorder  metrics.PodRecorder

	// event recorder used to post events to k8s
	recorder record.EventRecorder

	// libovsdb northbound client interface
	nbClient libovsdbclient.Client

	// libovsdb southbound client interface
	sbClient libovsdbclient.Client

	// default network controller
	ovnController *Controller
	// controller for all networks including default and non default networks,
	// key is netName of net-attach-def, value is *Controller
	allOvnControllers sync.Map

	// key nadName to connect to, value is map[<layer2_controller>]<connected_logical_routername>,
	// if connected_logical_routername is empty, it means they have  not yet been connected successfully
	//
	// access/update of this map is serialized as result of net-attach-def add/delete handling, no lock needed
	nadConnInfoMap map[string]map[*Controller]string
}

// Controller structure is the object which holds the controls for starting
// and reacting upon the watched resources (e.g. pods, endpoints)
type Controller struct {
	mc                        *OvnMHController
	wg                        *sync.WaitGroup
	stopChan                  chan struct{}
	podHandler                *factory.Handler
	nodeHandler               *factory.Handler
	namespaceHandler          *factory.Handler
	multiNetworkPolicyHandler *factory.Handler
	isStarted                 bool
	startMutex                sync.Mutex

	nadInfo *util.NetAttachDefInfo

	// configured cluster subnets
	clusterSubnets []config.CIDRNetworkEntry
	// FIXME DUAL-STACK -  Make IP Allocators more dual-stack friendly
	masterSubnetAllocator *subnetallocator.SubnetAllocator

	hoMaster *hocontroller.MasterController

	SCTPSupport bool

	// For TCP, UDP, and SCTP type traffic, cache OVN load-balancers used for the
	// cluster's east-west traffic.
	loadbalancerClusterCache map[kapi.Protocol]string

	// A cache of all logical switches seen by the watcher and their subnets
	lsManager *lsm.LogicalSwitchManager

	// A cache of all logical ports known to the controller
	logicalPortCache *portCache

	// Info about known namespaces. You must use oc.getNamespaceLocked() or
	// oc.waitForNamespaceLocked() to read this map, and oc.createNamespaceLocked()
	// or oc.deleteNamespaceLocked() to modify it. namespacesMutex is only held
	// from inside those functions.
	namespaces      map[string]*namespaceInfo
	namespacesMutex sync.Mutex

	externalGWCache map[ktypes.NamespacedName]*externalRouteInfo
	exGWCacheMutex  sync.RWMutex

	// egressFirewalls is a map of namespaces and the egressFirewall attached to it
	egressFirewalls sync.Map

	// EgressQoS
	egressQoSLister egressqoslisters.EgressQoSLister
	egressQoSSynced cache.InformerSynced
	egressQoSQueue  workqueue.RateLimitingInterface
	egressQoSCache  sync.Map

	egressQoSPodLister corev1listers.PodLister
	egressQoSPodSynced cache.InformerSynced
	egressQoSPodQueue  workqueue.RateLimitingInterface

	egressQoSNodeLister corev1listers.NodeLister
	egressQoSNodeSynced cache.InformerSynced
	egressQoSNodeQueue  workqueue.RateLimitingInterface

	// An address set factory that creates address sets
	addressSetFactory addressset.AddressSetFactory

	// For each logical port, the number of network policies that want
	// to add an ingress deny rule.
	lspIngressDenyCache map[string]int

	// For each logical port, the number of network policies that want
	// to add an egress deny rule.
	lspEgressDenyCache map[string]int

	// A mutex for lspIngressDenyCache and lspEgressDenyCache
	lspMutex *sync.Mutex

	// Supports multicast?
	multicastSupport bool

	// Cluster wide Load_Balancer_Group UUID.
	loadBalancerGroupUUID string

	// Cluster-wide router default Control Plane Protection (COPP) UUID
	defaultCOPPUUID string

	// Controller used for programming OVN for egress IP
	eIPC egressIPController

	// Controller used to handle services
	svcController *svccontroller.Controller
	// svcFactory used to handle service related events
	svcFactory informers.SharedInformerFactory

	egressFirewallDNS *EgressDNS

	// Is ACL logging enabled while configuring meters?
	aclLoggingEnabled bool

	joinSwIPManager *lsm.JoinSwitchIPManager

	// v4HostSubnetsUsed keeps track of number of v4 subnets currently assigned to nodes
	v4HostSubnetsUsed float64

	// v6HostSubnetsUsed keeps track of number of v6 subnets currently assigned to nodes
	v6HostSubnetsUsed float64

	// Objects for pods that need to be retried
	retryPods *retryObjs

	// Objects for network policies that need to be retried
	retryNetworkPolicies *retryObjs

	// Objects for egress firewall that need to be retried
	retryEgressFirewalls *retryObjs

	// Objects for egress IP that need to be retried
	retryEgressIPs *retryObjs
	// Objects for egress IP Namespaces that need to be retried
	retryEgressIPNamespaces *retryObjs
	// Objects for egress IP Pods that need to be retried
	retryEgressIPPods *retryObjs
	// Objects for Egress nodes that need to be retried
	retryEgressNodes *retryObjs
	// EgressIP Node-specific syncMap used by egressip node event handler
	addEgressNodeFailed sync.Map
	// Objects for nodes that need to be retried
	retryNodes *retryObjs
	// Objects for Cloud private IP config that need to be retried
	retryCloudPrivateIPConfig *retryObjs
	// Node-specific syncMap used by node event handler
	gatewaysFailed              sync.Map
	mgmtPortFailed              sync.Map
	addNodeFailed               sync.Map
	nodeClusterRouterPortFailed sync.Map

	adminPBRHandler          *factory.Handler
	adminPBRNodeHandler      *factory.Handler
	adminPBRNamespaceHandler *factory.Handler
	// map of admin pbr policies
	adminPBRStore      sync.Map
	adminPBRRetryQueue workqueue.RateLimitingInterface

	// map & workqueue for virtualIP operations
	virtualIPHandler    *factory.Handler
	virtualIPs          sync.Map
	virtualIPRetryQueue workqueue.RateLimitingInterface
}

const (
	// TCP is the constant string for the string "TCP"
	TCP = "TCP"

	// UDP is the constant string for the string "UDP"
	UDP = "UDP"

	// SCTP is the constant string for the string "SCTP"
	SCTP = "SCTP"
)

func GetIPFullMask(ip string) string {
	const (
		// IPv4FullMask is the maximum prefix mask for an IPv4 address
		IPv4FullMask = "/32"
		// IPv6FullMask is the maxiumum prefix mask for an IPv6 address
		IPv6FullMask = "/128"
	)

	if utilnet.IsIPv6(net.ParseIP(ip)) {
		return IPv6FullMask
	}
	return IPv4FullMask
}

func NewOvnMHController(ovnClient *util.OVNClientset, identity string, wf *factory.WatchFactory,
	stopChan chan struct{}, libovsdbOvnNBClient libovsdbclient.Client, libovsdbOvnSBClient libovsdbclient.Client,
	recorder record.EventRecorder, wg *sync.WaitGroup) *OvnMHController {
	return &OvnMHController{
		client: ovnClient.KubeClient,
		kube: &kube.Kube{
			KClient:              ovnClient.KubeClient,
			EIPClient:            ovnClient.EgressIPClient,
			EgressFirewallClient: ovnClient.EgressFirewallClient,
			CloudNetworkClient:   ovnClient.CloudNetworkClient,
			AdminPBRClient:       ovnClient.AdminPBRClient,
			VIPClient:            ovnClient.VirtualIPClient,
		},
		watchFactory:   wf,
		wg:             wg,
		stopChan:       stopChan,
		recorder:       recorder,
		nbClient:       libovsdbOvnNBClient,
		sbClient:       libovsdbOvnSBClient,
		identity:       identity,
		podRecorder:    metrics.NewPodRecorder(),
		nadConnInfoMap: map[string]map[*Controller]string{},
		//addressSetFactory: addressSetFactory,
	}
}

// If the default network net_attach_def does not exist, we'd need to create default OVN Controller based on config.
func (mc *OvnMHController) Init(addressSetFactory addressset.AddressSetFactory) error {
	// default controller already exists, nothing to do.
	if mc.ovnController != nil {
		return nil
	}

	defaultNetConf := &cnitypes.NetConf{
		NetConf: ctypes.NetConf{
			Name: ovntypes.DefaultNetworkName,
		},
		TopoType:    ovntypes.Layer3AttachDefTopoType,
		NetCidr:     config.Default.RawClusterSubnets,
		MTU:         config.Default.MTU,
		IsSecondary: false,
	}
	nadInfo, _ := util.NewNetAttachDefInfo(defaultNetConf)
	_, err := mc.NewOvnController(nadInfo, addressSetFactory)
	if err != nil {
		return err
	}

	// enableOVNLogicalDataPathGroups sets an OVN flag to enable logical datapath
	// groups on OVN 20.12 and later. The option is ignored if OVN doesn't
	// understand it. Logical datapath groups reduce the size of the southbound
	// database in large clusters. ovn-controllers should be upgraded to a version
	// that supports them before the option is turned on by the master.
	nbGlobal := nbdb.NBGlobal{
		Options: map[string]string{"use_logical_dp_groups": "true"},
	}
	if err := libovsdbops.UpdateNBGlobalSetOptions(mc.nbClient, &nbGlobal); err != nil {
		return fmt.Errorf("failed to set NB global option to enable logical datapath groups: %v", err)
	}

	if config.Default.DisableCTInvFlows {
		if err := mc.disableOVNCTInvalidFlows(); err != nil {
			return fmt.Errorf("failed to disable northd from configuring CT Invalid flows: %v", err)
		}
	}

	metrics.StartMasterMetricUpdater(mc.stopChan, mc.sbClient, mc.nbClient)
	if config.Metrics.EnableConfigDuration {
		// with k=10,
		//  for a cluster with 10 nodes, measurement of 1 in every 100 requests
		//  for a cluster with 100 nodes, measurement of 1 in every 1000 requests
		metrics.GetConfigDurationRecorder().Run(mc.nbClient, mc.kube, 10, time.Second*5, mc.stopChan)
	}

	mc.podRecorder.Run(mc.sbClient, mc.stopChan)

	// Start and sync the watch factory to begin listening for events
	if err := mc.watchFactory.Start(); err != nil {
		return err
	}
	return nil
}

// getPodNamespacedName returns logical port name for the provided pod for the specified nad
func getPodNamespacedName(pod *kapi.Pod, nadName string, isDefault bool) string {
	return util.GetLogicalPortName(pod.Namespace, pod.Name, nadName, isDefault)
}

// NewOvnController creates a new OVN controller for creating logical network
// infrastructure and policy
func (mc *OvnMHController) NewOvnController(nadInfo *util.NetAttachDefInfo,
	addressSetFactory addressset.AddressSetFactory) (*Controller, error) {
	if addressSetFactory == nil {
		addressSetFactory = addressset.NewOvnAddressSetFactory(nadInfo.NetNameInfo, mc.nbClient)
	}

	if nadInfo.NetCidr == "" {
		return nil, fmt.Errorf("netcidr: %s is not specified for network %s", nadInfo.NetCidr, nadInfo.NetName)
	}

	checkHostSubnetLength := (nadInfo.TopoType == ovntypes.Layer3AttachDefTopoType)
	clusterIPNet, err := config.ParseClusterSubnetEntries(nadInfo.NetCidr, checkHostSubnetLength)
	if err != nil {
		return nil, fmt.Errorf("cluster subnet %s for network %s is invalid: %v", nadInfo.NetCidr, nadInfo.NetName, err)
	}

	// Sort the list of cluster subnets based on number of host IPs available
	config.SortClusterSubnetEntries(clusterIPNet)

	stopChan := mc.stopChan
	if nadInfo.IsSecondary {
		stopChan = make(chan struct{})
	}
	var lsManager *lsm.LogicalSwitchManager
	if checkHostSubnetLength {
		lsManager = lsm.NewLogicalSwitchManager()
	} else {
		lsManager = lsm.NewLocalnetSwitchManager()
	}
	oc := &Controller{
		mc:                    mc,
		stopChan:              stopChan,
		nadInfo:               nadInfo,
		clusterSubnets:        clusterIPNet,
		masterSubnetAllocator: subnetallocator.NewSubnetAllocator(),
		lsManager:             lsManager,
		logicalPortCache:      newPortCache(stopChan),
		namespaces:            make(map[string]*namespaceInfo),
		namespacesMutex:       sync.Mutex{},
		externalGWCache:       make(map[ktypes.NamespacedName]*externalRouteInfo),
		exGWCacheMutex:        sync.RWMutex{},
		addressSetFactory:     addressSetFactory,
		lspIngressDenyCache:   make(map[string]int),
		lspEgressDenyCache:    make(map[string]int),
		lspMutex:              &sync.Mutex{},
		isStarted:             false,
		eIPC: egressIPController{
			egressIPAssignmentMutex:           &sync.Mutex{},
			podAssignmentMutex:                &sync.Mutex{},
			podAssignment:                     make(map[string]*podAssignmentState),
			pendingCloudPrivateIPConfigsMutex: &sync.Mutex{},
			pendingCloudPrivateIPConfigsOps:   make(map[string]map[string]*cloudPrivateIPConfigOp),
			allocator:                         allocator{&sync.Mutex{}, make(map[string]*egressNode)},
			nbClient:                          mc.nbClient,
			watchFactory:                      mc.watchFactory,
			egressIPTotalTimeout:              config.OVNKubernetesFeature.EgressIPReachabiltyTotalTimeout,
		},
		loadbalancerClusterCache:  make(map[kapi.Protocol]string),
		multicastSupport:          config.EnableMulticast,
		loadBalancerGroupUUID:     "",
		aclLoggingEnabled:         true,
		joinSwIPManager:           nil,
		retryPods:                 NewRetryObjs(factory.PodType, "", nil, nil, nil),
		retryNodes:                NewRetryObjs(factory.NodeType, "", nil, nil, nil),
		retryEgressFirewalls:      NewRetryObjs(factory.EgressFirewallType, "", nil, nil, nil),
		retryEgressIPs:            NewRetryObjs(factory.EgressIPType, "", nil, nil, nil),
		retryEgressIPNamespaces:   NewRetryObjs(factory.EgressIPNamespaceType, "", nil, nil, nil),
		retryEgressIPPods:         NewRetryObjs(factory.EgressIPPodType, "", nil, nil, nil),
		retryEgressNodes:          NewRetryObjs(factory.EgressNodeType, "", nil, nil, nil),
		retryCloudPrivateIPConfig: NewRetryObjs(factory.CloudPrivateIPConfigType, "", nil, nil, nil),
	}
	if !nadInfo.IsSecondary {
		oc.wg = mc.wg
		oc.retryNetworkPolicies = NewRetryObjs(factory.PolicyType, "", nil, nil, nil)
		mc.ovnController = oc
		oc.svcController, oc.svcFactory = newServiceController(mc.client, mc.nbClient, mc.recorder)
		mc.allOvnControllers.Store(nadInfo.NetName, oc)
	} else {
		oc.multicastSupport = false
		oc.wg = &sync.WaitGroup{}
		oc.retryNetworkPolicies = NewRetryObjs(factory.MultinetworkpolicyType, "", nil, nil, nil)
		_, loaded := mc.allOvnControllers.LoadOrStore(nadInfo.NetName, oc)
		if loaded {
			return nil, fmt.Errorf("non default Network attachment definition %s already exists", nadInfo.NetName)
		}
	}
	return oc, nil
}

// Run starts the actual watching.
func (oc *Controller) Run(ctx context.Context) error {
	if !oc.nadInfo.IsSecondary {
		oc.syncPeriodic()
	}
	klog.Infof("Starting all the Watchers for network %s...", oc.nadInfo.NetName)
	start := time.Now()

	// Sync external gateway routes. External gateway may be set in namespaces
	// or via pods. So execute an individual sync method at startup
	oc.cleanExGwECMPRoutes()

	// WatchNamespaces() should be started first because it has no other
	// dependencies, and WatchNodes() depends on it
	if err := oc.WatchNamespaces(); err != nil {
		return err
	}

	// WatchNodes must be started next because it creates the node switch
	// which most other watches depend on.
	// https://github.com/ovn-org/ovn-kubernetes/pull/859
	if err := oc.WatchNodes(); err != nil {
		return err
	}

	if !oc.nadInfo.IsSecondary {
		// Start service watch factory and sync services
		oc.svcFactory.Start(oc.stopChan)

		// Services should be started after nodes to prevent LB churn
		if err := oc.StartServiceController(oc.wg, true); err != nil {
			return err
		}
	}

	if err := oc.WatchPods(); err != nil {
		return err
	}

	if config.OVNKubernetesFeature.EnableVirtualIP {
		err := oc.WatchVirtualIPs()
		if err != nil {
			return err
		}
	}
	if config.OVNKubernetesFeature.EnableAdminPolicyBasedRouting {
		if err := oc.WatchAdminPolicyBasedRoutes(); err != nil {
			return err
		}
	}
	if !oc.nadInfo.IsSecondary {
		// WatchNetworkPolicy depends on WatchPods and WatchNamespaces
		if err := oc.WatchNetworkPolicy(); err != nil {
			return err
		}
		// Clean up stale L4 network policies.
		oc.CleanStaleNetworkPolicy()

		if config.OVNKubernetesFeature.EnableEgressIP {
			// This is probably the best starting order for all egress IP handlers.
			// WatchEgressIPNamespaces and WatchEgressIPPods only use the informer
			// cache to retrieve the egress IPs when determining if namespace/pods
			// match. It is thus better if we initialize them first and allow
			// WatchEgressNodes / WatchEgressIP to initialize after. Those handlers
			// might change the assignments of the existing objects. If we do the
			// inverse and start WatchEgressIPNamespaces / WatchEgressIPPod last, we
			// risk performing a bunch of modifications on the EgressIP objects when
			// we restart and then have these handlers act on stale data when they
			// sync.
			if err := oc.WatchEgressIPNamespaces(); err != nil {
				return err
			}
			if err := oc.WatchEgressIPPods(); err != nil {
				return err
			}
			if err := oc.WatchEgressNodes(); err != nil {
				return err
			}
			if err := oc.WatchEgressIP(); err != nil {
				return err
			}
			if util.PlatformTypeIsEgressIPCloudProvider() {
				if err := oc.WatchCloudPrivateIPConfig(); err != nil {
					return err
				}

			}
			if config.OVNKubernetesFeature.EgressIPReachabiltyTotalTimeout == 0 {
				klog.V(2).Infof("EgressIP node reachability check disabled")
			}
		}

		if config.OVNKubernetesFeature.EnableEgressFirewall {
			var err error
			oc.egressFirewallDNS, err = NewEgressDNS(oc.addressSetFactory, oc.stopChan)
			if err != nil {
				return err
			}
			oc.egressFirewallDNS.Run(egressFirewallDNSDefaultDuration)
			err = oc.WatchEgressFirewall()
			if err != nil {
				return err
			}
		}

		if config.OVNKubernetesFeature.EnableEgressQoS {
			oc.initEgressQoSController(
				oc.mc.watchFactory.EgressQoSInformer(),
				oc.mc.watchFactory.PodCoreInformer(),
				oc.mc.watchFactory.NodeCoreInformer())
			oc.wg.Add(1)
			go func() {
				defer oc.wg.Done()
				oc.runEgressQoSController(1, oc.stopChan)
			}()
		}
		klog.Infof("Completing all the Watchers took %v", time.Since(start))

		if config.Kubernetes.OVNEmptyLbEvents {
			klog.Infof("Starting unidling controller")
			unidlingController, err := unidling.NewController(
				oc.mc.recorder,
				oc.mc.watchFactory.ServiceInformer(),
				oc.mc.sbClient,
			)
			if err != nil {
				return err
			}
			oc.wg.Add(1)
			go func() {
				defer oc.wg.Done()
				unidlingController.Run(oc.stopChan)
			}()
		}

		if oc.hoMaster != nil {
			oc.wg.Add(1)
			go func() {
				defer oc.wg.Done()
				oc.hoMaster.Run(oc.stopChan)
			}()
		}
	} else {
		if oc.nadInfo.IsSecondary && config.OVNKubernetesFeature.EnableMultiNetworkPolicy {
			if err := oc.WatchMultiNetworkPolicy(); err != nil {
				return err
			}
			oc.CleanStaleNetworkPolicy()
		}
		klog.Infof("Completing all the Watchers for network %s took %v", oc.nadInfo.NetName, time.Since(start))
	}

	// Final step to cleanup after resource handlers have synced
	err := oc.ovnTopologyCleanup()
	if err != nil {
		klog.Errorf("Failed to cleanup OVN topology to version %d: %v", ovntypes.OvnCurrentTopologyVersion, err)
		return err
	}

	// Master is fully running and resource handlers have synced, update Topology version in OVN and the ConfigMap
	if err := oc.reportTopologyVersion(ctx); err != nil {
		klog.Errorf("Failed to report topology version: %v", err)
		return err
	}

	return nil
}

// syncPeriodic adds a goroutine that periodically does some work
// right now there is only one ticker registered
// for syncNodesPeriodic which deletes chassis records from the sbdb
// every 5 minutes
func (oc *Controller) syncPeriodic() {
	if oc.nadInfo.IsSecondary {
		return
	}

	go func() {
		nodeSyncTicker := time.NewTicker(5 * time.Minute)
		defer nodeSyncTicker.Stop()
		for {
			select {
			case <-nodeSyncTicker.C:
				oc.syncNodesPeriodic()
			case <-oc.stopChan:
				return
			}
		}
	}()
}

func (oc *Controller) recordPodEvent(reason string, addErr error, pod *kapi.Pod) {
	podRef, err := ref.GetReference(scheme.Scheme, pod)
	if err != nil {
		klog.Errorf("Couldn't get a reference to pod %s/%s to post an event: '%v'",
			pod.Namespace, pod.Name, err)
	} else {
		klog.V(5).Infof("Posting a %s event for Pod %s/%s", kapi.EventTypeWarning, pod.Namespace, pod.Name)
		oc.mc.recorder.Eventf(podRef, kapi.EventTypeWarning, reason, addErr.Error())
	}
}

func exGatewayAnnotationsChanged(oldPod, newPod *kapi.Pod) bool {
	return oldPod.Annotations[util.RoutingNamespaceAnnotation] != newPod.Annotations[util.RoutingNamespaceAnnotation] ||
		oldPod.Annotations[util.RoutingNetworkAnnotation] != newPod.Annotations[util.RoutingNetworkAnnotation] ||
		oldPod.Annotations[util.BfdAnnotation] != newPod.Annotations[util.BfdAnnotation]
}

func portSecurityAnnotationChanged(oldPod, newPod *kapi.Pod) bool {
	if oldPod == nil {
		// not an update event, creation flow will handle port_security
		return false
	}
	return oldPod.Annotations[util.PortSecurityInfoAnnotation] != newPod.Annotations[util.PortSecurityInfoAnnotation]
}

func networkStatusAnnotationsChanged(oldPod, newPod *kapi.Pod) bool {
	return oldPod.Annotations[nettypes.NetworkStatusAnnot] != newPod.Annotations[nettypes.NetworkStatusAnnot]
}

func podNodeNameLabelChanged(pod *kapi.Pod, nodeNameLabel map[string]string) bool {
	// check if label already exists and is same as nodeNameLabel.
	return pod.Labels[util.OvnPodNodeNameLabel] != nodeNameLabel[util.OvnPodNodeNameLabel]
}

// ensurePod tries to set up a pod. It returns nil on success and error on failure; failure
// indicates the pod set up should be retried later.
func (oc *Controller) ensurePod(oldPod, pod *kapi.Pod, addPort bool) error {
	if !oc.nadInfo.IsSecondary && pod.Spec.HostNetwork {
		if oldPod == nil {
			if util.PodScheduled(pod) {
				if err := oc.addHostNetworkPodToNamespace(pod); err != nil {
					return fmt.Errorf("failed to add host network pod %s/%s's IPs on node %s to the namespace address_set: %v",
						pod.Namespace, pod.Name, pod.Spec.NodeName, err)
				}
			}
		} else if oldPod.Spec.NodeName != pod.Spec.NodeName {
			if util.PodScheduled(oldPod) {
				if err := oc.delHostNetworkPodFromNamespace(oldPod); err != nil {
					return fmt.Errorf("failed to delete host network pod %s/%s's IPs on node %s from the namespace address_set: %v",
						oldPod.Namespace, oldPod.Name, oldPod.Spec.NodeName, err)
				}
			}
			if util.PodScheduled(pod) {
				if err := oc.addHostNetworkPodToNamespace(pod); err != nil {
					return fmt.Errorf("failed to add host network pod %s/%s's IPs on node %s to the namespace address_set: %v",
						pod.Namespace, pod.Name, pod.Spec.NodeName, err)
				}
			}
		}
	}
	// Try unscheduled pods later
	if !util.PodScheduled(pod) {
		return nil
	}

	if !oc.nadInfo.IsSecondary {
		if oldPod != nil && (exGatewayAnnotationsChanged(oldPod, pod) || networkStatusAnnotationsChanged(oldPod, pod)) {
			// No matter if a pod is ovn networked, or host networked, we still need to check for exgw
			// annotations. If the pod is ovn networked and is in update reschedule, addLogicalPort will take
			// care of updating the exgw updates
			if err := oc.deletePodExternalGW(oldPod); err != nil {
				return fmt.Errorf("ensurePod failed %s/%s: %w", pod.Namespace, pod.Name, err)
			}
		}

		nodeNameLabel := map[string]string{util.OvnPodNodeNameLabel: pod.Spec.NodeName}
		if podNodeNameLabelChanged(pod, nodeNameLabel) {
			err := oc.mc.kube.SetLabelsOnPod(pod, nodeNameLabel)
			if err != nil {
				return fmt.Errorf("failed to set %s labels on pod %s: %v", util.OvnPodNodeNameLabel, pod.Name, err)
			}
		}
	}

	if util.PodWantsNetwork(pod) && addPort {
		if err := oc.addLogicalPort(pod); err != nil {
			return fmt.Errorf("addLogicalPort failed for %s/%s network %s: %w", pod.Namespace, pod.Name, oc.nadInfo.NetName, err)
		}
	} else {
		if portSecurityAnnotationChanged(oldPod, pod) {
			if err := oc.updatePortSecurity(oldPod, pod); err != nil {
				klog.Errorf(err.Error())
				oc.recordPodEvent("ErrorUpdatingPortSecurity", err, pod)
				return err
			}
		}
		if oc.nadInfo.IsSecondary {
			return nil
		}

		// either pod is host-networked or its an update for a normal pod (addPort=false case)
		if oldPod == nil || exGatewayAnnotationsChanged(oldPod, pod) || networkStatusAnnotationsChanged(oldPod, pod) {
			if err := oc.addPodExternalGW(pod); err != nil {
				return fmt.Errorf("addPodExternalGW failed for %s/%s: %w", pod.Namespace, pod.Name, err)
			}
		}
	}

	return nil
}

// removePod tried to tear down a pod. It returns nil on success and error on failure;
// failure indicates the pod tear down should be retried later.
func (oc *Controller) removePod(pod *kapi.Pod, portInfoMap map[string]*lpInfo) error {
	if !oc.nadInfo.IsSecondary && pod.Spec.HostNetwork && util.PodScheduled(pod) {
		if err := oc.delHostNetworkPodFromNamespace(pod); err != nil {
			return fmt.Errorf("failed to delete host network pod %s/%s's IPs on node %s from the namespace address_set: %v",
				pod.Namespace, pod.Name, pod.Spec.NodeName, err)
		}
	}
	if !util.PodWantsNetwork(pod) && !oc.nadInfo.IsSecondary {
		if err := oc.deletePodExternalGW(pod); err != nil {
			return fmt.Errorf("unable to delete external gateway routes for pod %s: %w",
				getPodNamespacedName(pod, "", true), err)
		}
		return nil
	}

	if err := oc.deleteLogicalPort(pod, portInfoMap); err != nil {
		return fmt.Errorf("deleteLogicalPort failed for pod %s/%s: %w",
			pod.Namespace, pod.Name, err)
	}
	return nil
}

// WatchPods starts the watching of the Pod resource and calls back the appropriate handler logic
func (oc *Controller) WatchPods() error {
	var err error
	oc.podHandler, err = oc.WatchResource(oc.retryPods)
	return err
}

// WatchMultiNetworkPolicy starts the watching of multi network policy resource and calls
// back the appropriate handler logic
func (oc *Controller) WatchMultiNetworkPolicy() error {
	if !oc.nadInfo.IsSecondary {
		klog.Infof("WatchMultiNetworkPolicy for OVN Primary networkis a no-op")
		return nil
	}
	multiNetworkPolicyHandler, err := oc.WatchResource(oc.retryNetworkPolicies)
	if err == nil {
		oc.multiNetworkPolicyHandler = multiNetworkPolicyHandler
	}
	return err
}

func (oc *Controller) CleanStaleNetworkPolicy() {
	start := time.Now()
	klog.V(5).Infof("Cleaning up stale OVN ACLs that are left behind after L4 Port consolidation")
	// now that we have added all the OVN ACLs with optimization, it is time to remove the stale OVN
	// ACL entries from the database
	// want ACLs configured that don't have l4fused key and that have l4Match set (but not to None)
	pACL := func(item *nbdb.ACL) bool {
		if !util.HasExternalIDsForCluster(item.ExternalIDs) {
			return false
		}
		netName, ok := item.ExternalIDs["network_name"]
		if oc.nadInfo.IsSecondary {
			if !ok || netName != oc.nadInfo.NetName {
				return false
			}
		} else if ok {
			return false
		}
		if _, ok := item.ExternalIDs[l4MatchFusedExtIdKey]; !ok {
			if val, ok := item.ExternalIDs[l4MatchACLExtIdKey]; ok {
				if val != noneMatch {
					return true
				}
			}
		}
		return false
	}

	staleACLs, err := libovsdbops.FindACLsWithPredicate(oc.mc.nbClient, pACL)
	if err != nil {
		klog.Warningf("Failed to retrieve stale OVN ACL entries that wre not optimized " +
			"for L4 Ports consolidation: %v, err")
	} else {
		// it could be that delete all the acls in one go might fail for various reasons,
		// so lets try to delete one at a time so that we can remove as many stale acls
		// as possible.
		klog.V(5).Infof("Number of staleACLS to be cleaned is %d", len(staleACLs))
		for _, staleACL := range staleACLs {
			staleACL := staleACL
			nsName := staleACL.ExternalIDs[namespaceACLExtIdKey]
			policyName := staleACL.ExternalIDs[policyACLExtIdKey]
			pgName := fmt.Sprintf("%s_%s", nsName, policyName)
			pgName = util.GetClusterScopedName(oc.nadInfo.NetNameInfo.Prefix + hashedPortGroup(pgName))
			aclDesc := fmt.Sprintf("stale ACL %s/%s/%s in port group %s", staleACL.UUID, nsName, policyName, pgName)
			klog.V(5).Infof("About to delete %s", aclDesc)
			ops, err := libovsdbops.DeleteACLsFromPortGroupOps(oc.mc.nbClient, nil, pgName, staleACL)
			if err != nil {
				klog.Warningf("Failed to get ops to delete %s: %v", aclDesc, err)
				continue
			}
			_, err = libovsdbops.TransactAndCheck(oc.mc.nbClient, ops)
			if err != nil {
				klog.Warningf("Failed to delete %s: %v", aclDesc, err)
				continue
			}
		}
	}
	klog.V(5).Infof("Completed cleaning up stale OVN ACLs in %v", time.Since(start))
}

// WatchNetworkPolicy starts the watching of network policy resource and calls
// back the appropriate handler logic
func (oc *Controller) WatchNetworkPolicy() error {
	if oc.nadInfo.IsSecondary {
		klog.Infof("WatchNetworkPolicy for network %s is a no-op", oc.nadInfo.NetName)
		return nil
	}
	_, err := oc.WatchResource(oc.retryNetworkPolicies)
	return err
}

// WatchEgressFirewall starts the watching of egressfirewall resource and calls
// back the appropriate handler logic
func (oc *Controller) WatchEgressFirewall() error {
	_, err := oc.WatchResource(oc.retryEgressFirewalls)
	return err
}

// WatchEgressNodes starts the watching of egress assignable nodes and calls
// back the appropriate handler logic.
func (oc *Controller) WatchEgressNodes() error {
	if oc.nadInfo.IsSecondary {
		klog.Infof("WatchEgressNodes for network %s is a no-op", oc.nadInfo.NetName)
		return nil
	}

	_, err := oc.WatchResource(oc.retryEgressNodes)
	return err
}

// WatchCloudPrivateIPConfig starts the watching of cloudprivateipconfigs
// resource and calls back the appropriate handler logic.
func (oc *Controller) WatchCloudPrivateIPConfig() error {
	if oc.nadInfo.IsSecondary {
		klog.Infof("WatchCloudPrivateIPConfig for network %s is a no-op", oc.nadInfo.NetName)
		return nil
	}

	_, err := oc.WatchResource(oc.retryCloudPrivateIPConfig)
	return err
}

func (oc *Controller) WatchEgressIP() error {
	if oc.nadInfo.IsSecondary {
		klog.Infof("WatchEgressIP for network %s is a no-op", oc.nadInfo.NetName)
		return nil
	}

	_, err := oc.WatchResource(oc.retryEgressIPs)
	return err
}

// WatchEgressIP starts the watching of egressip resource and calls back the
// appropriate handler logic. It also initiates the other dedicated resource
// handlers for egress IP setup: namespaces, pods.
func (oc *Controller) WatchEgressIPNamespaces() error {
	if oc.nadInfo.IsSecondary {
		klog.Infof("WatchEgressIP for network %s is a no-op", oc.nadInfo.NetName)
		return nil
	}

	_, err := oc.WatchResource(oc.retryEgressIPNamespaces)
	return err
}

func (oc *Controller) WatchEgressIPPods() error {
	if oc.nadInfo.IsSecondary {
		klog.Infof("WatchEgressIP for network %s is a no-op", oc.nadInfo.NetName)
		return nil
	}

	_, err := oc.WatchResource(oc.retryEgressIPPods)
	return err
}

// WatchNamespaces starts the watching of namespace resource and calls
// back the appropriate handler logic
func (oc *Controller) WatchNamespaces() (err error) {
	start := time.Now()
	oc.namespaceHandler, err = oc.mc.watchFactory.AddNamespaceHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ns := obj.(*kapi.Namespace)
			oc.AddNamespace(ns)
		},
		UpdateFunc: func(old, newer interface{}) {
			oldNs, newNs := old.(*kapi.Namespace), newer.(*kapi.Namespace)
			oc.updateNamespace(oldNs, newNs)
		},
		DeleteFunc: func(obj interface{}) {
			ns := obj.(*kapi.Namespace)
			oc.deleteNamespace(ns)
		},
	}, oc.syncNamespaces)
	klog.Infof("Bootstrapping existing namespaces and cleaning stale namespaces took %v", time.Since(start))
	if err != nil {
		klog.Errorf("Failed to watch namespaces err: %v", err)
		return err
	}
	return nil
}

// WatchAdminPolicyBasedRoutes starts the watching of adminpolicybasedroute resource and calls
// back the appropriate handler logic
func (oc *Controller) WatchAdminPolicyBasedRoutes() error {
	start := time.Now()
	if !oc.nadInfo.IsSecondary {
		// delete logical router policies created by egressip since they would block rerouting between pods
		err := oc.deleteLogicalRouterPoliciesByPriority(ovntypes.DefaultNoRereoutePriority)
		if err != nil && err != libovsdbclient.ErrNotFound {
			return fmt.Errorf("failed to clean up egressip default noreroute policies: %v", err)
		}
	}
	oc.adminPBRRetryQueue = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "adminpbr")
	filterAdminPBR := func(obj interface{}) bool {
		apbr, ok := obj.(*adminpbrapi.AdminPolicyBasedRoute)
		if !ok {
			return false
		}
		_, ok = oc.nadInfo.NetAttachDefs.Load(apbr.Spec.NetworkAttachmentName)
		return ok
	}
	oc.adminPBRHandler, _ = oc.mc.watchFactory.AddHandlerWithFilterFunc(reflect.TypeOf(&adminpbrapi.AdminPolicyBasedRoute{}), filterAdminPBR, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			apbr, ok := obj.(*adminpbrapi.AdminPolicyBasedRoute)
			if !ok {
				klog.Errorf("Not an AdminPolicyBasedRoute object: %v", apbr)
				return
			}
			oc.onAdminPBRAddOrUpdate(apbr)
		},
		UpdateFunc: func(old, new interface{}) {
			if oc.nadInfo.TopoType != ovntypes.Layer3AttachDefTopoType {
				klog.V(5).Infof("Skipping AdminPBR event since the network topology of %s is not L3", oc.nadInfo.NetName)
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
				oc.onAdminPBRAddOrUpdate(newPolicy)
			}
		},
		DeleteFunc: func(obj interface{}) {
			if oc.nadInfo.TopoType != ovntypes.Layer3AttachDefTopoType {
				klog.V(5).Infof("Skipping AdminPBR event since the network topology of %s is not L3", oc.nadInfo.NetName)
				return
			}
			apbr, ok := obj.(*adminpbrapi.AdminPolicyBasedRoute)
			if !ok {
				klog.Errorf("Not an AdminPolicyBasedRoute object: %v", apbr)
				return
			}
			oc.onAdminPBRDelete(apbr)
		},
	}, nil)

	oc.adminPBRNodeHandler, _ = oc.mc.watchFactory.AddNodeHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			oc.syncAdminPBROnNodeChange(nil, obj)
		},
		UpdateFunc: func(old, new interface{}) {
			oc.syncAdminPBROnNodeChange(old, new)
		},
		DeleteFunc: func(obj interface{}) {},
	}, nil)

	oc.adminPBRNamespaceHandler, _ = oc.mc.watchFactory.AddNamespaceHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			oc.syncAdminPBROnNamespaceChange(nil, obj)
		},
		UpdateFunc: func(old, new interface{}) {
			oc.syncAdminPBROnNamespaceChange(old, new)
		},
		DeleteFunc: func(obj interface{}) {},
	}, nil)

	klog.Infof("Bootstrapping existing adminpbrs and cleaning stale adminpbrs took %v", time.Since(start))
	if oc.nadInfo.TopoType != ovntypes.Layer3AttachDefTopoType {
		klog.V(4).Infof("Skip periodical sync for non-L3 network %s(%s)", oc.nadInfo.NetName, oc.nadInfo.TopoType)
		return nil
	}
	go func() {
		ticker := time.NewTicker(ovntypes.AdminPBRResyncInterval)
		for {
			select {
			case <-ticker.C:
				oc.syncAdminPBRPeriodic()
				oc.syncAddressSetPeriodic()
			case <-oc.stopChan:
				ticker.Stop()
				oc.adminPBRRetryQueue.ShutDown()
				return
			}
		}
	}()
	go func() {
		for oc.retryAdminPBROperations() {
		}
	}()
	return nil
}

// WatchVirtualIPs starts the watching of virtual-ip resources and calls
// back the appropriate handler logic
func (oc *Controller) WatchVirtualIPs() error {
	start := time.Now()
	oc.virtualIPRetryQueue = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "virtualIP")
	// filterVirtualIP checks if the virtualIP nad belongs to this controller and
	filterVirtualIP := func(obj interface{}) bool {
		virtIP, ok := obj.(*virtualip.VirtualIP)
		if !ok {
			return false
		}
		_, ok = oc.nadInfo.NetAttachDefs.Load(virtIP.Spec.NetworkAttachmentName)
		return ok
	}

	// creates corresponding add/update/delete handlers
	oc.virtualIPHandler, _ = oc.mc.watchFactory.AddHandlerWithFilterFunc(reflect.TypeOf(&virtualip.VirtualIP{}), filterVirtualIP, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			virtIP := obj.(*virtualip.VirtualIP)
			err := oc.addVirtualIP(virtIP)
			if err != nil {
				klog.Errorf(err.Error())
				oc.recordVirtualIPEvent("VirtualIPAddError", err.Error(), virtIP)
			}
		},
		UpdateFunc: func(old, newer interface{}) {
			oldVirtIP := old.(*virtualip.VirtualIP)
			newVirtIP := newer.(*virtualip.VirtualIP)
			// only compare spec changes as we constantly do updates for
			// virtualIP status.
			if !reflect.DeepEqual(oldVirtIP.Spec, newVirtIP.Spec) {
				if err := oc.deleteVirtualIP(oldVirtIP); err != nil {
					klog.Errorf(err.Error())
				}
				if err := oc.addVirtualIP(newVirtIP); err != nil {
					klog.Errorf(err.Error())
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			virtIP := obj.(*virtualip.VirtualIP)
			if err := oc.deleteVirtualIP(virtIP); err != nil {
				klog.Error(err)
			}
		},
	}, nil)

	// run this only for l2 networks
	if oc.nadInfo.TopoType == ovntypes.Layer2AttachDefTopoType {
		dbModel, err := sbdb.FullDatabaseModel()
		if err != nil {
			return fmt.Errorf("failed to create sdbdb model: (%v)", err)
		}
		client, err := libovsdb.NewClient(config.OvnSouth, dbModel, oc.mc.stopChan)
		if err != nil {
			return err
		}

		go func() {
			ticker := time.NewTicker(ovntypes.VirtualIPResyncInterval)
			for {
				select {
				case <-ticker.C:
					oc.syncVirtualIPsPeriodic()
				case <-oc.stopChan:
					ticker.Stop()
					oc.virtualIPRetryQueue.ShutDown()
					return
				}
			}
		}()
		// for virtualIPRetry operations
		go func() {
			for oc.retryVirtualIPOperations() {
			}
		}()

		return oc.watchPortBindingTable(client)
	}
	klog.Infof("Bootstrapping existing virtualIPs and cleaning stale virtualIPs took %v", time.Since(start))
	return nil
}

// syncNodeGateway ensures a node's gateway router is configured
func (oc *Controller) syncNodeGateway(node *kapi.Node, hostSubnets []*net.IPNet) error {
	if oc.nadInfo.IsSecondary {
		klog.Infof("WatchNamespaces for network %s is a no-op", oc.nadInfo.NetName)
		return nil
	}

	l3GatewayConfig, err := util.ParseNodeL3GatewayAnnotation(node)
	if err != nil {
		return err
	}

	if len(hostSubnets) == 0 {
		hostSubnets, err = util.ParseNodeHostSubnetAnnotation(node, oc.nadInfo.NetName)
		if err != nil {
			return err
		}
	}

	if l3GatewayConfig.Mode == config.GatewayModeDisabled {
		if err := oc.gatewayCleanup(node.Name); err != nil {
			return fmt.Errorf("error cleaning up gateway for node %s: %v", node.Name, err)
		}
		if err := oc.joinSwIPManager.ReleaseJoinLRPIPs(node.Name); err != nil {
			return err
		}
	} else if hostSubnets != nil {
		var hostAddrs sets.String
		if config.Gateway.Mode == config.GatewayModeShared {
			hostAddrs, err = util.ParseNodeHostAddresses(node)
			if err != nil && !util.IsAnnotationNotSetError(err) {
				return fmt.Errorf("failed to get host addresses for node: %s: %v", node.Name, err)
			}
		}
		if err := oc.syncGatewayLogicalNetwork(node, l3GatewayConfig, hostSubnets, hostAddrs); err != nil {
			return fmt.Errorf("error creating gateway for node %s: %v", node.Name, err)
		}
	}
	return nil
}

// findNodeReadyCondition finds node ready condition in conditions array.
// Returns a pointer within the given node.
func findNodeReadyCondition(node *kapi.Node) *kapi.NodeCondition {
	for i, condition := range node.Status.Conditions {
		if condition.Type == kapi.NodeReady {
			return &node.Status.Conditions[i]
		}
	}
	return nil
}

// XXX should come from config
const noSchedTaintKey = "ngn2.nvidia.com/ovn"
const nodeDependentsAnnotationKey = "ngn2.nvidia.com/dpu-host-hostname"
const depedentTypeLabelKey = "ngn2.nvidia.com/dpu-hosttype"

var dependentTypesPropagated = map[string]bool{
	"GS": true,
}

// dependentNodename returns (dependentNodename, true) if the node has an annotation to indicate is has dependents
func dependentNodename(node *kapi.Node) (string, bool) {
	dep, present := node.Annotations[nodeDependentsAnnotationKey]
	return dep, present
}

// Return true if this node type must propagate not ready conditions to any dependent node.
func nodePropagatesReadiness(node *kapi.Node) bool {
	if hostTypeLabel, present := node.Labels[depedentTypeLabelKey]; present {
		_, present = dependentTypesPropagated[hostTypeLabel]
		return present
	}
	return false
}

// syncDependentNodeTaints syncs the taints on a dependent node with the ready condition of
// the node subject to reconciliation. If the 'within' duration is nonzero then the last
// transition time of the ready condition must be no older than that duration from now - within
// should be non-zero for Update events, to avoid a GET on every reconciliation of a node
// that has a dependent (e.g., within of 1m for updates).
func (oc *Controller) syncDependentNodeTaints(node *kapi.Node, within time.Duration) error {
	if !nodePropagatesReadiness(node) {
		return nil
	}

	dependentNodeName, present := dependentNodename(node)
	if !present {
		return nil
	}

	ourReadyCondition := findNodeReadyCondition(node)
	if ourReadyCondition == nil || within != 0 && time.Since(ourReadyCondition.LastTransitionTime.Time) > within {
		return nil
	}

	noSchedTaint := &kapi.Taint{
		Key:    noSchedTaintKey,
		Value:  "dpuNotReady",
		Effect: kapi.TaintEffectNoSchedule,
	}
	action := ""
	var err error
	switch ourReadyCondition.Status {
	case kapi.ConditionTrue:
		action = "removing taint"
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			return oc.mc.kube.RemoveTaintFromNode(dependentNodeName, noSchedTaint)
		})

	case kapi.ConditionFalse, kapi.ConditionUnknown:
		action = "adding taint"
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			return oc.mc.kube.SetTaintOnNode(dependentNodeName, noSchedTaint)
		})
	}

	if err != nil {
		err = fmt.Errorf("syncDependentNodeTaints error syncing ready condition to dependent node taint, %s error: %v",
			action, err)
	}
	return err
}

// WatchNodes starts the watching of node resource and calls
// back the appropriate handler logic
func (oc *Controller) WatchNodes() (err error) {
	if oc.nadInfo.TopoType == ovntypes.LocalnetAttachDefTopoType || oc.nadInfo.TopoType == ovntypes.Layer2AttachDefTopoType {
		return nil
	}
	oc.nodeHandler, err = oc.WatchResource(oc.retryNodes)
	return err
}

// GetNetworkPolicyACLLogging retrieves ACL deny policy logging setting for the Namespace
func (oc *Controller) GetNetworkPolicyACLLogging(ns string) *ACLLoggingLevels {
	nsInfo, nsUnlock := oc.getNamespaceLocked(ns, true)
	if nsInfo == nil {
		return &ACLLoggingLevels{
			Allow: "",
			Deny:  "",
		}
	}
	defer nsUnlock()
	return &nsInfo.aclLogging
}

// Verify if controller can support ACL logging and validate annotation
func (oc *Controller) aclLoggingCanEnable(annotation string, nsInfo *namespaceInfo) bool {
	if !oc.aclLoggingEnabled || annotation == "" {
		nsInfo.aclLogging.Deny = ""
		nsInfo.aclLogging.Allow = ""
		return false
	}
	var aclLevels ACLLoggingLevels
	err := json.Unmarshal([]byte(annotation), &aclLevels)
	if err != nil {
		return false
	}

	// Using newDenyLoggingLevel and newAllowLoggingLevel allows resetting nsinfo state.
	// This is important if a user sets either the allow level or the deny level flag to an
	// invalid value or after they remove either the allow or the deny annotation.
	// If either of the 2 (allow or deny logging level) is set with a valid level, return true.
	newDenyLoggingLevel := ""
	newAllowLoggingLevel := ""
	okCnt := 0
	for _, s := range []string{"alert", "warning", "notice", "info", "debug"} {
		if s == aclLevels.Deny {
			newDenyLoggingLevel = aclLevels.Deny
			okCnt++
		}
		if s == aclLevels.Allow {
			newAllowLoggingLevel = aclLevels.Allow
			okCnt++
		}
	}
	nsInfo.aclLogging.Deny = newDenyLoggingLevel
	nsInfo.aclLogging.Allow = newAllowLoggingLevel
	return okCnt > 0
}

func (mc *OvnMHController) initOvnController(netattachdef *nettypes.NetworkAttachmentDefinition) (*Controller, error) {
	nadInfo, nadConf, err := util.ParseNADInfo(netattachdef)
	if err != nil {
		return nil, err
	}
	klog.V(5).Infof("Add Network Attachment Definition %s/%s to nad %s", netattachdef.Namespace, netattachdef.Name, nadInfo.NetName)

	if !nadInfo.IsSecondary {
		mc.ovnController.nadInfo.NetAttachDefs.Store(util.GetNadKeyName(netattachdef.Namespace, netattachdef.Name), nadConf)
		return mc.ovnController, nil
	}

	// Note that net-attach-def add/delete/update events are serialized, so we don't need locks here.
	// Check if any Controller of the same netconf.Name already exists, if so, check its conf to see if they are the same.
	v, ok := mc.allOvnControllers.Load(nadInfo.NetName)
	if ok {
		oc := v.(*Controller)
		if oc.nadInfo.NetCidr != nadInfo.NetCidr || oc.nadInfo.MTU != nadInfo.MTU || oc.nadInfo.TopoType != nadInfo.TopoType ||
			oc.nadInfo.VlanId != nadInfo.VlanId || oc.nadInfo.ConnectToNad != nadInfo.ConnectToNad {
			return nil, fmt.Errorf("network attachment definition %s/%s does not share the same CNI config of name %s",
				netattachdef.Namespace, netattachdef.Name, nadInfo.NetName)
		} else {
			oc.nadInfo.NetAttachDefs.Store(util.GetNadKeyName(netattachdef.Namespace, netattachdef.Name), nadConf)
			return oc, nil
		}
	}

	nadInfo.NetAttachDefs.Store(util.GetNadKeyName(netattachdef.Namespace, netattachdef.Name), nadConf)
	return mc.NewOvnController(nadInfo, nil)
}

func (mc *OvnMHController) addNetworkAttachDefinition(netattachdef *nettypes.NetworkAttachmentDefinition) {
	klog.Infof("Add Network Attachment Definition %s/%s", netattachdef.Namespace, netattachdef.Name)
	oc, err := mc.initOvnController(netattachdef)
	if err != nil {
		// if the net-attach-def is not managed by OVN, return silently
		if err != util.ErrorAttachDefNotOvnManaged {
			klog.Errorf("Failed to add Network Attachment Definition %s/%s: %v", netattachdef.Namespace, netattachdef.Name, err)
		}
		return
	}

	// run the cluster controller to init the master
	err = oc.Init(context.TODO())
	if err != nil {
		klog.Errorf(err.Error())
		return
	}
	oc.connectToLayer2Network(util.GetNadKeyName(netattachdef.Namespace, netattachdef.Name))
}

func (mc *OvnMHController) deleteNetworkAttachDefinition(netattachdef *nettypes.NetworkAttachmentDefinition) {
	klog.Infof("Delete Network Attachment Definition %s/%s", netattachdef.Namespace, netattachdef.Name)
	netconf, err := util.ParseNetConf(netattachdef)
	if err != nil {
		if err != util.ErrorAttachDefNotOvnManaged {
			klog.Error(err)
		}
		return
	}
	nadInfo, err := util.NewNetAttachDefInfo(netconf)
	if err != nil {
		klog.Errorf(err.Error())
		return
	}
	nadName := util.GetNadKeyName(netattachdef.Namespace, netattachdef.Name)
	if !netconf.IsSecondary {
		_, ok := mc.ovnController.nadInfo.NetAttachDefs.LoadAndDelete(nadName)
		if ok {
			mc.ovnController.disconnectFromLayer2Network(nadName)
		}
	}
	v, ok := mc.allOvnControllers.Load(netconf.Name)
	if !ok {
		klog.Errorf("Failed to find network controller for network %s", netconf.Name)
		return
	}
	oc := v.(*Controller)
	_, ok = oc.nadInfo.NetAttachDefs.LoadAndDelete(nadName)
	if !ok {
		klog.Errorf("Failed to find nad %s from network controller for network %s", nadName, netconf.Name)
		return
	}
	oc.disconnectFromLayer2Network(nadName)

	// check if there any net-attach-def sharing the same CNI conf name left, if yes, just return
	netAttachDefLeft := false
	oc.nadInfo.NetAttachDefs.Range(func(key, value interface{}) bool {
		netAttachDefLeft = true
		return false
	})
	if netAttachDefLeft {
		return
	}
	klog.Infof("The last Network Attachment Definition %s/%s is deleted from nad %s, delete associated logical entities",
		netattachdef.Namespace, netattachdef.Name, netconf.Name)
	oc.wg.Wait()
	close(oc.stopChan)

	if oc.multiNetworkPolicyHandler != nil {
		oc.mc.watchFactory.RemoveMultiNetworkPolicyHandler(oc.multiNetworkPolicyHandler)
	}

	if oc.podHandler != nil {
		oc.mc.watchFactory.RemovePodHandler(oc.podHandler)
	}

	if oc.nodeHandler != nil {
		oc.mc.watchFactory.RemoveNodeHandler(oc.nodeHandler)
	}

	if oc.namespaceHandler != nil {
		oc.mc.watchFactory.RemoveNamespaceHandler(oc.namespaceHandler)
	}

	if oc.adminPBRHandler != nil {
		oc.mc.watchFactory.RemoveAdminPBRHandler(oc.adminPBRHandler)
	}
	if oc.adminPBRNodeHandler != nil {
		oc.mc.watchFactory.RemoveNodeHandler(oc.adminPBRNodeHandler)
	}
	if oc.adminPBRNamespaceHandler != nil {
		oc.mc.watchFactory.RemoveNamespaceHandler(oc.adminPBRNamespaceHandler)
	}
	if oc.virtualIPHandler != nil {
		oc.mc.watchFactory.RemoveVirtualIPHandler(oc.virtualIPHandler)
	}

	for namespace := range oc.namespaces {
		ns := kapi.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		oc.deleteNamespace(&ns)
	}

	oc.deleteMaster()

	if oc.nadInfo.TopoType != ovntypes.LocalnetAttachDefTopoType && oc.nadInfo.TopoType != ovntypes.Layer2AttachDefTopoType {
		existingNodes, err := oc.mc.kube.GetNodes()
		if err != nil {
			klog.Errorf("Error in initializing/fetching subnets: %v", err)
			return
		}

		// remove hostsubnet annoation for this network
		for _, node := range existingNodes.Items {
			if noHostSubnet(&node) {
				oc.lsManager.DeleteNode(util.GetClusterScopedName(nadInfo.Prefix + node.Name))
				continue
			}
			err := oc.deleteNodeLogicalNetwork(node.Name)
			if err != nil {
				klog.Errorf("Failed to delete node %s for network %s: %v", node.Name, oc.nadInfo.NetName, err)
			}
			_ = oc.updateNodeAnnotationWithRetry(node.Name, []*net.IPNet{})
			oc.lsManager.DeleteNode(util.GetClusterScopedName(nadInfo.Prefix + node.Name))
		}
	}
	mc.allOvnControllers.Delete(netconf.Name)
}

// syncNetworkAttachDefinition() walk through all net-attach-def and add them into Controller.nadInfo.NetAttachDefs
func (mc *OvnMHController) syncNetworkAttachDefinition(netattachdefs []interface{}) error {
	//// Get all the expected netNames
	//expectedNetworks := make(map[string]bool)

	// we need to walk through all net-attach-def and add them into Controller.nadInfo.NetAttachDefs, so that when each
	// Controller is running, watchPods()->addLogicalPod()->IsNetworkOnPod() can correctly check Pods need to be plumbed
	// for the specific Controller
	for _, netattachdefIntf := range netattachdefs {
		netattachdef, ok := netattachdefIntf.(*nettypes.NetworkAttachmentDefinition)
		if !ok {
			klog.Errorf("Spurious object in syncNetworkAttachDefinition: %v", netattachdefIntf)
			continue
		}

		// ovnController.nadInfo.NetAttachDefs
		_, err := mc.initOvnController(netattachdef)
		if err != nil {
			// if the net-attach-def is not managed by OVN, return silently
			if err != util.ErrorAttachDefNotOvnManaged {
				klog.Errorf(err.Error())
			}
			continue
		}
		//
		//if oc == nil {
		//	continue
		//}
		//expectedNetworks[oc.nadInfo.NetName] = true
	}

	//// Find all the logical node switches for the non-default networks and delete the ones that belong to the
	//// obsolete networks
	//nodeSwitches, err := libovsdbops.FindSwitchesWithOtherConfig(mc.nbClient)
	//if err != nil {
	//	klog.Errorf("Failed to get node logical switches which have other-config set error: %v", err)
	//	return
	//}
	//for _, nodeSwitch := range nodeSwitches {
	//	netName, ok := nodeSwitch.ExternalIDs["network_name"]
	//	if !ok {
	//		continue
	//	}
	//	if _, ok := expectedNetworks[netName]; ok {
	//		// network still exists, no cleanup to do
	//		continue
	//	}
	//	netPrefix := util.GetNetworkPrefix(netName, false)
	//	// items[0] is the switch name, which should be prefixed with netName
	//	if netName == ovntypes.DefaultNetworkName || !strings.HasPrefix(nodeSwitch.Name, netPrefix) {
	//		klog.Warningf("Unexpected logical switch %s for network %s during sync", nodeSwitch.Name, netName)
	//		continue
	//	}
	//
	//	nodeName := strings.TrimPrefix(nodeSwitch.Name, netPrefix)
	//	oc := &Controller{mc: mc, nadInfo: &util.NetAttachDefInfo{NetNameInfo: util.NetNameInfo{NetName: netName, Prefix: netPrefix, IsSecondary: true}}}
	//	if nodeName == ovntypes.OVNLocalnetSwitch {
	//		oc.nadInfo.TopoType = ovntypes.LocalnetAttachDefTopoType
	//		oc.deleteMaster()
	//	} else {
	//		if err := oc.deleteNodeLogicalNetwork(nodeName); err != nil {
	//			klog.Errorf("Error deleting node %s logical network: %v", nodeName, err)
	//		}
	//		_ = oc.updateNodeAnnotationWithRetry(nodeName, []*net.IPNet{})
	//	}
	//}
	//clusterRouters, err := libovsdbops.FindRoutersWitherExternalIds(mc.nbClient, map[string]string{"k8s-cluster-router": "yes"})
	//if err != nil {
	//	klog.Errorf("Failed to get all distributed logical routers: %v", err)
	//	return
	//}
	//for _, clusterRouter := range clusterRouters {
	//	netName, ok := clusterRouter.ExternalIDs["network_name"]
	//	if !ok {
	//		continue
	//	}
	//	if _, ok := expectedNetworks[netName]; ok {
	//		// network still exists, no cleanup to do
	//		continue
	//	}
	//
	//	netPrefix := util.GetNetworkPrefix(netName, false)
	//	// items[0] is the router name, which should be prefixed with netName
	//	if netName == ovntypes.DefaultNetworkName || !strings.HasPrefix(clusterRouter.Name, netPrefix) {
	//		klog.Warningf("Unexpected logical router %s for network %s during sync", clusterRouter.Name, netName)
	//		continue
	//	}
	//
	//	oc := &Controller{mc: mc, nadInfo: &util.NetAttachDefInfo{NetNameInfo: util.NetNameInfo{NetName: netName, Prefix: netPrefix, IsSecondary: true}}}
	//	oc.deleteMaster()
	//}
	return nil
}

// watchNetworkAttachmentDefinitions starts the watching of network attachment definition
// resource and calls back the appropriate handler logic
func (mc *OvnMHController) watchNetworkAttachmentDefinitions() (*factory.Handler, error) {
	return mc.watchFactory.AddNetworkattachmentdefinitionHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			netattachdef := obj.(*nettypes.NetworkAttachmentDefinition)
			mc.addNetworkAttachDefinition(netattachdef)
		},
		UpdateFunc: func(old, new interface{}) {},
		DeleteFunc: func(obj interface{}) {
			netattachdef := obj.(*nettypes.NetworkAttachmentDefinition)
			mc.deleteNetworkAttachDefinition(netattachdef)
		},
	}, mc.syncNetworkAttachDefinition)
}

// gatewayChanged() compares old annotations to new and returns true if something has changed.
func (oc *Controller) gatewayChanged(oldNode, newNode *kapi.Node) bool {
	if oc.nadInfo.IsSecondary {
		return false
	}
	oldL3GatewayConfig, _ := util.ParseNodeL3GatewayAnnotation(oldNode)
	l3GatewayConfig, _ := util.ParseNodeL3GatewayAnnotation(newNode)
	return !reflect.DeepEqual(oldL3GatewayConfig, l3GatewayConfig)
}

// hostAddressesChanged compares old annotations to new and returns true if the something has changed.
func hostAddressesChanged(oldNode, newNode *kapi.Node) bool {
	oldAddrs, _ := util.ParseNodeHostAddresses(oldNode)
	Addrs, _ := util.ParseNodeHostAddresses(newNode)
	return !oldAddrs.Equal(Addrs)
}

// macAddressChanged() compares old annotations to new and returns true if something has changed.
func macAddressChanged(oldNode, node *kapi.Node) bool {
	oldMacAddress, _ := util.ParseNodeManagementPortMACAddress(oldNode)
	macAddress, _ := util.ParseNodeManagementPortMACAddress(node)
	return !bytes.Equal(oldMacAddress, macAddress)
}

func nodeSubnetChanged(oldNode, node *kapi.Node, netName string) bool {
	oldSubnets, _ := util.ParseNodeHostSubnetAnnotation(oldNode, netName)
	newSubnets, _ := util.ParseNodeHostSubnetAnnotation(node, netName)
	return !reflect.DeepEqual(oldSubnets, newSubnets)
}

func nodeChassisChanged(oldNode, node *kapi.Node) bool {
	oldChassis, _ := util.ParseNodeChassisIDAnnotation(oldNode)
	newChassis, _ := util.ParseNodeChassisIDAnnotation(node)
	return oldChassis != newChassis
}

func skipPinnedLSChanged(oldNode, node *kapi.Node, nadInfo *util.NetAttachDefInfo) bool {
	oldSkipPinnedLS := util.ShouldSkipPinnedLS(oldNode, nadInfo)
	newSkipPinnedLS := util.ShouldSkipPinnedLS(node, nadInfo)
	return oldSkipPinnedLS != newSkipPinnedLS
}

// noHostSubnet() compares the no-hostsubenet-nodes flag with node labels to see if the node is manageing its
// own network.
func noHostSubnet(node *kapi.Node) bool {
	if config.Kubernetes.NoHostSubnetNodes == nil {
		return false
	}

	nodeSelector, _ := metav1.LabelSelectorAsSelector(config.Kubernetes.NoHostSubnetNodes)
	return nodeSelector.Matches(labels.Set(node.Labels))
}

// nonHostNetworkPodsExists verifies if node has pods non host network IP
func nonHostNetworkPodsExists(kube kube.Interface, node *kapi.Node) bool {
	nodeName := node.ObjectMeta.Name
	pods, err := kube.GetPodsFiltered("", "spec.nodeName="+nodeName)
	if err != nil {
		klog.Errorf("nonHostNetworkPodsExists: failed to get pods for Node '%s': %+v", nodeName, err)
		return true
	}
	for _, pod := range pods.Items {
		if !pod.Spec.HostNetwork {
			return true
		}
	}
	return false
}

// shouldUpdate() determines if the ovn-kubernetes plugin should update the state of the node.
// ovn-kube should not perform an update if it does not assign a hostsubnet, or if you want to change
// whether or not ovn-kubernetes assigns a hostsubnet
func shouldUpdate(kube kube.Interface, node, oldNode *kapi.Node) (bool, error) {
	newNoHostSubnet := noHostSubnet(node)
	oldNoHostSubnet := noHostSubnet(oldNode)

	if oldNoHostSubnet && newNoHostSubnet {
		return false, nil
	} else if oldNoHostSubnet && !newNoHostSubnet {
		if nonHostNetworkPodsExists(kube, node) {
			// if node has pods with non host network IP, then updating such node will be non-trivial task,
			// hence, return error
			return false, fmt.Errorf("error updating node %s, cannot remove assigned hostsubnet, please delete node and recreate.", node.Name)
		}
	} else if !oldNoHostSubnet && newNoHostSubnet {
		return false, fmt.Errorf("error updating node %s, cannot assign a hostsubnet to already created node, please delete node and recreate.", node.Name)
	}

	return true, nil
}

func newServiceController(client clientset.Interface, nbClient libovsdbclient.Client, recorder record.EventRecorder) (*svccontroller.Controller, informers.SharedInformerFactory) {
	// Create our own informers to start compartmentalizing the code
	// filter server side the things we don't care about
	noProxyName, err := labels.NewRequirement("service.kubernetes.io/service-proxy-name", selection.DoesNotExist, nil)
	if err != nil {
		panic(err)
	}

	noHeadlessEndpoints, err := labels.NewRequirement(kapi.IsHeadlessService, selection.DoesNotExist, nil)
	if err != nil {
		panic(err)
	}

	labelSelector := labels.NewSelector()
	labelSelector = labelSelector.Add(*noProxyName, *noHeadlessEndpoints)

	svcFactory := informers.NewSharedInformerFactoryWithOptions(client, 0,
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.LabelSelector = labelSelector.String()
		}))

	controller := svccontroller.NewController(
		client,
		nbClient,
		svcFactory.Core().V1().Services(),
		svcFactory.Discovery().V1().EndpointSlices(),
		svcFactory.Core().V1().Nodes(),
		recorder,
	)

	return controller, svcFactory
}

func (oc *Controller) StartServiceController(wg *sync.WaitGroup, runRepair bool) error {
	if oc.nadInfo.IsSecondary {
		klog.Infof("StartServiceController for network %s is a no-op", oc.nadInfo.NetName)
		return nil
	}
	klog.Infof("Starting OVN Service Controller: Using Endpoint Slices")
	wg.Add(1)
	go func() {
		defer wg.Done()
		useLBGroups := oc.loadBalancerGroupUUID != ""
		// use 5 workers like most of the kubernetes controllers in the
		// kubernetes controller-manager
		err := oc.svcController.Run(5, oc.stopChan, runRepair, useLBGroups)
		if err != nil {
			klog.Errorf("Error running OVN Kubernetes Services controller: %v", err)
		}
	}()
	return nil
}

func (oc *Controller) Init(ctx context.Context) error {
	oc.startMutex.Lock()
	if oc.isStarted {
		oc.startMutex.Unlock()
		return nil
	}
	oc.isStarted = true
	oc.startMutex.Unlock()
	klog.Infof("The first Network Attachment Definition is added to nad %s, create associated logical entities", oc.nadInfo.NetName)

	if err := oc.StartClusterMaster(); err != nil {
		return err
	}

	return oc.Run(ctx)
}
