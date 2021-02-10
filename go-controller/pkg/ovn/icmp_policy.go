package ovn

import (
	"fmt"

	onet "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/icmpnetworkpolicy/v1alpha1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
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

	err := oc.addressSetFactory.ForEachAddressSet(func(addrSetName, namespaceName, policyName string, icmpAddressSet bool) {
		if icmpAddressSet && policyName != "" && !expectedPolicies[namespaceName][policyName] {
			// policy doesn't exist on k8s. Delete the port group
			portGroupName := fmt.Sprintf("%s_%s", namespaceName, policyName)
			hashedLocalPortGroup := hashedPortGroup(portGroupName)
			deletePortGroup(hashedLocalPortGroup)

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
	policy *onet.ICMPNetworkPolicy, portInfo *lpInfo) {
	oc.lspMutex.Lock()
	defer oc.lspMutex.Unlock()

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

	// Handle condition 1 above.
	if !(len(policy.Spec.PolicyTypes) == 1 && policy.Spec.PolicyTypes[0] == onet.PolicyTypeEgress) {
		if oc.lspIngressDenyCache[portInfo.name] == 0 {
			if err := addToPortGroup(nsInfo.portGroupIngressDenyUUID, portInfo); err != nil {
				klog.Warningf("Failed to add port %s to ingress deny ACL: %v", portInfo.name, err)
			}
		}
		oc.lspIngressDenyCache[portInfo.name]++
	}

	// Handle condition 2 above.
	if (len(policy.Spec.PolicyTypes) == 1 && policy.Spec.PolicyTypes[0] == onet.PolicyTypeEgress) ||
		len(policy.Spec.Egress) > 0 || len(policy.Spec.PolicyTypes) == 2 {
		if oc.lspEgressDenyCache[portInfo.name] == 0 {
			if err := addToPortGroup(nsInfo.portGroupEgressDenyUUID, portInfo); err != nil {
				klog.Warningf("Failed to add port %s to egress deny ACL: %v", portInfo.name, err)
			}
		}
		oc.lspEgressDenyCache[portInfo.name]++
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
	logicalPort := podLogicalPortName(pod)
	portInfo, err := oc.logicalPortCache.get(logicalPort)
	if err != nil {
		klog.Warningf(err.Error())
		return
	}

	np.Lock()
	defer np.Unlock()

	if np.deleted {
		return
	}

	if _, ok := np.localPods[logicalPort]; ok {
		return
	}

	oc.icmpLocalPodAddDefaultDeny(nsInfo, policy, portInfo)

	if np.portGroupUUID == "" {
		return
	}

	_, stderr, err := util.RunOVNNbctl("--if-exists", "remove",
		"port_group", np.portGroupUUID, "ports", portInfo.uuid, "--",
		"add", "port_group", np.portGroupUUID, "ports", portInfo.uuid)
	if err != nil {
		klog.Errorf("Failed to add logicalPort %s to portGroup %s "+
			"stderr: %q (%v)", logicalPort, np.portGroupUUID, stderr, err)
	}

	np.localPods[logicalPort] = portInfo
}

func (oc *Controller) icmpHandleLocalPodSelector(
	policy *onet.ICMPNetworkPolicy, np *networkPolicy, nsInfo *namespaceInfo) {

	// NetworkPolicy is validated by the apiserver; this can't fail.
	sel, _ := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)

	h := oc.watchFactory.AddFilteredPodHandler(policy.Namespace, sel,
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
		}, nil)

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
	nsInfo, err := oc.waitForNamespaceLocked(policy.Namespace)
	if err != nil {
		klog.Errorf("Failed to wait for namespace %s event (%v)",
			policy.Namespace, err)
		return
	}
	_, alreadyExists := nsInfo.networkPolicies[policyName]
	if alreadyExists {
		nsInfo.Unlock()
		return
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

	np.portGroupUUID, err = createPortGroup(readableGroupName, np.portGroupName)
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

		ingress := newGressPolicy(onet.PolicyTypeIngress, i, policy.Namespace, policyName)

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
		ingress.localPodAddACL(np.portGroupName, np.portGroupUUID, nsInfo.aclLogging.Allow)
		np.ingressPolicies = append(np.ingressPolicies, ingress)
	}

	// Go through each egress rule.  For each egress rule, create an
	// addressSet for the peer pods.
	for i, egressJSON := range policy.Spec.Egress {
		klog.V(5).Infof("ICMP Network policy egress is %+v", egressJSON)

		egress := newGressPolicy(onet.PolicyTypeEgress, i, policy.Namespace, policyName)

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
		egress.localPodAddACL(np.portGroupName, np.portGroupUUID, nsInfo.aclLogging.Allow)
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
}

// Maybe consolidtae with deleteNetworkPolicy
func (oc *Controller) deleteICMPNetworkPolicy(policy *onet.ICMPNetworkPolicy) {
	klog.Infof("Deleting ICMP network policy %s in namespace %s",
		policy.Name, policy.Namespace)
	policyName := "icmp_" + policy.Name

	nsInfo := oc.getNamespaceLocked(policy.Namespace)
	if nsInfo == nil {
		klog.V(5).Infof("Failed to get namespace lock when deleting ICMP network policy %s in namespace %s",
			policyName, policy.Namespace)
		return
	}
	defer nsInfo.Unlock()

	np := nsInfo.networkPolicies[policyName]
	if np == nil {
		return
	}

	delete(nsInfo.networkPolicies, policyName)

	oc.destroyNetworkPolicy(np, nsInfo)
}
