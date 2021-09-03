package ovn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	ctypes "github.com/containernetworking/cni/pkg/types"
	"github.com/ebay/go-ovn"
	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	libovsdbclient "github.com/ovn-org/libovsdb/client"
	hocontroller "github.com/ovn-org/ovn-kubernetes/go-controller/hybrid-overlay/pkg/controller"
	cnitypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	egressipv1 "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/egressip/v1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/address_set"
	svccontroller "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/controller/services"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/controller/unidling"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/ipallocator"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/subnetallocator"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	egressfirewall "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/egressfirewall/v1"
	icmpnetworkpolicy "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/icmpnetworkpolicy/v1alpha1"

	multinetworkpolicy "github.com/k8snetworkplumbingwg/multi-networkpolicy/pkg/apis/k8s.cni.cncf.io/v1beta1"
	utilnet "k8s.io/utils/net"

	kapi "k8s.io/api/core/v1"
	kapisnetworking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	ref "k8s.io/client-go/tools/reference"
	"k8s.io/klog/v2"
)

const (
	clusterPortGroupName             string        = "clusterPortGroup"
	clusterRtrPortGroupName          string        = "clusterRtrPortGroup"
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
	nodeHostNetPodsCache map[string]map[string]bool

	// map from NetworkPolicy name to networkPolicy. You must hold the
	// namespaceInfo's mutex to add/delete/lookup policies, but must hold the
	// networkPolicy's mutex (and not necessarily the namespaceInfo's) to work with
	// the policy itself.
	networkPolicies map[string]*networkPolicy

	// routingExternalGWs is a slice of net.IP containing the values parsed from
	// annotation k8s.ovn.org/routing-external-gws
	routingExternalGWs gatewayInfo
	// podExternalRoutes is a cache keeping the LR routes added to the GRs when
	// the k8s.ovn.org/routing-external-gws annotation is used. The first map key
	// is the podIP, the second the GW and the third the GR
	podExternalRoutes map[string]map[string]string

	// routingExternalPodGWs contains a map of all pods serving as exgws as well as their
	// exgw IPs
	routingExternalPodGWs map[string]gatewayInfo

	// The UUID of the namespace-wide port group that contains all the pods in the namespace.
	portGroupUUID string

	multicastEnabled bool

	// If not empty, then it has to be set to a logging a severity level, e.g. "notice", "alert", etc
	aclLogging ACLLoggingLevels

	// Per-namespace port group default deny UUIDs
	portGroupIngressDenyUUID string // Port group UUID for ingress deny rule
	portGroupIngressDenyName string // Port group Name for ingress deny rule, without network prefix
	portGroupEgressDenyUUID  string // Port group UUID for egress deny rule
	portGroupEgressDenyName  string // Port group Name for egress deny rule, without network prefix
}

// multihome controller
type OvnMHController struct {
	client       clientset.Interface
	kube         kube.Interface
	watchFactory *factory.WatchFactory
	wg           *sync.WaitGroup
	stopChan     chan struct{}

	nodeName string

	// event recorder used to post events to k8s
	recorder record.EventRecorder

	// go-ovn northbound client interface
	ovnNBClient goovn.Client

	// go-ovn southbound client interface
	ovnSBClient goovn.Client

	// libovsdb northbound client interface
	nbClient libovsdbclient.Client

	// libovsdb southbound client interface
	sbClient libovsdbclient.Client

	// default network controller
	ovnController *Controller
	// controller for non default networks, key is netName of net-attach-def, value is *Controller
	nonDefaultOvnControllers sync.Map
}

// Controller structure is the object which holds the controls for starting
// and reacting upon the watched resources (e.g. pods, endpoints)
type Controller struct {
	mc                        *OvnMHController
	wg                        *sync.WaitGroup
	stopChan                  chan struct{}
	egressFirewallHandler     *factory.Handler
	icmpNetworkPolicyHandler  *factory.Handler
	podHandler                *factory.Handler
	nodeHandler               *factory.Handler
	namespaceHandler          *factory.Handler
	multiNetworkPolicyHandler *factory.Handler
	isStarted                 bool

	nadInfo *util.NetAttachDefInfo

	// configured cluster subnets
	clusterSubnets []config.CIDRNetworkEntry
	// FIXME DUAL-STACK -  Make IP Allocators more dual-stack friendly
	masterSubnetAllocator     *subnetallocator.SubnetAllocator
	nodeLocalNatIPv4Allocator *ipallocator.Range
	nodeLocalNatIPv6Allocator *ipallocator.Range

	hoMaster *hocontroller.MasterController

	SCTPSupport bool

	// For TCP, UDP, and SCTP type traffic, cache OVN load-balancers used for the
	// cluster's east-west traffic.
	loadbalancerClusterCache map[kapi.Protocol]string

	// A cache of all logical switches seen by the watcher and their subnets
	lsManager *logicalSwitchManager

	// A cache of all logical ports known to the controller
	logicalPortCache *portCache

	// Info about known namespaces. You must use oc.getNamespaceLocked() or
	// oc.waitForNamespaceLocked() to read this map, and oc.createNamespaceLocked()
	// or oc.deleteNamespaceLocked() to modify it. namespacesMutex is only held
	// from inside those functions.
	namespaces      map[string]*namespaceInfo
	namespacesMutex sync.Mutex

	// egressFirewalls is a map of namespaces and the egressFirewall attached to it
	egressFirewalls sync.Map

	// An address set factory that creates address sets
	addressSetFactory addressset.AddressSetFactory

	// Port group for all cluster logical switch ports
	clusterPortGroupUUID string

	// Port group for all node logical switch ports connected to the cluster
	// logical router
	clusterRtrPortGroupUUID string

	// For each logical port, the number of network policies that want
	// to add a ingress deny rule.
	lspIngressDenyCache map[string]int

	// For each logical port, the number of network policies that want
	// to add a egress deny rule.
	lspEgressDenyCache map[string]int

	// A mutex for lspIngressDenyCache and lspEgressDenyCache
	lspMutex *sync.Mutex

	// Supports multicast?
	multicastSupport bool

	// Controller used for programming OVN for egress IP
	eIPC egressIPController

	// Controller used to handle services
	svcController *svccontroller.Controller

	egressFirewallDNS *EgressDNS

	// Is ACL logging enabled while configuring meters?
	aclLoggingEnabled bool

	joinSwIPManager *joinSwitchIPManager

	// v4HostSubnetsUsed keeps track of number of v4 subnets currently assigned to nodes
	v4HostSubnetsUsed float64

	// v6HostSubnetsUsed keeps track of number of v6 subnets currently assigned to nodes
	v6HostSubnetsUsed float64

	// Map of pods that need to be retried, and the timestamp of when they last failed
	retryPods     map[types.UID]*retryEntry
	retryPodsLock sync.Mutex

	// channel to indicate we need to retry pods immediately
	retryPodsChan chan struct{}
}

type retryEntry struct {
	pod       *kapi.Pod
	timeStamp time.Time
	// whether to include this pod in retry iterations
	ignore bool
}

const (
	// TCP is the constant string for the string "TCP"
	TCP = "TCP"

	// UDP is the constant string for the string "UDP"
	UDP = "UDP"

	// SCTP is the constant string for the string "SCTP"
	SCTP = "SCTP"

	// ICMP is the constant string for the string "ICMP"
	ICMP = "ICMP"
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

func NewOvnMHController(ovnClient *util.OVNClientset, nodeName string, wf *factory.WatchFactory,
	stopChan chan struct{}, ovnNBClient goovn.Client, ovnSBClient goovn.Client,
	libovsdbOvnNBClient libovsdbclient.Client, libovsdbOvnSBClient libovsdbclient.Client,
	recorder record.EventRecorder, wg *sync.WaitGroup) *OvnMHController {
	return &OvnMHController{
		client: ovnClient.KubeClient,
		kube: &kube.Kube{
			KClient:              ovnClient.KubeClient,
			EIPClient:            ovnClient.EgressIPClient,
			EgressFirewallClient: ovnClient.EgressFirewallClient,
		},
		watchFactory: wf,
		wg:           wg,
		stopChan:     stopChan,
		recorder:     recorder,
		ovnNBClient:  ovnNBClient,
		ovnSBClient:  ovnSBClient,
		nbClient:     libovsdbOvnNBClient,
		sbClient:     libovsdbOvnSBClient,
		nodeName:     nodeName,
		//addressSetFactory: addressSetFactory,
	}
}

// If the default network net_attach_def does not exist, we'd need to create default OVN Controller based on config.
func (mc *OvnMHController) setDefaultOvnController(addressSetFactory addressset.AddressSetFactory) error {
	// default controller already exists, nothing to do.
	if mc.ovnController != nil {
		return nil
	}

	defaultNetConf := &cnitypes.NetConf{
		NetConf: ctypes.NetConf{
			Name: ovntypes.DefaultNetworkName,
		},
		NetCidr:    config.Default.RawClusterSubnets,
		MTU:        config.Default.MTU,
		NotDefault: false,
	}
	nadInfo, _ := util.NewNetAttachDefInfo(defaultNetConf)
	_, err := mc.NewOvnController(nadInfo, addressSetFactory)
	if err != nil {
		return err
	}
	return nil
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

	clusterIPNet, err := config.ParseClusterSubnetEntries(nadInfo.NetCidr, nadInfo.TopoType != ovntypes.LocalnetAttachDefTopoType)
	if err != nil {
		return nil, fmt.Errorf("cluster subnet %s for network %s is invalid: %v", nadInfo.NetCidr, nadInfo.NetName, err)
	}

	stopChan := mc.stopChan
	if nadInfo.NotDefault {
		stopChan = make(chan struct{})
	}
	var lsm *logicalSwitchManager
	if nadInfo.TopoType != ovntypes.LocalnetAttachDefTopoType {
		lsm = newLogicalSwitchManager()
	} else {
		lsm = newLocalnetSwitchManager()
		var hostSubnets []*net.IPNet
		for _, subnet := range clusterIPNet {
			hostSubnet := subnet.CIDR
			hostSubnets = append(hostSubnets, hostSubnet)
		}
		err = lsm.AddNode(ovntypes.OVNLocalnetSwitch, hostSubnets)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize localnet switch IP manager for network %s: %v", nadInfo.NetName, err)
		}
		for _, excludeCidr := range nadInfo.ExcludeCidrs {
			// convert IPNet struct mask and address to uint32
			// network is BigEndian
			var ip net.IP
			for ip = util.NextIP(excludeCidr.IP); ; ip = util.NextIP(ip) {
				if !excludeCidr.Contains(ip) {
					break
				}
				_ = lsm.AllocateIPs(ovntypes.OVNLocalnetSwitch, []*net.IPNet{{IP: ip, Mask: excludeCidr.Mask}})
			}
		}
	}
	oc := &Controller{
		mc:                        mc,
		stopChan:                  stopChan,
		nadInfo:                   nadInfo,
		clusterSubnets:            clusterIPNet,
		masterSubnetAllocator:     subnetallocator.NewSubnetAllocator(),
		nodeLocalNatIPv4Allocator: &ipallocator.Range{},
		nodeLocalNatIPv6Allocator: &ipallocator.Range{},
		lsManager:                 lsm,
		logicalPortCache:          newPortCache(stopChan),
		namespaces:                make(map[string]*namespaceInfo),
		namespacesMutex:           sync.Mutex{},
		addressSetFactory:         addressSetFactory,
		lspIngressDenyCache:       make(map[string]int),
		lspEgressDenyCache:        make(map[string]int),
		lspMutex:                  &sync.Mutex{},
		isStarted:                 false,
		eIPC: egressIPController{
			assignmentRetryMutex:  &sync.Mutex{},
			assignmentRetry:       make(map[string]bool),
			namespaceHandlerMutex: &sync.Mutex{},
			namespaceHandlerCache: make(map[string]factory.Handler),
			podHandlerMutex:       &sync.Mutex{},
			podHandlerCache:       make(map[string]factory.Handler),
			allocatorMutex:        &sync.Mutex{},
			allocator:             make(map[string]*egressNode),
		},
		loadbalancerClusterCache: make(map[kapi.Protocol]string),
		multicastSupport:         config.EnableMulticast,
		aclLoggingEnabled:        true,
		joinSwIPManager:          nil,
		retryPods:                make(map[types.UID]*retryEntry),
		retryPodsChan:            make(chan struct{}, 1),
	}
	if !nadInfo.NotDefault {
		oc.wg = mc.wg
		mc.ovnController = oc
		oc.svcController = newServiceController(mc.client, mc.nbClient, stopChan)
	} else {
		oc.multicastSupport = false
		oc.wg = &sync.WaitGroup{}
		_, loaded := mc.nonDefaultOvnControllers.LoadOrStore(nadInfo.NetName, oc)
		if loaded {
			return nil, fmt.Errorf("non default Network attachment definition %s already exists", nadInfo.NetName)
		}
	}
	return oc, nil
}

// Run starts the actual watching.
func (oc *Controller) Run(nodeName string) error {
	if !oc.nadInfo.NotDefault {
		oc.syncPeriodic()
	}
	klog.Infof("Starting all the Watchers for network %s...", oc.nadInfo.NetName)
	start := time.Now()

	// Sync external gateway routes. External gateway may be set in namespaces
	// or via pods. So execute an individual sync method at startup
	oc.cleanExGwECMPRoutes()

	// WatchNamespaces() should be started first because it has no other
	// dependencies, and WatchNodes() depends on it
	oc.WatchNamespaces()

	// WatchNodes must be started next because it creates the node switch
	// which most other watches depend on.
	// https://github.com/ovn-org/ovn-kubernetes/pull/859
	oc.WatchNodes()

	if !oc.nadInfo.NotDefault {
		// Services should be started after nodes to prevent LB churn
		if err := oc.StartServiceController(oc.wg, true); err != nil {
			return err
		}
	}

	oc.WatchPods()

	if !oc.nadInfo.NotDefault {
		// WatchNetworkPolicy depends on WatchPods and WatchNamespaces
		oc.WatchNetworkPolicy()

		if config.OVNKubernetesFeature.EnableICMPNetworkPolicy {
			oc.icmpNetworkPolicyHandler = oc.WatchICMPNetworkPolicy()
		}

		if config.OVNKubernetesFeature.EnableEgressIP {
			oc.WatchEgressNodes()
			oc.WatchEgressIP()
		}

		if config.OVNKubernetesFeature.EnableEgressFirewall {
			var err error
			oc.egressFirewallDNS, err = NewEgressDNS(oc.addressSetFactory, oc.stopChan)
			if err != nil {
				return err
			}
			oc.egressFirewallDNS.Run(egressFirewallDNSDefaultDuration)
			oc.egressFirewallHandler = oc.WatchEgressFirewall()
		}

		klog.Infof("Completing all the Watchers took %v", time.Since(start))
		if config.Kubernetes.OVNEmptyLbEvents {
			klog.Infof("Starting unidling controller")
			unidlingController := unidling.NewController(
				oc.mc.recorder,
				oc.mc.watchFactory.ServiceInformer(),
				oc.mc.sbClient,
			)
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
		if oc.nadInfo.NotDefault && config.OVNKubernetesFeature.EnableMultiNetworkPolicy {
			oc.multiNetworkPolicyHandler = oc.WatchMultiNetworkPolicy()
		}
		klog.Infof("Completing all the Watchers for network %s took %v", oc.nadInfo.NetName, time.Since(start))
	}

	// Final step to cleanup after resource handlers have synced
	err := oc.ovnTopologyCleanup()
	if err != nil {
		klog.Errorf("Failed to cleanup OVN topology to version %d: %v", ovntypes.OvnCurrentTopologyVersion, err)
		return err
	}

	// Master is fully running and resource handlers have synced, update Topology version in OVN
	var stdout, stderr string
	if oc.nadInfo.TopoType != ovntypes.LocalnetAttachDefTopoType {
		clusterRouterName := oc.nadInfo.Prefix + ovntypes.OVNClusterRouter
		stdout, stderr, err = util.RunOVNNbctl("set", "logical_router", clusterRouterName,
			fmt.Sprintf("external_ids:k8s-ovn-topo-version=%d", ovntypes.OvnCurrentTopologyVersion))
	} else {
		ovnLocalnetSwitch := oc.nadInfo.Prefix + ovntypes.OVNLocalnetSwitch
		stdout, stderr, err = util.RunOVNNbctl("set", "logical_switch", ovnLocalnetSwitch,
			fmt.Sprintf("external_ids:k8s-ovn-topo-version=%d", ovntypes.OvnCurrentTopologyVersion))
	}
	if err != nil {
		klog.Errorf("Failed to set topology version in OVN for network %s, "+
			"stdout: %q, stderr: %q, error: %v", oc.nadInfo.NetName, stdout, stderr, err)
		return err
	}

	if !oc.nadInfo.NotDefault {
		// Update topology version on node
		node, err := oc.mc.kube.GetNode(nodeName)
		if err != nil {
			return fmt.Errorf("unable to get node: %s", nodeName)
		}
		err = oc.mc.kube.SetAnnotationsOnNode(node, map[string]interface{}{ovntypes.OvnK8sTopoAnno: strconv.Itoa(ovntypes.OvnCurrentTopologyVersion)})
		if err != nil {
			return fmt.Errorf("failed to set topology annotation for node %s", node.Name)
		}
	}

	return nil
}

func (oc *Controller) ovnTopologyCleanup() error {
	ver, err := util.DetermineOVNTopoVersionFromOVN(oc.nadInfo.Prefix, oc.nadInfo.TopoType)
	if err != nil {
		return err
	}

	// Cleanup address sets in non dual stack formats in all versions known to possibly exist.
	if ver <= ovntypes.OvnPortBindingTopoVersion && !oc.nadInfo.NotDefault {
		err = addressset.NonDualStackAddressSetCleanup(oc.nadInfo.NetNameInfo, oc.mc.nbClient)
	}
	return err
}

// syncPeriodic adds a goroutine that periodically does some work
// right now there is only one ticker registered
// for syncNodesPeriodic which deletes chassis records from the sbdb
// every 5 minutes
func (oc *Controller) syncPeriodic() {
	if oc.nadInfo.NotDefault {
		return
	}

	go func() {
		nodeSyncTicker := time.NewTicker(5 * time.Minute)
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

func (oc *Controller) recordPodEvent(addErr error, pod *kapi.Pod) {
	podRef, err := ref.GetReference(scheme.Scheme, pod)
	if err != nil {
		klog.Errorf("Couldn't get a reference to pod %s/%s to post an event: '%v'",
			pod.Namespace, pod.Name, err)
	} else {
		klog.V(5).Infof("Posting a %s event for Pod %s/%s", kapi.EventTypeWarning, pod.Namespace, pod.Name)
		oc.mc.recorder.Eventf(podRef, kapi.EventTypeWarning, "ErrorAddingLogicalPort", addErr.Error())
	}
}

// iterateRetryPods checks if any outstanding pods have been waiting for 60 seconds of last known failure
// then tries to re-add them if so
// updateAll forces all pods to be attempted to be retried regardless of the 1 minute delay
func (oc *Controller) iterateRetryPods(updateAll bool) {
	oc.retryPodsLock.Lock()
	defer oc.retryPodsLock.Unlock()
	now := time.Now()
	for uid, podEntry := range oc.retryPods {
		if podEntry.ignore {
			continue
		}
		pod := podEntry.pod
		if !util.PodScheduled(pod) {
			continue
		}
		podTimer := podEntry.timeStamp.Add(time.Minute)
		if updateAll || now.After(podTimer) {
			podDesc := fmt.Sprintf("[%s/%s/%s]", pod.UID, pod.Namespace, pod.Name)
			// it could be that the Pod got deleted, but Pod's DeleteFunc has not been called yet, so don't retry
			_, err := oc.mc.watchFactory.GetPod(pod.Namespace, pod.Name)
			if err != nil {
				if e, ok := err.(*errors.StatusError); ok && e.ErrStatus.Code == http.StatusNotFound {
					klog.Infof("%s pod not found in the informers cache, not going to retry pod setup", podDesc)
					delete(oc.retryPods, uid)
					continue
				}
			}

			klog.Infof("%s retry pod setup", podDesc)

			if oc.ensurePod(nil, pod, true) {
				klog.Infof("%s pod setup successful", podDesc)
				delete(oc.retryPods, uid)
			} else {
				klog.Infof("%s setup retry failed; will try again later", podDesc)
				oc.retryPods[uid] = &retryEntry{pod, time.Now(), false}
			}
		}
	}
}

// checkAndDeleteRetryPod deletes a specific entry from the map, if it existed, returns true
func (oc *Controller) checkAndDeleteRetryPod(uid types.UID) bool {
	oc.retryPodsLock.Lock()
	defer oc.retryPodsLock.Unlock()
	if _, ok := oc.retryPods[uid]; ok {
		delete(oc.retryPods, uid)
		return true
	}
	return false
}

// checkAndSkipRetryPod sets a specific entry from the map to be ignored for subsequent retries
// if it existed, returns true
func (oc *Controller) checkAndSkipRetryPod(uid types.UID) bool {
	oc.retryPodsLock.Lock()
	defer oc.retryPodsLock.Unlock()
	if entry, ok := oc.retryPods[uid]; ok {
		entry.ignore = true
		return true
	}
	return false
}

// unSkipRetryPod ensures a pod is no longer ignored for retry loop
func (oc *Controller) unSkipRetryPod(pod *kapi.Pod) {
	oc.retryPodsLock.Lock()
	defer oc.retryPodsLock.Unlock()
	if entry, ok := oc.retryPods[pod.UID]; ok {
		entry.ignore = false
	}
}

// initRetryPod tracks a failed pod to potentially retry later
// initially it is marked as skipped for retry loop (ignore = true)
func (oc *Controller) initRetryPod(pod *kapi.Pod) {
	oc.retryPodsLock.Lock()
	defer oc.retryPodsLock.Unlock()
	if entry, ok := oc.retryPods[pod.UID]; ok {
		entry.timeStamp = time.Now()
	} else {
		oc.retryPods[pod.UID] = &retryEntry{pod, time.Now(), true}
	}
}

// addRetryPods adds multiple pods to retry later
func (oc *Controller) addRetryPods(pods []kapi.Pod) {
	oc.retryPodsLock.Lock()
	defer oc.retryPodsLock.Unlock()
	for _, pod := range pods {
		if entry, ok := oc.retryPods[pod.UID]; ok {
			entry.timeStamp = time.Now()
		} else {
			oc.retryPods[pod.UID] = &retryEntry{&pod, time.Now(), false}
		}
	}
}

func exGatewayAnnotationsChanged(oldPod, newPod *kapi.Pod) bool {
	return oldPod.Annotations[routingNamespaceAnnotation] != newPod.Annotations[routingNamespaceAnnotation] ||
		oldPod.Annotations[routingNetworkAnnotation] != newPod.Annotations[routingNetworkAnnotation] ||
		oldPod.Annotations[bfdAnnotation] != newPod.Annotations[bfdAnnotation]
}

func networkStatusAnnotationsChanged(oldPod, newPod *kapi.Pod) bool {
	return oldPod.Annotations[nettypes.NetworkStatusAnnot] != newPod.Annotations[nettypes.NetworkStatusAnnot]
}

func podNodeNameLabelChanged(pod *kapi.Pod, nodeNameLabel map[string]string) bool {
	// check if label already exists and is same as nodeNameLabel.
	return pod.Labels[util.OvnPodNodeNameLabel] != nodeNameLabel[util.OvnPodNodeNameLabel]
}

// ensurePod tries to set up a pod. It returns success or failure; failure
// indicates the pod should be retried later.
func (oc *Controller) ensurePod(oldPod, pod *kapi.Pod, addPort bool) bool {
	// Try unscheduled pods later
	if !util.PodScheduled(pod) {
		return false
	}

	if !oc.nadInfo.NotDefault {
		if oldPod != nil && (exGatewayAnnotationsChanged(oldPod, pod) || networkStatusAnnotationsChanged(oldPod, pod)) {
			// No matter if a pod is ovn networked, or host networked, we still need to check for exgw
			// annotations. If the pod is ovn networked and is in update reschedule, addLogicalPort will take
			// care of updating the exgw updates
			oc.deletePodExternalGW(oldPod)
		}

		nodeNameLabel := map[string]string{util.OvnPodNodeNameLabel: pod.Spec.NodeName}
		if podNodeNameLabelChanged(pod, nodeNameLabel) {
			err := oc.mc.kube.SetLabelsOnPod(pod, nodeNameLabel)
			if err != nil {
				klog.Errorf("Failed to set %s labels on pod %s: %v", util.OvnPodNodeNameLabel, pod.Name, err)
				oc.recordPodEvent(err, pod)
				return false
			}
		}
	}

	if util.PodWantsNetwork(pod) && addPort {
		if err := oc.addLogicalPort(pod); err != nil {
			klog.Errorf(err.Error())
			oc.recordPodEvent(err, pod)
			return false
		}
	} else {
		if oc.nadInfo.NotDefault {
			return true
		}

		// either pod is host-networked or its an update for a normal pod (addPort=false case)
		if oldPod == nil || exGatewayAnnotationsChanged(oldPod, pod) || networkStatusAnnotationsChanged(oldPod, pod) {
			if err := oc.addPodExternalGW(pod); err != nil {
				klog.Errorf(err.Error())
				oc.recordPodEvent(err, pod)
				return false
			}
		}
	}

	return true
}

func (oc *Controller) requestRetryPods() {
	select {
	case oc.retryPodsChan <- struct{}{}:
		klog.V(5).Infof("Iterate retry pods requested")
	default:
		klog.V(5).Infof("Iterate retry pods already requested")
	}
}

// WatchPods starts the watching of Pod resource and calls back the appropriate handler logic
func (oc *Controller) WatchPods() {
	go func() {
		// track the retryPods map and every 30 seconds check if any pods need to be retried
		for {
			select {
			case <-time.After(30 * time.Second):
				oc.iterateRetryPods(false)
			case <-oc.retryPodsChan:
				oc.iterateRetryPods(true)
			case <-oc.stopChan:
				return
			}
		}
	}()

	start := time.Now()
	oc.podHandler = oc.mc.watchFactory.AddPodHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod := obj.(*kapi.Pod)
			if pod.Spec.HostNetwork && util.PodScheduled(pod) && !oc.nadInfo.NotDefault {
				if err := oc.addHostNetworkPodToNamespace(pod); err != nil {
					klog.Warningf("Failed to add host network pod %s/%s's IPs on node %s to the namespace address_set: %v",
						pod.Namespace, pod.Name, pod.Spec.NodeName, err)
				}
			}
			oc.initRetryPod(pod)
			if !oc.ensurePod(nil, pod, true) {
				oc.unSkipRetryPod(pod)
				return
			}
			oc.checkAndDeleteRetryPod(pod.UID)
		},
		UpdateFunc: func(old, newer interface{}) {
			oldPod := old.(*kapi.Pod)
			pod := newer.(*kapi.Pod)
			if pod.Spec.HostNetwork && (oldPod.Spec.NodeName != pod.Spec.NodeName) && !oc.nadInfo.NotDefault {
				if util.PodScheduled(oldPod) {
					if err := oc.delHostNetworkPodFromNamespace(oldPod); err != nil {
						klog.Warningf("Failed to delete host network pod %s/%s's IPs on node %s from the namespace address_set: %v",
							oldPod.Namespace, oldPod.Name, oldPod.Spec.NodeName, err)
					}
				}
				if util.PodScheduled(pod) {
					if err := oc.addHostNetworkPodToNamespace(pod); err != nil {
						klog.Warningf("Failed to add host network pod %s/%s's IPs on node %s to the namespace address_set: %v",
							pod.Namespace, pod.Name, pod.Spec.NodeName, err)
					}
				}
			}
			if !oc.ensurePod(oldPod, pod, oc.checkAndSkipRetryPod(pod.UID)) {
				// unskip failed pod for next retry iteration
				oc.unSkipRetryPod(pod)
				return
			}
			oc.checkAndDeleteRetryPod(pod.UID)
		},
		DeleteFunc: func(obj interface{}) {
			pod := obj.(*kapi.Pod)
			if pod.Spec.HostNetwork && util.PodScheduled(pod) && !oc.nadInfo.NotDefault {
				if err := oc.delHostNetworkPodFromNamespace(pod); err != nil {
					klog.Warningf("Failed to delete host network pod %s/%s's IPs on node %s from the namespace address_set: %v",
						pod.Namespace, pod.Name, pod.Spec.NodeName, err)
				}
			}
			oc.checkAndDeleteRetryPod(pod.UID)
			if !util.PodWantsNetwork(pod) && !oc.nadInfo.NotDefault {
				oc.deletePodExternalGW(pod)
				return
			}
			// deleteLogicalPort will take care of removing exgw for ovn networked pods
			oc.deleteLogicalPort(pod)
		},
	}, oc.syncPods)
	klog.Infof("Bootstrapping existing pods and cleaning stale pods took %v", time.Since(start))
}

// WatchMultiNetworkPolicy starts the watching of multi network policy resource and calls
// back the appropriate handler logic
func (oc *Controller) WatchMultiNetworkPolicy() *factory.Handler {
	start := time.Now()
	if !oc.nadInfo.NotDefault {
		klog.Infof("WatchMultiNetworkPolicy for default network is a no-op")
		return nil
	}
	handler := oc.mc.watchFactory.AddMultiNetworkPolicyHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			policy := obj.(*multinetworkpolicy.MultiNetworkPolicy)
			oc.addMultiNetworkPolicy(policy)
		},
		UpdateFunc: func(old, newer interface{}) {
			oldPolicy := old.(*multinetworkpolicy.MultiNetworkPolicy)
			newPolicy := newer.(*multinetworkpolicy.MultiNetworkPolicy)
			if !reflect.DeepEqual(oldPolicy, newPolicy) {
				oc.deleteMultiNetworkPolicy(oldPolicy)
				oc.addMultiNetworkPolicy(newPolicy)
			}
		},
		DeleteFunc: func(obj interface{}) {
			policy := obj.(*multinetworkpolicy.MultiNetworkPolicy)
			oc.deleteMultiNetworkPolicy(policy)
		},
	}, oc.syncMultiNetworkPolicies)
	klog.Infof("Bootstrapping existing mulit network policies and cleaning stale policies took %v", time.Since(start))
	return handler
}

// WatchNetworkPolicy starts the watching of network policy resource and calls
// back the appropriate handler logic
func (oc *Controller) WatchNetworkPolicy() {
	if oc.nadInfo.NotDefault {
		klog.Infof("WatchNetworkPolicy for network %s is a no-op", oc.nadInfo.NetName)
		return
	}
	start := time.Now()
	oc.mc.watchFactory.AddPolicyHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			policy := obj.(*kapisnetworking.NetworkPolicy)
			oc.addNetworkPolicy(policy)
		},
		UpdateFunc: func(old, newer interface{}) {
			oldPolicy := old.(*kapisnetworking.NetworkPolicy)
			newPolicy := newer.(*kapisnetworking.NetworkPolicy)
			if !reflect.DeepEqual(oldPolicy, newPolicy) {
				oc.deleteNetworkPolicy(oldPolicy)
				oc.addNetworkPolicy(newPolicy)
			}
		},
		DeleteFunc: func(obj interface{}) {
			policy := obj.(*kapisnetworking.NetworkPolicy)
			oc.deleteNetworkPolicy(policy)
		},
	}, oc.syncNetworkPolicies)
	klog.Infof("Bootstrapping existing policies and cleaning stale policies took %v", time.Since(start))
}

// WatchICMPNetworkPolicy starts the watching of icmpnetworkpolicy resource and calls
// back the appropriate handler logic
func (oc *Controller) WatchICMPNetworkPolicy() *factory.Handler {
	if oc.nadInfo.NotDefault {
		klog.Infof("WatchNetworkPolicy for network %s is a no-op", oc.nadInfo.NetName)
		return nil
	}

	start := time.Now()
	defer func() {
		klog.Infof("Bootstrapping existing icmp policies and cleaning stale icmp policies took %v", time.Since(start))
	}()
	return oc.mc.watchFactory.AddICMPNetworkPolicyHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			icmpnetworkpolicy := obj.(*icmpnetworkpolicy.ICMPNetworkPolicy)
			oc.addICMPNetworkPolicy(icmpnetworkpolicy)
		},
		UpdateFunc: func(old, newer interface{}) {
			newpolicy := newer.(*icmpnetworkpolicy.ICMPNetworkPolicy)
			oldpolicy := old.(*icmpnetworkpolicy.ICMPNetworkPolicy)
			if !reflect.DeepEqual(oldpolicy, newpolicy) {
				oc.deleteICMPNetworkPolicy(oldpolicy)
				oc.addICMPNetworkPolicy(newpolicy)
			}
		},
		DeleteFunc: func(obj interface{}) {
			icmpnetworkpolicy := obj.(*icmpnetworkpolicy.ICMPNetworkPolicy)
			oc.deleteICMPNetworkPolicy(icmpnetworkpolicy)
		},
	}, oc.syncICMPNetworkPolicies)
}

// WatchEgressFirewall starts the watching of egressfirewall resource and calls
// back the appropriate handler logic
func (oc *Controller) WatchEgressFirewall() *factory.Handler {
	if oc.nadInfo.NotDefault {
		return nil
	}

	return oc.mc.watchFactory.AddEgressFirewallHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			egressFirewall := obj.(*egressfirewall.EgressFirewall).DeepCopy()
			txn := util.NewNBTxn()
			addErrors := oc.addEgressFirewall(egressFirewall, txn)
			if addErrors != nil {
				klog.Error(addErrors)
				egressFirewall.Status.Status = egressFirewallAddError
			} else {
				_, stderr, err := txn.Commit()
				if err != nil {
					klog.Errorf("Failed to commit db changes for egressFirewall in namespace %s stderr: %q, err: %+v", egressFirewall.Namespace, stderr, err)
					egressFirewall.Status.Status = egressFirewallAddError
				} else {
					egressFirewall.Status.Status = egressFirewallAppliedCorrectly
				}
			}

			err := oc.updateEgressFirewallWithRetry(egressFirewall)
			if err != nil {
				klog.Error(err)
			}
		},
		UpdateFunc: func(old, newer interface{}) {
			newEgressFirewall := newer.(*egressfirewall.EgressFirewall).DeepCopy()
			oldEgressFirewall := old.(*egressfirewall.EgressFirewall)
			if !reflect.DeepEqual(oldEgressFirewall.Spec, newEgressFirewall.Spec) {
				txn := util.NewNBTxn()
				errList := oc.updateEgressFirewall(oldEgressFirewall, newEgressFirewall, txn)
				if errList != nil {
					newEgressFirewall.Status.Status = egressFirewallUpdateError
					klog.Error(errList)
				} else {
					_, stderr, err := txn.Commit()
					if err != nil {
						klog.Errorf("Failed to commit db changes for egressFirewall in namespace %s stderr: %q, err: %+v", newEgressFirewall.Namespace, stderr, err)
						newEgressFirewall.Status.Status = egressFirewallUpdateError

					} else {
						newEgressFirewall.Status.Status = egressFirewallAppliedCorrectly
					}
				}
				err := oc.updateEgressFirewallWithRetry(newEgressFirewall)
				if err != nil {
					klog.Error(err)
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			egressFirewall := obj.(*egressfirewall.EgressFirewall)
			txn := util.NewNBTxn()
			deleteErrors := oc.deleteEgressFirewall(egressFirewall, txn)
			if deleteErrors != nil {
				klog.Error(deleteErrors)
				return
			}
			stdout, stderr, err := txn.Commit()
			if err != nil {
				klog.Errorf("Failed to commit db changes for egressFirewall in namespace %s stdout: %q, stderr: %q, err: %+v", egressFirewall.Namespace, stdout, stderr, err)
			}
		},
	}, oc.syncEgressFirewall)
}

// WatchEgressNodes starts the watching of egress assignable nodes and calls
// back the appropriate handler logic.
func (oc *Controller) WatchEgressNodes() {
	if oc.nadInfo.NotDefault {
		klog.Infof("WatchEgressNodes for network %s is a no-op", oc.nadInfo.NetName)
		return
	}

	nodeEgressLabel := util.GetNodeEgressLabel()
	oc.mc.watchFactory.AddNodeHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node := obj.(*kapi.Node)
			if err := oc.addNodeForEgress(node); err != nil {
				klog.Error(err)
			}
			nodeLabels := node.GetLabels()
			_, hasEgressLabel := nodeLabels[nodeEgressLabel]
			if hasEgressLabel {
				oc.setNodeEgressAssignable(node.Name, true)
			}
			isReady := oc.isEgressNodeReady(node)
			if isReady {
				oc.setNodeEgressReady(node.Name, true)
			}
			isReachable := oc.isEgressNodeReachable(node)
			if isReachable {
				oc.setNodeEgressReachable(node.Name, true)
			}
			if hasEgressLabel && isReachable && isReady {
				if err := oc.addEgressNode(node); err != nil {
					klog.Error(err)
				}
			}
		},
		UpdateFunc: func(old, new interface{}) {
			oldNode := old.(*kapi.Node)
			newNode := new.(*kapi.Node)
			if err := oc.initEgressIPAllocator(newNode); err != nil {
				klog.Error(err)
			}
			oldLabels := oldNode.GetLabels()
			newLabels := newNode.GetLabels()
			_, oldHadEgressLabel := oldLabels[nodeEgressLabel]
			_, newHasEgressLabel := newLabels[nodeEgressLabel]
			if !oldHadEgressLabel && !newHasEgressLabel {
				return
			}
			if oldHadEgressLabel && !newHasEgressLabel {
				klog.Infof("Node: %s has been un-labelled, deleting it from egress assignment", newNode.Name)
				oc.setNodeEgressAssignable(oldNode.Name, false)
				if err := oc.deleteEgressNode(oldNode); err != nil {
					klog.Error(err)
				}
				return
			}
			isOldReady := oc.isEgressNodeReady(oldNode)
			isNewReady := oc.isEgressNodeReady(newNode)
			isNewReachable := oc.isEgressNodeReachable(newNode)
			oc.setNodeEgressReady(newNode.Name, isNewReady)
			oc.setNodeEgressReachable(newNode.Name, isNewReachable)
			if !oldHadEgressLabel && newHasEgressLabel {
				klog.Infof("Node: %s has been labelled, adding it for egress assignment", newNode.Name)
				oc.setNodeEgressAssignable(newNode.Name, true)
				if isNewReady && isNewReachable {
					if err := oc.addEgressNode(newNode); err != nil {
						klog.Error(err)
					}
				} else {
					klog.Warningf("Node: %s has been labelled, but node is not ready and reachable, cannot use it for egress assignment", newNode.Name)
				}
				return
			}
			if isOldReady == isNewReady {
				return
			}
			if !isNewReady {
				klog.Warningf("Node: %s is not ready, deleting it from egress assignment", newNode.Name)
				if err := oc.deleteEgressNode(newNode); err != nil {
					klog.Error(err)
				}
			} else if isNewReady && isNewReachable {
				klog.Infof("Node: %s is ready and reachable, adding it for egress assignment", newNode.Name)
				if err := oc.addEgressNode(newNode); err != nil {
					klog.Error(err)
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			node := obj.(*kapi.Node)
			if err := oc.deleteNodeForEgress(node); err != nil {
				klog.Error(err)
			}
			nodeLabels := node.GetLabels()
			if _, hasEgressLabel := nodeLabels[nodeEgressLabel]; hasEgressLabel {
				if err := oc.deleteEgressNode(node); err != nil {
					klog.Error(err)
				}
			}
		},
	}, oc.initClusterEgressPolicies)
}

// WatchEgressIP starts the watching of egressip resource and calls
// back the appropriate handler logic.
func (oc *Controller) WatchEgressIP() {
	if oc.nadInfo.NotDefault {
		klog.Infof("WatchEgressIP for network %s is a no-op", oc.nadInfo.NetName)
		return
	}

	oc.mc.watchFactory.AddEgressIPHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			eIP := obj.(*egressipv1.EgressIP).DeepCopy()
			oc.eIPC.assignmentRetryMutex.Lock()
			defer oc.eIPC.assignmentRetryMutex.Unlock()
			if err := oc.addEgressIP(eIP); err != nil {
				klog.Error(err)
			}
			if err := oc.updateEgressIPWithRetry(eIP); err != nil {
				klog.Error(err)
			}
		},
		UpdateFunc: func(old, new interface{}) {
			oldEIP := old.(*egressipv1.EgressIP)
			newEIP := new.(*egressipv1.EgressIP).DeepCopy()
			if !reflect.DeepEqual(oldEIP.Spec, newEIP.Spec) {
				if err := oc.deleteEgressIP(oldEIP); err != nil {
					klog.Error(err)
				}
				newEIP.Status = egressipv1.EgressIPStatus{
					Items: []egressipv1.EgressIPStatusItem{},
				}
				oc.eIPC.assignmentRetryMutex.Lock()
				defer oc.eIPC.assignmentRetryMutex.Unlock()
				if err := oc.addEgressIP(newEIP); err != nil {
					klog.Error(err)
				}
				if err := oc.updateEgressIPWithRetry(newEIP); err != nil {
					klog.Error(err)
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			eIP := obj.(*egressipv1.EgressIP)
			if err := oc.deleteEgressIP(eIP); err != nil {
				klog.Error(err)
			}
		},
	}, oc.syncEgressIPs)
}

// WatchNamespaces starts the watching of namespace resource and calls
// back the appropriate handler logic
func (oc *Controller) WatchNamespaces() {
	start := time.Now()
	oc.namespaceHandler = oc.mc.watchFactory.AddNamespaceHandler(cache.ResourceEventHandlerFuncs{
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
}

// syncNodeGateway ensures a node's gateway router is configured
func (oc *Controller) syncNodeGateway(node *kapi.Node, hostSubnets []*net.IPNet) error {
	if oc.nadInfo.NotDefault {
		klog.Infof("WatchNamespaces for network %s is a no-op", oc.nadInfo.NetName)
		return nil
	}

	l3GatewayConfig, err := util.ParseNodeL3GatewayAnnotation(node)
	if err != nil {
		return err
	}

	if hostSubnets == nil {
		hostSubnets, _ = util.ParseNodeHostSubnetAnnotation(node, oc.nadInfo.NetName)
	}

	if l3GatewayConfig.Mode == config.GatewayModeDisabled {
		if err := gatewayCleanup(node.Name); err != nil {
			return fmt.Errorf("error cleaning up gateway for node %s: %v", node.Name, err)
		}
		if err := oc.joinSwIPManager.releaseJoinLRPIPs(node.Name); err != nil {
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

// WatchNodes starts the watching of node resource and calls
// back the appropriate handler logic
func (oc *Controller) WatchNodes() {
	var gatewaysFailed sync.Map
	var mgmtPortFailed sync.Map
	var addNodeFailed sync.Map
	var nodeClusterRouterPortFailed sync.Map

	if oc.nadInfo.TopoType == ovntypes.LocalnetAttachDefTopoType {
		return
	}
	start := time.Now()
	oc.nodeHandler = oc.mc.watchFactory.AddNodeHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node := obj.(*kapi.Node)
			if noHostSubnet := noHostSubnet(node); noHostSubnet {
				err := oc.lsManager.AddNoHostSubnetNode(node.Name)
				if err != nil {
					klog.Errorf("Error creating logical switch cache for node %s: %v", node.Name, err)
				}
				return
			}

			klog.V(5).Infof("Added event for Node %q", node.Name)
			hostSubnets, err := oc.addNode(node)
			if err != nil {
				klog.Errorf("NodeAdd: error creating subnet for node %s: %v", node.Name, err)
				addNodeFailed.Store(node.Name, true)
				mgmtPortFailed.Store(node.Name, true)
				gatewaysFailed.Store(node.Name, true)
				return
			}

			if err = oc.syncNodeClusterRouterPort(node, hostSubnets); err != nil {
				if !util.IsAnnotationNotSetError(err) {
					klog.Warningf(err.Error())
				}
				nodeClusterRouterPortFailed.Store(node.Name, true)
			}

			// ensure pods that already exist on this node have their logical ports created
			options := metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("spec.nodeName", node.Name).String()}
			pods, err := oc.mc.client.CoreV1().Pods(metav1.NamespaceAll).List(context.TODO(), options)
			if err != nil {
				klog.Errorf("Unable to list existing pods on node: %s, existing pods on this node may not function")
			} else {
				oc.addRetryPods(pods.Items)
				oc.requestRetryPods()
			}

			if oc.nadInfo.NotDefault {
				return
			}

			err = oc.syncNodeManagementPort(node, hostSubnets)
			if err != nil {
				if !util.IsAnnotationNotSetError(err) {
					klog.Warningf("Error creating management port for node %s: %v", node.Name, err)
				}
				mgmtPortFailed.Store(node.Name, true)
			}

			if err := oc.syncNodeGateway(node, hostSubnets); err != nil {
				if !util.IsAnnotationNotSetError(err) {
					klog.Warningf(err.Error())
				}
				gatewaysFailed.Store(node.Name, true)
			}
		},
		UpdateFunc: func(old, new interface{}) {
			oldNode := old.(*kapi.Node)
			node := new.(*kapi.Node)

			shouldUpdate, err := shouldUpdate(node, oldNode)
			if err != nil {
				klog.Errorf(err.Error())
			}
			if !shouldUpdate {
				// the hostsubnet is not assigned by ovn-kubernetes
				return
			}

			var hostSubnets []*net.IPNet
			_, failed := addNodeFailed.Load(node.Name)
			if failed {
				hostSubnets, err = oc.addNode(node)
				if err != nil {
					klog.Errorf("NodeUpdate: error creating subnet for node %s: %v", node.Name, err)
					return
				}
				addNodeFailed.Delete(node.Name)
			}

			_, failed = nodeClusterRouterPortFailed.Load(node.Name)
			if failed || nodeChassisChanged(oldNode, node) || nodeSubnetChanged(oldNode, node, oc.nadInfo.NetName) {
				if err = oc.syncNodeClusterRouterPort(node, nil); err != nil {
					if !util.IsAnnotationNotSetError(err) {
						klog.Warningf(err.Error())
					}
					nodeClusterRouterPortFailed.Store(node.Name, true)
				} else {
					nodeClusterRouterPortFailed.Delete(node.Name)
				}
			}

			if oc.nadInfo.NotDefault {
				return
			}

			_, failed = mgmtPortFailed.Load(node.Name)
			if failed || macAddressChanged(oldNode, node) || nodeSubnetChanged(oldNode, node, oc.nadInfo.NetName) {
				err := oc.syncNodeManagementPort(node, hostSubnets)
				if err != nil {
					if !util.IsAnnotationNotSetError(err) {
						klog.Errorf("Error updating management port for node %s: %v", node.Name, err)
					}
					mgmtPortFailed.Store(node.Name, true)
				} else {
					mgmtPortFailed.Delete(node.Name)
				}
			}

			if nodeChassisChanged(oldNode, node) {
				// delete stale chassis in SBDB if any
				oc.deleteStaleNodeChassis(node)
			}

			oc.clearInitialNodeNetworkUnavailableCondition(oldNode, node)

			_, failed = gatewaysFailed.Load(node.Name)
			if failed || oc.gatewayChanged(oldNode, node) || nodeSubnetChanged(oldNode, node, oc.nadInfo.NetName) || hostAddressesChanged(oldNode, node) {
				err := oc.syncNodeGateway(node, nil)
				if err != nil {
					if !util.IsAnnotationNotSetError(err) {
						klog.Errorf(err.Error())
					}
					gatewaysFailed.Store(node.Name, true)
				} else {
					gatewaysFailed.Delete(node.Name)
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			node := obj.(*kapi.Node)
			klog.V(5).Infof("Delete event for Node %q. Removing the node from "+
				"various caches", node.Name)

			nodeSubnets, _ := util.ParseNodeHostSubnetAnnotation(node, oc.nadInfo.NetName)
			dnatSnatIPs, _ := util.ParseNodeLocalNatIPAnnotation(node)
			oc.deleteNode(node.Name, nodeSubnets, dnatSnatIPs)
			oc.lsManager.DeleteNode(node.Name)
			addNodeFailed.Delete(node.Name)
			mgmtPortFailed.Delete(node.Name)
			gatewaysFailed.Delete(node.Name)
			nodeClusterRouterPortFailed.Delete(node.Name)
		},
	}, oc.syncNodes)
	klog.Infof("Bootstrapping existing nodes and cleaning stale nodes took %v", time.Since(start))
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
	okCnt := 0
	for _, s := range []string{"alert", "warning", "notice", "info", "debug"} {
		if aclLevels.Deny != "" && s == aclLevels.Deny {
			nsInfo.aclLogging.Deny = aclLevels.Deny
			okCnt++
		}
		if aclLevels.Allow != "" && s == aclLevels.Allow {
			nsInfo.aclLogging.Allow = aclLevels.Allow
			okCnt++
		}
	}
	return okCnt > 0
}

func (mc *OvnMHController) initOvnController(netattachdef *nettypes.NetworkAttachmentDefinition) (*Controller, error) {
	netconf := &cnitypes.NetConf{MTU: config.Default.MTU}

	// looking for network attachment definition that use OVN K8S CNI only
	err := json.Unmarshal([]byte(netattachdef.Spec.Config), &netconf)
	if err != nil {
		return nil, fmt.Errorf("error parsing Network Attachment Definition %s: %v", netattachdef.Name, err)
	}

	if netconf.Type != "ovn-k8s-cni-overlay" {
		klog.V(5).Infof("Network Attachment Definition %s is not based on OVN plugin", netattachdef.Name)
		return nil, nil
	}

	if netconf.Name == "" {
		netconf.Name = netattachdef.Name
	}

	nadInfo, err := util.NewNetAttachDefInfo(netconf)
	if err != nil {
		return nil, err
	}

	if !nadInfo.NotDefault {
		mc.ovnController.nadInfo.NetAttachDefs.Store(netattachdef.Namespace+"_"+netattachdef.Name, true)
		return mc.ovnController, nil
	}

	if nadInfo.NetName == ovntypes.DefaultNetworkName {
		return nil, fmt.Errorf("non-default Network attachment definition's name cannot be %s", ovntypes.DefaultNetworkName)
	}

	// Note that net-attach-def add/delete/update events are serialized, so we don't need locks here.
	// Check if any Controller of the same netconf.Name already exists, if so, check its conf to see if they are the same.
	v, ok := mc.nonDefaultOvnControllers.Load(nadInfo.NetName)
	if ok {
		oc := v.(*Controller)
		if oc.nadInfo.NetCidr != nadInfo.NetCidr || oc.nadInfo.MTU != nadInfo.MTU {
			return nil, fmt.Errorf("network attachment definition %s/%s does not share the same CNI config of name %s",
				netattachdef.Namespace, netattachdef.Name, nadInfo.NetName)
		} else {
			oc.nadInfo.NetAttachDefs.Store(netattachdef.Namespace+"_"+netattachdef.Name, true)
			return oc, nil
		}
	}

	nadInfo.NetAttachDefs.Store(netattachdef.Namespace+"_"+netattachdef.Name, true)
	return mc.NewOvnController(nadInfo, nil)
}

func (mc *OvnMHController) addNetworkAttachDefinition(netattachdef *nettypes.NetworkAttachmentDefinition) {
	oc, err := mc.initOvnController(netattachdef)
	if err != nil {
		klog.Errorf(err.Error())
		return
	}

	// This controller may already started if it is shared by multiple net-attach-def
	if oc == nil || oc.isStarted {
		return
	}

	// run the cluster controller to init the master
	err = oc.StartClusterMaster(mc.nodeName)
	if err != nil {
		klog.Errorf(err.Error())
		return
	}

	err = oc.Run(oc.mc.nodeName)
	if err != nil {
		klog.Errorf(err.Error())
	}
}

func (mc *OvnMHController) deleteNetworkAttachDefinition(netattachdef *nettypes.NetworkAttachmentDefinition) {
	netconf := &cnitypes.NetConf{}
	err := json.Unmarshal([]byte(netattachdef.Spec.Config), &netconf)
	if err != nil && netconf.Type != "ovn-k8s-cni-overlay" {
		return
	}

	if netconf.Type != "ovn-k8s-cni-overlay" {
		klog.V(5).Infof("Network Attachment Definition %s is not based on OVN plugin", netattachdef.Name)
		return
	}

	if netconf.Name == "" {
		netconf.Name = netattachdef.Name
	}

	nadInfo, err := util.NewNetAttachDefInfo(netconf)
	if err != nil {
		klog.Errorf(err.Error())
		return
	}

	if !nadInfo.NotDefault {
		mc.ovnController.nadInfo.NetAttachDefs.Delete(netattachdef.Namespace + "_" + netattachdef.Name)
		return
	}

	v, ok := mc.nonDefaultOvnControllers.Load(nadInfo.NetName)
	if !ok {
		klog.Errorf("Failed to find network controller for network %s", nadInfo.NetName)
		return
	}

	oc := v.(*Controller)
	oc.nadInfo.NetAttachDefs.Delete(netattachdef.Namespace + "_" + netattachdef.Name)

	// check if there any net-attach-def sharing the same CNI conf name left, if yes, just return
	netAttachDefLeft := false
	oc.nadInfo.NetAttachDefs.Range(func(key, value interface{}) bool {
		netAttachDefLeft = true
		return false
	})

	if netAttachDefLeft {
		return
	}

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

	for namespace := range oc.namespaces {
		ns := kapi.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		oc.deleteNamespace(&ns)
	}

	oc.deleteMaster()

	if oc.nadInfo.TopoType != ovntypes.LocalnetAttachDefTopoType {
		existingNodes, err := oc.mc.kube.GetNodes()
		if err != nil {
			klog.Errorf("Error in initializing/fetching subnets: %v", err)
			return
		}

		// remove hostsubnet annoation for this network
		for _, node := range existingNodes.Items {
			err := oc.deleteNodeLogicalNetwork(node.Name)
			if err != nil {
				klog.Error("Failed to delete node %s for network %s: %v", node.Name, oc.nadInfo.NetName, err)
			}
			_ = oc.updateNodeAnnotationWithRetry(node.Name, []*net.IPNet{})
			oc.lsManager.DeleteNode(node.Name)
		}
	}

	mc.nonDefaultOvnControllers.Delete(nadInfo.NetName)
}

// syncNetworkAttachDefinition() delete OVN logical entities of the obsoleted netNames.
func (mc *OvnMHController) syncNetworkAttachDefinition(netattachdefs []interface{}) {
	// Get all the expected netNames
	expectedNetworks := make(map[string]bool)

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
		oc, err := mc.initOvnController(netattachdef)
		if err != nil {
			klog.Errorf(err.Error())
			continue
		}

		if oc == nil {
			continue
		}
		expectedNetworks[oc.nadInfo.NetName] = true
	}

	// Find all the logical node switches for the non-default networks and delete the ones that belong to the
	// obsolete networks
	nodeSwitches, stderr, err := util.RunOVNNbctl("--data=bare", "--format=csv", "--no-heading",
		"--columns=name,external_ids", "find", "logical_switch", "external_ids:network_name!=_")
	if err != nil {
		klog.Errorf("No logical switches with non-default network: stderr: %q, error: %v", stderr, err)
		return
	}
	for _, result := range strings.Split(nodeSwitches, "\n") {
		items := strings.Split(result, ",")
		if len(items) != 2 || len(items[0]) == 0 {
			continue
		}

		netName := util.GetNetworkNameFromExternalId(items[1])
		if _, ok := expectedNetworks[netName]; ok {
			// network still exists, no cleanup to do
			continue
		}

		netPrefix := util.GetNetworkPrefix(netName, false)
		// items[0] is the switch name, which should be prefixed with netName
		if netName == ovntypes.DefaultNetworkName || !strings.HasPrefix(items[0], netPrefix) {
			klog.Warningf("Unexpected logical switch %s for network %s during sync external_id %s", items[0], netName, items[1])
			continue
		}

		nodeName := strings.TrimPrefix(items[0], netPrefix)
		oc := &Controller{nadInfo: &util.NetAttachDefInfo{NetNameInfo: util.NetNameInfo{NetName: netName, Prefix: netPrefix, NotDefault: true}}}
		if nodeName == ovntypes.OVNLocalnetSwitch {
			oc.nadInfo.TopoType = ovntypes.LocalnetAttachDefTopoType
			oc.deleteMaster()
		} else {
			if err := oc.deleteNodeLogicalNetwork(nodeName); err != nil {
				klog.Errorf("Error deleting node %s logical network: %v", nodeName, err)
			}
			_ = oc.updateNodeAnnotationWithRetry(nodeName, []*net.IPNet{})
		}
	}
	clusterRouters, stderr, err := util.RunOVNNbctl("--data=bare", "--format=csv", "--no-heading",
		"--columns=name,external_ids", "find", "logical_router", "external_ids:network_name!=_")
	if err != nil {
		klog.Errorf("Failed to get logical routers with non-default network name: stderr: %q, error: %v",
			stderr, err)
		return
	}
	for _, result := range strings.Split(clusterRouters, "\n") {
		items := strings.Split(result, ",")
		if len(items) != 2 || len(items[0]) == 0 {
			continue
		}

		netName := util.GetNetworkNameFromExternalId(items[1])
		if _, ok := expectedNetworks[netName]; ok {
			// network still exists, no cleanup to do
			continue
		}

		netPrefix := util.GetNetworkPrefix(netName, false)
		// items[0] is the router name, which should be prefixed with netName
		if netName == ovntypes.DefaultNetworkName || !strings.HasPrefix(items[0], netPrefix) {
			klog.Warningf("Unexpected logical router %s for network %s during sync, external_ids: %s", items[0], netPrefix, items[1])
			continue
		}

		oc := &Controller{nadInfo: &util.NetAttachDefInfo{NetNameInfo: util.NetNameInfo{NetName: netName, Prefix: netPrefix, NotDefault: true}}}
		oc.deleteMaster()
	}
}

// watchNetworkAttachmentDefinitions starts the watching of network attachment definition
// resource and calls back the appropriate handler logic
func (mc *OvnMHController) watchNetworkAttachmentDefinitions() *factory.Handler {
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
	if oc.nadInfo.NotDefault {
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

// noHostSubnet() compares the no-hostsubenet-nodes flag with node labels to see if the node is manageing its
// own network.
func noHostSubnet(node *kapi.Node) bool {
	if config.Kubernetes.NoHostSubnetNodes == nil {
		return false
	}

	nodeSelector, _ := metav1.LabelSelectorAsSelector(config.Kubernetes.NoHostSubnetNodes)
	return nodeSelector.Matches(labels.Set(node.Labels))
}

// shouldUpdate() determines if the ovn-kubernetes plugin should update the state of the node.
// ovn-kube should not perform an update if it does not assign a hostsubnet, or if you want to change
// whether or not ovn-kubernetes assigns a hostsubnet
func shouldUpdate(node, oldNode *kapi.Node) (bool, error) {
	newNoHostSubnet := noHostSubnet(node)
	oldNoHostSubnet := noHostSubnet(oldNode)

	if oldNoHostSubnet && newNoHostSubnet {
		return false, nil
	} else if oldNoHostSubnet && !newNoHostSubnet {
		return false, fmt.Errorf("error updating node %s, cannot remove assigned hostsubnet, please delete node and recreate.", node.Name)
	} else if !oldNoHostSubnet && newNoHostSubnet {
		return false, fmt.Errorf("error updating node %s, cannot assign a hostsubnet to already created node, please delete node and recreate.", node.Name)
	}

	return true, nil
}

func newServiceController(client clientset.Interface, nbClient libovsdbclient.Client, stopChan <-chan struct{}) *svccontroller.Controller {
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
		svcFactory.Discovery().V1beta1().EndpointSlices(),
		svcFactory.Core().V1().Nodes(),
	)

	svcFactory.Start(stopChan)

	return controller
}

func (oc *Controller) StartServiceController(wg *sync.WaitGroup, runRepair bool) error {
	if oc.nadInfo.NotDefault {
		klog.Infof("StartServiceController for network %s is a no-op", oc.nadInfo.NetName)
		return nil
	}
	klog.Infof("Starting OVN Service Controller: Using Endpoint Slices")
	wg.Add(1)
	go func() {
		defer wg.Done()
		// use 5 workers like most of the kubernetes controllers in the
		// kubernetes controller-manager
		err := oc.svcController.Run(5, oc.stopChan, runRepair, oc.clusterPortGroupUUID)
		if err != nil {
			klog.Errorf("Error running OVN Kubernetes Services controller: %v", err)
		}
	}()
	return nil
}
