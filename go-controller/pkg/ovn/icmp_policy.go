package ovn

import (
	"fmt"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	ovsdb "github.com/ovn-org/libovsdb/ovsdb"
	onet "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/icmpnetworkpolicy/v1alpha1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/libovsdbops"
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

	stalePGs := []string{}
	err := oc.addressSetFactory.ProcessEachAddressSet(func(addrSetName, namespaceName, policyName string, icmpAddressSet bool) {
		if icmpAddressSet && policyName != "" && !expectedPolicies[namespaceName][policyName] {
			// policy doesn't exist on k8s. Delete the port group
			portGroupName := fmt.Sprintf("%s_%s", namespaceName, policyName)
			hashedLocalPortGroup := hashedPortGroup(portGroupName)
			stalePGs = append(stalePGs, hashedLocalPortGroup)
			// delete the address sets for this old policy from OVN
			if err := oc.addressSetFactory.DestroyAddressSetInBackingStore(addrSetName); err != nil {
				klog.Errorf(err.Error())
			}
		}
	})
	if err != nil {
		klog.Errorf("Error in syncing ICMP network policies: %v", err)
	}

	if len(stalePGs) > 0 {
		err = libovsdbops.DeletePortGroups(oc.mc.nbClient, stalePGs...)
		if err != nil {
			klog.Errorf("Error removing stale port groups %v: %v", stalePGs, err)
		}
	}
}

func (oc *Controller) icmpLocalPodAddDefaultDeny(policy *onet.ICMPNetworkPolicy,
	ports ...*lpInfo) (ingressDenyPorts, egressDenyPorts []string) {
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

	ingressDenyPorts = []string{}
	egressDenyPorts = []string{}

	// Handle condition 1 above.
	if !(len(policy.Spec.PolicyTypes) == 1 && policy.Spec.PolicyTypes[0] == onet.PolicyTypeEgress) {
		for _, portInfo := range ports {
			// if this is the first NP referencing this pod, then we
			// need to add it to the port group.
			if oc.lspIngressDenyCache[portInfo.name] == 0 {
				ingressDenyPorts = append(ingressDenyPorts, portInfo.uuid)
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
				egressDenyPorts = append(egressDenyPorts, portInfo.uuid)
			}

			// bump reference count
			oc.lspEgressDenyCache[portInfo.name]++
		}
	}

	// we're done with the lsp cache - release the lock before transacting
	oc.lspMutex.Unlock()
	return
}

func (oc *Controller) icmpHandleLocalPodSelectorAddFunc(policy *onet.ICMPNetworkPolicy, np *networkPolicy,
	portGroupIngressDenyName, portGroupEgressDenyName string, obj interface{}) {
	np.RLock()
	defer np.RUnlock()
	if np.deleted {
		return
	}

	policyPorts, ingressDenyPorts, egressDenyPorts := oc.icmpProcessLocalPodSelectorSetPods(policy, np, obj)

	ops, err := libovsdbops.AddPortsToPortGroupOps(oc.mc.nbClient, nil, oc.nadInfo.Prefix+portGroupIngressDenyName, ingressDenyPorts...)
	if err != nil {
		oc.processLocalPodSelectorDelPods(np, obj)
		klog.Errorf(err.Error())
	}

	ops, err = libovsdbops.AddPortsToPortGroupOps(oc.mc.nbClient, ops, oc.nadInfo.Prefix+portGroupEgressDenyName, egressDenyPorts...)
	if err != nil {
		oc.processLocalPodSelectorDelPods(np, obj)
		klog.Errorf(err.Error())
	}

	ops, err = libovsdbops.AddPortsToPortGroupOps(oc.mc.nbClient, ops, oc.nadInfo.Prefix+np.portGroupName, policyPorts...)
	if err != nil {
		oc.processLocalPodSelectorDelPods(np, obj)
		klog.Errorf(err.Error())
	}

	_, err = libovsdbops.TransactAndCheck(oc.mc.nbClient, ops)
	if err != nil {
		oc.processLocalPodSelectorDelPods(np, obj)
		klog.Errorf(err.Error())
		return
	}
}

func (oc *Controller) icmpHandleLocalPodSelectorDelFunc(policy *onet.ICMPNetworkPolicy, np *networkPolicy,
	portGroupIngressDenyName, portGroupEgressDenyName string, obj interface{}) {
	np.RLock()
	defer np.RUnlock()
	if np.deleted {
		return
	}

	policyPorts, ingressDenyPorts, egressDenyPorts := oc.processLocalPodSelectorDelPods(np, obj)

	ops, err := libovsdbops.DeletePortsFromPortGroupOps(oc.mc.nbClient, nil, oc.nadInfo.Prefix+portGroupIngressDenyName, ingressDenyPorts...)
	if err != nil {
		oc.icmpProcessLocalPodSelectorSetPods(policy, np, obj)
		klog.Errorf(err.Error())
		return
	}

	ops, err = libovsdbops.DeletePortsFromPortGroupOps(oc.mc.nbClient, ops, oc.nadInfo.Prefix+portGroupEgressDenyName, egressDenyPorts...)
	if err != nil {
		oc.icmpProcessLocalPodSelectorSetPods(policy, np, obj)
		klog.Errorf(err.Error())
		return
	}

	ops, err = libovsdbops.DeletePortsFromPortGroupOps(oc.mc.nbClient, ops, oc.nadInfo.Prefix+np.portGroupName, policyPorts...)
	if err != nil {
		oc.icmpProcessLocalPodSelectorSetPods(policy, np, obj)
		klog.Errorf(err.Error())
	}

	_, err = libovsdbops.TransactAndCheck(oc.mc.nbClient, ops)
	if err != nil {
		oc.icmpProcessLocalPodSelectorSetPods(policy, np, obj)
		klog.Errorf(err.Error())
		return
	}
}

func (oc *Controller) icmpHandleLocalPodSelector(
	policy *onet.ICMPNetworkPolicy, np *networkPolicy, portGroupIngressDenyName, portGroupEgressDenyName string,
	handleInitialItems func([]interface{})) {

	// NetworkPolicy is validated by the apiserver; this can't fail.
	sel, _ := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)

	h := oc.mc.watchFactory.AddFilteredPodHandler(policy.Namespace, sel,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				oc.icmpHandleLocalPodSelectorAddFunc(policy, np, portGroupIngressDenyName, portGroupEgressDenyName, obj)
			},
			DeleteFunc: func(obj interface{}) {
				oc.icmpHandleLocalPodSelectorDelFunc(policy, np, portGroupIngressDenyName, portGroupEgressDenyName, obj)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				oc.icmpHandleLocalPodSelectorAddFunc(policy, np, portGroupIngressDenyName, portGroupEgressDenyName, newObj)
			},
		}, func(objs []interface{}) {
			handleInitialItems(objs)
		})

	np.Lock()
	defer np.Unlock()
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

// createNetworkPolicy creates a network policy
func (oc *Controller) createICMPNetworkPolicy(np *networkPolicy, policy *onet.ICMPNetworkPolicy, aclLogDeny, aclLogAllow,
	portGroupIngressDenyName, portGroupEgressDenyName string, statelessACL bool) {

	policyName := "icmp_" + policy.Name

	np.Lock()

	if aclLogDeny != "" || aclLogAllow != "" {
		klog.Infof("ACL logging for ICMP network policy %s in namespace %s set to deny=%s, allow=%s",
			policyName, policy.Namespace, aclLogDeny, aclLogAllow)
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
		klog.V(5).Infof("Network ICMP policy ingress is %+v", ingressJSON)

		ingress := newGressPolicy(onet.PolicyTypeIngress, i, policy.Namespace, policyName,
			oc.nadInfo, statelessACL)

		// Each ingress rule can have multiple type/code to which we allow traffic.
		for _, protocolJSON := range ingressJSON.Protocols {
			ingress.addICMPPolicy(&protocolJSON)
		}

		if icmpHasAnyLabelSelector(ingressJSON.From) {
			if err := ingress.ensurePeerAddressSet(oc.addressSetFactory); err != nil {
				klog.Errorf(err.Error())
				continue
			}
			if !oc.nadInfo.IsSecondary {
				// Start service handlers ONLY if there's an ingress Address Set
				oc.handlePeerService(policy.Namespace, ingress, np)
			}
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
		klog.V(5).Infof("Network ICMP policy egress is %+v", egressJSON)

		egress := newGressPolicy(onet.PolicyTypeEgress, i, policy.Namespace, policyName,
			oc.nadInfo, statelessACL)

		// Each egress rule can have multiple ports to which we allow traffic.
		for _, protocolJSON := range egressJSON.Protocols {
			egress.addICMPPolicy(&protocolJSON)
		}

		if icmpHasAnyLabelSelector(egressJSON.To) {
			klog.V(5).Infof("Network ICMP policy %s with egress rule %s has a selector", policyName, egress.policyName)
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

	for _, handler := range policyHandlers {
		if handler.namespaceSelector != nil && handler.podSelector != nil {
			// For each rule that contains both peer namespace selector and
			// peer pod selector, we create a watcher for each matching namespace
			// that populates the addressSet
			oc.handlePeerNamespaceAndPodSelector(handler.namespaceSelector, handler.podSelector, handler.gress, np)
		} else if handler.namespaceSelector != nil {
			// For each peer namespace selector, we create a watcher that
			// populates ingress.peerAddressSets
			oc.handlePeerNamespaceSelector(handler.namespaceSelector, handler.gress, np)
		} else if handler.podSelector != nil {
			// For each peer pod selector, we create a watcher that
			// populates the addressSet
			oc.handlePeerPodSelector(policy.Namespace,
				handler.podSelector, handler.gress, np)
		}
	}

	readableGroupName := fmt.Sprintf("%s_%s", policy.Namespace, policyName)
	np.portGroupName = hashedPortGroup(readableGroupName)
	ops := []ovsdb.Operation{}

	// Build policy ACLs
	acls := oc.buildNetworkPolicyACLs(np, aclLogAllow)
	ops, err := libovsdbops.CreateOrUpdateACLsOps(oc.mc.nbClient, ops, acls...)
	if err != nil {
		klog.Errorf(err.Error())
		return
	}

	// Build a port group for the policy. All the pods that this policy
	// selects will be eventually added to this port group.
	pg := buildPortGroup(np.portGroupName, readableGroupName, nil, acls, oc.nadInfo.NetNameInfo)

	// Add a handler to update the policy and deny port groups with the pods
	// this policy applies to.
	// Handle initial items locally to minimize DB ops.
	var selectedPods []interface{}
	handleInitialSelectedPods := func(objs []interface{}) {
		selectedPods = objs
		policyPorts, ingressDenyPorts, egressDenyPorts := oc.icmpProcessLocalPodSelectorSetPods(policy, np, selectedPods...)
		pg.Ports = append(pg.Ports, policyPorts...)
		ops, err = libovsdbops.AddPortsToPortGroupOps(oc.mc.nbClient, ops, oc.nadInfo.Prefix+portGroupIngressDenyName, ingressDenyPorts...)
		if err != nil {
			oc.processLocalPodSelectorDelPods(np, selectedPods...)
			klog.Errorf(err.Error())
		}
		ops, err = libovsdbops.AddPortsToPortGroupOps(oc.mc.nbClient, ops, oc.nadInfo.Prefix+portGroupEgressDenyName, egressDenyPorts...)
		if err != nil {
			oc.processLocalPodSelectorDelPods(np, selectedPods...)
			klog.Errorf(err.Error())
		}
	}
	oc.icmpHandleLocalPodSelector(policy, np, portGroupIngressDenyName, portGroupEgressDenyName, handleInitialSelectedPods)

	np.Lock()
	defer np.Unlock()
	if np.deleted {
		oc.processLocalPodSelectorDelPods(np, selectedPods...)
		return
	}

	ops, err = libovsdbops.CreateOrUpdatePortGroupsOps(oc.mc.nbClient, ops, pg)
	if err != nil {
		oc.processLocalPodSelectorDelPods(np, selectedPods...)
		klog.Errorf(err.Error())
		return
	}

	_, err = libovsdbops.TransactAndCheck(oc.mc.nbClient, ops)
	if err != nil {
		oc.processLocalPodSelectorDelPods(np, selectedPods...)
		klog.Errorf(err.Error())
		return
	}
	np.created = true
}

func (oc *Controller) icmpProcessLocalPodSelectorSetPods(policy *onet.ICMPNetworkPolicy,
	np *networkPolicy, objs ...interface{}) (policyPorts, ingressDenyPorts, egressDenyPorts []string) {
	klog.Infof("Processing ICMP NetworkPolicy %s/%s to have %d local pods...", np.namespace, np.name, len(objs))

	// get list of pods and their logical ports to add
	// theoretically this should never filter any pods but it's always good to be
	// paranoid.
	policyPorts = make([]string, 0, len(objs))
	policyPortsInfo := make([]*lpInfo, 0, len(objs))
	for _, obj := range objs {
		pod := obj.(*kapi.Pod)

		if pod.Spec.NodeName == "" {
			continue
		}

		// Get the logical port info
		logicalPorts := util.GetAllLogicalPortNames(pod, oc.nadInfo)
		for _, logicalPort := range logicalPorts {
			portInfo, err := oc.logicalPortCache.get(logicalPort)
			// pod is not yet handled
			// no big deal, we'll get the update when it is.
			if err != nil {
				continue
			}

			// this is portInfo of the previous deleted Pod of the same name
			// wait for the next Pod update event
			if !portInfo.expires.IsZero() {
				klog.Warningf("Port %s is already marked for removal", logicalPort)
				continue
			}

			// this pod is somehow already added to this policy, then skip
			if _, ok := np.localPods.LoadOrStore(portInfo.name, portInfo); ok {
				continue
			}

			policyPortsInfo = append(policyPortsInfo, portInfo)
			policyPorts = append(policyPorts, portInfo.uuid)
		}
	}

	ingressDenyPorts, egressDenyPorts = oc.icmpLocalPodAddDefaultDeny(policy, policyPortsInfo...)

	return
}

// addICMPNetworkPolicy creates and applies OVN ACLs to pod logical switch
// ports from Kubernetes NetworkPolicy objects using OVN Port Groups
func (oc *Controller) addICMPNetworkPolicy(policy *onet.ICMPNetworkPolicy) {
	klog.Infof("Adding ICMP network policy %s in namespace %s", policy.Name,
		policy.Namespace)
	policyName := "icmp_" + policy.Name
	nsInfo, nsUnlock, err := oc.ensureNamespaceLocked(policy.Namespace, false, nil)
	if err != nil {
		klog.Errorf("Unable to ensure namespace for network policy: %s, namespace: %s, error: %v",
			policyName, policy.Namespace, err)
		return
	}
	_, alreadyExists := nsInfo.networkPolicies[policyName]
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
		err = oc.createDefaultDenyPGAndACLs(policy.Namespace, policyName, nsInfo)
		if err != nil {
			klog.Errorf(err.Error())
			nsUnlock()
			return
		}

	}
	nsInfo.networkPolicies[policyName] = np
	aclLogDeny := nsInfo.aclLogging.Deny
	aclLogAllow := nsInfo.aclLogging.Allow
	portGroupIngressDenyName := nsInfo.portGroupIngressDenyName
	portGroupEgressDenyName := nsInfo.portGroupEgressDenyName
	nsUnlock()
	oc.createICMPNetworkPolicy(np, policy, aclLogDeny, aclLogAllow, portGroupIngressDenyName, portGroupEgressDenyName, statelessACL)
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
