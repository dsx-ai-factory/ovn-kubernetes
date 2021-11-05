package ovn

import (
	"fmt"
	"github.com/ebay/go-ovn"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	onet "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/icmpnetworkpolicy/v1alpha1"
	kapi "k8s.io/api/core/v1"
	knet "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)

//
// We prepend icmp_ to the policy name from ICMPNetworkPolicy to differentiate
// it from the same name that could be used for NetworkPolicy. We use
// icmp_ since "_" is not a valid name for XXXNetworkPolicy. We leave
// NetworkPolicy unchanged, since there could be existing policies and
// changes to NetworkPolicy might need to maintain compatibility.
// At the same time we want to maintain some underlying commonality between
// ICMPNetworkPolicy and NetworkPolicy since we need, e.g., default gress
// policies even if only one is configured. Mutating ICMPNetworkPolicy
// name in the underlying implementation seems a safe way of achieving both
// objectives.
//

func (oc *Controller) syncICMPNetworkPolicies(networkPolicies []interface{}) {
	expectedPolicies := make(map[string]map[string]bool)
	for _, npInterface := range networkPolicies {
		policy, ok := npInterface.(*onet.ICMPNetworkPolicy)
		if !ok {
			klog.Errorf("Spurious object in syncICMPNetworkPolicies: %v",
				npInterface)
			continue
		}
		policyName := "icmp_" + policy.Name
		if nsMap, ok := expectedPolicies[policy.Namespace]; ok {
			nsMap[policyName] = true
		} else {
			expectedPolicies[policy.Namespace] = map[string]bool{
				policyName: true,
			}
		}
	}

	err := oc.addressSetFactory.ProcessEachAddressSet(func(addrSetName, namespaceName, policyName string, icmpAddressSet bool) {
		if icmpAddressSet && policyName != "" && !expectedPolicies[namespaceName][policyName] {
			// policy doesn't exist on k8s. Delete the port group
			portGroupName := fmt.Sprintf("%s_%s", namespaceName, policyName)
			hashedLocalPortGroup := hashedPortGroup(portGroupName)
			err := deletePortGroup(oc.mc.ovnNBClient, hashedLocalPortGroup, oc.nadInfo.NetNameInfo)
			if err != nil {
				klog.Errorf("%v", err)
			}

			// delete the address sets for this old policy from OVN
			if err := oc.addressSetFactory.DestroyAddressSetInBackingStore(addrSetName); err != nil {
				klog.Errorf(err.Error())
			}
		}
	})
	if err != nil {
		klog.Errorf("Error in syncing ICMP network policies: %v", err)
	}
}

func (oc *Controller) icmpLocalPodAddDefaultDeny(nsInfo *namespaceInfo,
	policy *onet.ICMPNetworkPolicy, ports ...*lpInfo) {
	oc.lspMutex.Lock()

	// Default deny rule.
	// 1. Any pod that matches a network policy should get a default
	// ingress deny rule.  This is irrespective of whether there
	// is a ingress section in the network policy. But, if
	// PolicyTypes in the policy has only "egress" in it, then
	// it is a 'egress' only network policy and we should not
	// add any default deny rule for ingress.
	// 2. If there is any "egress" section in the policy or
	// the PolicyTypes has 'egress' in it, we add a default
	// egress deny rule.

	addIngressPorts := []*lpInfo{}
	addEgressPorts := []*lpInfo{}

	// Handle condition 1 above.
	if !(len(policy.Spec.PolicyTypes) == 1 && policy.Spec.PolicyTypes[0] == onet.PolicyTypeEgress) {
		for _, portInfo := range ports {
			// if this is the first NP referencing this pod, then we
			// need to add it to the port group.
			if oc.lspIngressDenyCache[portInfo.name] == 0 {
				addIngressPorts = append(addIngressPorts, portInfo)
			}

			// increment the reference count.
			oc.lspIngressDenyCache[portInfo.name]++
		}
	}

	// Handle condition 2 above.
	if (len(policy.Spec.PolicyTypes) == 1 && policy.Spec.PolicyTypes[0] == onet.PolicyTypeEgress) ||
		len(policy.Spec.Egress) > 0 || len(policy.Spec.PolicyTypes) == 2 {
		for _, portInfo := range ports {
			if oc.lspEgressDenyCache[portInfo.name] == 0 {
				// again, reference count is 0, so add to port
				addEgressPorts = append(addEgressPorts, portInfo)
			}

			// bump reference count
			oc.lspEgressDenyCache[portInfo.name]++
		}
	}

	// we're done with the lsp cache - release the lock before transacting
	oc.lspMutex.Unlock()

	// Generate a single OVN transaction that adds all ports to the
	// appropriate port groups.
	commands := make([]*goovn.OvnCommand, 0, len(addIngressPorts)+len(addEgressPorts))

	for _, portInfo := range addIngressPorts {
		portGroupName := oc.nadInfo.Prefix + nsInfo.portGroupIngressDenyName
		cmd, err := oc.mc.ovnNBClient.PortGroupAddPort(portGroupName, portInfo.uuid)
		if err != nil {
			klog.Warningf("Failed to create command: add port %s to ingress deny portgroup %s: %v",
				portInfo.name, portGroupName, err)
			continue
		}
		commands = append(commands, cmd)
	}

	for _, portInfo := range addEgressPorts {
		portGroupName := oc.nadInfo.Prefix + nsInfo.portGroupEgressDenyName
		cmd, err := oc.mc.ovnNBClient.PortGroupAddPort(portGroupName, portInfo.uuid)
		if err != nil {
			klog.Warningf("Failed to create command: add port %s to egress deny portgroup %s: %v",
				portInfo.name, portGroupName, err)
			continue
		}
		commands = append(commands, cmd)
	}

	err := oc.mc.ovnNBClient.Execute(commands...)
	if err != nil {
		klog.Warningf("Failed to execute add-to-default-deny-portgroup transaction: %v", err)
	}
}

func (oc *Controller) icmpHandleLocalPodSelectorAddFunc(
	policy *onet.ICMPNetworkPolicy, np *networkPolicy, nsInfo *namespaceInfo,
	obj interface{}) {
	pod := obj.(*kapi.Pod)

	if pod.Spec.NodeName == "" {
		return
	}

	// Get the logical port info
	logicalPorts := util.GetAllLogicalPortNames(pod.Namespace, pod.Name, oc.nadInfo)
	portsToAdd := make([]*lpInfo, 0, len(logicalPorts))
	for _, logicalPort := range logicalPorts {
		portInfo, err := oc.logicalPortCache.get(logicalPort)
		if err != nil {
			klog.Warningf(err.Error())
			continue
		}
		// If we've already processed this pod, shortcut.
		if _, ok := np.localPods.Load(logicalPort); ok {
			continue
		}
		portsToAdd = append(portsToAdd, portInfo)
	}

	np.RLock()
	defer np.RUnlock()
	if np.deleted {
		return
	}

	oc.icmpLocalPodAddDefaultDeny(nsInfo, policy, portsToAdd...)

	if np.portGroupUUID == "" {
		return
	}

	err := addToPortGroup(oc.mc.ovnNBClient, oc.nadInfo.Prefix+np.portGroupName, portsToAdd...)

	if err != nil {
		klog.Errorf("Failed to add logicalPorts to portGroup %s (%v)", np.portGroupUUID, err)
	}

	for _, portInfo := range portsToAdd {
		np.localPods.Store(portInfo.name, portInfo)
	}
}

func (oc *Controller) icmpHandleLocalPodSelectorSetPods(
	policy *onet.ICMPNetworkPolicy, np *networkPolicy, nsInfo *namespaceInfo,
	objs []interface{}) {

	// Take the write lock since this is called once and we will want to bulk-update
	// localPods
	np.Lock()
	defer np.Unlock()
	if np.deleted {
		return
	}

	klog.Infof("Setting icmp NetworkPolicy %s/%s to have %d local pods...",
		np.namespace, np.name, len(objs))

	// get list of pods and their logical ports to add
	// theoretically this should never filter any pods but it's always good to be
	// paranoid.
	portsToAdd := make([]*lpInfo, 0, len(objs))
	for _, obj := range objs {
		pod := obj.(*kapi.Pod)

		if pod.Spec.NodeName == "" {
			continue
		}

		logicalPorts := util.GetAllLogicalPortNames(pod.Namespace, pod.Name, oc.nadInfo)
		for _, logicalPort := range logicalPorts {
			portInfo, err := oc.logicalPortCache.get(logicalPort)
			// pod is not yet handled
			// no big deal, we'll get the update when it is.
			if err != nil {
				continue
			}
			// this pod is somehow already added to this policy, then skip
			if _, ok := np.localPods.Load(portInfo.name); ok {
				continue
			}
			portsToAdd = append(portsToAdd, portInfo)
		}
	}

	// add all ports to default deny
	oc.icmpLocalPodAddDefaultDeny(nsInfo, policy, portsToAdd...)

	if np.portGroupUUID == "" {
		return
	}

	err := setPortGroup(oc.mc.ovnNBClient, oc.nadInfo.Prefix+np.portGroupName, portsToAdd...)
	if err != nil {
		klog.Errorf("Failed to set ports in PortGroup for icmp network policy %s/%s: %v", np.namespace, np.name, err)
	}

	for _, portInfo := range portsToAdd {
		np.localPods.Store(portInfo.name, portInfo)
	}

	klog.Infof("Done setting icmp NetworkPolicy %s/%s local pods",
		np.namespace, np.name)

}

func (oc *Controller) icmpHandleLocalPodSelector(
	policy *onet.ICMPNetworkPolicy, np *networkPolicy, nsInfo *namespaceInfo) {

	// NetworkPolicy is validated by the apiserver; this can't fail.
	sel, _ := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)

	h := oc.mc.watchFactory.AddFilteredPodHandler(policy.Namespace, sel,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				oc.icmpHandleLocalPodSelectorAddFunc(policy, np, nsInfo, obj)
			},
			DeleteFunc: func(obj interface{}) {
				oc.handleLocalPodSelectorDelFunc(np, nsInfo, obj)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				oc.icmpHandleLocalPodSelectorAddFunc(policy, np, nsInfo, newObj)
			},
		}, func(objs []interface{}) {
			oc.icmpHandleLocalPodSelectorSetPods(policy, np, nsInfo, objs)
		})

	np.podHandlerList = append(np.podHandlerList, h)
}

// we only need to create an address set if there is a podSelector or namespaceSelector
func icmpHasAnyLabelSelector(peers []onet.NetworkPolicyPeer) bool {
	for _, peer := range peers {
		if peer.PodSelector != nil || peer.NamespaceSelector != nil {
			return true
		}
	}
	return false
}

// addICMPNetworkPolicy creates and applies OVN ACLs to pod logical switch
// ports from Kubernetes NetworkPolicy objects using OVN Port Groups
func (oc *Controller) addICMPNetworkPolicy(policy *onet.ICMPNetworkPolicy) {
	klog.Infof("Adding ICMP network policy %s in namespace %s", policy.Name,
		policy.Namespace)
	policyName := "icmp_" + policy.Name
	nsInfo, nsUnlock, err := oc.waitForNamespaceLocked(policy.Namespace, false)
	if err != nil {
		klog.Errorf("Failed to wait for namespace %s event (%v)",
			policy.Namespace, err)
		return
	}
	_, alreadyExists := nsInfo.networkPolicies[policy.Name]
	if alreadyExists {
		nsUnlock()
		return
	}

	// icmp network policy will be annotated with this
	// annotation -- [ "k8s.ovn.org/acl-stateless": "true"] for the ingress/egress
	// policies to be added as stateless OVN ACL's.
	// if the above annotation is not present or set to false in network policy,
	// then corresponding egress/ingress policies will be added as stateful OVN ACL's.
	var statelessACL bool
	val, ok := policy.Annotations[ovnStatelessACLAnnotationName]
	if ok && val == "true" {
		statelessACL = true
	}

	np := NewNetworkPolicy(policy.Namespace, policyName, policy.Spec.PolicyTypes)
	if len(nsInfo.networkPolicies) == 0 {
		err := oc.createDefaultDenyPortGroup(policy.Namespace, nsInfo, knet.PolicyTypeIngress, nsInfo.aclLogging.Deny, policyName)
		if err != nil {
			nsInfo.Unlock()
			klog.Errorf(err.Error())
			return
		}
		err = oc.createDefaultDenyPortGroup(policy.Namespace, nsInfo, knet.PolicyTypeEgress, nsInfo.aclLogging.Deny, policyName)
		if err != nil {
			nsInfo.Unlock()
			klog.Errorf(err.Error())
			return
		}
	}
	nsInfo.networkPolicies[policyName] = np

	nsInfo.Unlock()
	np.Lock()

	// Create a port group for the policy. All the pods that this policy
	// selects will be eventually added to this port group.
	// We add "icmp_" to differentiate this from any policy that could
	// be created with the same name with NetworkPolicy API.
	readableGroupName := fmt.Sprintf("%s_%s", policy.Namespace, policyName)
	np.portGroupName = hashedPortGroup(readableGroupName)

	np.portGroupUUID, err = createPortGroup(oc.mc.ovnNBClient, readableGroupName, np.portGroupName, oc.nadInfo.NetNameInfo)
	if err != nil {
		klog.Errorf("Failed to create port_group for network policy %s in "+
			"namespace %s", policyName, policy.Namespace)
		return
	}

	type policyHandler struct {
		gress             *gressPolicy
		namespaceSelector *metav1.LabelSelector
		podSelector       *metav1.LabelSelector
	}
	var policyHandlers []policyHandler
	// Go through each ingress rule.  For each ingress rule, create an
	// addressSet for the peer pods.
	for i, ingressJSON := range policy.Spec.Ingress {
		klog.V(5).Infof("ICMP Network policy ingress is %+v", ingressJSON)

		ingress := newGressPolicy(onet.PolicyTypeIngress, i, policy.Namespace, policyName, oc.nadInfo, statelessACL)

		// Each ingress rule can have multiple type/code to which we allow traffic.
		for _, protocolJSON := range ingressJSON.Protocols {
			ingress.addICMPPolicy(&protocolJSON)
		}

		if icmpHasAnyLabelSelector(ingressJSON.From) {
			if err := ingress.ensurePeerAddressSet(oc.addressSetFactory); err != nil {
				klog.Errorf(err.Error())
				continue
			}
			// Start service handlers ONLY if there's an ingress Address Set
			oc.handlePeerService(policy.Namespace, ingress, np)
		}

		for _, fromJSON := range ingressJSON.From {
			// Add IPBlock to ingress network policy
			if fromJSON.IPBlock != nil {
				kJSONIPBlock := &knet.IPBlock{CIDR: fromJSON.IPBlock.CIDR, Except: fromJSON.IPBlock.Except}
				ingress.addIPBlock(kJSONIPBlock)
			}

			policyHandlers = append(policyHandlers, policyHandler{
				gress:             ingress,
				namespaceSelector: fromJSON.NamespaceSelector,
				podSelector:       fromJSON.PodSelector,
			})
		}
		np.ingressPolicies = append(np.ingressPolicies, ingress)
	}

	// Go through each egress rule.  For each egress rule, create an
	// addressSet for the peer pods.
	for i, egressJSON := range policy.Spec.Egress {
		klog.V(5).Infof("ICMP Network policy egress is %+v", egressJSON)

		egress := newGressPolicy(onet.PolicyTypeEgress, i, policy.Namespace, policyName, oc.nadInfo, statelessACL)

		// Each egress rule can have multiple typese/code to which we allow traffic.
		for _, protocolJSON := range egressJSON.Protocols {
			egress.addICMPPolicy(&protocolJSON)
		}

		if icmpHasAnyLabelSelector(egressJSON.To) {
			if err := egress.ensurePeerAddressSet(oc.addressSetFactory); err != nil {
				klog.Errorf(err.Error())
				continue
			}
		}

		for _, toJSON := range egressJSON.To {
			// Add IPBlock to egress network policy
			if toJSON.IPBlock != nil {
				kJSONIPBlock := &knet.IPBlock{CIDR: toJSON.IPBlock.CIDR, Except: toJSON.IPBlock.Except}
				egress.addIPBlock(kJSONIPBlock)
			}

			policyHandlers = append(policyHandlers, policyHandler{
				gress:             egress,
				namespaceSelector: toJSON.NamespaceSelector,
				podSelector:       toJSON.PodSelector,
			})
		}

		np.egressPolicies = append(np.egressPolicies, egress)
	}
	np.Unlock()

	// For all the pods in the local namespace that this policy
	// effects, add them to the port group.
	oc.icmpHandleLocalPodSelector(policy, np, nsInfo)

	for _, handler := range policyHandlers {
		if handler.namespaceSelector != nil && handler.podSelector != nil {
			// For each rule that contains both peer namespace selector and
			// peer pod selector, we create a watcher for each matching namespace
			// that populates the addressSet
			oc.handlePeerNamespaceAndPodSelector(
				handler.namespaceSelector, handler.podSelector,
				handler.gress, np)
		} else if handler.namespaceSelector != nil {
			// For each peer namespace selector, we create a watcher that
			// populates ingress.peerAddressSets
			oc.handlePeerNamespaceSelector(
				handler.namespaceSelector, handler.gress, np)
		} else if handler.podSelector != nil {
			// For each peer pod selector, we create a watcher that
			// populates the addressSet
			oc.handlePeerPodSelector(policy.Namespace,
				handler.podSelector, handler.gress, np)
		}
	}

	// Finally, make sure that all ACLs are set
	oc.addNetworkPolicyACL(np, nsInfo.aclLogging.Allow)
}

// Maybe consolidtae with deleteNetworkPolicy
func (oc *Controller) deleteICMPNetworkPolicy(policy *onet.ICMPNetworkPolicy) {
	klog.Infof("Deleting ICMP network policy %s in namespace %s",
		policy.Name, policy.Namespace)
	policyName := "icmp_" + policy.Name

	nsInfo, nsUnlock := oc.getNamespaceLocked(policy.Namespace, false)
	if nsInfo == nil {
		klog.V(5).Infof("Failed to get namespace lock when deleting policy %s in namespace %s",
			policyName, policy.Namespace)
		return
	}
	defer nsUnlock()

	np := nsInfo.networkPolicies[policyName]
	if np == nil {
		return
	}

	delete(nsInfo.networkPolicies, policyName)

	oc.destroyNetworkPolicy(np, nsInfo)
}
