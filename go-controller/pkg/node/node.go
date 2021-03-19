package node

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	honode "github.com/ovn-org/ovn-kubernetes/go-controller/hybrid-overlay/pkg/controller"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/informer"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
)

// OvnNode is the object holder for utilities meant for node management
type OvnNode struct {
	name         string
	Kube         kube.Interface
	watchFactory factory.NodeWatchFactory
	stopChan     chan struct{}
	recorder     record.EventRecorder
	gateway      Gateway
}

// NewNode creates a new controller for node management
func NewNode(kubeClient kubernetes.Interface, wf factory.NodeWatchFactory, name string, stopChan chan struct{}, eventRecorder record.EventRecorder) *OvnNode {
	return &OvnNode{
		name:         name,
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

	_, stderr, err := util.RunOVSVsctl("set",
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
		"external_ids:ovn-enable-lflow-cache=false",
	)
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

// Start learns the subnets assigned to it by the master controller
// and calls the SetupNode script which establishes the logical switch
func (n *OvnNode) Start(wg *sync.WaitGroup) error {
	var err error
	var node *kapi.Node
	var subnets []*net.IPNet

	// Setting debug log level during node bring up to expose bring up process.
	// Log level is returned to configured value when bring up is complete.
	var level klog.Level
	if err := level.Set("5"); err != nil {
		klog.Errorf("Setting klog \"loglevel\" to 5 failed, err: %v", err)
	}

	for _, auth := range []config.OvnAuthConfig{config.OvnNorth, config.OvnSouth} {
		if err := auth.SetDBAuth(); err != nil {
			return err
		}
	}

	if node, err = n.Kube.GetNode(n.name); err != nil {
		return fmt.Errorf("error retrieving node %s: %v", n.name, err)
	}
	err = setupOVNNode(node)
	if err != nil {
		return err
	}

	// First wait for the node logical switch to be created by the Master, timeout is 300s.
	err = wait.PollImmediate(500*time.Millisecond, 300*time.Second, func() (bool, error) {
		if node, err = n.Kube.GetNode(n.name); err != nil {
			klog.Infof("Waiting to retrieve node %s: %v", n.name, err)
			return false, nil
		}
		subnets, err = util.ParseNodeHostSubnetAnnotation(node)
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

	if _, err = isOVNControllerReady(n.name); err != nil {
		return err
	}

	nodeAnnotator := kube.NewNodeAnnotator(n.Kube, node)
	waiter := newStartupWaiter()

	// Initialize management port resources on the node
	mgmtPortConfig, err := createManagementPort(n.name, subnets, nodeAnnotator, waiter)
	if err != nil {
		return err
	}

	// Initialize gateway resources on the node
	if err := n.initGateway(subnets, nodeAnnotator, waiter, mgmtPortConfig); err != nil {
		return err
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

	if config.HybridOverlay.Enabled {
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

	// start health check to ensure there are no stale OVS internal ports
	go checkForStaleOVSInterfaces(n.stopChan)

	// start management port health check
	go checkManagementPortHealth(mgmtPortConfig, n.stopChan)

	confFile := filepath.Join(config.CNI.ConfDir, config.CNIConfFileName)
	_, err = os.Stat(confFile)
	if os.IsNotExist(err) {
		err = config.WriteCNIConfig()
		if err != nil {
			return err
		}
	}

	n.WatchEndpoints()

	cniServer := cni.NewCNIServer("", n.watchFactory)
	err = cniServer.Start(cni.HandleCNIRequest)

	return err
}

func (n *OvnNode) WatchEndpoints() {
	var nodeIP string

	start := time.Now()
	node, err := n.Kube.GetNode(n.name)
	if err != nil {
		klog.Errorf("Error retrieving node %s: %v", n.name, err)
	} else {
		nodeIP, err = util.GetNodePrimaryIP(node)
		if err != nil {
			klog.Errorf("Failed to obtain local IP from node %q: %v", node.Name, err)
		}
	}

	n.watchFactory.AddEndpointsHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ep := obj.(*kapi.Endpoints)
			// Add the ep ports to ovn-firewall zone
			// if backed by k8s-node ip's
			addEPToFirewallZone(nodeIP, ep.Subsets)
		},
		UpdateFunc: func(old, new interface{}) {
			epNew := new.(*kapi.Endpoints)
			epOld := old.(*kapi.Endpoints)
			updateRequired := true
			if len(epOld.Subsets) == len(epNew.Subsets) {
				updateRequired = false
			outer:
				for i := 0; i < len(epOld.Subsets); i++ {
					if !reflect.DeepEqual(epOld.Subsets[i].Ports, epNew.Subsets[i].Ports) {
						updateRequired = true
						break
					}
					if len(epOld.Subsets[i].Addresses) != len(epNew.Subsets[i].Addresses) {
						updateRequired = true
						break
					}
					epOldAddrLen := len(epOld.Subsets[i].Addresses)
					epOldAddress := make(map[string]bool)
					for j := 0; j < epOldAddrLen; j++ {
						epOldAddress[epOld.Subsets[i].Addresses[j].IP] = true
					}
					for j := 0; j < epOldAddrLen; j++ {
						if _, ok := epOldAddress[epNew.Subsets[i].Addresses[j].IP]; !ok {
							updateRequired = true
							break outer
						}
					}
				}
			}
			if !updateRequired {
				return
			}
			updateEndpoints(nodeIP, epOld.Subsets, epNew.Subsets)
		},
		DeleteFunc: func(obj interface{}) {
			ep := obj.(*kapi.Endpoints)
			// Deletes the ep ports from ovn and ngn-admin zone if the endpoint IP is same as the nodeIP.
			// Also deletes any connection tracking entries for UDP and SCTP ports
			for _, subset := range ep.Subsets {
				for _, port := range subset.Ports {
					for _, address := range subset.Addresses {
						if nodeIP == address.IP {
							err := removePortFromFirewallZone(ovnFirewallZone, port.Port, port.Protocol)
							if err != nil {
								klog.Errorf("Error in removing port %d to ovn firewall zone: (%v)", port.Port, err)
							}
							err = removePortFromFirewallZone(ngnAdminFirewallZone, port.Port, port.Protocol)
							if err != nil {
								klog.Errorf("Error in removing port %d to ngn-admin firewall zone: (%v)",
									port.Port, err)
							}
						}
						if port.Protocol == kapi.ProtocolUDP || port.Protocol == kapi.ProtocolSCTP {
							err := deleteConntrack(address.IP, port.Port, port.Protocol)
							if err != nil {
								klog.Errorf("Failed to delete conntrack entry for %s: %v", address.IP, err)
							}
						}
					}
				}
			}
		},
	}, syncEndpoints)
	klog.Infof("Bootstrapping existing endpoints took %v", time.Since(start))
}

// syncEndpoints add's ovn-k8s-gw0 & ovn-k8s-mp0 ports to
// ovn firewall zone when node restarts.
func syncEndpoints(endpoints []interface{}) {
	if err := addInterfaceToFirewallZone(types.K8sMgmtIntfName, ovnFirewallZone); err != nil {
		klog.Errorf("Failed to add interface %s to ovn firewall zone: (%v)",
			types.K8sMgmtIntfName, err)
	}
	if err := addInterfaceToFirewallZone(localnetGatewayNextHopPort, ovnFirewallZone); err != nil {
		klog.Errorf("Failed to add interface %s to ovn firewall zone: (%v)",
			localnetGatewayNextHopPort, err)
	}
}

func addEPToFirewallZone(nodeIP string, epSubsets []kapi.EndpointSubset) {
	for _, subset := range epSubsets {
		for _, address := range subset.Addresses {
			if nodeIP != address.IP {
				continue
			}
			for _, port := range subset.Ports {
				err := addPortToFirewallZone(ovnFirewallZone, port.Port, port.Protocol)
				if err != nil {
					klog.Errorf("Error in adding port %d to ovn firewall zone: (%v)", port.Port, err)
				}
				err = addPortToFirewallZone(ngnAdminFirewallZone, port.Port, port.Protocol)
				if err != nil {
					klog.Errorf("Error in adding port %d to ngn-admin firewall zone: (%v)", port.Port, err)
				}
			}
			return
		}
	}
}

func endPointExists(epIP string, epPort int32, protocol kapi.Protocol, epNewSubsets []kapi.EndpointSubset) bool {
	for _, subset := range epNewSubsets {
		for _, address := range subset.Addresses {
			for _, port := range subset.Ports {
				if epIP == address.IP && epPort == port.Port && protocol == port.Protocol {
					return true
				}
			}
		}
	}
	return false
}

func updateEndpoints(nodeIP string, epOldSubsets, epNewSubsets []kapi.EndpointSubset) {
	// add any new ports to the firewalld zone
	addEPToFirewallZone(nodeIP, epNewSubsets)

	// now remove any old ports that are not present in the new endpoint resource
	for _, subset := range epOldSubsets {
		for _, port := range subset.Ports {
			for _, address := range subset.Addresses {
				// if the port is neither UDP nor SCTP and endpointIP doesn't match the node's IP, then
				// there is nothing to do
				if nodeIP != address.IP && port.Protocol != kapi.ProtocolUDP && port.Protocol != kapi.ProtocolSCTP {
					continue
				}
				if endPointExists(address.IP, port.Port, port.Protocol, epNewSubsets) {
					continue
				}
				if nodeIP == address.IP {
					err := removePortFromFirewallZone(ovnFirewallZone, port.Port, port.Protocol)
					if err != nil {
						klog.Errorf("Error in removing port %d to ovn firewall zone: (%v)", port.Port, err)
					}
					err = removePortFromFirewallZone(ngnAdminFirewallZone, port.Port, port.Protocol)
					if err != nil {
						klog.Errorf("Error in removing port %d to ngn-admin firewall zone: (%v)", port.Port, err)
					}
				}
				if port.Protocol == kapi.ProtocolUDP || port.Protocol == kapi.ProtocolSCTP {
					err := deleteConntrack(address.IP, port.Port, port.Protocol)
					if err != nil {
						klog.Errorf("Failed to delete conntrack entry for %s: %v", address.IP, err)
					}
				}
			}
		}
	}
}
