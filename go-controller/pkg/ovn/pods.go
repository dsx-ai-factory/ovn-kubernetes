package ovn

import (
	"fmt"
	"net"
	"strings"
	"time"

	networkattachmentdefinitionapi "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/metrics"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/ipallocator"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	util "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	kapi "k8s.io/api/core/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"

	libovsdbclient "github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/ovsdb"
	libovsdbops "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdbops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
)

func (oc *Controller) syncPodsRetriable(pods []interface{}) error {
	// get the list of logical switch ports (equivalent to pods). Reserve all existing Pod IPs to
	// avoid subsequent new Pods getting the same duplicate Pod IP.
	//
	// TBD: Before this succeeds, add Pod handler should not continue to allocate IPs for the new Pods.
	expectedLogicalPorts := make(map[string]bool)
	// get the list of namespaces need update
	nsNeedUpdate := make(map[string][]net.IP)
	for _, podInterface := range pods {
		pod, ok := podInterface.(*kapi.Pod)
		if !ok {
			return fmt.Errorf("spurious object in syncPods: %v", podInterface)
		}
		if !util.PodScheduled(pod) || !util.PodWantsNetwork(pod) || util.PodCompleted(pod) {
			continue
		}
		on, networkMap, err := util.IsNetworkOnPod(pod, oc.nadInfo)
		if err != nil || !on {
			continue
		}
		nodeName := pod.Spec.NodeName
		if oc.nadInfo.TopoType == ovntypes.LocalnetAttachDefTopoType {
			nodeName = ovntypes.OVNLocalnetSwitch
		} else if oc.nadInfo.TopoType == ovntypes.Layer2AttachDefTopoType {
			nodeName = ovntypes.OvnLayer2Switch
		}
		switchName := util.GetClusterScopedName(oc.nadInfo.Prefix + nodeName)
		// skip nodes that are not running ovnk (inferred from host subnets)
		if oc.lsManager.IsNonHostSubnetSwitch(switchName) {
			continue
		}
		for nadName := range networkMap {
			annoNadKeyName := util.GetAnnotationKeyFromNadName(nadName, !oc.nadInfo.IsSecondary)
			annotations, err := util.UnmarshalPodAnnotation(pod.Annotations, annoNadKeyName)
			if err == nil {
				logicalPort := util.GetLogicalPortName(pod.Namespace, pod.Name, nadName, !oc.nadInfo.IsSecondary)
				expectedLogicalPorts[logicalPort] = true
				if err = oc.waitForNodeLogicalSwitchInCache(switchName); err != nil {
					return fmt.Errorf("failed to wait for switch %s to be added to cache. IP allocation may fail!",
						switchName)
				}
				if err = oc.lsManager.AllocateIPs(switchName, annotations.IPs); err != nil {
					if err == ipallocator.ErrAllocated {
						// already allocated: log an error but not stop syncPod from continuing
						klog.Errorf("Already allocated IPs: %s for pod: %s on switch: %s",
							util.JoinIPNetIPs(annotations.IPs, " "), logicalPort,
							switchName)
					} else {
						return fmt.Errorf("couldn't allocate IPs: %s for pod: %s on switch: %s"+
							" error: %v", util.JoinIPNetIPs(annotations.IPs, " "), logicalPort,
							switchName, err)
					}
				}
			}
		}
	}

	var switches []string
	if oc.nadInfo.TopoType == ovntypes.LocalnetAttachDefTopoType {
		switches = []string{util.GetClusterScopedName(oc.nadInfo.Prefix + ovntypes.OVNLocalnetSwitch)}
	} else if oc.nadInfo.TopoType == ovntypes.Layer2AttachDefTopoType {
		switches = []string{util.GetClusterScopedName(oc.nadInfo.Prefix + ovntypes.OvnLayer2Switch)}

	} else {
		// get all the nodes from the watchFactory
		nodes, err := oc.mc.watchFactory.GetNodes()
		if err != nil {
			return fmt.Errorf("failed to get nodes: %v", err)
		}
		switches = make([]string, 0, len(nodes))
		for _, n := range nodes {
			if noHostSubnet(n) {
				// skip those nodes that's not OVN managed
				continue
			}
			switches = append(switches, util.GetClusterScopedName(oc.nadInfo.Prefix+n.Name))
		}
	}

	// for default network, logical_port name is in the form of podNamespace_podName, for secondary networks,
	// logical_port name is in the form of <network_prefix>_podNamespace_podName.
	// When cluster_name is set (i.e. cluster scoped), for default network, logical_port name will be in the form of
	//<cluster_name>_podNamespace_podName and secondary networks in the form
	//<cluster_name>_<network_prefix>_podNamespace_podName.
	// index to get podNamespace from logical_port name would be different in these two cases and incremented accordingly.
	nsIndex := 0
	if util.IsClusterScoped() { // Increment to account for extra cluster_name prefix.
		nsIndex++
	}
	if oc.nadInfo.IsSecondary {
		nsIndex++
	}

	var ops []ovsdb.Operation
	for _, switchName := range switches {
		p := func(item *nbdb.LogicalSwitchPort) bool {
			if !util.HasExternalIDsForCluster(item.ExternalIDs) {
				return false
			}
			netName, ok := item.ExternalIDs["network_name"]
			if oc.nadInfo.IsSecondary {
				return item.ExternalIDs["pod"] == "true" && ok && netName == oc.nadInfo.NetName && !expectedLogicalPorts[item.Name]
			} else {
				return item.ExternalIDs["pod"] == "true" && !ok && !expectedLogicalPorts[item.Name]
			}
		}
		sw := nbdb.LogicalSwitch{Name: switchName}
		sw.UUID, _ = oc.lsManager.GetUUID(switchName)

		lsps, err := libovsdbops.FindLogicalSwitchPortsWithPredicate(oc.mc.nbClient, &sw, p)
		if err != nil {
			return fmt.Errorf("could not get all stale ports from logical switch %s (%+v)", switchName, err)
		}
		for _, lsp := range lsps {
			_, ip, err := util.ExtractPortAddresses(lsp)
			// need update address_set later
			if err == nil {
				ns := strings.Split(lsp.Name, "_")[nsIndex]
				ips := nsNeedUpdate[ns]
				ips = append(ips, ip...)
				nsNeedUpdate[ns] = ips
			}
		}
		ops, err = libovsdbops.DeleteLogicalSwitchPortsWithPredicateOps(oc.mc.nbClient, ops, &sw, p)
		if err != nil {
			return fmt.Errorf("could not generate ops to delete stale ports from logical switch %s (%+v)", switchName, err)
		}

	}

	_, err := libovsdbops.TransactAndCheck(oc.mc.nbClient, ops)
	if err != nil {
		return fmt.Errorf("could not remove stale logicalPorts for network %s (%+v)", oc.nadInfo.NetName, err)
	}

	// update namespace addressSet has stale ips
	for ns, ips := range nsNeedUpdate {
		klog.Infof("Found stale IPs %s in Namespace %s. Removing them", ips, ns)
		nsInfo, nsUnlock := oc.getNamespaceLocked(ns, true)
		if nsInfo == nil {
			continue
		}
		defer nsUnlock()
		if nsInfo.addressSet != nil {
			err := nsInfo.addressSet.DeleteIPs(ips)
			if err != nil {
				klog.Errorf("Unable to delete stale IPs %v from namespace %s address_set, "+
					"error: %v", ips, ns, err)
			}
		}
	}

	return nil
}

func (oc *Controller) deleteLogicalPort(pod *kapi.Pod, portInfoMap map[string]*lpInfo) (err error) {
	podDesc := pod.Namespace + "/" + pod.Name
	klog.Infof("Deleting pod %s", podDesc)

	if !oc.nadInfo.IsSecondary {
		if err = oc.deletePodExternalGW(pod); err != nil {
			return fmt.Errorf("unable to delete external gateway routes for pod %s: %w", podDesc, err)
		}
	}
	if pod.Spec.HostNetwork {
		return nil
	}
	if !util.PodScheduled(pod) {
		return nil
	}

	on, networkMap, err := util.IsNetworkOnPod(pod, oc.nadInfo)
	if err != nil || !on {
		// the pod is not attached to this specific network
		return nil
	}

	lsManagerNodeName := pod.Spec.NodeName
	if oc.nadInfo.TopoType == ovntypes.LocalnetAttachDefTopoType {
		lsManagerNodeName = ovntypes.OVNLocalnetSwitch
	} else if oc.nadInfo.TopoType == ovntypes.Layer2AttachDefTopoType {
		lsManagerNodeName = ovntypes.OvnLayer2Switch
	}

	for nadName, network := range networkMap {
		err = oc.delLogicalPort4Nad(pod, nadName, lsManagerNodeName, network, portInfoMap[nadName])
		if err != nil {
			return err
		}
	}
	return nil
}

func (oc *Controller) delLogicalPort4Nad(pod *kapi.Pod, nadName, nodeName string,
	network *networkattachmentdefinitionapi.NetworkSelectionElement, portInfo *lpInfo) error {
	var err error
	switchName := util.GetClusterScopedName(oc.nadInfo.Prefix + nodeName)
	podDesc := pod.Namespace + "/" + pod.Name
	klog.Infof("Deleting pod %s on nad: %s", podDesc, nadName)

	logicalPort := util.GetLogicalPortName(pod.Namespace, pod.Name, nadName, !oc.nadInfo.IsSecondary)
	oc.logicalPortCache.remove(logicalPort)
	portUUID := ""
	var podIfAddrs []*net.IPNet
	if portInfo == nil {
		// If ovnkube-master restarts, it is also possible the Pod's logical switch port
		// is not re-added into the cache. Delete logical switch port anyway.
		annoNadKeyName := util.GetAnnotationKeyFromNadName(nadName, !oc.nadInfo.IsSecondary)
		annotation, err := util.UnmarshalPodAnnotation(pod.Annotations, annoNadKeyName)
		if err != nil {
			if util.IsAnnotationNotSetError(err) {
				// if the annotation doesn’t exist, that’s not an error. It means logical port does not need to be deleted.
				klog.V(5).Infof("No annotations on pod %s/%s, no need to delete its logical port: %s", pod.Namespace, pod.Name, logicalPort)
				return nil
			}
			return fmt.Errorf("unable to unmarshal pod annotations for pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}
		podIfAddrs = annotation.IPs
	} else {
		portUUID = portInfo.uuid
		podIfAddrs = portInfo.ips
	}

	shouldRelease := true
	// check to make sure no other pods are using this IP before we try to release it if this is a completed pod.
	if util.PodCompleted(pod) {
		if shouldRelease, err = oc.lsManager.ConditionalIPRelease(switchName, podIfAddrs, func() (bool, error) {
			pods, err := oc.mc.watchFactory.GetAllPods()
			if err != nil {
				return false, fmt.Errorf("unable to get pods to determine if completed pod IP is in use by another pod. "+
					"Will not release pod %s/%s IP: %#v from allocator", pod.Namespace, pod.Name, podIfAddrs)
			}
			// iterate through all pods, ignore pods on other nodes
			for _, p := range pods {
				if util.PodCompleted(p) || !util.PodWantsNetwork(p) || !util.PodScheduled(p) || p.Spec.NodeName != pod.Spec.NodeName {
					continue
				}
				// check if the pod addresses match in the OVN annotation
				pAddrs, err := util.GetAllPodIPs(p, oc.nadInfo)
				if err != nil {
					continue
				}

				for _, pAddr := range pAddrs {
					for _, podAddr := range podIfAddrs {
						if pAddr.Equal(podAddr.IP) {
							klog.Infof("Will not release IP address: %s for pod %s/%s. Detected another pod"+
								" using this IP: %s/%s", pAddr.String(), pod.Namespace, pod.Name, p.Namespace, p.Name)
							return false, nil
						}
					}
				}
			}
			klog.Infof("Releasing IPs for Completed pod: %s/%s, ips: %s", pod.Namespace, pod.Name,
				util.JoinIPNetIPs(podIfAddrs, " "))
			return true, nil
		}); err != nil {
			return fmt.Errorf("cannot determine if IPs are safe to release for completed pod: %s: %w", podDesc, err)
		}
	}

	var allOps, ops []ovsdb.Operation

	// if the ip is in use by another pod we should not try to remove it from the address set
	if shouldRelease {
		if ops, err = oc.deletePodFromNamespace(pod.Namespace, podIfAddrs, portUUID); err != nil {
			return fmt.Errorf("unable to delete pod %s from namespace: %w", podDesc, err)
		}
		allOps = append(allOps, ops...)
	}

	ops, err = oc.delLSPOps(logicalPort, switchName, portUUID)
	if err != nil {
		return fmt.Errorf("failed to create delete ops for the lsp: %s: %s", logicalPort, err)
	}
	allOps = append(allOps, ops...)

	recordOps, txOkCallBack, _, err := metrics.GetConfigDurationRecorder().AddOVN(oc.mc.nbClient, "pod", pod.Namespace,
		pod.Name, oc.nadInfo.NetNameInfo)
	if err != nil {
		klog.Errorf("Failed to record config duration: %v", err)
	}
	allOps = append(allOps, recordOps...)
	_, err = libovsdbops.TransactAndCheck(oc.mc.nbClient, allOps)
	if err != nil {
		return fmt.Errorf("cannot delete logical switch port %s, %v", logicalPort, err)
	}
	txOkCallBack()

	// do not remove SNATs/GW routes/IPAM for an IP address unless we have validated no other pod is using it
	if !shouldRelease {
		return nil
	}

	if !oc.nadInfo.IsSecondary {
		if config.Gateway.DisableSNATMultipleGWs {
			if err := deletePerPodGRSNAT(oc.mc.nbClient, pod.Spec.NodeName, []*net.IPNet{}, podIfAddrs); err != nil {
				return fmt.Errorf("cannot delete GR SNAT for pod %s: %w", podDesc, err)
			}
		}
		podNsName := ktypes.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
		if err := oc.deleteGWRoutesForPod(podNsName, podIfAddrs); err != nil {
			return fmt.Errorf("cannot delete GW Routes for pod %s: %w", podDesc, err)
		}
	}

	// Releasing IPs needs to happen last so that we can deterministically know that if delete failed that
	// the IP of the pod needs to be released. Otherwise we could have a completed pod failed to be removed
	// and we dont know if the IP was released or not, and subsequently could accidentally release the IP
	// while it is now on another pod
	klog.Infof("Attempting to release IPs for pod: %s/%s, ips: %s", pod.Namespace, pod.Name,
		util.JoinIPNetIPs(podIfAddrs, " "))
	if err := oc.lsManager.ReleaseIPs(switchName, podIfAddrs); err != nil {
		return fmt.Errorf("cannot release IPs for pod %s: %w", podDesc, err)
	}

	return nil
}

func (oc *Controller) waitForNodeLogicalSwitch(switchName string) (*nbdb.LogicalSwitch, error) {
	// Wait for the node logical switch to be created by the ClusterController and be present
	// in libovsdb's cache. The node switch will be created when the node's logical network infrastructure
	// is created by the node watch
	ls := &nbdb.LogicalSwitch{Name: switchName}
	if err := wait.PollImmediate(30*time.Millisecond, 30*time.Second, func() (bool, error) {
		if lsUUID, ok := oc.lsManager.GetUUID(switchName); !ok {
			return false, fmt.Errorf("error getting logical switch for switch %s: %s", switchName, "switch not in logical switch cache")
		} else {
			ls.UUID = lsUUID
			return true, nil
		}
	}); err != nil {
		return nil, fmt.Errorf("timed out waiting for logical switch in logical switch cache %q subnet: %v", switchName, err)
	}
	return ls, nil
}

func (oc *Controller) waitForNodeLogicalSwitchInCache(switchName string) error {
	// Wait for the node logical switch to be created by the ClusterController.
	// The node switch will be created when the node's logical network infrastructure
	// is created by the node watch.
	var subnets []*net.IPNet
	if err := wait.PollImmediate(30*time.Millisecond, 30*time.Second, func() (bool, error) {
		subnets = oc.lsManager.GetSwitchSubnets(switchName)
		return subnets != nil, nil
	}); err != nil {
		return fmt.Errorf("timed out waiting for logical switch %q subnet: %v", switchName, err)
	}
	return nil
}

func (oc *Controller) addRoutesGatewayIP(pod *kapi.Pod, podAnnotation *util.PodAnnotation, nodeSubnets []*net.IPNet,
	network *networkattachmentdefinitionapi.NetworkSelectionElement) error {

	if oc.nadInfo.IsSecondary {
		// non default network, see if its network-attachment's annotation has default-route key.
		// If present, then we need to add default route for it
		podAnnotation.Gateways = append(podAnnotation.Gateways, network.GatewayRequest...)
		for _, podIfAddr := range podAnnotation.IPs {
			isIPv6 := utilnet.IsIPv6CIDR(podIfAddr)
			// TBD localnet type does need this only for a temp workaround, to be removed.
			nodeSubnet, err := util.MatchIPNetFamily(isIPv6, nodeSubnets)
			if err != nil {
				return err
			}
			gatewayIPnet := util.GetNodeGatewayIfAddr(nodeSubnet)
			for _, clusterSubnet := range oc.clusterSubnets {
				if isIPv6 == utilnet.IsIPv6CIDR(clusterSubnet.CIDR) {
					podAnnotation.Routes = append(podAnnotation.Routes, util.PodRoute{
						Dest:    clusterSubnet.CIDR,
						NextHop: gatewayIPnet.IP,
					})
				}
			}
		}
		return nil
	}

	// For default network only: network may be nil for default network
	// if there are other network attachments for the pod, then check if those network-attachment's
	// annotation has default-route key. If present, then we need to skip adding default route for
	// OVN interface
	networks, err := util.GetK8sPodAllNetworks(pod)
	if err != nil {
		return fmt.Errorf("error while getting all network attachment definitions for [%s/%s]: %v",
			pod.Namespace, pod.Name, err)
	}
	otherDefaultRouteV4 := false
	otherDefaultRouteV6 := false
	for _, n := range networks {
		for _, gatewayRequest := range n.GatewayRequest {
			if utilnet.IsIPv6(gatewayRequest) {
				otherDefaultRouteV6 = true
			} else {
				otherDefaultRouteV4 = true
			}
		}
	}

	for _, podIfAddr := range podAnnotation.IPs {
		isIPv6 := utilnet.IsIPv6CIDR(podIfAddr)
		nodeSubnet, err := util.MatchIPNetFamily(isIPv6, nodeSubnets)
		if err != nil {
			return err
		}

		gatewayIPnet := util.GetNodeGatewayIfAddr(nodeSubnet)

		otherDefaultRoute := otherDefaultRouteV4
		if isIPv6 {
			otherDefaultRoute = otherDefaultRouteV6
		}
		var gatewayIP net.IP
		if otherDefaultRoute {
			for _, clusterSubnet := range oc.clusterSubnets {
				if isIPv6 == utilnet.IsIPv6CIDR(clusterSubnet.CIDR) {
					podAnnotation.Routes = append(podAnnotation.Routes, util.PodRoute{
						Dest:    clusterSubnet.CIDR,
						NextHop: gatewayIPnet.IP,
					})
				}
			}
			for _, serviceSubnet := range config.Kubernetes.ServiceCIDRs {
				if isIPv6 == utilnet.IsIPv6CIDR(serviceSubnet) {
					podAnnotation.Routes = append(podAnnotation.Routes, util.PodRoute{
						Dest:    serviceSubnet,
						NextHop: gatewayIPnet.IP,
					})
				}
			}
		} else {
			gatewayIP = gatewayIPnet.IP
		}

		if len(config.HybridOverlay.ClusterSubnets) > 0 {
			// Add a route for each hybrid overlay subnet via the hybrid
			// overlay port on the pod's logical switch.
			nextHop := util.GetNodeHybridOverlayIfAddr(nodeSubnet).IP
			for _, clusterSubnet := range config.HybridOverlay.ClusterSubnets {
				if utilnet.IsIPv6CIDR(clusterSubnet.CIDR) == isIPv6 {
					podAnnotation.Routes = append(podAnnotation.Routes, util.PodRoute{
						Dest:    clusterSubnet.CIDR,
						NextHop: nextHop,
					})
				}
			}
		}
		if gatewayIP != nil {
			podAnnotation.Gateways = append(podAnnotation.Gateways, gatewayIP)
		}
	}
	return nil
}

func (oc *Controller) updatePodAnnotationWithRetry(origPod *kapi.Pod, podInfo *util.PodAnnotation, annoNadKeyName string) error {
	resultErr := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		// Informer cache should not be mutated, so get a copy of the object
		pod, err := oc.mc.watchFactory.GetPod(origPod.Namespace, origPod.Name)
		if err != nil {
			return err
		}

		cpod := pod.DeepCopy()
		err = util.MarshalPodAnnotation(&cpod.Annotations, podInfo, annoNadKeyName)
		if err != nil {
			if util.IsAnnotationAlreadySetError(err) {
				return nil
			}
			return err
		}
		err = oc.mc.kube.UpdatePod(cpod)
		if err != nil {
			klog.V(5).Infof("Failed to update pod annotation %s/%s on network %s, err (%v)",
				cpod.Namespace, cpod.Name, annoNadKeyName, err)
		}
		return err
	})
	if resultErr != nil {
		return fmt.Errorf("failed to update annotation on pod %s/%s for nad %s: %v",
			origPod.Namespace, origPod.Name, annoNadKeyName, resultErr)
	}
	return nil
}

func (oc *Controller) addLogicalPort(pod *kapi.Pod) (err error) {
	lsManagerNodeName := pod.Spec.NodeName
	if oc.nadInfo.TopoType == ovntypes.LocalnetAttachDefTopoType {
		lsManagerNodeName = ovntypes.OVNLocalnetSwitch
	} else if oc.nadInfo.TopoType == ovntypes.Layer2AttachDefTopoType {
		lsManagerNodeName = ovntypes.OvnLayer2Switch
	}
	// If a node does node have an assigned hostsubnet don't wait for the logical switch to appear
	if oc.lsManager.IsNonHostSubnetSwitch(util.GetClusterScopedName(oc.nadInfo.Prefix + lsManagerNodeName)) {
		return nil
	}
	podDesc := fmt.Sprintf("[%s/%s/%s]", pod.UID, pod.Namespace, pod.Name)
	on, networkMap, err := util.IsNetworkOnPod(pod, oc.nadInfo)
	if err != nil || !on {
		// the pod is not attached to this specific network
		klog.V(5).Infof("Pod %s is not attached on this overlay network controller %s error (%v) ", podDesc,
			oc.nadInfo.NetName, err)
		return nil
	}

	klog.V(5).Infof("Pod %s is attached on this network: %s", podDesc, oc.nadInfo.NetName)
	for nadName, network := range networkMap {
		err1 := oc.addLogicalPort4Nad(pod, nadName, lsManagerNodeName, network)
		if err1 != nil {
			err = err1
		}
	}
	return err
}

func (oc *Controller) addLogicalPort4Nad(pod *kapi.Pod, nadName, nodeName string,
	network *networkattachmentdefinitionapi.NetworkSelectionElement) (err error) {
	var libovsdbExecuteTime time.Duration
	var podAnnoTime time.Duration
	podDesc := fmt.Sprintf("%s/%s/%s", pod.UID, pod.Namespace, pod.Name)
	// Keep track of how long syncs take.
	start := time.Now()
	defer func() {
		klog.Infof("[%s] addLogicalPort for nad %s took %v, libovsdb time %v, annotation time: %v, err: %v",
			podDesc, nadName, time.Since(start), libovsdbExecuteTime, podAnnoTime, err)
	}()

	logicalSwitch := util.GetClusterScopedName(oc.nadInfo.Prefix + nodeName)
	ls, err := oc.waitForNodeLogicalSwitch(logicalSwitch)
	if err != nil {
		return err
	}

	portName := util.GetLogicalPortName(pod.Namespace, pod.Name, nadName, !oc.nadInfo.IsSecondary)
	klog.Infof("[%s] creating logical port for pod on switch %s for nad %s", podDesc, logicalSwitch, nadName)

	var podMac net.HardwareAddr
	var podIfAddrs []*net.IPNet
	var addresses []string
	var releaseIPs bool
	lspExist := false
	needsIP := true
	skipIPAM := util.SkipIPAMForNAD(pod.Annotations, nadName)

	// Check if the pod's logical switch port already exists. If it
	// does don't re-add the port to OVN as this will change its
	// UUID and and the port cache, address sets, and port groups
	// will still have the old UUID.
	lsp := &nbdb.LogicalSwitchPort{
		Name: portName,
	}
	existingLSP, err := libovsdbops.GetLogicalSwitchPort(oc.mc.nbClient, lsp)
	if err != nil && err != libovsdbclient.ErrNotFound {
		return fmt.Errorf("unable to get the lsp %s from the nbdb: %s", portName, err)
	}
	lspExist = err != libovsdbclient.ErrNotFound

	lsp.Options = make(map[string]string)
	// Unique identifier to distinguish interfaces for recreated pods, also set by ovnkube-node
	// ovn-controller will claim the OVS interface only if external_ids:iface-id
	// matches with the Port_Binding.logical_port and external_ids:iface-id-ver matches
	// with the Port_Binding.options:iface-id-ver. This is not mandatory.
	// If Port_binding.options:iface-id-ver is not set, then OVS
	// Interface.external_ids:iface-id-ver if set is ignored.
	// Don't set iface-id-ver for already existing LSP if it wasn't set before,
	// because the corresponding OVS port may not have it set
	// (then ovn-controller won't bind the interface).
	// May happen on upgrade, because ovnkube-node doesn't update
	// existing OVS interfaces with new iface-id-ver option.
	if !lspExist || len(existingLSP.Options["iface-id-ver"]) != 0 {
		lsp.Options["iface-id-ver"] = string(pod.UID)
	}
	if !config.Kubernetes.SkipRequestedChassis {
		// Bind the port to the node's chassis; prevents ping-ponging between
		// chassis if ovnkube-node isn't running correctly and hasn't cleared
		// out iface-id for an old instance of this pod, and the pod got
		// rescheduled.
		lsp.Options["requested-chassis"] = pod.Spec.NodeName
	}

	annoNadKeyName := util.GetAnnotationKeyFromNadName(nadName, !oc.nadInfo.IsSecondary)
	annotation, err := util.UnmarshalPodAnnotation(pod.Annotations, annoNadKeyName)

	// the IPs we allocate in this function need to be released back to the
	// IPAM pool if there is some error in any step of addLogicalPort past
	// the point the IPs were assigned via the IPAM manager.
	// this needs to be done only when releaseIPs is set to true (the case where
	// we truly have assigned podIPs in this call) AND when there is no error in
	// the rest of the functionality of addLogicalPort. It is important to use a
	// named return variable for defer to work correctly.

	defer func() {
		if releaseIPs && err != nil {
			if relErr := oc.lsManager.ReleaseIPs(logicalSwitch, podIfAddrs); relErr != nil {
				klog.Errorf("Error when releasing IPs for switch: %s, err: %q",
					logicalSwitch, relErr)
			} else {
				klog.Infof("Released IPs: %s for switch: %s", util.JoinIPNetIPs(podIfAddrs, " "), logicalSwitch)
			}
		}
	}()

	if err == nil {
		podMac = annotation.MAC
		podIfAddrs = annotation.IPs

		// If the pod already has annotations use the existing static
		// IP/MAC from the annotation.
		lsp.DynamicAddresses = nil

		// ensure we have reserved the IPs in the annotation
		if err = oc.lsManager.AllocateIPs(logicalSwitch, podIfAddrs); err != nil && err != ipallocator.ErrAllocated {
			return fmt.Errorf("unable to ensure IPs allocated for already annotated pod: %s, IPs: %s, error: %v",
				pod.Name, util.JoinIPNetIPs(podIfAddrs, " "), err)
		} else {
			needsIP = false
		}
	}

	// It is possible that IPs have already been allocated for this pod and annotation has been updated, then the last
	// addLogicalPort4Nad() failed afterwards. In the current retry attempt, if the input pod argument got from
	// the informer cache still lags behind, we would fail to get the updated pod annotation. Just continue to allocate
	// new IPs and this function will eventually fail in updatePodAnnotationWithRetry() with ErrOverridePodIPs
	// when it tries to override the pod IP annotation. Newly allocated IPs will be released then.
	if needsIP {
		if existingLSP != nil {
			// try to get the MAC and IPs from existing OVN port first
			podMac, podIfAddrs, err = oc.getPortAddresses(logicalSwitch, existingLSP)
			if err != nil {
				return fmt.Errorf("failed to get pod addresses for pod %s on switch: %s, err: %v",
					portName, logicalSwitch, err)
			}
		}
		needsNewAllocation := false

		// ensure we have reserved the IPs found in OVN
		if len(podIfAddrs) == 0 {
			needsNewAllocation = true
		} else if !skipIPAM {
			if err = oc.lsManager.AllocateIPs(logicalSwitch, podIfAddrs); err != nil && err != ipallocator.ErrAllocated {
				klog.Warningf("Unable to allocate IPs found on existing OVN port: %s, for pod %s on switch: %s"+
					" error: %v", util.JoinIPNetIPs(podIfAddrs, " "), portName, logicalSwitch, err)
				needsNewAllocation = true
			}
		}
		if needsNewAllocation {
			// Previous attempts to use already configured IPs failed, need to assign new
			generatedPodMac, generatedPodIfAddrs, err := oc.assignPodAddresses(logicalSwitch, skipIPAM)
			if err != nil {
				return fmt.Errorf("failed to assign pod addresses for pod %s on switch: %s, err: %v",
					portName, logicalSwitch, err)
			}
			if podMac == nil {
				podMac = generatedPodMac
			}
			if len(generatedPodIfAddrs) > 0 {
				podIfAddrs = generatedPodIfAddrs
			}
		}

		releaseIPs = true
		if network != nil && network.MacRequest != "" {
			klog.V(5).Infof("Pod %s for nad %s requested custom MAC: %s", podDesc,
				nadName, network.MacRequest)
			podMac, err = net.ParseMAC(network.MacRequest)
			if err != nil {
				return fmt.Errorf("failed to parse mac %s requested in annotation for pod %s on network %s: Error %v",
					network.MacRequest, pod.Name, nadName, err)
			}
		}
		podAnnotation := util.PodAnnotation{
			IPs: podIfAddrs,
			MAC: podMac,
			MTU: oc.nadInfo.MTU,
		}
		var nodeSubnets []*net.IPNet
		if nodeSubnets = oc.lsManager.GetSwitchSubnets(logicalSwitch); nodeSubnets == nil {
			return fmt.Errorf("cannot retrieve subnet for assigning gateway routes for pod %s, switch: %s, nad %s",
				podDesc, logicalSwitch, nadName)
		}
		err = oc.addRoutesGatewayIP(pod, &podAnnotation, nodeSubnets, network)
		if err != nil {
			return err
		}
		klog.V(5).Infof("Annotation values for pod %s nad %s: ip=%v ; mac=%s ; gw=%s\n",
			podDesc, nadName, podIfAddrs, podMac, podAnnotation.Gateways)

		annoStart := time.Now()
		err = oc.updatePodAnnotationWithRetry(pod, &podAnnotation, annoNadKeyName)
		podAnnoTime = time.Since(annoStart)
		if err != nil {
			return fmt.Errorf("failed to update annotation on pod %s/%s for nad %s: %v", pod.Namespace, pod.Name, annoNadKeyName, err)
		}
		releaseIPs = false
	}

	// Ensure the namespace/nsInfo exists
	routingExternalGWs, routingPodGWs, ops, err := oc.addPodToNamespace(pod.Namespace, podIfAddrs)
	if err != nil {
		return err
	}

	if !oc.nadInfo.IsSecondary {
		// if we have any external or pod Gateways, add routes
		gateways := make([]*gatewayInfo, 0, len(routingExternalGWs.gws)+len(routingPodGWs))

		if len(routingExternalGWs.gws) > 0 {
			gateways = append(gateways, routingExternalGWs)
		}
		for key := range routingPodGWs {
			gw := routingPodGWs[key]
			if len(gw.gws) > 0 {
				if err = validateRoutingPodGWs(routingPodGWs); err != nil {
					klog.Error(err)
				}
				gateways = append(gateways, &gw)
			} else {
				klog.Warningf("Found routingPodGW with no gateways ip set for namespace %s", pod.Namespace)
			}
		}

		if len(gateways) > 0 {
			podNsName := ktypes.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
			err = oc.addGWRoutesForPod(gateways, podIfAddrs, podNsName, nodeName)
			if err != nil {
				return err
			}
		} else if config.Gateway.DisableSNATMultipleGWs {
			// Add NAT rules to pods if disable SNAT is set and does not have
			// namespace annotations to go through external egress router
			if extIPs, err := getExternalIPsGRSNAT(oc.mc.watchFactory, nodeName); err != nil {
				return err
			} else if ops, err = oc.addOrUpdatePerPodGRSNATReturnOps(nodeName, extIPs, podIfAddrs, ops); err != nil {
				return err
			}
		}
	}

	// set addresses on the port
	// LSP addresses in OVN are a single space-separated value
	addresses = []string{podMac.String()}
	for _, podIfAddr := range podIfAddrs {
		addresses[0] = addresses[0] + " " + podIfAddr.IP.String()
	}

	lsp.Addresses = addresses

	// add external ids
	lsp.ExternalIDs = map[string]string{"namespace": pod.Namespace, "pod": "true"}
	lsp.ExternalIDs = util.ExternalIDsForCluster(lsp.ExternalIDs)
	if oc.nadInfo.IsSecondary {
		lsp.ExternalIDs["network_name"] = oc.nadInfo.NetName
		lsp.ExternalIDs["nad_name"] = nadName
		lsp.ExternalIDs = util.ExternalIDsForCluster(lsp.ExternalIDs)
	}

	// CNI depends on the flows from port security, delay setting it until end
	//
	// There are Pods which requires the spoof check to be disabled, so do it here.
	// ex: for IGW's private and public underlay networks we need the port security to be disabled
	// on the corresponding LSP so that it can provide high availability for the default gateway IP.
	// TODO(gmoodalbail): need a correct way to disable portSecurity for default network
	skipPortSecurity := util.SkipSpoofCheckForNAD(pod.Annotations, annoNadKeyName)
	if skipPortSecurity {
		klog.Infof("Skip setting port security for port %s on NAD %s", portName, nadName)
	} else {
		if skipIPAM {
			if allowedIPs, err := util.GetAllowedIPsForNetwork(nadName, pod.Annotations); err != nil {
				return fmt.Errorf("failed to parse port security info for %s: %v", nadName, err)
			} else if len(allowedIPs) > 0 {
				allowedAddresses := []string{}
				for _, ip := range allowedIPs {
					allowedAddresses = append(allowedAddresses, fmt.Sprintf("%s %s", podMac.String(), ip))
				}
				lsp.PortSecurity = allowedAddresses
			} else {
				klog.V(5).Infof("No allowed IPs are specified for skip-ipam port %s, adding mac %s only", portName, podMac.String())
				lsp.PortSecurity = []string{podMac.String()}
			}
		} else {
			lsp.PortSecurity = addresses
		}
	}

	ops, err = libovsdbops.CreateOrUpdateLogicalSwitchPortsOnSwitchOps(oc.mc.nbClient, ops, ls, lsp)
	if err != nil {
		return fmt.Errorf("error creating logical switch port %+v on switch %+v: %+v", *lsp, *ls, err)
	}

	recordOps, txOkCallBack, _, err := metrics.GetConfigDurationRecorder().AddOVN(oc.mc.nbClient, "pod", pod.Namespace,
		pod.Name, oc.nadInfo.NetNameInfo)
	if err != nil {
		klog.Errorf("Config duration recorder: %v", err)
	}
	ops = append(ops, recordOps...)

	transactStart := time.Now()
	_, err = libovsdbops.TransactAndCheckAndSetUUIDs(oc.mc.nbClient, lsp, ops)
	libovsdbExecuteTime = time.Since(transactStart)
	if err != nil {
		return fmt.Errorf("error transacting operations %+v: %v", ops, err)
	}
	txOkCallBack()
	// primary network lsp does not set nadName external_ids so set its nadName to be empty
	if oc.nadInfo.IsSecondary {
		oc.mc.podRecorder.AddLSP(pod.UID, oc.nadInfo.NetNameInfo, nadName)
	} else {
		oc.mc.podRecorder.AddLSP(pod.UID, oc.nadInfo.NetNameInfo, "")
	}

	if !oc.nadInfo.IsSecondary {
		// check if this pod is serving as an external GW
		err = oc.addPodExternalGW(pod)
		if err != nil {
			return fmt.Errorf("failed to handle external GW check: %v", err)
		}
	}

	// if somehow lspUUID is empty, there is a bug here with interpreting OVSDB results
	if len(lsp.UUID) == 0 {
		return fmt.Errorf("UUID is empty from LSP: %+v", *lsp)
	}

	// Add the pod's logical switch port to the port cache
	portInfo := oc.logicalPortCache.add(logicalSwitch, portName, lsp.UUID, podMac, podIfAddrs)

	// If multicast is allowed and enabled for the namespace, add the port to the allow policy.
	// FIXME: there's a race here with the Namespace multicastUpdateNamespace() handler, but
	// it's rare and easily worked around for now.
	ns, err := oc.mc.watchFactory.GetNamespace(pod.Namespace)
	if err != nil {
		return err
	}
	if oc.multicastSupport && isNamespaceMulticastEnabled(ns.Annotations) {
		if err := podAddAllowMulticastPolicy(oc.mc.nbClient, pod.Namespace, portInfo, oc.nadInfo.NetNameInfo); err != nil {
			return err
		}
	}
	// observe the pod creation latency metric, default network for now
	if !oc.nadInfo.IsSecondary && !util.PodReady(pod) {
		metrics.RecordPodCreated(pod)
	}
	return nil
}

// Given a switch, gets the next set of addresses (from the IPAM) for each of the node's
// subnets to assign to the new pod; if skipIPAM is true, return mac address only
func (oc *Controller) assignPodAddresses(switchName string, skipIPAM bool) (net.HardwareAddr, []*net.IPNet, error) {
	var (
		podMAC   net.HardwareAddr
		podCIDRs []*net.IPNet
		err      error
	)
	if skipIPAM {
		mac, err := util.GenerateRandMAC()
		if err != nil {
			return nil, nil, err
		}
		return mac, nil, nil
	}
	podCIDRs, err = oc.lsManager.AllocateNextIPs(switchName)
	if err != nil {
		return nil, nil, err
	}
	if len(podCIDRs) > 0 {
		podMAC = util.IPAddrToHWAddr(podCIDRs[0].IP)
	}
	return podMAC, podCIDRs, nil
}

// Given a logical switch port and the switch on which it is scheduled, get all
// addresses currently assigned to it including subnet masks.
func (oc *Controller) getPortAddresses(switchName string, existingLSP *nbdb.LogicalSwitchPort) (net.HardwareAddr, []*net.IPNet, error) {
	podMac, podIPs, err := util.ExtractPortAddresses(existingLSP)
	if err != nil {
		return nil, nil, err
	} else if podMac == nil || len(podIPs) == 0 {
		return nil, nil, nil
	}

	var podIPNets []*net.IPNet

	nodeSubnets := oc.lsManager.GetSwitchSubnets(switchName)

	for _, ip := range podIPs {
		for _, subnet := range nodeSubnets {
			if subnet.Contains(ip) {
				podIPNets = append(podIPNets,
					&net.IPNet{
						IP:   ip,
						Mask: subnet.Mask,
					})
				break
			}
		}
	}
	return podMac, podIPNets, nil
}

// delLSPOps returns the ovsdb operations required to delete the given logical switch port (LSP)
func (oc *Controller) delLSPOps(logicalPort, logicalSwitch, lspUUID string) ([]ovsdb.Operation, error) {
	lsUUID, _ := oc.lsManager.GetUUID(logicalSwitch)
	lsw := nbdb.LogicalSwitch{
		UUID: lsUUID,
		Name: logicalSwitch,
	}
	lsp := nbdb.LogicalSwitchPort{
		UUID: lspUUID,
		Name: logicalPort,
	}
	ops, err := libovsdbops.DeleteLogicalSwitchPortsOps(oc.mc.nbClient, nil, &lsw, &lsp)
	if err != nil {
		return nil, fmt.Errorf("error deleting logical switch port %+v from switch %+v: %v", lsp, lsw, err)
	}

	return ops, nil
}
