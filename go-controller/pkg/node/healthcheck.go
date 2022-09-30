package node

import (
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube/healthcheck"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/pkg/errors"

	kapi "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
)

// initLoadBalancerHealthChecker initializes the health check server for
// ServiceTypeLoadBalancer services

type loadBalancerHealthChecker struct {
	sync.Mutex
	nodeName  string
	server    healthcheck.Server
	services  map[ktypes.NamespacedName]uint16
	endpoints map[ktypes.NamespacedName]int
}

func newLoadBalancerHealthChecker(nodeName string) *loadBalancerHealthChecker {
	return &loadBalancerHealthChecker{
		nodeName:  nodeName,
		server:    healthcheck.NewServer(nodeName, nil, nil, nil),
		services:  make(map[ktypes.NamespacedName]uint16),
		endpoints: make(map[ktypes.NamespacedName]int),
	}
}

func (l *loadBalancerHealthChecker) AddService(svc *kapi.Service) {
	if svc.Spec.HealthCheckNodePort != 0 {
		l.Lock()
		defer l.Unlock()
		name := ktypes.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}
		l.services[name] = uint16(svc.Spec.HealthCheckNodePort)
		_ = l.server.SyncServices(l.services)
	}
}

func (l *loadBalancerHealthChecker) UpdateService(old, new *kapi.Service) {
	// HealthCheckNodePort can't be changed on update
}

func (l *loadBalancerHealthChecker) DeleteService(svc *kapi.Service) {
	if svc.Spec.HealthCheckNodePort != 0 {
		l.Lock()
		defer l.Unlock()
		name := ktypes.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}
		delete(l.services, name)
		delete(l.endpoints, name)
		_ = l.server.SyncServices(l.services)
	}
}

func (l *loadBalancerHealthChecker) SyncServices(svcs []interface{}) error {
	return nil
}

func (l *loadBalancerHealthChecker) AddEndpointSlice(epSlice *discovery.EndpointSlice) {
	namespacedName := namespacedNameFromEPSlice(epSlice)
	l.Lock()
	defer l.Unlock()
	if _, exists := l.services[namespacedName]; exists {
		l.endpoints[namespacedName] = countReadyEndpoints(epSlice)
		_ = l.server.SyncEndpoints(l.endpoints)
	}
}

func (l *loadBalancerHealthChecker) UpdateEndpointSlice(oldEpSlice, newEpSlice *discovery.EndpointSlice) {
	namespacedName := namespacedNameFromEPSlice(newEpSlice)
	l.Lock()
	defer l.Unlock()
	if _, exists := l.services[namespacedName]; exists {
		l.endpoints[namespacedName] = countReadyEndpoints(newEpSlice)
		_ = l.server.SyncEndpoints(l.endpoints)
	}
}

func (l *loadBalancerHealthChecker) DeleteEndpointSlice(epSlice *discovery.EndpointSlice) {
	namespacedName := namespacedNameFromEPSlice(epSlice)
	l.Lock()
	defer l.Unlock()
	delete(l.endpoints, namespacedName)
	_ = l.server.SyncEndpoints(l.endpoints)
}

func countReadyEndpoints(epSlice *discovery.EndpointSlice) int {
	var num int
	for _, endpoint := range epSlice.Endpoints {
		if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
			num++
		}
	}
	return num
}

// hasLocalHostNetworkEndpoints returns true if there is at least one host-networked endpoint
// in the provided list that is local to this node.
// It returns false if none of the endpoints are local host-networked endpoints or if ep.Subsets is nil.
func hasLocalHostNetworkEndpoints(epSlices []*discovery.EndpointSlice, nodeAddresses []net.IP) bool {
	for _, epSlice := range epSlices {
		for _, endpoint := range epSlice.Endpoints {
			for _, ip := range endpoint.Addresses {
				for _, nodeIP := range nodeAddresses {
					if nodeIP.String() == ip {
						return true
					}
				}
			}
		}
	}
	return false
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
		if val == types.K8sMgmtIntfName {
			klog.Errorf("The representor for the ovn-k8s-mp0 management port is missing on the DPU. " +
				"Perhaps the host rebooted or SR-IOV VFs were disabled on the host.")
			continue
		}
		klog.Warningf("Found stale interface %s, so queuing it to be deleted", val)
		if len(staleInterfaceArgs) > 0 {
			staleInterfaceArgs = append(staleInterfaceArgs, "--")
		}
		staleInterfaceArgs = append(staleInterfaceArgs, "--if-exists", "--with-iface", "del-port", val)
	}

	_, stderr, err := util.RunOVSVsctl(staleInterfaceArgs...)
	if err != nil {
		klog.Errorf("Failed to delete OVS port/interfaces: stderr: %s (%v)",
			stderr, err)
	}
}

// checkForStaleOVSRepresentorInterfaces checks for stale OVS ports backed by Representor interfaces,
// derive iface-id from pod name and namespace then remove any interfaces associated with a sandbox that are
// not scheduled to the node.
func checkForStaleOVSRepresentorInterfaces(nodeName string, wf factory.ObjectCacheInterface) {
	// Get all ovn-kubernetes Pod interfaces. these are OVS interfaces that have their external_ids:sandbox set.
	ovsArgs := []string{"--columns=name,external_ids", "--data=bare", "--no-headings",
		"--format=csv", "find", "Interface", "external_ids:sandbox!=\"\""}
	out, stderr, err := util.RunOVSVsctl(ovsArgs...)
	if err != nil {
		klog.Errorf("Failed to list ovn-k8s OVS interfaces, stderr: %q, error: %v", stderr, err)
		return
	}

	// parse this data into local struct
	type interfaceInfo struct {
		Name       string
		Attributes map[string]string
	}

	lines := strings.Split(out, "\n")
	interfaceInfos := make([]*interfaceInfo, 0, len(lines))
	for _, line := range lines {
		cols := strings.Split(line, ",")
		// Note: There are exactly 2 column entries as requested in the ovs query
		// Col 0: interface name
		// Col 1: space separated key=val pairs of external_ids attributes
		if len(cols) < 2 {
			// unlikely to happen
			continue
		}
		ifcInfo := interfaceInfo{Name: strings.TrimSpace(cols[0]), Attributes: make(map[string]string)}
		for _, attr := range strings.Split(cols[1], " ") {
			keyVal := strings.SplitN(attr, "=", 2)
			if len(keyVal) != 2 {
				// unlikely to happen
				continue
			}
			ifcInfo.Attributes[keyVal[0]] = keyVal[1]
		}
		interfaceInfos = append(interfaceInfos, &ifcInfo)
	}

	if len(interfaceInfos) == 0 {
		return
	}

	// list Pods and calculate the expected iface-ids.
	// Note: we do this after scanning ovs interfaces to avoid deleting ports of pods that where just scheduled
	// on the node.
	pods, err := wf.GetPods("")
	if err != nil {
		klog.Errorf("Failed to list pods. %v", err)
		return
	}
	expectedIfaceIdsWithoutPrefix := make(map[string]bool)
	for _, pod := range pods {
		if pod.Spec.NodeName == nodeName && util.PodWantsNetwork(pod) {
			// Note: wf (WatchFactory) *usually* returns pods assigned to this node, however we dont rely on it
			// and add this check to filter out pods assigned to other nodes. (e.g when ovnkube master and node
			// share the same process)
			expectedIfaceIdsWithoutPrefix[util.GetIfaceId(pod.Namespace, pod.Name, types.DefaultNetworkName, true)] = true
		}
	}

	// Remove any stale representor ports
	for _, ifaceInfo := range interfaceInfos {
		// TBD, upgrde path? ignore non-vf representor ports
		//if _, ok := ifaceInfo.Attributes["vf-netdev-name"]; !ok {
		//	continue
		//}
		ifaceId, ok := ifaceInfo.Attributes["iface-id"]
		if !ok {
			// interface with external-ids:sandbox set but no iface-id, delete it
			klog.Warningf("iface-id attribute was not found for OVS interface %s. "+
				"deleting OVS port with interface %s", ifaceInfo.Name)
			_, stderr, err := util.RunOVSVsctl("--if-exists", "--with-iface", "del-port", ifaceInfo.Name)
			if err != nil {
				klog.Errorf("Failed to delete interface %q . stderr: %q, error: %v",
					ifaceInfo.Name, stderr, err)
			}
			continue
		}
		prefix := ""
		nadName, ok := ifaceInfo.Attributes["network_name"]
		if ok {
			prefix = util.GetNetworkPrefix(nadName, false)
			if !strings.HasPrefix(ifaceId, prefix) {
				klog.Warningf("iface-id of OVS interface %s for network %s is invalid: %s", ifaceInfo.Name,
					nadName, ifaceId)
				continue
			}
			ifaceId = strings.TrimPrefix(ifaceId, prefix)
		}

		if _, ok := expectedIfaceIdsWithoutPrefix[ifaceId]; !ok {
			klog.Warningf("Found stale OVS Interface, deleting OVS Port with interface %s", ifaceInfo.Name)
			_, stderr, err := util.RunOVSVsctl("--if-exists", "--with-iface", "del-port", ifaceInfo.Name)
			if err != nil {
				klog.Errorf("Failed to delete interface %q, stderr: %q, error: %v",
					ifaceInfo.Name, stderr, err)
				continue
			}
		}
	}
}

// checkForStaleOVSInterfaces periodically checks for stale OVS interfaces
func checkForStaleOVSInterfaces(nodeName string, wf factory.ObjectCacheInterface) {
	if config.OvnKubeNode.Mode == types.NodeModeFull {
		checkForStaleOVSInternalPorts()
	}
	checkForStaleOVSRepresentorInterfaces(nodeName, wf)
}

type openflowManager struct {
	defaultBridge         *bridgeConfiguration
	externalGatewayBridge *bridgeConfiguration
	// flow cache, use map instead of array for readability when debugging
	flowCache     map[string][]string
	flowMutex     sync.Mutex
	exGWFlowCache map[string][]string
	exGWFlowMutex sync.Mutex
	// channel to indicate we need to update flows immediately
	flowChan chan struct{}
}

func (c *openflowManager) updateFlowCacheEntry(key string, flows []string) {
	c.flowMutex.Lock()
	defer c.flowMutex.Unlock()
	c.flowCache[key] = flows
}

func (c *openflowManager) deleteFlowsByKey(key string) {
	c.flowMutex.Lock()
	defer c.flowMutex.Unlock()
	delete(c.flowCache, key)
}

func (c *openflowManager) updateExBridgeFlowCacheEntry(key string, flows []string) {
	c.exGWFlowMutex.Lock()
	defer c.exGWFlowMutex.Unlock()
	c.exGWFlowCache[key] = flows
}

func (c *openflowManager) requestFlowSync() {
	select {
	case c.flowChan <- struct{}{}:
		klog.V(5).Infof("Gateway OpenFlow sync requested")
	default:
		klog.V(5).Infof("Gateway OpenFlow sync already requested")
	}
}

func (c *openflowManager) syncFlows() {
	c.flowMutex.Lock()
	defer c.flowMutex.Unlock()

	flows := []string{}
	for _, entry := range c.flowCache {
		flows = append(flows, entry...)
	}

	_, stderr, err := util.ReplaceOFFlows(c.defaultBridge.bridgeName, flows)
	if err != nil {
		klog.Errorf("Failed to add flows, error: %v, stderr, %s, flows: %s", err, stderr, c.flowCache)
	}

	if c.externalGatewayBridge != nil {
		c.exGWFlowMutex.Lock()
		defer c.exGWFlowMutex.Unlock()

		flows := []string{}
		for _, entry := range c.exGWFlowCache {
			flows = append(flows, entry...)
		}

		_, stderr, err := util.ReplaceOFFlows(c.externalGatewayBridge.bridgeName, flows)
		if err != nil {
			klog.Errorf("Failed to add flows, error: %v, stderr, %s, flows: %s", err, stderr, c.exGWFlowCache)
		}
	}
}

// checkDefaultOpenFlow checks for the existence of default OpenFlow rules and
// exits if the output is not as expected
func (c *openflowManager) Run(stopChan <-chan struct{}, doneWg *sync.WaitGroup) {
	doneWg.Add(1)
	go func() {
		defer doneWg.Done()
		syncPeriod := 15 * time.Second
		timer := time.NewTicker(syncPeriod)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				if err := checkPorts(c.defaultBridge); err != nil {
					klog.Errorf("Checkports failed %v", err)
					continue
				}
				if c.externalGatewayBridge != nil {
					if err := checkPorts(c.externalGatewayBridge); err != nil {
						klog.Errorf("Checkports failed %v", err)
						continue
					}
				}
				if c.flowCache != nil {
					c.syncFlows()
				}
			case <-c.flowChan:
				c.syncFlows()
				timer.Reset(syncPeriod)
			case <-stopChan:
				return
			}
		}
	}()
}

func checkPorts(bridge *bridgeConfiguration) error {
	// it could be that the ovn-controller recreated the patch between the host OVS bridge and
	// the integration bridge, as a result the ofport number changed for that patch interface
	curOfportPatch, stderr, err := util.GetOVSOfPort("--if-exists", "get", "Interface", bridge.patchPort, "ofport")
	if err != nil {
		return errors.Wrapf(err, "Failed to get ofport of %s, stderr: %q", bridge.patchPort, stderr)

	}
	// For XDP gateway the localnet patch port may be deleted and recreated as needed. So, we can't
	// always expect the ofPortPatch to agree. If the ofPortPatch changes we just check if there
	// are any flows using the ofPortPatch and error out if so; i.e. the localnet is deleted
	// but flows using the localnet port are still around.
	// However, if the of ports disagree, but there are no flows that use the old of port,
	// then it is not an error.
	// This assumes ofPortPhys doesn't change, which we'll still consider as fatal.
	// For the N/S gateway we should not have a situation where the patch's OF port changes,
	// so will make this check specific to localnet ports.
	if bridge.ofPortPatch != curOfportPatch {
		// XXX- Maybe, use gateway type
		if strings.Contains(bridge.patchPort, "localnet_port") {
			xdpCheckPatchPort(bridge.bridgeName, bridge.ofPortPhys, bridge.patchPort, bridge.ofPortPatch, curOfportPatch)
		} else {
			klog.Errorf("Fatal error: patch port %s ofport changed from %s to %s",
				bridge.patchPort, bridge.ofPortPatch, curOfportPatch)
			os.Exit(1)
		}
	}

	// it could be that someone removed the physical interface and added it back on the OVS host
	// bridge, as a result the ofport number changed for that physical interface
	curOfportPhys, stderr, err := util.GetOVSOfPort("--if-exists", "get", "interface", bridge.uplinkName, "ofport")
	if err != nil {
		return errors.Wrapf(err, "Failed to get ofport of %s, stderr: %q", bridge.uplinkName, stderr)
	}
	if bridge.ofPortPhys != curOfportPhys {
		klog.Errorf("Fatal error: phys port %s ofport changed from %s to %s",
			bridge.uplinkName, bridge.ofPortPhys, curOfportPhys)
		os.Exit(1)
	}

	// it could be ofport number of host representor interface changed
	if bridge.hostRepName != "" {
		curOfportHost, stderr, err := util.GetOVSOfPort("--if-exists", "get", "interface", bridge.hostRepName, "ofport")
		if err != nil {
			return errors.Wrapf(err, "Failed to get ofport of %s, stderr: %q", bridge.hostRepName, stderr)
		}
		if bridge.ofPortHost != curOfportHost {
			klog.Errorf("Fatal error: host representor port %s ofport changed from %s to %s",
				bridge.hostRepName, bridge.ofPortHost, curOfportHost)
			os.Exit(1)
		}
	}
	return nil
}

func namespacedNameFromEPSlice(epSlice *discovery.EndpointSlice) ktypes.NamespacedName {
	svcName := epSlice.Labels[discovery.LabelServiceName]
	return ktypes.NamespacedName{Namespace: epSlice.Namespace, Name: svcName}
}
