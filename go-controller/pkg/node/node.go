package node

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	kapi "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"

	ctypes "github.com/containernetworking/cni/pkg/types"
	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	honode "github.com/ovn-org/ovn-kubernetes/go-controller/hybrid-overlay/pkg/controller"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni"
	cnitypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/informer"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/node/controllers/upgrade"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

// OvnNode is the object holder for utilities meant for node management
type OvnNode struct {
	name         string
	client       clientset.Interface
	Kube         kube.Interface
	watchFactory factory.NodeWatchFactory
	stopChan     chan struct{}
	recorder     record.EventRecorder
	gateway      Gateway

	defaultNodeController     *ovnNodeController
	nonDefaultNodeControllers sync.Map
}

type ovnNodeController struct {
	node       *OvnNode
	nadInfo    *util.NetAttachDefInfo
	podHandler *factory.Handler
	added      bool
}

// NewNode creates a new controller for node management
func NewNode(kubeClient clientset.Interface, wf factory.NodeWatchFactory, name string, stopChan chan struct{}, eventRecorder record.EventRecorder) *OvnNode {
	return &OvnNode{
		name:         name,
		client:       kubeClient,
		Kube:         &kube.Kube{KClient: kubeClient},
		watchFactory: wf,
		stopChan:     stopChan,
		recorder:     eventRecorder,
	}
}

func setupOVNNode(node *kapi.Node) error {
	var err error

	encapIP := config.Default.EncapIP
	if encapIP == "" {
		encapIP, err = util.GetNodePrimaryIP(node)
		if err != nil {
			return fmt.Errorf("failed to obtain local IP from node %q: %v", node.Name, err)
		}
	} else {
		if ip := net.ParseIP(encapIP); ip == nil {
			return fmt.Errorf("invalid encapsulation IP provided %q", encapIP)
		}
	}

	setExternalIdsCmd := []string{
		"set",
		"Open_vSwitch",
		".",
		fmt.Sprintf("external_ids:ovn-encap-type=%s", config.Default.EncapType),
		fmt.Sprintf("external_ids:ovn-encap-ip=%s", encapIP),
		fmt.Sprintf("external_ids:ovn-remote-probe-interval=%d",
			config.Default.InactivityProbe),
		fmt.Sprintf("external_ids:ovn-openflow-probe-interval=%d",
			config.Default.OpenFlowProbe),
		fmt.Sprintf("external_ids:hostname=\"%s\"", node.Name),
		"external_ids:ovn-monitor-all=true",
		fmt.Sprintf("external_ids:ovn-enable-lflow-cache=%t", config.Default.LFlowCacheEnable),
	}

	if config.Default.LFlowCacheLimit > 0 {
		setExternalIdsCmd = append(setExternalIdsCmd,
			fmt.Sprintf("external_ids:ovn-limit-lflow-cache=%d", config.Default.LFlowCacheLimit),
		)
	}

	if config.Default.LFlowCacheLimitKb > 0 {
		setExternalIdsCmd = append(setExternalIdsCmd,
			fmt.Sprintf("external_ids:ovn-limit-lflow-cache-kb=%d", config.Default.LFlowCacheLimitKb),
		)
	}

	_, stderr, err := util.RunOVSVsctl(setExternalIdsCmd...)
	if err != nil {
		return fmt.Errorf("error setting OVS external IDs: %v\n  %q", err, stderr)
	}
	// If EncapPort is not the default tell sbdb to use specified port.
	if config.Default.EncapPort != config.DefaultEncapPort {
		systemID, err := util.GetNodeChassisID()
		if err != nil {
			return err
		}
		uuid, _, err := util.RunOVNSbctl("--data=bare", "--no-heading", "--columns=_uuid", "find", "Encap",
			fmt.Sprintf("chassis_name=%s", systemID))
		if err != nil {
			return err
		}
		if len(uuid) == 0 {
			return fmt.Errorf("unable to find encap uuid to set geneve port for chassis %s", systemID)
		}
		_, stderr, errSet := util.RunOVNSbctl("set", "encap", uuid,
			fmt.Sprintf("options:dst_port=%d", config.Default.EncapPort),
		)
		if errSet != nil {
			return fmt.Errorf("error setting OVS encap-port: %v\n  %q", errSet, stderr)
		}
	}
	if config.Monitoring.NetFlowTargets != nil {
		collectors := ""
		for _, v := range config.Monitoring.NetFlowTargets {
			collectors += "\"" + util.JoinHostPortInt32(v.Host.String(), v.Port) + "\"" + ","
		}
		collectors = strings.TrimSuffix(collectors, ",")

		_, stderr, err := util.RunOVSVsctl(
			"--",
			"--id=@netflow",
			"create",
			"netflow",
			fmt.Sprintf("targets=[%s]", collectors),
			"active_timeout=60",
			"--",
			"set", "bridge", "br-int", "netflow=@netflow",
		)
		if err != nil {
			return fmt.Errorf("error setting NetFlow: %v\n  %q", err, stderr)
		}
	}
	if config.Monitoring.SFlowTargets != nil {
		collectors := ""
		for _, v := range config.Monitoring.SFlowTargets {
			collectors += "\"" + util.JoinHostPortInt32(v.Host.String(), v.Port) + "\"" + ","
		}
		collectors = strings.TrimSuffix(collectors, ",")

		_, stderr, err := util.RunOVSVsctl(
			"--",
			"--id=@sflow",
			"create",
			"sflow",
			"agent="+types.SFlowAgent,
			fmt.Sprintf("targets=[%s]", collectors),
			"--",
			"set", "bridge", "br-int", "sflow=@sflow",
		)
		if err != nil {
			return fmt.Errorf("error setting SFlow: %v\n  %q", err, stderr)
		}
	}
	if config.Monitoring.IPFIXTargets != nil {
		collectors := ""
		for _, v := range config.Monitoring.IPFIXTargets {
			collectors += "\"" + util.JoinHostPortInt32(v.Host.String(), v.Port) + "\"" + ","
		}
		collectors = strings.TrimSuffix(collectors, ",")

		_, stderr, err := util.RunOVSVsctl(
			"--",
			"--id=@ipfix",
			"create",
			"ipfix",
			fmt.Sprintf("targets=[%s]", collectors),
			"cache_active_timeout=60",
			"--",
			"set", "bridge", "br-int", "ipfix=@ipfix",
		)
		if err != nil {
			return fmt.Errorf("error setting IPFIX: %v\n  %q", err, stderr)
		}
	}
	return nil
}

func isOVNControllerReady(name string) (bool, error) {
	runDir := util.GetOvnRunDir()

	pid, err := ioutil.ReadFile(runDir + "ovn-controller.pid")
	if err != nil {
		return false, fmt.Errorf("unknown pid for ovn-controller process: %v", err)
	}

	err = wait.PollImmediate(500*time.Millisecond, 60*time.Second, func() (bool, error) {
		ctlFile := runDir + fmt.Sprintf("ovn-controller.%s.ctl", strings.TrimSuffix(string(pid), "\n"))
		ret, _, err := util.RunOVSAppctl("-t", ctlFile, "connection-status")
		if err == nil {
			klog.Infof("Node %s connection status = %s", name, ret)
			return ret == "connected", nil
		}
		return false, err
	})
	if err != nil {
		return false, fmt.Errorf("timed out waiting sbdb for node %s: %v", name, err)
	}

	err = wait.PollImmediate(500*time.Millisecond, 60*time.Second, func() (bool, error) {
		_, _, err := util.RunOVSVsctl("--", "br-exists", "br-int")
		if err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return false, fmt.Errorf("timed out checking whether br-int exists or not on node %s: %v", name, err)
	}

	err = wait.PollImmediate(500*time.Millisecond, 60*time.Second, func() (bool, error) {
		stdout, _, err := util.RunOVSOfctl("dump-aggregate", "br-int")
		if err != nil {
			klog.V(5).Infof("Error dumping aggregate flows: %v "+
				"for node: %s", err, name)
			return false, nil
		}
		ret := strings.Contains(stdout, "flow_count=0")
		if ret {
			klog.V(5).Infof("Got a flow count of 0 when "+
				"dumping flows for node: %s", name)
		}
		return !ret, nil
	})
	if err != nil {
		return false, fmt.Errorf("timed out dumping br-int flow entries for node %s: %v", name, err)
	}

	return true, nil
}

func (n *OvnNode) NewOvnNodeController(nadInfo *util.NetAttachDefInfo) (*ovnNodeController, error) {
	nc := &ovnNodeController{
		node:    n,
		nadInfo: nadInfo,
		added:   false,
	}
	if !nadInfo.NotDefault {
		n.defaultNodeController = nc
	} else {
		_, loaded := n.nonDefaultNodeControllers.LoadOrStore(nadInfo.NetName, nc)
		if loaded {
			return nil, fmt.Errorf("non default Network attachment definition %s already exists", nadInfo.NetName)
		}
	}
	return nc, nil
}

// Start learns the subnets assigned to it by the master controller
// and calls the SetupNode script which establishes the logical switch
func (n *OvnNode) Start(wg *sync.WaitGroup) error {
	var err error
	var node *kapi.Node
	var subnets []*net.IPNet
	var mgmtPort ManagementPort
	var mgmtPortConfig *managementPortConfig
	var cniServer *cni.Server
	networkUnavailableTaint := &kapi.Taint{
		Key:    types.OvnK8sNetworkUnavailable,
		Effect: kapi.TaintEffectNoSchedule,
	}

	klog.Infof("OVN Kube Node initialization, Mode: %s", config.OvnKubeNode.Mode)

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-n.stopChan
		klog.Infof("Received node's stop channel signal. Adding taint %s.", networkUnavailableTaint.ToString())
		// Add the NoSchedule Taint on the node, before ovnkube pod gets deleted. Ignore errors.
		err := n.Kube.SetTaintOnNode(n.name, networkUnavailableTaint)
		if err != nil {
			klog.Infof("Unable to add taint %s on node %s: %v", networkUnavailableTaint.ToString(), n.name, err)
		}
	}()

	// Setting debug log level during node bring up to expose bring up process.
	// Log level is returned to configured value when bring up is complete.
	var level klog.Level
	if err := level.Set("5"); err != nil {
		klog.Errorf("Setting klog \"loglevel\" to 5 failed, err: %v", err)
	}

	if node, err = n.Kube.GetNode(n.name); err != nil {
		return fmt.Errorf("error retrieving node %s: %v", n.name, err)
	}

	nodeAddrStr, err := util.GetNodePrimaryIP(node)
	if err != nil {
		return err
	}
	nodeAddr := net.ParseIP(nodeAddrStr)
	if nodeAddr == nil {
		return fmt.Errorf("failed to parse kubernetes node IP address. %v", err)
	}

	if config.OvnKubeNode.Mode != types.NodeModeSmartNICHost {
		for _, auth := range []config.OvnAuthConfig{config.OvnNorth, config.OvnSouth} {
			if err := auth.SetDBAuth(); err != nil {
				return err
			}
		}

		err = setupOVNNode(node)
		if err != nil {
			return err
		}
	}

	// First wait for the node logical switch to be created by the Master, timeout is 300s.
	err = wait.PollImmediate(500*time.Millisecond, 300*time.Second, func() (bool, error) {
		if node, err = n.Kube.GetNode(n.name); err != nil {
			klog.Infof("Waiting to retrieve node %s: %v", n.name, err)
			return false, nil
		}
		subnets, err = util.ParseNodeHostSubnetAnnotation(node, types.DefaultNetworkName)
		if err != nil {
			klog.Infof("Waiting for node %s to start, no annotation found on node for subnet: %v", n.name, err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("timed out waiting for node's: %q logical switch: %v", n.name, err)
	}
	klog.Infof("Node %s ready for ovn initialization with subnet %s", n.name, util.JoinIPNets(subnets, ","))

	// Create CNI Server
	if config.OvnKubeNode.Mode != types.NodeModeSmartNIC {
		kclient, ok := n.Kube.(*kube.Kube)
		if !ok {
			return fmt.Errorf("cannot get kubeclient for starting CNI server")
		}
		cniServer, err = cni.NewCNIServer("", true, n.watchFactory, kclient.KClient)
		if err != nil {
			return err
		}
	}

	// Setup Management port and gateway
	if config.OvnKubeNode.Mode != types.NodeModeSmartNICHost {
		if _, err = isOVNControllerReady(n.name); err != nil {
			return err
		}
	}

	mgmtPort = NewManagementPort(n.name, subnets)
	nodeAnnotator := kube.NewNodeAnnotator(n.Kube, node)
	waiter := newStartupWaiter()

	mgmtPortConfig, err = mgmtPort.Create(nodeAnnotator, waiter)
	if err != nil {
		return err
	}

	// Initialize gateway
	if config.OvnKubeNode.Mode == types.NodeModeSmartNICHost {
		err = n.initGatewaySmartNicHost(nodeAddr)
		if err != nil {
			return err
		}
	} else {
		if err := n.initGateway(subnets, nodeAnnotator, waiter, mgmtPortConfig, nodeAddr); err != nil {
			return err
		}
	}

	if err := nodeAnnotator.Run(); err != nil {
		return fmt.Errorf("failed to set node %s annotations: %v", n.name, err)
	}

	// Wait for management port and gateway resources to be created by the master
	klog.Infof("Waiting for gateway and management port readiness...")
	start := time.Now()
	if err := waiter.Wait(); err != nil {
		return err
	}
	go n.gateway.Run(n.stopChan, wg)
	klog.Infof("Gateway and management port readiness took %v", time.Since(start))

	// Note(adrianc): Smart-NIC deployments are expected to support the new shared gateway changes, upgrade flow
	// is not needed. Future upgrade flows will need to take Smart-NICs into account.
	if config.OvnKubeNode.Mode == types.NodeModeFull {
		// Upgrade for Node. If we upgrade workers before masters, then we need to keep service routing via
		// mgmt port until masters have been updated and modified OVN config. Run a goroutine to handle this case

		// note this will change in the future to control-plane:
		// https://github.com/kubernetes/kubernetes/pull/95382
		masterNode, err := labels.NewRequirement("node-role.kubernetes.io/master", selection.Exists, nil)
		if err != nil {
			return err
		}

		labelSelector := labels.NewSelector()
		labelSelector = labelSelector.Add(*masterNode)

		informerFactory := informers.NewSharedInformerFactoryWithOptions(n.client, 0,
			informers.WithTweakListOptions(func(options *metav1.ListOptions) {
				options.LabelSelector = labelSelector.String()
			}))

		upgradeController := upgrade.NewController(n.Kube, informerFactory.Core().V1().Nodes())
		initialTopoVersion := upgradeController.GetInitialTopoVersion()
		bridgeName := n.gateway.GetGatewayBridgeIface()

		needLegacySvcRoute := true
		if initialTopoVersion >= types.OvnHostToSvcOFTopoVersion && config.GatewayModeShared == config.Gateway.Mode {
			// Configure route for svc towards shared gw bridge
			// Have to have the route to bridge for multi-NIC mode, where the default gateway may go to a non-OVS interface
			if err := configureSvcRouteViaBridge(bridgeName); err != nil {
				return err
			}
			needLegacySvcRoute = false
		}

		// Determine if we need to run upgrade checks
		if initialTopoVersion != types.OvnCurrentTopologyVersion {
			if needLegacySvcRoute && config.GatewayModeShared == config.Gateway.Mode {
				klog.Info("System may be upgrading, falling back to to legacy K8S Service via mp0")
				// add back legacy route for service via mp0
				link, err := util.LinkSetUp(types.K8sMgmtIntfName)
				if err != nil {
					return fmt.Errorf("unable to get link for %s, error: %v", types.K8sMgmtIntfName, err)
				}
				var gwIP net.IP
				for _, subnet := range config.Kubernetes.ServiceCIDRs {
					if utilnet.IsIPv4CIDR(subnet) {
						gwIP = mgmtPortConfig.ipv4.gwIP
					} else {
						gwIP = mgmtPortConfig.ipv6.gwIP
					}
					err := util.LinkRoutesAdd(link, gwIP, []*net.IPNet{subnet}, 0)
					if err != nil && !os.IsExist(err) {
						return fmt.Errorf("unable to add legacy route for services via mp0, error: %v", err)
					}
				}
			}
			// need to run upgrade controller
			informerStop := make(chan struct{})
			informerFactory.Start(informerStop)
			go func() {
				if err := upgradeController.Run(n.stopChan, informerStop); err != nil {
					klog.Fatalf("Error while running upgrade controller: %v", err)
				}
				// upgrade complete now see what needs upgrading
				// migrate service route from ovn-k8s-mp0 to shared gw bridge
				if initialTopoVersion < types.OvnHostToSvcOFTopoVersion && config.GatewayModeShared == config.Gateway.Mode {
					if err := upgradeServiceRoute(bridgeName); err != nil {
						klog.Fatalf("Failed to upgrade service route for node, error: %v", err)
					}
				}
				// ensure CNI support for port binding built into OVN, as masters have been upgraded
				if initialTopoVersion < types.OvnPortBindingTopoVersion && cniServer != nil {
					cniServer.EnableOVNPortUpSupport()
				}
			}()
		}
	}

	if config.HybridOverlay.Enabled {
		// Not supported with Smart-NIC, enforced in config
		// TODO(adrianc): Revisit above comment
		nodeController, err := honode.NewNode(
			n.Kube,
			n.name,
			n.watchFactory.NodeInformer(),
			n.watchFactory.LocalPodInformer(),
			informer.NewDefaultEventHandler,
		)
		if err != nil {
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			nodeController.Run(n.stopChan)
		}()
	}

	err = util.SetOvnKubeLogLevel(n.Kube, n.name, "ovnkube-node")
	if err != nil {
		klog.Errorf("Reset of klog \"loglevel\" failed, err: %v", err)
	}

	// start management port health check
	mgmtPort.CheckManagementPortHealth(mgmtPortConfig, n.stopChan)

	if config.OvnKubeNode.Mode != types.NodeModeSmartNICHost {
		// start health check to ensure there are no stale OVS internal ports
		go wait.Until(func() {
			checkForStaleOVSInterfaces(n.name, n.watchFactory.(*factory.WatchFactory))
		}, time.Minute, n.stopChan)
	}

	var nodeIP string
	nodeIP, err = util.GetNodePrimaryIP(node)
	if err != nil {
		klog.Errorf("Failed to obtain local IP from node %q: %v", node.Name, err)
	}

	n.WatchEndpointSlices(nodeIP)

	if config.OvnKubeNode.Mode != types.NodeModeSmartNIC {
		// conditionally write cni config file
		confFile := filepath.Join(config.CNI.ConfDir, config.CNIConfFileName)
		_, err = os.Stat(confFile)
		if os.IsNotExist(err) {
			err = config.WriteCNIConfig()
			if err != nil {
				return err
			}
		}
	}

	// Remove the NoSchedule Taint from the node, now that networking setup is done. Ignore errors.
	err = n.Kube.RemoveTaintFromNode(n.name, networkUnavailableTaint)
	if err != nil {
		klog.Infof("Unable to remove taint %s on node %s: %v", networkUnavailableTaint.ToString(), n.name, err)
	}

	if config.OvnKubeNode.Mode != types.NodeModeSmartNICHost {
		// create default OVN Node Controller to watch for Pods event for smart-nic plumbing/annotation
		defaultNetConf := &cnitypes.NetConf{
			NetConf: ctypes.NetConf{
				Name: types.DefaultNetworkName,
			},
			NetCidr:    config.Default.RawClusterSubnets,
			MTU:        config.Default.MTU,
			NotDefault: false,
		}
		nadInfo, _ := util.NewNetAttachDefInfo(defaultNetConf)
		nc, _ := n.NewOvnNodeController(nadInfo)

		if config.OvnKubeNode.Mode == types.NodeModeSmartNIC {
			nc.watchSmartNicPods()
		}

		if config.OVNKubernetesFeature.EnableMultiNetwork {
			_ = n.watchNetworkAttachmentDefinitions()
		}
	}

	if config.OvnKubeNode.Mode != types.NodeModeSmartNIC {
		// start the cni server
		err = cniServer.Start(cni.HandleCNIRequest)
	}

	return err
}

// watchNetworkAttachmentDefinitions starts the watching of network attachment definition
// resource and calls back the appropriate handler logic
func (n *OvnNode) watchNetworkAttachmentDefinitions() *factory.Handler {
	return n.watchFactory.AddNetworkattachmentdefinitionHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			netattachdef := obj.(*nettypes.NetworkAttachmentDefinition)
			n.addNetworkAttachDefinition(netattachdef)
		},
		UpdateFunc: func(old, new interface{}) {},
		DeleteFunc: func(obj interface{}) {
			netattachdef := obj.(*nettypes.NetworkAttachmentDefinition)
			n.deleteNetworkAttachDefinition(netattachdef)
		},
	}, n.syncNetworkAttachDefinition)
}

func (n *OvnNode) initOvnNodeController(netattachdef *nettypes.NetworkAttachmentDefinition) (*ovnNodeController, error) {
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
		n.defaultNodeController.nadInfo.NetAttachDefs.Store(netattachdef.Namespace+"_"+netattachdef.Name, true)
		return n.defaultNodeController, nil
	}

	if nadInfo.NetName == types.DefaultNetworkName {
		return nil, fmt.Errorf("non-default Network attachment definition's name cannot be %s", types.DefaultNetworkName)
	}

	// Note that net-attach-def add/delete/update events are serialized, so we don't need locks here.
	// Check if any Controller of the same netconf.Name already exists, if so, check its conf to see if they are the same.
	v, ok := n.nonDefaultNodeControllers.Load(nadInfo.NetName)
	if ok {
		nc := v.(*ovnNodeController)
		if nc.nadInfo.NetCidr != nadInfo.NetCidr || nc.nadInfo.MTU != nadInfo.MTU {
			return nil, fmt.Errorf("network attachment definition %s/%s does not share the same CNI config of name %s",
				netattachdef.Namespace, netattachdef.Name, nadInfo.NetName)
		} else {
			nc.nadInfo.NetAttachDefs.Store(netattachdef.Namespace+"_"+netattachdef.Name, true)
		}
		return nc, nil
	}

	nadInfo.NetAttachDefs.Store(netattachdef.Namespace+"_"+netattachdef.Name, true)
	return n.NewOvnNodeController(nadInfo)
}

// syncNetworkAttachDefinition() delete OVN logical entities of the obsoleted netNames.
func (n *OvnNode) syncNetworkAttachDefinition(netattachdefs []interface{}) {
	// we need to walk through all net-attach-def and add them into Controller.nadInfo.NetAttachDefs, so that when each
	// Controller is running, watchSmartNicPods()->IsNetworkOnPod() can correctly check Pods need to be plumbed
	// for the specific Controller
	for _, netattachdefIntf := range netattachdefs {
		netattachdef, ok := netattachdefIntf.(*nettypes.NetworkAttachmentDefinition)
		if !ok {
			klog.Errorf("Spurious object in syncNetworkAttachDefinition: %v", netattachdefIntf)
			continue
		}

		_, err := n.initOvnNodeController(netattachdef)
		if err != nil {
			klog.Errorf(err.Error())
		}
	}
}

func (n *OvnNode) addNetworkAttachDefinition(netattachdef *nettypes.NetworkAttachmentDefinition) {
	nc, err := n.initOvnNodeController(netattachdef)
	if err != nil {
		klog.Errorf(err.Error())
		return
	}

	if nc == nil || nc.added {
		return
	}

	nc.added = true
	if config.OvnKubeNode.Mode != types.NodeModeSmartNICHost {
		if nc.nadInfo.TopoType == types.LocalnetAttachDefTopoType {
			// for smart-nic mode and full mode
			err = nc.updateLocalnetOvnBridgeMapping(true)
			if err != nil {
				klog.Errorf(err.Error())
			}
		}
	}

	if config.OvnKubeNode.Mode == types.NodeModeSmartNIC {
		nc.watchSmartNicPods()
	}
}

func (nc *ovnNodeController) updateLocalnetOvnBridgeMapping(toAdd bool) error {
	if nc.nadInfo.TopoType != types.LocalnetAttachDefTopoType || config.OvnKubeNode.Mode == types.NodeModeSmartNICHost {
		return nil
	}

	// ovn-bridge-mappings maps a physical network name to a local ovs bridge
	// that provides connectivity to that network. It is in the form of physnet1:br1,physnet2:br2.
	// Note that there may be multiple ovs bridge mappings, be sure not to override
	// the mappings for the other physical network
	networkName := nc.nadInfo.Prefix + types.LocalNetBridgeName
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
		if ok && bridge == nc.nadInfo.BridgeName {
			return nil
		}
		bridgeMap[networkName] = nc.nadInfo.BridgeName
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

func (n *OvnNode) deleteNetworkAttachDefinition(netattachdef *nettypes.NetworkAttachmentDefinition) {

	netconf := &cnitypes.NetConf{}

	// looking for network attachment definition that use OVN K8S CNI only
	err := json.Unmarshal([]byte(netattachdef.Spec.Config), &netconf)
	if err != nil {
		klog.Errorf("Error parsing Network Attachment Definition %s: %v", netattachdef.Name, err)
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
		n.defaultNodeController.nadInfo.NetAttachDefs.Delete(netattachdef.Namespace + "_" + netattachdef.Name)
		return
	}

	v, ok := n.nonDefaultNodeControllers.Load(nadInfo.NetName)
	if !ok {
		klog.Errorf("Failed to find network controller for network %s", nadInfo.NetName)
		return
	}

	nc := v.(*ovnNodeController)
	nc.nadInfo.NetAttachDefs.Delete(netattachdef.Namespace + "_" + netattachdef.Name)

	// check if there any net-attach-def sharing the same CNI conf name left, if yes, just return
	netAttachDefLeft := false
	nc.nadInfo.NetAttachDefs.Range(func(key, value interface{}) bool {
		netAttachDefLeft = true
		return false
	})

	if netAttachDefLeft {
		return
	}

	if config.OvnKubeNode.Mode != types.NodeModeSmartNICHost && nc.nadInfo.TopoType == types.LocalnetAttachDefTopoType {
		err = nc.updateLocalnetOvnBridgeMapping(false)
		if err != nil {
			klog.Errorf(err.Error())
		}
	}

	if config.OvnKubeNode.Mode == types.NodeModeSmartNIC {
		if nc.podHandler != nil {
			nc.node.watchFactory.RemovePodHandler(nc.podHandler)
		}
	}

	n.nonDefaultNodeControllers.Delete(nadInfo.NetName)
}

func getReadyEndpointAddresses(endpointSlice *discovery.EndpointSlice) []string {
	readyEndpointsAddress := make([]string, 0)
	for _, endpoint := range endpointSlice.Endpoints {
		//skip endpoints that are not ready
		if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
			continue
		}
		readyEndpointsAddress = append(readyEndpointsAddress, endpoint.Addresses...)
	}
	return readyEndpointsAddress
}

func (n *OvnNode) WatchEndpointSlices(nodeIP string) {
	start := time.Now()

	n.watchFactory.AddEndpointSliceHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if config.OvnKubeNode.Mode != types.NodeModeSmartNIC {
				endpointSlice := obj.(*discovery.EndpointSlice)
				klog.Infof("Processing add for endpoint slice %s on namespace %s",
					endpointSlice.Name, endpointSlice.Namespace)
				startTime := time.Now()
				addEPSliceToFirewallZone(nodeIP, endpointSlice)
				klog.Infof("Took %v to add endpoint slice %s", time.Since(startTime), endpointSlice.Name)
			}
		},
		UpdateFunc: func(prevObj, obj interface{}) {
			oldEndpointSlice := prevObj.(*discovery.EndpointSlice)
			newEndpointSlice := obj.(*discovery.EndpointSlice)
			oldReadyEpAddr := getReadyEndpointAddresses(oldEndpointSlice)
			newReadyEpAddr := getReadyEndpointAddresses(newEndpointSlice)
			if reflect.DeepEqual(oldEndpointSlice.Ports, newEndpointSlice.Ports) &&
				reflect.DeepEqual(oldReadyEpAddr, newReadyEpAddr) {
				return
			}
			klog.Infof("Processing update for endpoint slice %s on namespace %s",
				newEndpointSlice.Name, newEndpointSlice.Namespace)
			startTime := time.Now()
			updateEndpointSlice(nodeIP, oldEndpointSlice, newEndpointSlice)
			klog.Infof("Took %v to update endpoint slice %s", time.Since(startTime), newEndpointSlice.Name)
		},
		DeleteFunc: func(obj interface{}) {
			endpointSlice := obj.(*discovery.EndpointSlice)

			// Deletes the ep ports from ovn and ngn-admin zone if the endpoint IP is same as the nodeIP.
			// Also deletes any connection tracking entries for UDP and SCTP ports
			for _, port := range endpointSlice.Ports {
				for _, endpoint := range endpointSlice.Endpoints {
					for _, ip := range endpoint.Addresses {
						klog.V(5).Infof("Endpoint address is %s and NodeIP is %s for port %d/%s",
							ip, nodeIP, *port.Port, *port.Protocol)
						if config.OvnKubeNode.Mode != types.NodeModeSmartNIC && nodeIP == ip {
							err := removePortFromFirewallZone(ovnFirewallZone,
								*port.Port, *port.Protocol)
							if err != nil {
								klog.Errorf("Error in removing port %d to "+
									"ovn firewall zone: (%v)", *port.Port, err)
							}
							err = removePortFromFirewallZone(ngnAdminFirewallZone,
								*port.Port, *port.Protocol)
							if err != nil {
								klog.Errorf("Error in removing port %d to "+
									"ngn-admin firewall zone: (%v)", *port.Port, err)
							}
						}
						if config.OvnKubeNode.Mode != types.NodeModeSmartNICHost &&
							(*port.Protocol == kapi.ProtocolUDP || *port.Protocol == kapi.ProtocolSCTP) {
							err := deleteConntrack(ip, *port.Port, *port.Protocol)
							if err != nil {
								klog.Errorf("Failed to delete conntrack entry for %s: %v", ip, err)
							}
						}
					}
				}
			}
		},
	}, syncEndpointSlices)
	klog.Infof("Bootstrapping existing EndpointSlices took %v", time.Since(start))
}

func addEPSliceToFirewallZone(nodeIP string, endpointSlice *discovery.EndpointSlice) {
	for _, port := range endpointSlice.Ports {
		for _, endpoint := range endpointSlice.Endpoints {
			for _, ip := range endpoint.Addresses {
				klog.V(5).Infof("Endpoint address is %s and NodeIP is %s for port  %d/%s",
					ip, nodeIP, *port.Port, *port.Protocol)
				if nodeIP != ip {
					continue
				}
				err := addPortToFirewallZone(ovnFirewallZone, *port.Port, *port.Protocol)
				if err != nil {
					klog.Errorf("Error in adding port %d to ovn firewall zone: (%v)", *port.Port, err)
				}
				err = addPortToFirewallZone(ngnAdminFirewallZone, *port.Port, *port.Protocol)
				if err != nil {
					klog.Errorf("Error in adding port %d to ngn-admin firewall zone: (%v)", *port.Port, err)
				}
			}
		}
	}
}

func isEPSliceContainsEndpoint(epSlice *discovery.EndpointSlice,
	epIP string, epPort int32, protocol kapi.Protocol) bool {
	for _, port := range epSlice.Ports {
		for _, endpoint := range epSlice.Endpoints {
			for _, ip := range endpoint.Addresses {
				if ip == epIP && *port.Port == epPort && *port.Protocol == protocol {
					return true
				}
			}
		}
	}
	return false
}

// validateGatewayMTU checks if the MTU of the given network interface is big
// enough to carry the `config.Default.MTU` and the Geneve header. If the MTU
// is not big enough, it will taint the node with the value of
// `types.OvnK8sSmallMTUTaintKey`
func (n *OvnNode) validateGatewayMTU(gatewayInterfaceName string) error {
	tooSmallMTUTaint := &kapi.Taint{Key: types.OvnK8sSmallMTUTaintKey, Effect: kapi.TaintEffectNoSchedule}

	mtu, err := util.GetNetworkInterfaceMTU(gatewayInterfaceName)
	if err != nil {
		return fmt.Errorf("could not get MTU from gateway network interface %s: %w", gatewayInterfaceName, err)
	}

	// calc required MTU
	var requiredMTU int
	if config.IPv4Mode && !config.IPv6Mode {
		// we run in single-stack IPv4 only
		requiredMTU = config.Default.MTU + types.GeneveHeaderLengthIPv4
	} else {
		// we run in single-stack IPv6 or dual-stack mode
		requiredMTU = config.Default.MTU + types.GeneveHeaderLengthIPv6
	}

	// check if node needs to be tainted
	if mtu < requiredMTU {
		klog.V(2).Infof("MTU (%d) of gateway network interface %s is not big enough to deal with Geneve header overhead (sum %d). Tainting node with %v...", mtu, gatewayInterfaceName, requiredMTU, tooSmallMTUTaint)

		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			return n.Kube.SetTaintOnNode(n.name, tooSmallMTUTaint)
		})
	} else {
		klog.V(2).Infof("MTU (%d) of gateway network interface %s is big enough to deal with Geneve header overhead (sum %d). Making sure node is not tainted with %v...", mtu, gatewayInterfaceName, requiredMTU, tooSmallMTUTaint)

		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			return n.Kube.RemoveTaintFromNode(n.name, tooSmallMTUTaint)
		})
	}
}

func updateEndpointSlice(nodeIP string, oldEndpointSlice, newEndpointSlice *discovery.EndpointSlice) {
	if config.OvnKubeNode.Mode != types.NodeModeSmartNIC {
		// add any new ports to the firewalld zone that are not in oldEndpointSlice
		for _, port := range newEndpointSlice.Ports {
			for _, endpoint := range newEndpointSlice.Endpoints {
				for _, ip := range endpoint.Addresses {
					klog.V(5).Infof("Endpoint address is %s and nodeIP is %s for port %d/%s",
						ip, nodeIP, *port.Port, *port.Protocol)
					if nodeIP != ip {
						continue
					}
					if isEPSliceContainsEndpoint(oldEndpointSlice, ip, *port.Port, *port.Protocol) {
						continue
					}
					klog.V(5).Infof("Adding the endpoint that is not present in old slice %s/%d/%s",
						ip, *port.Port, *port.Protocol)
					err := addPortToFirewallZone(ovnFirewallZone, *port.Port, *port.Protocol)
					if err != nil {
						klog.Errorf("Error in adding port %d to ovn firewall zone: (%v)", *port.Port, err)
					}
					err = addPortToFirewallZone(ngnAdminFirewallZone, *port.Port, *port.Protocol)
					if err != nil {
						klog.Errorf("Error in adding port %d to ngn-admin firewall zone: (%v)", *port.Port, err)
					}
				}
			}
		}
	}

	// now remove any old ports that are not present in the new endpointSlice resource
	for _, port := range oldEndpointSlice.Ports {
		for _, endpoint := range oldEndpointSlice.Endpoints {
			for _, ip := range endpoint.Addresses {
				// if the port is neither UDP nor SCTP and endpointIP doesn't match the node's IP, then
				// there is nothing to do
				switch config.OvnKubeNode.Mode {
				case types.NodeModeSmartNIC:
					if *port.Protocol != kapi.ProtocolUDP && *port.Protocol != kapi.ProtocolSCTP {
						continue
					}
				case types.NodeModeSmartNICHost:
					if nodeIP != ip {
						continue
					}
				case types.NodeModeFull:
					if nodeIP != ip && *port.Protocol != kapi.ProtocolUDP && *port.Protocol != kapi.ProtocolSCTP {
						continue
					}
				}
				if isEPSliceContainsEndpoint(newEndpointSlice, ip, *port.Port, *port.Protocol) {
					continue
				}
				klog.Infof("Removing the endpoint %s/%d/%s not present in new slice but present in old slice",
					ip, *port.Port, *port.Protocol)
				if config.OvnKubeNode.Mode != types.NodeModeSmartNIC && nodeIP == ip {
					err := removePortFromFirewallZone(ovnFirewallZone, *port.Port, *port.Protocol)
					if err != nil {
						klog.Errorf("Error in removing port %d to ovn firewall zone: (%v)", *port.Port, err)
					}
					err = removePortFromFirewallZone(ngnAdminFirewallZone, *port.Port, *port.Protocol)
					if err != nil {
						klog.Errorf("Error in removing port %d to ngn-admin firewall zone: (%v)", *port.Port, err)
					}
				}
				if config.OvnKubeNode.Mode != types.NodeModeSmartNICHost &&
					(*port.Protocol == kapi.ProtocolUDP || *port.Protocol == kapi.ProtocolSCTP) {
					err := deleteConntrack(ip, *port.Port, *port.Protocol)
					if err != nil {
						klog.Errorf("Failed to delete conntrack entry for %s: %v", ip, err)
					}
				}
			}
		}
	}
}

func syncEndpointSlices(obj []interface{}) {
	if config.OvnKubeNode.Mode != types.NodeModeSmartNIC {
		if err := addInterfaceToFirewallZone(types.K8sMgmtIntfName, ovnFirewallZone); err != nil {
			klog.Errorf("Failed to add interface %s to ovn firewall zone: (%v)",
				types.K8sMgmtIntfName, err)
		}
		if err := addInterfaceToFirewallZone(localnetGatewayNextHopPort, ovnFirewallZone); err != nil {
			klog.Errorf("Failed to add interface %s to ovn firewall zone: (%v)",
				localnetGatewayNextHopPort, err)
		}
	}
}

func configureSvcRouteViaBridge(bridge string) error {
	gwIPs, _, err := getGatewayNextHops()
	if err != nil {
		return fmt.Errorf("unable to get the gateway next hops, error: %v", err)
	}
	return configureSvcRouteViaInterface(bridge, gwIPs)
}

func upgradeServiceRoute(bridgeName string) error {
	klog.Info("Updating K8S Service route")
	// Flush old routes
	link, err := util.LinkSetUp(types.K8sMgmtIntfName)
	if err != nil {
		return fmt.Errorf("unable to get link: %s, error: %v", types.K8sMgmtIntfName, err)
	}
	if err := util.LinkRoutesDel(link, config.Kubernetes.ServiceCIDRs); err != nil {
		return fmt.Errorf("unable to delete routes on upgrade, error: %v", err)
	}
	// add route via OVS bridge
	if err := configureSvcRouteViaBridge(bridgeName); err != nil {
		return fmt.Errorf("unable to add svc route via OVS bridge interface, error: %v", err)
	}
	klog.Info("Successfully updated Kubernetes service route towards OVS")
	// Clean up gw0 and local ovs bridge as best effort
	if err := deleteLocalNodeAccessBridge(); err != nil {
		klog.Warningf("Error while removing Local Node Access Bridge, error: %v", err)
	}
	return nil
}
