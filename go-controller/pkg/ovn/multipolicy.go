package ovn

import (
	"fmt"
	"strings"

	multinetworkpolicy "github.com/k8snetworkplumbingwg/multi-networkpolicy/pkg/apis/k8s.cni.cncf.io/v1beta2"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/libovsdbops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	knet "k8s.io/api/networking/v1"
	"k8s.io/klog/v2"
)

const PolicyForAnnotation = "k8s.v1.cni.cncf.io/policy-for"

func (oc *Controller) syncMultiNetworkPolicies(multiPolicies []interface{}) {
	expectedPolicies := make(map[string]map[string]bool)
	for _, npInterface := range multiPolicies {
		policy, ok := npInterface.(*multinetworkpolicy.MultiNetworkPolicy)
		if !ok {
			klog.Errorf("Spurious object in syncMultiNetworkPolicies: %v",
				npInterface)
			continue
		}
		if !oc.shouldApplyMultiPolicy(policy) {
			klog.V(5).Infof("[controller(%s)] skipping syncing policy %s/%s",
				oc.nadInfo.NetName, policy.Namespace, policy.Name)
			continue
		}
		if nsMap, ok := expectedPolicies[policy.Namespace]; ok {
			nsMap[policy.Name] = true
		} else {
			expectedPolicies[policy.Namespace] = map[string]bool{
				policy.Name: true,
			}
		}
	}

	stalePGs := []string{}
	err := oc.addressSetFactory.ProcessEachAddressSet(func(addrSetName, namespaceName, policyName string) {
		if policyName != "" && !expectedPolicies[namespaceName][policyName] {
			// policy doesn't exist on k8s. Delete the port group
			portGroupName := fmt.Sprintf("%s_%s", namespaceName, policyName)
			hashedLocalPortGroup := oc.nadInfo.Prefix + hashedPortGroup(portGroupName)
			stalePGs = append(stalePGs, hashedLocalPortGroup)
			// delete the address sets for this old policy from OVN
			if err := oc.addressSetFactory.DestroyAddressSetInBackingStore(addrSetName); err != nil {
				klog.Errorf(err.Error())
			}
		}
	})
	if err != nil {
		klog.Errorf("Error in syncing multi network policies: %v", err)
	}
	if len(stalePGs) > 0 {
		err = libovsdbops.DeletePortGroups(oc.mc.nbClient, stalePGs...)
		if err != nil {
			klog.Errorf("Error removing stale port groups %v: %v", stalePGs, err)
		} else {
			klog.V(5).Infof("Removed following stale port groups: %v", stalePGs)
		}
	}
}

func (oc *Controller) shouldApplyMultiPolicy(mpolicy *multinetworkpolicy.MultiNetworkPolicy) bool {
	policyForAnnot, ok := mpolicy.Annotations[PolicyForAnnotation]
	if !ok {
		// should never happen.
		return false
	}
	policyForAnnot = strings.ReplaceAll(policyForAnnot, " ", "")
	policyForNetworks := strings.Split(policyForAnnot, ",")
	for _, networkName := range policyForNetworks {
		networkNamespace := mpolicy.Namespace
		a := strings.Split(networkName, "/")
		if len(a) > 1 {
			networkName = a[1]
			networkNamespace = a[0]
		}
		if _, ok := oc.nadInfo.NetAttachDefs.Load(util.GetNadKeyName(networkNamespace, networkName)); ok {
			return true
		}
	}
	return false
}

func convertMultiNetPolicyToNetPolicy(mpolicy *multinetworkpolicy.MultiNetworkPolicy) *knet.NetworkPolicy {
	var policy knet.NetworkPolicy
	var ipb *knet.IPBlock

	policy.Name = mpolicy.Name
	policy.Namespace = mpolicy.Namespace
	policy.Spec.PodSelector = mpolicy.Spec.PodSelector
	policy.Annotations = mpolicy.Annotations
	policy.Spec.Ingress = make([]knet.NetworkPolicyIngressRule, len(mpolicy.Spec.Ingress))
	for i, mingress := range mpolicy.Spec.Ingress {
		var ingress knet.NetworkPolicyIngressRule
		ingress.Ports = make([]knet.NetworkPolicyPort, len(mingress.Ports))
		for j, mport := range mingress.Ports {
			ingress.Ports[j] = knet.NetworkPolicyPort{
				Protocol: mport.Protocol,
				Port:     mport.Port,
				EndPort:  mport.EndPort,
			}
		}
		ingress.From = make([]knet.NetworkPolicyPeer, len(mingress.From))
		for j, mfrom := range mingress.From {
			ipb = nil
			if mfrom.IPBlock != nil {
				ipb = &knet.IPBlock{CIDR: mfrom.IPBlock.CIDR, Except: mfrom.IPBlock.Except}
			}
			ingress.From[j] = knet.NetworkPolicyPeer{
				PodSelector:       mfrom.PodSelector,
				NamespaceSelector: mfrom.NamespaceSelector,
				IPBlock:           ipb,
			}
		}
		policy.Spec.Ingress[i] = ingress
	}
	policy.Spec.Egress = make([]knet.NetworkPolicyEgressRule, len(mpolicy.Spec.Egress))
	for i, megress := range mpolicy.Spec.Egress {
		var egress knet.NetworkPolicyEgressRule
		egress.Ports = make([]knet.NetworkPolicyPort, len(megress.Ports))
		for j, mport := range megress.Ports {
			egress.Ports[j] = knet.NetworkPolicyPort{
				Protocol: mport.Protocol,
				Port:     mport.Port,
				EndPort:  mport.EndPort,
			}
		}
		egress.To = make([]knet.NetworkPolicyPeer, len(megress.To))
		for j, mto := range megress.To {
			ipb = nil
			if mto.IPBlock != nil {
				ipb = &knet.IPBlock{CIDR: mto.IPBlock.CIDR, Except: mto.IPBlock.Except}
			}
			egress.To[j] = knet.NetworkPolicyPeer{
				PodSelector:       mto.PodSelector,
				NamespaceSelector: mto.NamespaceSelector,
				IPBlock:           ipb,
			}
		}
		policy.Spec.Egress[i] = egress
	}
	policy.Spec.PolicyTypes = make([]knet.PolicyType, len(mpolicy.Spec.PolicyTypes))
	for i, mpolicytype := range mpolicy.Spec.PolicyTypes {
		policy.Spec.PolicyTypes[i] = knet.PolicyType(mpolicytype)
	}
	return &policy
}

// addMultiNetworkPolicy creates and applies OVN ACLs to pod logical switch
// ports from Kubernetes NetworkPolicy objects using OVN Port Groups
func (oc *Controller) addMultiNetworkPolicy(mpolicy *multinetworkpolicy.MultiNetworkPolicy) {
	if !oc.shouldApplyMultiPolicy(mpolicy) {
		return
	}
	klog.Infof("[controller(%s)] adding multi network policy %s in namespace %s for networks %q",
		oc.nadInfo.NetName, mpolicy.Name, mpolicy.Namespace, mpolicy.Annotations[PolicyForAnnotation])
	policy := convertMultiNetPolicyToNetPolicy(mpolicy)
	oc.addNetworkPolicy(policy)
}

func (oc *Controller) deleteMultiNetworkPolicy(mpolicy *multinetworkpolicy.MultiNetworkPolicy) {
	if !oc.shouldApplyMultiPolicy(mpolicy) {
		return
	}
	klog.Infof("[controller(%s)] deleting multi network policy %s in namespace %s for networks %q",
		oc.nadInfo.NetName, mpolicy.Name, mpolicy.Namespace, mpolicy.Annotations[PolicyForAnnotation])
	oc.deleteNetworkPolicy(mpolicy.Name, mpolicy.Namespace)
}
