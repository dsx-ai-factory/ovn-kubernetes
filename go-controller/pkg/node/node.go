package node

import (
	"context"
	"errors"
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
	discovery "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
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
	"github.com/vishvananda/netlink"
)

const (
	ovnSkipFirewalldAnnotationName = "k8s.ovn.org/skip-firewalld"
	ngnHostTypeAnnotationName      = "ngn2.nvidia.com/hosttype"
)

// Special reserved values for k8s.ovn.org/miss-rl-config.
// Doesn't seem there is any valid reason to configure these values explicitly.
const (
	ClampdownDoSRate  uint = 1
	ClampdownDoSBurst uint = 1
)

// OvnNode is the object holder for utilities meant for node management
type OvnNode struct {
	name         string
	hostType     string
	client       clientset.Interface
	Kube         kube.Interface
	watchFactory factory.NodeWatchFactory
	wg           *sync.WaitGroup
	stopChan     chan struct{}
	recorder     record.EventRecorder
	gateway      Gateway
	ovnUpEnabled bool

	defaultNodeController     *ovnNodeController
	nonDefaultNodeControllers sync.Map
	svcAnnotationMap          sync.Map
}

type ovnNodeController struct {
	node       *OvnNode
	nadInfo    *util.NetAttachDefInfo
	podHandler *factory.Handler
	// Some controllers, e.g those needing XDP and on non-primary DPU, need to manage shared gateway
	// other than the NS gateway on the primary DPU.
	gateway Gateway
	added   bool
	// servedPods tracks the pods that got a VF
	servedPods sync.Map
	// podNadCache stores all the net-attach-defs that the given Pod is attached for this controller,
	// we assume that Pod's Network Attachment Selection Annotation will not change over time.
	// key is pod.UUID, value is networkMap of map[string]*util.PodNadInfo type
	podNadCache sync.Map
	// Count of NADs that needs to be checked for DoS.
	// We could do this per nad, but it might be more efficient to do it on the controller.
	sync.RWMutex
	dosCheckEnabled bool
	wGroup          *sync.WaitGroup
	stopChan        chan struct{}
}

// NewNode creates a new controller for node management
func NewNode(kubeClient clientset.Interface, wf factory.NodeWatchFactory, name string, stopChan chan struct{}, eventRecorder record.EventRecorder, wg *sync.WaitGroup) *OvnNode {
	return &OvnNode{
		name:             name,
		client:           kubeClient,
		Kube:             &kube.Kube{KClient: kubeClient},
		watchFactory:     wf,
		stopChan:         stopChan,
		recorder:         eventRecorder,
		svcAnnotationMap: sync.Map{},
		wg:               wg,
	}
}

func clearOVSFlowTargets() error {
	_, _, err := util.RunOVSVsctl(
		"--",
		"clear", "bridge", "br-int", "netflow",
		"--",
		"clear", "bridge", "br-int", "sflow",
		"--",
		"clear", "bridge", "br-int", "ipfix",
	)
	if err != nil {
		return err
	}
	return nil
}

// collectorsString joins all HostPort entry into a string that is acceptable as
// target by the ovs-vsctl command. If an entry has an empty host, it uses the Node IP
func collectorsString(node *kapi.Node, targets []config.HostPort) (string, error) {
	if len(targets) == 0 {
		return "", errors.New("collector targets can't be empty")
	}
	var joined strings.Builder
	for n, v := range targets {
		if n == 0 {
			joined.WriteByte('"')
		} else {
			joined.WriteString(`","`)
		}
		var host string
		if v.Host != nil && len(*v.Host) != 0 {
			host = v.Host.String()
		} else {
			var err error
			if host, err = util.GetNodePrimaryIP(node); err != nil {
				return "", fmt.Errorf("composing flow collectors' IPs: %w", err)
			}
		}
		joined.WriteString(util.JoinHostPortInt32(host, v.Port))
	}
	joined.WriteByte('"')
	return joined.String(), nil
}

func setOVSFlowTargets(node *kapi.Node) error {
	if len(config.Monitoring.NetFlowTargets) != 0 {
		collectors, err := collectorsString(node, config.Monitoring.NetFlowTargets)
		if err != nil {
			return fmt.Errorf("error joining NetFlow targets: %w", err)
		}

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
	if len(config.Monitoring.SFlowTargets) != 0 {
		collectors, err := collectorsString(node, config.Monitoring.SFlowTargets)
		if err != nil {
			return fmt.Errorf("error joining SFlow targets: %w", err)
		}

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
	if len(config.Monitoring.IPFIXTargets) != 0 {
		collectors, err := collectorsString(node, config.Monitoring.IPFIXTargets)
		if err != nil {
			return fmt.Errorf("error joining IPFIX targets: %w", err)
		}

		args := []string{
			"--",
			"--id=@ipfix",
			"create",
			"ipfix",
			fmt.Sprintf("targets=[%s]", collectors),
			fmt.Sprintf("cache_active_timeout=%d", config.IPFIX.CacheActiveTimeout),
		}
		if config.IPFIX.CacheMaxFlows != 0 {
			args = append(args, fmt.Sprintf("cache_max_flows=%d", config.IPFIX.CacheMaxFlows))
		}
		if config.IPFIX.Sampling != 0 {
			args = append(args, fmt.Sprintf("sampling=%d", config.IPFIX.Sampling))
		}
		args = append(args, "--", "set", "bridge", "br-int", "ipfix=@ipfix")
		_, stderr, err := util.RunOVSVsctl(args...)
		if err != nil {
			return fmt.Errorf("error setting IPFIX: %v\n  %q", err, stderr)
		}
	}
	return nil
}

func setupOVNNode(node *kapi.Node) error {
	var err error

	encapIP := config.Default.EncapIP
	if encapIP == "" {
		encapIP, err = util.GetNodePrimaryIP(node)
		if err != nil {
			return fmt.Errorf("failed to obtain local IP from node %q: %v", node.Name, err)
		}
		config.Default.EncapIP = encapIP
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
		fmt.Sprintf("external_ids:ovn-encap-tos=%s",
			config.Default.EncapToSValue),
		fmt.Sprintf("external_ids:ovn-monitor-all=%t", config.Default.MonitorAll),
		fmt.Sprintf("external_ids:ovn-ofctrl-wait-before-clear=%d", config.Default.OfctrlWaitBeforeClear),
		fmt.Sprintf("external_ids:ovn-enable-lflow-cache=%t", config.Default.LFlowCacheEnable),
	}

	if config.Default.LFlowCacheLimit > 0 {
		setExternalIdsCmd = append(setExternalIdsCmd,
			fmt.Sprintf("external_ids:ovn-limit-lflow-cache=%d", config.Default.LFlowCacheLimit),
		)
	}

	if config.Default.LFlowCacheLimitKb > 0 {
		setExternalIdsCmd = append(setExternalIdsCmd,
			fmt.Sprintf("external_ids:ovn-memlimit-lflow-cache-kb=%d", config.Default.LFlowCacheLimitKb),
		)
	}

	// In the case of DPU, the hostname should be that of the DPU and not the K8s Node.
	// So, skip setting the incorrect hostname.
	if config.OvnKubeNode.Mode != types.NodeModeDPU {
		setExternalIdsCmd = append(setExternalIdsCmd, fmt.Sprintf("external_ids:hostname=\"%s\"", node.Name))
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

	// clear stale ovs flow targets if needed
	err = clearOVSFlowTargets()
	if err != nil {
		return fmt.Errorf("error clearing stale ovs flow targets: %q", err)
	}
	// set new ovs flow targets if needed
	err = setOVSFlowTargets(node)
	if err != nil {
		return fmt.Errorf("error setting ovs flow targets: %q", err)
	}

	// set max-revalidator, min-revalidate-pps and max-idle for dpu node if the values are set
	if config.OvnKubeNode.Mode == types.NodeModeDPU {
		var err error
		if config.OvnKubeNode.MaxRevalidator == 0 {
			// clear to use default
			err = updateOVSOtherConfig("max-revalidator", nil)
		} else {
			err = updateOVSOtherConfig("max-revalidator", config.OvnKubeNode.MaxRevalidator)
		}
		if err != nil {
			return err
		}
		err = updateOVSOtherConfig("min-revalidate-pps", config.OvnKubeNode.MinRevalidatePPS)
		if err != nil {
			return err
		}
		if config.OvnKubeNode.MaxIdle == 0 {
			// clear to use default
			err = updateOVSOtherConfig("max-idle", nil)
		} else {
			err = updateOVSOtherConfig("max-idle", config.OvnKubeNode.MaxIdle)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func isOVNControllerReady() (bool, error) {
	// check node's connection status
	runDir := util.GetOvnRunDir()
	pid, err := ioutil.ReadFile(runDir + "ovn-controller.pid")
	if err != nil {
		return false, fmt.Errorf("unknown pid for ovn-controller process: %v", err)
	}
	ctlFile := runDir + fmt.Sprintf("ovn-controller.%s.ctl", strings.TrimSuffix(string(pid), "\n"))
	ret, _, err := util.RunOVSAppctl("-t", ctlFile, "connection-status")
	if err != nil {
		return false, fmt.Errorf("could not get connection status: %w", err)
	}
	klog.Infof("Node connection status = %s", ret)
	if ret != "connected" {
		return false, nil
	}

	// check whether br-int exists on node
	_, _, err = util.RunOVSVsctl("--", "br-exists", "br-int")
	if err != nil {
		return false, nil
	}

	// check by dumping br-int flow entries
	stdout, _, err := util.RunOVSOfctl("dump-aggregate", "br-int")
	if err != nil {
		klog.V(5).Infof("Error dumping aggregate flows: %v", err)
		return false, nil
	}
	hasFlowCountZero := strings.Contains(stdout, "flow_count=0")
	if hasFlowCountZero {
		klog.V(5).Info("Got a flow count of 0 when dumping flows for node")
		return false, nil
	}

	return true, nil
}

func (n *OvnNode) NewOvnNodeController(nadInfo *util.NetAttachDefInfo) (*ovnNodeController, error) {
	sc := make(chan struct{})
	wg := &sync.WaitGroup{}
	nc := &ovnNodeController{
		node:     n,
		nadInfo:  nadInfo,
		added:    false,
		stopChan: sc,
		wGroup:   wg,
	}
	if !nadInfo.IsSecondary {
		n.defaultNodeController = nc
	} else {
		_, loaded := n.nonDefaultNodeControllers.LoadOrStore(nadInfo.NetName, nc)
		if loaded {
			return nil, fmt.Errorf("non default Network attachment definition %s already exists", nadInfo.NetName)
		}
	}
	return nc, nil
}

// Starting with v21.03.0 OVN sets OVS.Interface.external-id:ovn-installed
// and OVNSB.Port_Binding.up when all OVS flows associated to a
// logical port have been successfully programmed.
// OVS.Interface.external-id:ovn-installed can only be used correctly
// in a combination with OVS.Interface.external-id:iface-id-ver
func getOVNIfUpCheckMode() (bool, error) {
	if config.OvnKubeNode.DisableOVNIfaceIdVer {
		klog.Infof("'iface-id-ver' is manually disabled, ovn-installed feature can't be used")
		return false, nil
	}
	klog.Infof("Detected support for port binding with external IDs")
	return true, nil
}

// Start learns the subnets assigned to it by the master controller
// and calls the SetupNode script which establishes the logical switch
func (n *OvnNode) Start(ctx context.Context, wg *sync.WaitGroup) error {
	var err error
	var node *kapi.Node
	var subnets []*net.IPNet
	var mgmtPort ManagementPort
	var mgmtPortConfig *managementPortConfig
	var cniServer *cni.Server

	klog.Infof("OVN Kube Node initialization, Mode: %s", config.OvnKubeNode.Mode)

	// Setting debug log level during node bring up to expose bring up process.
	// Log level is returned to configured value when bring up is complete.
	var level klog.Level
	if err := level.Set("5"); err != nil {
		klog.Errorf("Setting klog \"loglevel\" to 5 failed, err: %v", err)
	}

	// Start and sync the watch factory to begin listening for events
	if err := n.watchFactory.Start(); err != nil {
		return err
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
	if config.OvnKubeNode.Mode == types.NodeModeDPU {
		if hostType, exists := node.Labels[ngnHostTypeAnnotationName]; !exists {
			klog.Errorf("%s: annotation \"%s\" is required for dpu node", n.name, ngnHostTypeAnnotationName)
			return fmt.Errorf("%s: annotation \"%s\" is required for dpu node", n.name, ngnHostTypeAnnotationName)
		} else {
			n.hostType = hostType
		}
	}
	if config.OvnKubeNode.Mode != types.NodeModeDPUHost {
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

	if config.OvnKubeNode.Mode != types.NodeModeDPUHost {
		n.ovnUpEnabled, err = getOVNIfUpCheckMode()
		if err != nil {
			return err
		}
	}

	// Create CNI Server
	if config.OvnKubeNode.Mode != types.NodeModeDPU {
		kclient, ok := n.Kube.(*kube.Kube)
		if !ok {
			return fmt.Errorf("cannot get kubeclient for starting CNI server")
		}
		cniServer, err = cni.NewCNIServer("", n.ovnUpEnabled, n.watchFactory, kclient.KClient)
		if err != nil {
			return err
		}
	}

	// Setup Management port and gateway
	mgmtPort = NewManagementPort(n.name, subnets)
	nodeAnnotator := kube.NewNodeAnnotator(n.Kube, node.Name)
	waiter := newStartupWaiter()

	mgmtPortConfig, err = mgmtPort.Create(nodeAnnotator, waiter)
	if err != nil {
		return err
	}

	// Initialize gateway
	if config.OvnKubeNode.Mode == types.NodeModeDPUHost {
		err = n.initGatewayDPUHost(nodeAddr)
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
	n.gateway.Start(n.stopChan, wg)
	klog.Infof("Gateway and management port readiness took %v", time.Since(start))

	// Note(adrianc): DPU deployments are expected to support the new shared gateway changes, upgrade flow
	// is not needed. Future upgrade flows will need to take DPUs into account.
	if config.OvnKubeNode.Mode == types.NodeModeFull {
		// Upgrade for Node. If we upgrade workers before masters, then we need to keep service routing via
		// mgmt port until masters have been updated and modified OVN config. Run a goroutine to handle this case
		upgradeController := upgrade.NewController(n.client, n.watchFactory)
		initialTopoVersion, err := upgradeController.GetTopologyVersion(ctx)
		if err != nil {
			return fmt.Errorf("failed to get initial topology version: %w", err)
		}
		klog.Infof("Current control-plane topology version is %d", initialTopoVersion)
		bridgeName := n.gateway.GetGatewayBridgeIface()

		needLegacySvcRoute := true
		if (initialTopoVersion >= types.OvnHostToSvcOFTopoVersion && config.GatewayModeShared == config.Gateway.Mode) ||
			(initialTopoVersion >= types.OvnRoutingViaHostTopoVersion) {
			// Configure route for svc towards shared gw bridge
			// Have to have the route to bridge for multi-NIC mode, where the default gateway may go to a non-OVS interface
			if err := configureSvcRouteViaBridge(bridgeName); err != nil {
				return err
			}
			needLegacySvcRoute = false
		}

		// Determine if we need to run upgrade checks
		if initialTopoVersion != types.OvnCurrentTopologyVersion {
			if needLegacySvcRoute {
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
					err := util.LinkRoutesAddOrUpdateMTU(link, gwIP, []*net.IPNet{subnet}, config.Default.RoutableMTU)
					if err != nil {
						return fmt.Errorf("unable to add legacy route for services via mp0, error: %v", err)
					}
				}
			}
			// need to run upgrade controller
			go func() {
				if err := upgradeController.WaitForTopologyVersion(ctx, types.OvnCurrentTopologyVersion, 30*time.Minute); err != nil {
					klog.Fatalf("Error while waiting for Topology Version to be updated: %v", err)
				}
				// upgrade complete now see what needs upgrading
				// migrate service route from ovn-k8s-mp0 to shared gw bridge
				if (initialTopoVersion < types.OvnHostToSvcOFTopoVersion && config.GatewayModeShared == config.Gateway.Mode) ||
					(initialTopoVersion < types.OvnRoutingViaHostTopoVersion) {
					if err := upgradeServiceRoute(bridgeName); err != nil {
						klog.Fatalf("Failed to upgrade service route for node, error: %v", err)
					}
				}
				// ensure CNI support for port binding built into OVN, as masters have been upgraded
				if initialTopoVersion < types.OvnPortBindingTopoVersion && cniServer != nil && !n.ovnUpEnabled {
					n.ovnUpEnabled, err = getOVNIfUpCheckMode()
					if err != nil {
						klog.Errorf("%v", err)
					}
					if n.ovnUpEnabled {
						cniServer.EnableOVNPortUpSupport()
					}
				}
			}()
		}
	}

	if config.HybridOverlay.Enabled {
		// Not supported with DPUs, enforced in config
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

	if config.OvnKubeNode.Mode != types.NodeModeDPUHost {
		// start health check to ensure there are no stale OVS internal ports
		go wait.Until(func() {
			checkForStaleOVSInterfaces(n.name, n.watchFactory.(*factory.WatchFactory))
		}, time.Minute, n.stopChan)
	}

	if config.OvnKubeNode.Mode != types.NodeModeDPU {
		var nodeIP string
		nodeIP, err = util.GetNodePrimaryIP(node)
		if err != nil {
			return fmt.Errorf("failed to obtain local IP from node %q: %v", node.Name, err)
		}

		err = n.WatchEndpointSlices(nodeIP)
		if err != nil {
			return fmt.Errorf("failed to watch endpointSlices: %w", err)
		}
	}

	if config.OvnKubeNode.Mode == types.NodeModeDPU && config.OvnKubeNode.IsPrimaryDPU {
		err = n.WatchEndpointSlicesOnDPU()
		if err != nil {
			return fmt.Errorf("failed to watch endpointSlices: %w", err)
		}
	}

	if config.OvnKubeNode.Mode != types.NodeModeDPUHost {
		// create the default OVN Node Controller to watch for Pods event for dpu plumbing/annotation
		defaultNetConf := &cnitypes.NetConf{
			NetConf: ctypes.NetConf{
				Name: types.DefaultNetworkName,
			},
			NetCidr:     config.Default.RawClusterSubnets,
			MTU:         config.Default.MTU,
			IsSecondary: false,
		}
		nadInfo, _ := util.NewNetAttachDefInfo(defaultNetConf)
		// Default node controller should not fail, e.g for XDP
		nc, _ := n.NewOvnNodeController(nadInfo)

		// Mark default controller to be "added" so that the other default network net-attach-def
		// won't start pod watcher
		nc.added = true

		if config.OVNKubernetesFeature.EnableMultiNetwork {
			err = n.watchNetworkAttachmentDefinitions()
			if err != nil {
				return fmt.Errorf("failed to watch network attachment definitions: %w", err)
			}
		}

		// Only start default network Pod watcher after other default net-attach-defs are added.
		// This is needed to correctly determine if a pod is scheduled on the default network
		// if the pod's default network is defined by v1.multus-cni.io/default-network annotation.
		if config.OvnKubeNode.Mode == types.NodeModeDPU {
			// Get all the PFMACs on the DPU Host
			pfMACs, err := util.GetAllDPUHostPFMACAddress()
			if err != nil {
				return fmt.Errorf("failed to get the MAC address for all the PFs on the host: %v", err)
			}
			if err = nc.watchPodsDPU(n.ovnUpEnabled, pfMACs); err != nil {
				return err
			}
		}

		// default network only
		if config.OvnKubeNode.Mode != types.NodeModeDPUHost {
			util.SetARPTimeout()
			err := nc.WatchNamespaces()
			if err != nil {
				return fmt.Errorf("failed to watch namespaces: %w", err)
			}
			// every minute cleanup stale conntrack entries if any
			go wait.Until(func() {
				nc.checkAndDeleteStaleConntrackEntries()
			}, time.Minute*1, n.stopChan)
		}
	}

	if config.OvnKubeNode.Mode != types.NodeModeDPU {
		// start the cni server
		if err := cniServer.Start(cni.HandleCNIRequest); err != nil {
			return err
		}

		// conditionally write cni config file
		confFile := filepath.Join(config.CNI.ConfDir, config.CNIConfFileName)
		_, err = os.Stat(confFile)
		if os.IsNotExist(err) {
			err = config.WriteCNIConfig()
			if err != nil {
				return err
			}
		} else if err != nil {
			return fmt.Errorf("failed while checking whether to write CNI config file or not for node %q: %v",
				node.Name, err)
		}
	}

	klog.Infof("OVN Kube Node initialized and ready.")
	return nil
}

// watchNetworkAttachmentDefinitions starts the watching of network attachment definition
// resource and calls back the appropriate handler logic
func (n *OvnNode) watchNetworkAttachmentDefinitions() error {
	start := time.Now()
	_, err := n.watchFactory.AddNetworkattachmentdefinitionHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			netattachdef := obj.(*nettypes.NetworkAttachmentDefinition)
			n.addNetworkAttachDefinition(netattachdef)
		},
		UpdateFunc: func(old, new interface{}) {
			if config.OvnKubeNode.Mode != types.NodeModeDPU {
				// only update vf rate limit for dpu mode
				return
			}
			oldNAD := old.(*nettypes.NetworkAttachmentDefinition)
			newNAD := new.(*nettypes.NetworkAttachmentDefinition)
			n.updateRateLimitingConfig(oldNAD, newNAD)
		},
		DeleteFunc: func(obj interface{}) {
			netattachdef := obj.(*nettypes.NetworkAttachmentDefinition)
			n.deleteNetworkAttachDefinition(netattachdef)
		},
	}, n.syncNetworkAttachDefinition)
	klog.Infof("Bootstrapping existing Network Attachment Definitions took %v", time.Since(start))
	return err
}

// We'll start a checker when any nad on this controller has PPS limit > 0; but we don't
// disable it when the limits get reset for all nads.. assuming it is possible for that NAD
// to be  configured with the limits again. Primarily to keep the logic simple.
func (nc *ovnNodeController) enableDoSChecker() {
	// Only supported on DPU
	klog.V(5).Infof("Enabling DoS checker for %s", nc.nadInfo.NetName)
	if config.OvnKubeNode.Mode != types.NodeModeDPU {
		return
	}
	// Check if we need to start the doscheck thread if this NAD has a limit configured
	nc.Lock()
	defer nc.Unlock()
	if nc.dosCheckEnabled {
		klog.V(5).Infof("DoS checker already enabled for %s", nc.nadInfo.NetName)
		return
	}
	klog.Infof("Starting DoS checker for %s", nc.nadInfo.NetName)
	nc.dosCheckEnabled = true
	nc.wGroup.Add(1)
	go func() {
		defer nc.wGroup.Done()
		timer := time.NewTicker(time.Duration(config.Default.DoSCheckInterval) * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				nc.checkforDoSSuspects()
			case <-nc.stopChan:
				return
			}
		}
	}()
}

func (n *OvnNode) initOvnNodeController(netattachdef *nettypes.NetworkAttachmentDefinition) (*ovnNodeController, error) {
	nadInfo, nadConf, err := util.ParseNADInfo(netattachdef)
	if err != nil {
		return nil, err
	}

	klog.Infof("NewNetAttachDefInfo: PPS info for nad %s/%s is %d/%d, applicable to host type(s) %s", netattachdef.Namespace, netattachdef.Name,
		nadConf.MaxNewConnPPS, nadConf.MaxNewConnBurst, nadConf.HostTypes)

	if !nadInfo.IsSecondary {
		n.defaultNodeController.nadInfo.NetAttachDefs.Store(util.GetNadKeyName(netattachdef.Namespace, netattachdef.Name), nadConf)
		// Don't look for disabledoscheck etc. we'll assume this nad will need the checker at some point, else
		// it becomes very complex.
		if nadConf.MaxNewConnPPS > 0 {
			n.defaultNodeController.enableDoSChecker()
		}
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
		if nc.nadInfo.NetCidr != nadInfo.NetCidr || nc.nadInfo.MTU != nadInfo.MTU || nc.nadInfo.XDPService != nadInfo.XDPService {
			return nil, fmt.Errorf("network attachment definition %s/%s does not share the same CNI config of name %s",
				netattachdef.Namespace, netattachdef.Name, nadInfo.NetName)
		} else {
			nc.nadInfo.NetAttachDefs.Store(util.GetNadKeyName(netattachdef.Namespace, netattachdef.Name), nadConf)
			if nadConf.MaxNewConnPPS > 0 {
				nc.enableDoSChecker()
			}
		}
		return nc, nil
	}

	nadInfo.NetAttachDefs.Store(util.GetNadKeyName(netattachdef.Namespace, netattachdef.Name), nadConf)
	nc, err := n.NewOvnNodeController(nadInfo)
	if err == nil && nadConf.MaxNewConnPPS > 0 {
		nc.enableDoSChecker()
	}
	return nc, err
}

// syncNetworkAttachDefinition() delete OVN logical entities of the obsoleted netNames.
func (n *OvnNode) syncNetworkAttachDefinition(netattachdefs []interface{}) error {
	// we need to walk through all net-attach-def and add them into Controller.nadInfo.NetAttachDefs, so that when each
	// Controller is running, watchPodsDPU()->IsNetworkOnPod() can correctly check Pods need to be plumbed
	// for the specific Controller
	for _, netattachdefIntf := range netattachdefs {
		netattachdef, ok := netattachdefIntf.(*nettypes.NetworkAttachmentDefinition)
		if !ok {
			klog.Errorf("Spurious object in syncNetworkAttachDefinition: %v", netattachdefIntf)
			continue
		}
		_, err := n.initOvnNodeController(netattachdef)
		if err != nil {
			// ignore error if the net-attach-def is not managed by OVN
			if err != util.ErrorAttachDefNotOvnManaged {
				klog.Errorf(err.Error())
			}
		}
	}
	return nil
}

func (n *OvnNode) addNetworkAttachDefinition(netattachdef *nettypes.NetworkAttachmentDefinition) {
	nc, err := n.initOvnNodeController(netattachdef)
	if err != nil {
		if err != util.ErrorAttachDefNotOvnManaged {
			klog.Errorf(err.Error())
		}
		return
	}

	if nc.added {
		return
	}

	nc.added = true
	if config.OvnKubeNode.Mode != types.NodeModeDPUHost {
		if nc.nadInfo.TopoType == types.LocalnetAttachDefTopoType {
			// for dpu mode and full mode
			err = nc.updateLocalnetOvnBridgeMapping(true)
			if err != nil {
				klog.Errorf(err.Error())
			}
		}
	}

	if config.OvnKubeNode.Mode == types.NodeModeDPU {
		// Get all the PFMACs on the DPU Host
		pfMACs, err := util.GetAllDPUHostPFMACAddress()
		if err != nil {
			// TODO(gmoodalbail): should this be fatal error
			klog.Errorf("Failed to get the MAC address for all the PFs on the host: %v", err)
			return
		}
		// If this NAD is backed by a XDP service, initialize the shared XDP gateway on the controller.
		// Currently only supported for localnet NAD.  nc.gateway should be nil here.
		if nc.nadInfo.XDPService {
			klog.Infof("NAD %s configured for XDP", nc.nadInfo.NetName)
			if nc.nadInfo.BridgeName == "" {
				klog.Errorf("Failed getting XDP bridge for NAD %s", nc.nadInfo.NetName)
				return
			}
			if nc.gateway != nil {
				klog.Infof("Gateway already configured for %s", nc.nadInfo.NetName)
			} else {
				gw, err := n.initGatewayDPUXDP(nc.nadInfo)
				if err != nil {
					klog.Errorf("Failed initializing XDP for NAD %s: %v", nc.nadInfo.NetName, err)
					return
				}
				nc.gateway = gw
				klog.Infof("Initialized XDP for NAD %s", nc.nadInfo.NetName)
			}
		}
		if err = nc.watchPodsDPU(n.ovnUpEnabled, pfMACs); err != nil {
			klog.Errorf("Failed watch dpu annotation update of Pods: %v", nc.nadInfo.NetName, err)
		}
	}
}

func (nc *ovnNodeController) updateLocalnetOvnBridgeMapping(toAdd bool) error {
	if nc.nadInfo.TopoType != types.LocalnetAttachDefTopoType || config.OvnKubeNode.Mode == types.NodeModeDPUHost {
		return nil
	}

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

		bridgeMapConfs := strings.Split(stdout, ",")
		for _, bridgeMapConf := range bridgeMapConfs {
			maps := strings.Split(bridgeMapConf, ":")
			if len(maps) == 2 && strings.HasPrefix(nc.nadInfo.NetName, maps[0]) {
				bridgeName = maps[1]
				break
			}
		}

		if bridgeName == "" {
			klog.V(5).Infof("Localnet network %s is not needed on this node %s", nc.nadInfo.NetName, nc.node.name)
			return nil
		}
		nc.nadInfo.BridgeName = bridgeName
	} else {
		bridgeName = nc.nadInfo.BridgeName
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

func (n *OvnNode) deleteNetworkAttachDefinition(netattachdef *nettypes.NetworkAttachmentDefinition) {
	klog.Infof("Delete Network Attachment Definition %s/%s", netattachdef.Namespace, netattachdef.Name)
	netconf, err := util.ParseNetConf(netattachdef)
	if err != nil {
		if err != util.ErrorAttachDefNotOvnManaged {
			klog.Error(err)
		}
		return
	}
	nadName := util.GetNadKeyName(netattachdef.Namespace, netattachdef.Name)
	if !netconf.IsSecondary {
		n.defaultNodeController.nadInfo.NetAttachDefs.Delete(nadName)
		return
	}

	netName := netconf.Name
	v, ok := n.nonDefaultNodeControllers.Load(netName)
	if !ok {
		klog.Errorf("Failed to find network controller for network %s", netName)
		return
	}

	nc := v.(*ovnNodeController)
	_, ok = nc.nadInfo.NetAttachDefs.LoadAndDelete(nadName)
	if !ok {
		klog.Errorf("Failed to find nad %s from network controller for network %s", nadName, netName)
		return
	}

	// check if there any net-attach-def sharing the same CNI conf name left, if yes, just return
	netAttachDefLeft := false
	nc.nadInfo.NetAttachDefs.Range(func(key, value interface{}) bool {
		netAttachDefLeft = true
		return false
	})

	if netAttachDefLeft {
		return
	}

	if config.OvnKubeNode.Mode != types.NodeModeDPUHost && nc.nadInfo.TopoType == types.LocalnetAttachDefTopoType {
		err = nc.updateLocalnetOvnBridgeMapping(false)
		if err != nil {
			klog.Errorf(err.Error())
		}
	}

	if config.OvnKubeNode.Mode == types.NodeModeDPU {
		if nc.podHandler != nil {
			// wait for pod handler routines to exit
			nc.node.watchFactory.RemovePodHandler(nc.podHandler)
		}
		// Currently, only XDP uses nc.gateway, so we can assume XDP if
		// this is set.
		if nc.gateway != nil {
			klog.Infof("Removing XDP for NAD %s", nadName)
			err = n.cleanGatewayDPUXDP(nc.nadInfo, nc.gateway.(*gateway))
			if err != nil {
				klog.Infof("Failed to remove XDP config: %v", err)
			}
			klog.Infof("Destroyed XDP config")
		}
		// We can do this regardless of dosCheckEnabled
		close(nc.stopChan)
		nc.wGroup.Wait()
	}

	n.nonDefaultNodeControllers.Delete(netName)
}

// checkForSkipFirewalldAnnotation looks for "k8s.ovn.org/skip-firewalld" annotation
// on service of endpointslice and returns the corresponding value.
func (n *OvnNode) checkForSkipFirewalldAnnotation(epSlice *discovery.EndpointSlice) bool {
	svcName, ok := epSlice.Labels[discovery.LabelServiceName]
	if !ok || svcName == "" {
		klog.Errorf("EndpointSlice %s/%s missing %s label",
			epSlice.Namespace, epSlice.Name, discovery.LabelServiceName)
		return false
	}
	svc, err := n.watchFactory.GetService(epSlice.Namespace, svcName)
	if err != nil {
		klog.Errorf("%s/%s service not found in informers cache :(%v)",
			epSlice.Namespace, svcName, err)
		return false
	}
	val, ok := svc.Annotations[ovnSkipFirewalldAnnotationName]
	if ok && val == "true" {
		return true
	}
	return false
}

func (n *OvnNode) WatchEndpointSlices(nodeIP string) error {
	start := time.Now()

	_, err := n.watchFactory.AddEndpointSliceHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			endpointSlice := obj.(*discovery.EndpointSlice)
			klog.Infof("Processing add for endpoint slice %s on namespace %s",
				endpointSlice.Name, endpointSlice.Namespace)
			startTime := time.Now()
			// open firewalld ports for host node services only if
			// "k8s.ovn.org/skip-firewalld" annotation is not set to true
			// on the corresponding service for endpoints.
			skipFirewalldAnnotation := n.checkForSkipFirewalldAnnotation(endpointSlice)
			annotationMapKey := endpointSlice.Namespace + "/" + endpointSlice.Name
			n.svcAnnotationMap.Store(annotationMapKey, skipFirewalldAnnotation)
			if !skipFirewalldAnnotation {
				addEPSliceToFirewallZone(nodeIP, endpointSlice)
			} else {
				klog.Infof("Skipping firewalld for endpointslice: %s/%s", endpointSlice.Namespace, endpointSlice.Name)
			}
			klog.Infof("Took %v to add endpoint slice %s/%s",
				time.Since(startTime), endpointSlice.Namespace, endpointSlice.Name)
		},
		UpdateFunc: func(prevObj, obj interface{}) {
			var skipFirewalldAnnotation bool
			oldEndpointSlice := prevObj.(*discovery.EndpointSlice)
			newEndpointSlice := obj.(*discovery.EndpointSlice)
			annotationMapKey := newEndpointSlice.Namespace + "/" + newEndpointSlice.Name
			if val, ok := n.svcAnnotationMap.Load(annotationMapKey); ok {
				skipFirewalldAnnotation = val.(bool)
			}
			oldEpAddr := getEndpointAddresses(oldEndpointSlice)
			newEpAddr := getEndpointAddresses(newEndpointSlice)
			if reflect.DeepEqual(oldEndpointSlice.Ports, newEndpointSlice.Ports) &&
				reflect.DeepEqual(oldEpAddr, newEpAddr) {
				return
			}
			klog.Infof("Processing update for endpoint slice %s on namespace %s",
				newEndpointSlice.Name, newEndpointSlice.Namespace)
			startTime := time.Now()
			updateEndpointSlice(nodeIP, skipFirewalldAnnotation, oldEndpointSlice, newEndpointSlice)
			klog.Infof("Took %v to update endpoint slice %s/%s",
				time.Since(startTime), newEndpointSlice.Namespace, newEndpointSlice.Name)
		},
		DeleteFunc: func(obj interface{}) {
			var skipFirewalldAnnotation bool
			endpointSlice := obj.(*discovery.EndpointSlice)
			annotationMapKey := endpointSlice.Namespace + "/" + endpointSlice.Name
			if val, ok := n.svcAnnotationMap.LoadAndDelete(annotationMapKey); ok {
				skipFirewalldAnnotation = val.(bool)
			}

			// Deletes the ep ports from ovn and ngn-admin zone if the endpoint IP
			// is same as the nodeIP and "k8s.ovn.org/skip-firewalld" annotation is not set to true.
			// Also deletes any connection tracking entries for UDP and SCTP ports
			for _, port := range endpointSlice.Ports {
				for _, endpoint := range endpointSlice.Endpoints {
					for _, ip := range endpoint.Addresses {
						klog.V(7).Infof("Endpoint address is %s and NodeIP is %s for port %d/%s",
							ip, nodeIP, *port.Port, *port.Protocol)
						if nodeIP == ip && !skipFirewalldAnnotation {
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
						if config.OvnKubeNode.Mode != types.NodeModeDPUHost &&
							(*port.Protocol == kapi.ProtocolUDP || *port.Protocol == kapi.ProtocolSCTP) {
							err := util.DeleteConntrack(ip, *port.Port, *port.Protocol, netlink.ConntrackReplyAnyIP, nil)
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
	return err
}

func addEPSliceToFirewallZone(nodeIP string, endpointSlice *discovery.EndpointSlice) {
	for _, port := range endpointSlice.Ports {
		for _, endpoint := range endpointSlice.Endpoints {
			for _, ip := range endpoint.Addresses {
				klog.V(7).Infof("Endpoint address is %s and NodeIP is %s for port %d/%s",
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

// doesEPSliceContainEndpoint checks whether the endpointslice
// contains a specific endpoint with IP/Port/Protocol
func doesEPSliceContainEndpoint(epSlice *discovery.EndpointSlice,
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

func exGatewayPodsAnnotationsChanged(oldNs, newNs *kapi.Namespace) bool {
	// In reality we only care about exgw pod deletions, however since the list of IPs is not expected to change
	// that often, let's check for *any* changes to these annotations compared to their previous state and trigger
	// the logic for checking if we need to delete any conntrack entries
	return (oldNs.Annotations[util.ExternalGatewayPodIPsAnnotation] != newNs.Annotations[util.ExternalGatewayPodIPsAnnotation]) ||
		(oldNs.Annotations[util.RoutingExternalGWsAnnotation] != newNs.Annotations[util.RoutingExternalGWsAnnotation])
}

func (nc *ovnNodeController) checkAndDeleteStaleConntrackEntries() {
	namespaces, err := nc.node.watchFactory.GetNamespaces()
	if err != nil {
		klog.Errorf("Unable to get pods from informer: %v", err)
	}
	for _, namespace := range namespaces {
		_, foundRoutingExternalGWsAnnotation := namespace.Annotations[util.RoutingExternalGWsAnnotation]
		_, foundExternalGatewayPodIPsAnnotation := namespace.Annotations[util.ExternalGatewayPodIPsAnnotation]
		if foundRoutingExternalGWsAnnotation || foundExternalGatewayPodIPsAnnotation {
			pods, err := nc.node.watchFactory.GetPods(namespace.Name)
			if err != nil {
				klog.Warningf("Unable to get pods from informer for namespace %s: %v", namespace.Name, err)
			}
			if len(pods) > 0 || err != nil {
				// we only need to proceed if there is at least one pod in this namespace on this node
				// OR if we couldn't fetch the pods for some reason at this juncture
				nc.checkAndDeleteStaleConntrackEntriesForNamespace(namespace)
			}
		}
	}
}

func (nc *ovnNodeController) checkAndDeleteStaleConntrackEntriesForNamespace(newNs *kapi.Namespace) {
	// loop through all the IPs on the annotations; ARP for their MACs and form an allowlist
	gatewayIPs := strings.Split(newNs.Annotations[util.ExternalGatewayPodIPsAnnotation], ",")
	gatewayIPs = append(gatewayIPs, strings.Split(newNs.Annotations[util.RoutingExternalGWsAnnotation], ",")...)
	var wg sync.WaitGroup
	wg.Add(len(gatewayIPs))
	validMACs := sync.Map{}
	for _, gwIP := range gatewayIPs {
		go func(gwIP string) {
			defer wg.Done()
			if len(gwIP) > 0 {
				if hwAddr, err := util.GetMACAddressFromARP(net.ParseIP(gwIP)); err != nil {
					klog.Errorf("Failed to lookup hardware address for gatewayIP %s: %v", gwIP, err)
				} else if len(hwAddr) > 0 {
					// we need to reverse the mac before passing it to the conntrack filter since OVN saves the MAC in the following format
					// +------------------------------------------------------------ +
					// | 128 ...  112 ... 96 ... 80 ... 64 ... 48 ... 32 ... 16 ... 0|
					// +------------------+-------+--------------------+-------------|
					// |                  | UNUSED|    MAC ADDRESS     |   UNUSED    |
					// +------------------+-------+--------------------+-------------+
					for i, j := 0, len(hwAddr)-1; i < j; i, j = i+1, j-1 {
						hwAddr[i], hwAddr[j] = hwAddr[j], hwAddr[i]
					}
					validMACs.Store(gwIP, []byte(hwAddr))
				}
			}
		}(gwIP)
	}
	wg.Wait()

	validNextHopMACs := [][]byte{}
	validMACs.Range(func(key interface{}, value interface{}) bool {
		validNextHopMACs = append(validNextHopMACs, value.([]byte))
		return true
	})
	// Handle corner case where there are 0 IPs on the annotations OR none of the ARPs were successful; i.e allowMACList={empty}.
	// This means we *need to* pass a label > 128 bits that will not match on any conntrack entry labels for these pods.
	// That way any remaining entries with labels having MACs set will get purged.
	if len(validNextHopMACs) == 0 {
		validNextHopMACs = append(validNextHopMACs, []byte("does-not-contain-anything"))
	}

	pods, err := nc.node.watchFactory.GetPods(newNs.Name)
	if err != nil {
		klog.Errorf("Unable to get pods from informer: %v", err)
	}
	for _, pod := range pods {
		pod := pod
		podIPs, err := util.GetAllPodIPs(pod, nc.nadInfo)
		if err != nil {
			klog.Errorf("Unable to fetch IP for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		}
		for _, podIP := range podIPs { // flush conntrack only for UDP
			// for this pod, we check if the conntrack entry has a label that is not in the provided allowlist of MACs
			// only caveat here is we assume egressGW served pods shouldn't have conntrack entries with other labels set
			err := util.DeleteConntrack(podIP.String(), 0, kapi.ProtocolUDP, netlink.ConntrackOrigDstIP, validNextHopMACs)
			if err != nil {
				klog.Errorf("Failed to delete conntrack entry for pod %s: %v", podIP.String(), err)
			}
		}
	}
}

func (nc *ovnNodeController) WatchNamespaces() error {
	_, err := nc.node.watchFactory.AddNamespaceHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(old, new interface{}) {
			oldNs, newNs := old.(*kapi.Namespace), new.(*kapi.Namespace)
			if exGatewayPodsAnnotationsChanged(oldNs, newNs) {
				nc.checkAndDeleteStaleConntrackEntriesForNamespace(newNs)
			}
		},
	}, nil)
	return err
}

// validateVTEPInterfaceMTU checks if the MTU of the interface that has ovn-encap-ip is big
// enough to carry the `config.Default.MTU` and the Geneve header. If the MTU is not big
// enough, it will return an error
func (n *OvnNode) validateVTEPInterfaceMTU() error {
	ovnEncapIP := net.ParseIP(config.Default.EncapIP)
	if ovnEncapIP == nil {
		return fmt.Errorf("the set OVN Encap IP is invalid: (%s)", config.Default.EncapIP)
	}
	interfaceName, mtu, err := util.GetIFNameAndMTUForAddress(ovnEncapIP)
	if err != nil {
		return fmt.Errorf("could not get MTU for the interface with address %s: %w", ovnEncapIP, err)
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

	if mtu < requiredMTU {
		return fmt.Errorf("interface MTU (%d) is too small for specified overlay MTU (%d)", mtu, requiredMTU)
	}
	klog.V(2).Infof("MTU (%d) of network interface %s is big enough to deal with Geneve header overhead (sum %d). ",
		mtu, interfaceName, requiredMTU)
	return nil
}

func updateEndpointSlice(nodeIP string, skipFirewalldAnnotation bool,
	oldEndpointSlice, newEndpointSlice *discovery.EndpointSlice) {
	// don't add ports to firewalld if skip-firewalld annotation is set
	// on service of endpointslice
	if !skipFirewalldAnnotation {
		for _, port := range newEndpointSlice.Ports {
			for _, endpoint := range newEndpointSlice.Endpoints {
				for _, ip := range endpoint.Addresses {
					klog.V(7).Infof("Endpoint address is %s and NodeIP is %s for port %d/%s",
						ip, nodeIP, *port.Port, *port.Protocol)
					if nodeIP != ip {
						continue
					}
					if doesEPSliceContainEndpoint(oldEndpointSlice, ip, *port.Port, *port.Protocol) {
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
				if nodeIP != ip && *port.Protocol != kapi.ProtocolUDP && *port.Protocol != kapi.ProtocolSCTP {
					continue
				}
				if doesEPSliceContainEndpoint(newEndpointSlice, ip, *port.Port, *port.Protocol) {
					continue
				}
				// if skip-firewalld annotation is set, don't remove the
				// ports from firewalld
				if nodeIP == ip && !skipFirewalldAnnotation {
					klog.Infof("Removing the endpoint %s/%d/%s not present in new slice but present in old slice",
						ip, *port.Port, *port.Protocol)
					err := removePortFromFirewallZone(ovnFirewallZone, *port.Port, *port.Protocol)
					if err != nil {
						klog.Errorf("Error in removing port %d to ovn firewall zone: (%v)", *port.Port, err)
					}
					err = removePortFromFirewallZone(ngnAdminFirewallZone, *port.Port, *port.Protocol)
					if err != nil {
						klog.Errorf("Error in removing port %d to ngn-admin firewall zone: (%v)", *port.Port, err)
					}
				}
				if config.OvnKubeNode.Mode != types.NodeModeDPUHost &&
					(*port.Protocol == kapi.ProtocolUDP || *port.Protocol == kapi.ProtocolSCTP) {
					err := util.DeleteConntrack(ip, *port.Port, *port.Protocol, netlink.ConntrackReplyAnyIP, nil)
					if err != nil {
						klog.Errorf("Failed to delete conntrack entry for %s: %v", ip, err)
					}
				}
			}
		}
	}
}

func syncEndpointSlices(obj []interface{}) error {
	err := addInterfaceToFirewallZone(types.K8sMgmtIntfName, ovnFirewallZone)
	if err != nil {
		klog.Errorf("Failed to add interface %s to ovn firewall zone: (%v)",
			types.K8sMgmtIntfName, err)
	}
	// TODO(gmoodalbail): we need to clean up any stale ports in ovn and ngn-admin zone
	return err
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
	// Clean up gw0 related IPTable rules as best effort.
	for _, ip := range []string{types.V4NodeLocalNATSubnet, types.V6NodeLocalNATSubnet} {
		_, IPNet, err := net.ParseCIDR(ip)
		if err != nil {
			klog.Errorf("Failed to LocalGatewayNATRules: %v", err)
		}
		rules := getLocalGatewayNATRules(types.LocalnetGatewayNextHopPort, IPNet)
		if err := delIptRules(rules); err != nil {
			klog.Errorf("Failed to LocalGatewayNATRules: %v", err)
		}
	}
	return nil
}

func (n *OvnNode) WatchEndpointSlicesOnDPU() error {
	_, err := n.watchFactory.AddEndpointSliceHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(prevObj, obj interface{}) {
			oldEndpointSlice := prevObj.(*discovery.EndpointSlice)
			newEndpointSlice := obj.(*discovery.EndpointSlice)
			oldEpAddr := getEndpointAddresses(oldEndpointSlice)
			newEpAddr := getEndpointAddresses(newEndpointSlice)
			if reflect.DeepEqual(oldEndpointSlice.Ports, newEndpointSlice.Ports) &&
				reflect.DeepEqual(oldEpAddr, newEpAddr) {
				return
			}
			klog.Infof("Processing update for endpoint slice %s on namespace %s",
				newEndpointSlice.Name, newEndpointSlice.Namespace)
			startTime := time.Now()
			deleteConntrackEntries(newEndpointSlice, oldEndpointSlice)
			klog.Infof("Took %v to complete update for endpoint slice %s/%s",
				time.Since(startTime), newEndpointSlice.Namespace, newEndpointSlice.Name)
		},
		DeleteFunc: func(obj interface{}) {
			deleteConntrackEntries(nil, obj.(*discovery.EndpointSlice))
		},
	}, nil)
	return err
}

func (n *OvnNode) updateRateLimitingConfig(old, new *nettypes.NetworkAttachmentDefinition) {
	oldNetconf, err1 := util.ParseNetConf(old)
	newNetconf, err2 := util.ParseNetConf(new)
	if err1 == util.ErrorAttachDefNotOvnManaged && err2 == util.ErrorAttachDefNotOvnManaged {
		n.recorder.Eventf(new, kapi.EventTypeWarning, "UnsupportedChange", "net-attach-def %s/%s not managed by ovn", old.Namespace, old.Name)
		klog.Warningf("net-attach-def %s/%s not managed by ovn", old.Namespace, old.Name)
		return
	}
	if oldNetconf == nil || newNetconf == nil || !reflect.DeepEqual(oldNetconf, newNetconf) {
		n.recorder.Event(new, kapi.EventTypeWarning, "UnsupportedChange", "Netconf change is not supported")
		klog.Errorf("Netconf change is not supported for Network Attachment Definition %s/%s", old.Namespace, old.Name)
		return
	}
	// compare values of k8s.ovn.org/miss-rl-config
	oldMRLConfigStr := old.Annotations[util.MissRateLimitConfigAnnot]
	newMRLConfigStr := new.Annotations[util.MissRateLimitConfigAnnot]
	if oldMRLConfigStr == newMRLConfigStr {
		// no change
		return
	}

	nadInfo, nadConfig, err := util.ParseNADInfo(new)
	if err != nil {
		klog.Errorf("Failed to parse NAD configuration for %s/%s: %v", new.Namespace, new.Name, err)
		return
	}

	if !nadInfo.IsSecondary {
		// update nad config in default controller
		if err := n.defaultNodeController.updateNADConfig(util.GetNadKeyName(new.Namespace, new.Name), nadConfig); err != nil {
			klog.Errorf("Failed to update network config in controller: %v", err)
		} else {
			n.updateRateLimitingForPods(n.defaultNodeController, util.GetNadName(new.Namespace, new.Name, true))
		}
		return
	}
	val, ok := n.nonDefaultNodeControllers.Load(nadInfo.NetName)
	if !ok || val == nil {
		klog.Errorf("NodeController for %s not found", nadInfo.NetName)
		return
	}
	nc := val.(*ovnNodeController)
	// update nad config in non-default controller
	if err := nc.updateNADConfig(util.GetNadKeyName(new.Namespace, new.Name), nadConfig); err != nil {
		klog.Errorf("Failed to update network config in controller: %v", err)
	} else {
		n.updateRateLimitingForPods(nc, util.GetNadName(new.Namespace, new.Name, false))
	}
}

// go through pods to update rate limit config
func (n *OvnNode) updateRateLimitingForPods(controller *ovnNodeController, nadKey string) {
	// informer cache has pods filtered by node name
	pods, err := n.watchFactory.GetAllPods()
	if err != nil {
		klog.Errorf("Failed to list pods: %v", err)
		return
	}
	for _, pod := range pods {
		klog.V(5).Infof("Updating rate limit config for pod %s/%s", pod.Namespace, pod.Name)
		if err := controller.updateRateLimitingForPod(pod, nadKey); err != nil {
			klog.Error(err)
		}
	}
}

// Also deletes any connection tracking entries for UDP and SCTP ports
func deleteConntrackEntries(checkEpSlice, fromEpSlice *discovery.EndpointSlice) {
	for _, port := range fromEpSlice.Ports {
		for _, endpoint := range fromEpSlice.Endpoints {
			for _, ip := range endpoint.Addresses {
				if *port.Protocol != kapi.ProtocolUDP && *port.Protocol != kapi.ProtocolSCTP {
					continue
				}
				if checkEpSlice != nil {
					if doesEPSliceContainEndpoint(checkEpSlice, ip, *port.Port, *port.Protocol) {
						continue
					}
				}
				err := util.DeleteConntrack(ip, *port.Port, *port.Protocol, netlink.ConntrackReplyAnyIP, nil)
				if err != nil {
					klog.Errorf("Failed to delete conntrack entry for %s: %v", ip, err)
				}
			}
		}
	}
}

func updateOVSOtherConfig(key string, value interface{}) error {
	var args []string
	if value == nil {
		// no value is passed, remove the config
		args = []string{"remove", "Open_vSwitch", ".", "other_config", key}
	} else {
		// set config
		args = []string{"set", "Open_vSwitch", ".", fmt.Sprintf("other_config:%s=%v", key, value)}
	}
	if _, stderr, err := util.RunOVSVsctl(args...); err != nil {
		return fmt.Errorf("error setting/removing other_config:%s: %v\n  %q", key, err, stderr)
	}
	return nil
}
