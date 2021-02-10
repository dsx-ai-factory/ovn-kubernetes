package ovn

import (
	"fmt"
	"strings"

	multinetworkpolicy "github.com/k8snetworkplumbingwg/multi-networkpolicy/pkg/apis/k8s.cni.cncf.io/v1beta1"
	knet "k8s.io/api/networking/v1"
	"k8s.io/klog/v2"
)

const PolicyNetworkAnnotation = "k8s.v1.cni.cncf.io/policy-for"

func (oc *Controller) syncMultiNetworkPolicies(multiPolicies []interface{}) {
	expectedPolicies := make(map[string]map[string]bool)
	for _, npInterface := range multiPolicies {
		policy, ok := npInterface.(*multinetworkpolicy.MultiNetworkPolicy)
		if !ok {
			klog.Errorf("Spurious object in syncMultiNetworkPolicies: %v",
				npInterface)
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

	err := oc.addressSetFactory.ProcessEachAddressSet(func(addrSetName, namespaceName, policyName string, icmpAddressSet bool) {
		if !icmpAddressSet && policyName != "" && !expectedPolicies[namespaceName][policyName] {
			// policy doesn't exist on k8s. Delete the port group
			portGroupName := fmt.Sprintf("%s_%s", namespaceName, policyName)
			hashedLocalPortGroup := hashedPortGroup(portGroupName)
			err := deletePortGroup(oc.mc.ovnNBClient, hashedLocalPortGroup, oc.nadInfo.NetNameInfo)
			if err != nil {
				klog.Errorf("%v", err)
			}

			// delete the address sets for this old policy from OVN
			if err = oc.addressSetFactory.DestroyAddressSetInBackingStore(addrSetName); err != nil {
				klog.Errorf(err.Error())
			}
		}
	})
	if err != nil {
		klog.Errorf("Error in syncing multi network policies: %v", err)
	}
}

func multiNetworkPolicy2NetworkPolicy(mpolicy *multinetworkpolicy.MultiNetworkPolicy) *knet.NetworkPolicy {
	var policy knet.NetworkPolicy
	var ipb *knet.IPBlock

	policy.Name = mpolicy.Name
	policy.Namespace = mpolicy.Namespace

	policy.Spec.PodSelector = mpolicy.Spec.PodSelector
	policy.Spec.Ingress = make([]knet.NetworkPolicyIngressRule, len(mpolicy.Spec.Ingress))
	for i, mingress := range mpolicy.Spec.Ingress {
		var ingress knet.NetworkPolicyIngressRule
		ingress.Ports = make([]knet.NetworkPolicyPort, len(mingress.Ports))
		for j, mport := range mingress.Ports {
			ingress.Ports[j] = knet.NetworkPolicyPort{
				Protocol: mport.Protocol,
				Port:     mport.Port,
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
	klog.Infof("Adding multi network policy %s in namespace %s: %v", mpolicy.Name, mpolicy.Namespace, mpolicy)

	policyNetworksAnnot, ok := mpolicy.Annotations[PolicyNetworkAnnotation]
	if !ok {
		return
	}
	policyNetworksAnnot = strings.ReplaceAll(policyNetworksAnnot, " ", "")
	policyNetworks := strings.Split(policyNetworksAnnot, ",")
	found := false
	for _, networkName := range policyNetworks {
		expectedNamespace := mpolicy.Namespace
		a := strings.Split(networkName, "/")
		if len(a) > 1 {
			networkName = a[1]
			expectedNamespace = a[0]
		}
		if _, ok := oc.nadInfo.NetAttachDefs.Load(expectedNamespace + "_" + networkName); ok {
			found = true
			break
		}
	}
	if !found {
		return
	}

	policy := multiNetworkPolicy2NetworkPolicy(mpolicy)
	klog.Infof("Adding multi network policy :%v", policy)
	oc.addNetworkPolicy(policy)
}

func (oc *Controller) deleteMultiNetworkPolicy(mpolicy *multinetworkpolicy.MultiNetworkPolicy) {
	klog.Infof("Deleting multi network policy %s in namespace %s: %v",
		mpolicy.Name, mpolicy.Namespace, mpolicy)

	policyNetworksAnnot, ok := mpolicy.Annotations[PolicyNetworkAnnotation]
	if !ok {
		return
	}
	policyNetworksAnnot = strings.ReplaceAll(policyNetworksAnnot, " ", "")
	policyNetworks := strings.Split(policyNetworksAnnot, ",")
	found := false
	for _, networkName := range policyNetworks {
		expectedNamespace := mpolicy.Namespace
		a := strings.Split(networkName, "/")
		if len(a) > 1 {
			networkName = a[1]
			expectedNamespace = a[0]
		}
		if _, ok := oc.nadInfo.NetAttachDefs.Load(expectedNamespace + "_" + networkName); ok {
			found = true
			break
		}
	}
	if !found {
		return
	}

	policy := multiNetworkPolicy2NetworkPolicy(mpolicy)
	klog.Infof("Deleting multi network policy :%v", policy)
	oc.deleteNetworkPolicy(policy)
}
