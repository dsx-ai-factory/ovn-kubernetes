package ovn

import (
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	adminpbrapi "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/adminpbr/v1beta1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdbops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	addressset "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/address_set"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

const (
	RoutePolicyPriorityNoRerouteJoinSubnet = 81
	RoutePolicyPriorityAdminPBR            = 80
	messagePrefixAddressSet                = "AddressSet"
	addressSetClusterSubnets               = "cluster_subnets"
)

type action string

const (
	actionAddAdminPBR        action = "add-adminpbr"
	actionUpdateAdminPBR     action = "update-adminpbr"
	actionAddPod4AdminPBR    action = "add-pod-for-adminpbr"
	actionUpdatePod4AdminPBR action = "update-pod-for-adminpbr"
)

// selectively match the labels for filtering the pods
const (
	APBR_MATCH_PODSEL = 1 << iota
	APBR_MATCH_NSSEL
	APBR_MATCH_NODESEL
	APBR_MATCH_ALL = (APBR_MATCH_PODSEL | APBR_MATCH_NSSEL | APBR_MATCH_NODESEL)
)

type retryRequest struct {
	action   action
	adminpbr *adminpbrapi.AdminPolicyBasedRoute
	policy   *internalAdminPBRPolicy
	pod      *corev1.Pod
}

type internalAdminPBRPolicy struct {
	sync.Mutex
	hash              string
	name              string
	controller        *Controller
	podHandler        *factory.Handler
	addressSet        addressset.AddressSet
	network           string
	nextHopIPs        []string
	podSelector       labels.Selector
	namespaceSelector labels.Selector
	nodeSelector      labels.Selector
	to                networkingv1.IPBlock
	logicalRouterName string
	addressSetErrors  map[string]string
}

func newInternalAdminPBRPolicy(controller *Controller, apbr *adminpbrapi.AdminPolicyBasedRoute, policy *adminpbrapi.RoutingPolicyRule) (*internalAdminPBRPolicy, error) {
	if util.IsEmptySelector(&policy.From.PodSelector) && util.IsEmptySelector(&policy.From.NamespaceSelector) && util.IsEmptySelector(&policy.From.NodeSelector) {
		return nil, fmt.Errorf("%s: at least 1 selector is required", apbr.Name)
	}
	pol := &internalAdminPBRPolicy{
		controller:        controller,
		name:              apbr.Name,
		network:           apbr.Spec.NetworkAttachmentName,
		nextHopIPs:        policy.NextHop.NextHopIPs,
		to:                policy.To,
		logicalRouterName: util.GetClusterScopedName(controller.nadInfo.Prefix + types.OVNClusterRouter),
		addressSetErrors:  make(map[string]string),
	}

	// parse pod selector
	if !util.IsEmptySelector(&policy.From.PodSelector) {
		podSelector, err := metav1.LabelSelectorAsSelector(&policy.From.PodSelector)
		if err != nil {
			return nil, fmt.Errorf("error parsing pod selector %s, %s: %v", apbr.Name, policy.From.PodSelector.String(), err)
		}
		pol.podSelector = podSelector
	}
	// parse namespace selector
	if !util.IsEmptySelector(&policy.From.NamespaceSelector) {
		nsSelector, err := metav1.LabelSelectorAsSelector(&policy.From.NamespaceSelector)
		if err != nil {
			return nil, fmt.Errorf("error parsing namespace selector %s, %s: %v", apbr.Name, policy.From.NamespaceSelector.String(), err)
		}
		pol.namespaceSelector = nsSelector
	}
	// parse node selector
	if !util.IsEmptySelector(&policy.From.NodeSelector) {
		nodeSelector, err := metav1.LabelSelectorAsSelector(&policy.From.NodeSelector)
		if err != nil {
			return nil, fmt.Errorf("error parsing node selector %s, %s: %v", apbr.Name, policy.From.NodeSelector.String(), err)
		}
		pol.nodeSelector = nodeSelector
	}
	return pol, nil
}

func (pol *internalAdminPBRPolicy) addressSetName() string {
	return fmt.Sprintf("%s-%s", pol.name, pol.hash)
}

func hashAdminPBRPolicy(networkName string, pol *adminpbrapi.RoutingPolicyRule) string {
	content := pol.From.PodSelector.String()
	content = fmt.Sprintf("%s-%s", content, pol.From.NamespaceSelector.String())
	content = fmt.Sprintf("%s-%s", content, pol.From.NodeSelector.String())
	content = fmt.Sprintf("%s-%s", content, pol.To.String())
	content = fmt.Sprintf("%s-%s", content, strings.Join(pol.NextHop.NextHopIPs, ","))
	content = fmt.Sprintf("%s-%s", content, networkName)
	return util.HashForOVN(content)
}

func (pol *internalAdminPBRPolicy) attachPodHandler() {
	if pol.podHandler != nil {
		return
	}
	pol.podHandler, _ = pol.controller.mc.watchFactory.AddHandlerWithFilterFunc(reflect.TypeOf(&corev1.Pod{}), pol.filterPodsByAllSelectors, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pol.addPodToAddressSet(obj)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			pol.addPodToAddressSet(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			pol.removePodFromAddressSet(obj)
		},
	}, nil)
}

func (pol *internalAdminPBRPolicy) detachPodHandler() {
	if pol.podHandler == nil {
		return
	}
	pol.controller.mc.watchFactory.RemovePodHandler(pol.podHandler)
	pol.podHandler = nil
}

func (pol *internalAdminPBRPolicy) filterPodsByAllSelectors(obj interface{}) bool {
	return pol.filterPodsByFlags(obj, APBR_MATCH_ALL)
}

func (pol *internalAdminPBRPolicy) filterPodsByFlags(obj interface{}, filterFlags int) bool {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return false
	}
	if pod.Spec.NodeName == "" || pod.Status.PodIP == "" || !util.PodWantsNetwork(pod) {
		// ignore
		return false
	}
	for _, flag := range []int{APBR_MATCH_PODSEL, APBR_MATCH_NSSEL, APBR_MATCH_NODESEL} {
		switch filterFlags & flag {
		case APBR_MATCH_PODSEL:
			if pol.podSelector != nil {
				if !pol.podSelector.Matches(labels.Set(pod.Labels)) {
					return false
				}
				klog.V(4).Infof("Pod %s/%s matches the pod selector of policy %s", pod.Namespace, pod.Name, pol.name)
			}
		case APBR_MATCH_NSSEL:
			if pol.namespaceSelector != nil {
				ns, err := pol.controller.mc.watchFactory.GetNamespace(pod.Namespace)
				if err != nil {
					klog.Errorf("Failed to get namespace %s: %v", pod.Namespace, err)
					return false
				}
				if !pol.namespaceSelector.Matches(labels.Set(ns.Labels)) {
					return false
				}
				klog.V(6).Infof("Pod %s/%s matches the namespace selector of policy %s", pod.Namespace, pod.Name, pol.name)
			}
		case APBR_MATCH_NODESEL:
			if pol.nodeSelector != nil {
				node, err := pol.controller.mc.watchFactory.GetNode(pod.Spec.NodeName)
				if err != nil {
					klog.Errorf("Failed to get node %s: %v", pod.Spec.NodeName, err)
					return false
				}
				if !pol.nodeSelector.Matches(labels.Set(node.Labels)) {
					return false
				}
				klog.V(6).Infof("Pod %s/%s matches the node selector of policy %s", pod.Namespace, pod.Name, pol.name)
			}
		}
	}
	return true
}

func (pol *internalAdminPBRPolicy) ensureAddressSet(oc *Controller) error {
	pol.Lock()
	defer pol.Unlock()
	if pol.addressSet != nil {
		return nil
	}
	if addressSet, err := oc.addressSetFactory.EnsureAddressSet(pol.addressSetName()); err != nil {
		return err
	} else {
		pol.addressSet = addressSet
	}
	return nil
}

func (pol *internalAdminPBRPolicy) deleteAddressSet() error {
	pol.Lock()
	defer pol.Unlock()
	if pol.addressSet == nil {
		return nil
	}
	if err := pol.addressSet.Destroy(); err != nil {
		return err
	}
	pol.addressSet = nil
	return nil
}

func (pol *internalAdminPBRPolicy) addPodToAddressSet(obj interface{}) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}
	if pod.Status.PodIP == "" || !pod.DeletionTimestamp.IsZero() || !util.PodWantsNetwork(pod) {
		klog.V(5).Infof("Skipping pod %s/%s for AdminPBR policy %s", pod.Namespace, pod.Name, pol.name)
		return
	}
	pol.Lock()
	defer pol.Unlock()
	if pol.addressSet == nil {
		return
	}
	// check if pod exists or not for retry path
	var err error
	if pod, err = pol.controller.mc.watchFactory.GetPod(pod.Namespace, pod.Name); err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Stop handling pod %s/%s as it does not exist", pod.Namespace, pod.Name)
		} else {
			klog.Errorf("Error retrieving pod %s/%s from cache: %v", pod.Namespace, pod.Name, err)
			pol.controller.requeuePod4AdminPBR(actionAddPod4AdminPBR, pol, pod)
			pol.addErrorMessage(pod, fmt.Sprintf("Error retrieving pod from cache: %v", err))
		}
		return
	}
	ipv4Set, _ := pol.addressSet.GetIPs()
	if util.ArrayHasString(ipv4Set, pod.Status.PodIP) {
		pol.clearErrorMessage(pod)
		return
	}
	if err := pol.addressSet.AddIPs([]net.IP{net.ParseIP(pod.Status.PodIP)}); err != nil {
		klog.Errorf("Failed to update address set %s for policy %s: %v", pol.addressSetName(), pol.name, err)
		pol.controller.requeuePod4AdminPBR(actionAddPod4AdminPBR, pol, pod)
		pol.addErrorMessage(pod, fmt.Sprintf("Failed to add pod to address set: %v", err))
		return
	}
	pol.clearErrorMessage(pod)
	klog.V(5).Infof("Successfully added %s to address set %s", pod.Status.PodIP, pol.addressSetName())
}

func (pol *internalAdminPBRPolicy) removePodFromAddressSet(obj interface{}) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}
	if pod.Status.PodIP == "" {
		klog.V(5).Infof("%s/%s: pod IP gone, ignore", pod.Namespace, pod.Name)
		return
	}
	pol.Lock()
	defer pol.Unlock()
	if pol.addressSet == nil {
		pol.clearErrorMessage(pod)
		return
	}
	ipv4Set, _ := pol.addressSet.GetIPs()
	if !util.ArrayHasString(ipv4Set, pod.Status.PodIP) {
		pol.clearErrorMessage(pod)
		return
	}
	if err := pol.addressSet.DeleteIPs([]net.IP{net.ParseIP(pod.Status.PodIP)}); err != nil {
		klog.Errorf("Failed to remove %s from address set %s for policy %s: %v", pod.Status.PodIP, pol.addressSetName(), pol.name, err)
		pol.addErrorMessage(pod, fmt.Sprintf("Failed to remove pod from address set: %v", err))
		return
	}
	pol.clearErrorMessage(pod)
	klog.V(5).Infof("Successfully removed %s from address set %s", pod.Status.PodIP, pol.addressSetName())
}

func (pol *internalAdminPBRPolicy) syncPods(objects []interface{}, filterFlags int) {
	// add qualified existing pods to address set
	for _, obj := range objects {
		if !pol.filterPodsByFlags(obj, filterFlags) {
			continue
		}
		pol.addPodToAddressSet(obj)
	}
}

func (pol *internalAdminPBRPolicy) disqualifyPods(objects []interface{}) {
	// remove disqualified pods from address set
	for _, obj := range objects {
		pol.removePodFromAddressSet(obj)
	}
}

func (pol *internalAdminPBRPolicy) getMatchExpression() string {
	source, _ := pol.addressSet.GetASHashNames()
	match := fmt.Sprintf("ip4.src == {$%s}", source)
	if len(pol.to.Except) == 0 {
		match = fmt.Sprintf("%s && ip4.dst == %s", match, pol.to.CIDR)
	} else {
		match = fmt.Sprintf("%s && ip4.dst == %s && ip4.dst != {%s}", match, pol.to.CIDR, strings.Join(pol.to.Except, ", "))
	}
	return match
}

func (pol *internalAdminPBRPolicy) addErrorMessage(pod *corev1.Pod, message string) {
	pol.addressSetErrors[util.NamespacedName(pod)] = message
	pol.updateAddressSetStatus(types.OvnK8sStatusFailed)
}

func (pol *internalAdminPBRPolicy) clearErrorMessage(pod *corev1.Pod) {
	if _, ok := pol.addressSetErrors[util.NamespacedName(pod)]; !ok {
		return
	}
	delete(pol.addressSetErrors, util.NamespacedName(pod))
	pol.updateAddressSetStatus(types.OvnK8sStatusSucceeded)
}

func (pol *internalAdminPBRPolicy) updateAddressSetStatus(status types.OvnK8sStatus) {
	adminpbr, err := pol.controller.mc.watchFactory.GetAdminPBR(pol.name)
	if err != nil {
		if !errors.IsNotFound(err) {
			klog.Errorf("Error retrieving adminpbr %s from cache: %v", pol.name, err)
		}
		return
	}
	if err := pol.controller.updateAdminPBRStatus(adminpbr, "", status); err != nil {
		klog.Errorf("Error updating adminpbr %s status: %s", adminpbr.Name, err)
	}
}

func (oc *Controller) onAdminPBRAddOrUpdate(adminpbr *adminpbrapi.AdminPolicyBasedRoute) {
	if adminpbr == nil {
		return
	}
	klog.V(5).Infof("Receiving event for adminpbr: %v", adminpbr)
	unlock := util.LockByKey.Acquire(lockNameOfAdminPBR(adminpbr.Name))
	defer unlock()
	// check topo type
	if oc.nadInfo.TopoType != types.Layer3AttachDefTopoType {
		setErrorForAdminPBR(oc, adminpbr, fmt.Sprintf("Skipping AdminPBR %s since the network topology of %s is not L3", adminpbr.Name, oc.nadInfo.NetName))
		return
	}
	// check if adminpbr exists or not for retry path
	var err error
	if adminpbr, err = oc.mc.watchFactory.GetAdminPBR(adminpbr.Name); err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Stop handling adminpbr %s as it does not exist", adminpbr.Name)
		} else {
			klog.Errorf("Error retrieving adminpbr %s from cache: %v", adminpbr.Name, err)
			oc.requeueAdminPBR(actionAddAdminPBR, adminpbr)
		}
		return
	}
	policies := map[string]*internalAdminPBRPolicy{}
	val, ok := oc.adminPBRStore.Load(util.NamespacedName(adminpbr))
	if ok {
		policies = val.(map[string]*internalAdminPBRPolicy)
	}
	stalePolicyHashes := make(map[string]bool)
	for hash := range policies {
		stalePolicyHashes[hash] = true
	}
	for index, policy := range adminpbr.Spec.Policies {
		var internalPol *internalAdminPBRPolicy
		hash := hashAdminPBRPolicy(adminpbr.Spec.NetworkAttachmentName, &policy)
		if _, ok := policies[hash]; ok {
			// found existing rule, remove it from stale map
			delete(stalePolicyHashes, hash)
			continue
		}
		internalPol, err := newInternalAdminPBRPolicy(oc, adminpbr, &policy)
		if err != nil {
			setErrorForAdminPBR(oc, adminpbr, fmt.Sprintf("Failed parsing policy at #%d: %v", index, err))
			return
		}
		internalPol.hash = hash

		// create address set
		if err := internalPol.ensureAddressSet(oc); err != nil {
			msg := fmt.Sprintf("Error creating address set %s: %v", internalPol.addressSetName(), err)
			setErrorForAdminPBR(oc, adminpbr, msg)
			oc.requeueAdminPBR(actionAddAdminPBR, adminpbr)
			return
		}
		oc.mc.recorder.Eventf(adminpbr, corev1.EventTypeNormal, "AddressSet", "address set created for rule #%d", index)
		// add logical router policy
		if err := oc.applyAdminPBR(adminpbr, internalPol); err != nil {
			setErrorForAdminPBR(oc, adminpbr, fmt.Sprintf("Error saving ovn route policy %s: %v", adminpbr.Name, err))
			oc.requeueAdminPBR(actionAddAdminPBR, adminpbr)
			return
		}
		oc.mc.recorder.Eventf(adminpbr, corev1.EventTypeNormal, "LogicalRoutePolicy", "logical route policy created for rule #%d", index)
		// set up handlers
		internalPol.attachPodHandler()
		policies[internalPol.hash] = internalPol
	}
	// clean up stale rules
	oc.cleanupStalePolicies(adminpbr, policies, stalePolicyHashes)
	oc.adminPBRStore.Store(util.NamespacedName(adminpbr), policies)
	if err := oc.updateAdminPBRStatus(adminpbr, "Route policy applied in OVN", types.OvnK8sStatusSucceeded); err != nil {
		klog.Errorf("Failed to update adminpbr %s: %v", adminpbr.Name, err)
	}
}

func (oc *Controller) onAdminPBRDelete(apbr *adminpbrapi.AdminPolicyBasedRoute) {
	unlock := util.LockByKey.Acquire(lockNameOfAdminPBR(apbr.Name))
	defer unlock()
	policies := map[string]*internalAdminPBRPolicy{}
	if val, _ := oc.adminPBRStore.LoadAndDelete(util.NamespacedName(apbr)); val != nil {
		policies = val.(map[string]*internalAdminPBRPolicy)
	}
	for _, policy := range policies {
		if err := oc.cleanupLogicalRouterPolicy(apbr.Name, policy); err != nil {
			klog.Error(err)
		}
	}
}

func (oc *Controller) applyAdminPBR(adminpbr *adminpbrapi.AdminPolicyBasedRoute, policy *internalAdminPBRPolicy) error {
	ownerVal := k8stypes.NamespacedName{Name: adminpbr.Name}
	match := policy.getMatchExpression()
	lrp := nbdb.LogicalRouterPolicy{
		Priority: RoutePolicyPriorityAdminPBR,
		Match:    match,
		Nexthops: policy.nextHopIPs,
		Action:   nbdb.LogicalRouterPolicyActionReroute,
	}
	// add external IDs
	lrp.ExternalIDs = util.ExternalIDsForCluster(map[string]string{
		types.ExternalIDK8sOwner:     ownerVal.String(),
		types.OvnK8sPrefix + "/kind": util.GroupKindOf(adminpbr),
		types.ExternalIDHash:         policy.hash,
		types.ExternalIDNetAttachDef: adminpbr.Spec.NetworkAttachmentName,
		types.ExternalIDRouter:       policy.logicalRouterName,
	})

	p := func(item *nbdb.LogicalRouterPolicy) bool {
		return item.Priority == RoutePolicyPriorityAdminPBR && item.Match == match &&
			item.ExternalIDs[types.ExternalIDK8sOwner] == ownerVal.String() &&
			item.ExternalIDs[types.ExternalIDNetAttachDef] == adminpbr.Spec.NetworkAttachmentName &&
			util.HasExternalIDsForCluster(item.ExternalIDs)
	}

	err := libovsdbops.CreateOrUpdateLogicalRouterPolicyWithPredicate(oc.mc.nbClient, policy.logicalRouterName, &lrp, p,
		&lrp.Nexthops, &lrp.Match, &lrp.Action, &lrp.Priority, &lrp.ExternalIDs)
	if err != nil {
		return fmt.Errorf("unable to apply adminpbr %s, err: %v", ownerVal.String(), err)
	}
	return nil
}

func (oc *Controller) updateAdminPBRStatus(adminpbr *adminpbrapi.AdminPolicyBasedRoute, message string, status types.OvnK8sStatus) error {
	update := adminpbr.DeepCopy()
	errors := oc.getAddressSetErrors(adminpbr)
	if message != "" {
		update.Status.Messages = append(errors, message)
	} else {
		generalMessages := []string{}
		for _, msg := range adminpbr.Status.Messages {
			if strings.HasPrefix(msg, messagePrefixAddressSet) {
				continue
			}
			generalMessages = append(generalMessages, msg)
		}
		update.Status.Messages = append(errors, generalMessages...)
	}
	if len(errors) > 0 {
		update.Status.Status = types.OvnK8sStatusFailed
	} else {
		update.Status.Status = status
	}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		_, err := oc.mc.kube.UpdateAdminPBRStatus(update)
		return err
	}); err != nil {
		return err
	}
	return nil
}

func (oc *Controller) cleanupStalePolicies(apbr *adminpbrapi.AdminPolicyBasedRoute, allPolicies map[string]*internalAdminPBRPolicy, stalePolicies map[string]bool) {
	// get existing policies from ovn by apbr name
	for hash := range stalePolicies {
		policy := allPolicies[hash]
		klog.V(4).Infof("Cleaning up stale rule %s", policy.hash)
		delete(allPolicies, hash)
		if err := oc.cleanupLogicalRouterPolicy(apbr.Name, policy); err != nil {
			klog.Error(err)
		}
	}
}

// check if node's change qualifies/disqualifies the route policies, add/remove ovn records accordingly
func (oc *Controller) syncAdminPBROnNodeChange(old, new interface{}) {
	newNode, ok := new.(*corev1.Node)
	if !ok {
		klog.Errorf("Not a node: %v", new)
		return
	}
	nodeIP := ""
	for _, addr := range newNode.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			nodeIP = addr.Address
			break
		}
	}
	if nodeIP == "" || !newNode.DeletionTimestamp.IsZero() {
		return
	}
	var oldNode *corev1.Node
	if old != nil {
		oldNode, ok = old.(*corev1.Node)
		if !ok {
			klog.Errorf("Not a node: %v", old)
			return
		}
	}
	if oldNode != nil && reflect.DeepEqual(oldNode.Labels, newNode.Labels) {
		return
	}
	podIndexer := oc.mc.watchFactory.PodInformer().GetIndexer()
	pods, err := podIndexer.ByIndex(types.CacheIndexPodByNodeIP, nodeIP)
	if err != nil {
		klog.Errorf("Failed to get pods by node ip %s: %v", nodeIP, err)
		return
	}
	oc.adminPBRStore.Range(func(key interface{}, value interface{}) bool {
		policyMap := value.(map[string]*internalAdminPBRPolicy)
		for _, policy := range policyMap {
			if policy.nodeSelector == nil {
				continue
			}
			if oldNode == nil {
				if policy.nodeSelector.Matches(labels.Set(newNode.Labels)) {
					// node matches policy's nodeSelector
					policy.syncPods(pods, APBR_MATCH_PODSEL|APBR_MATCH_NSSEL)
				}
			} else {
				oldNodeMatchPolicyLabels := policy.nodeSelector.Matches(labels.Set(oldNode.Labels))
				newNodeMatchPolicyLabels := policy.nodeSelector.Matches(labels.Set(newNode.Labels))
				if oldNodeMatchPolicyLabels && !newNodeMatchPolicyLabels {
					// node doesn't match the policy's nodeSelector anymore
					policy.disqualifyPods(pods)
				} else if !oldNodeMatchPolicyLabels && newNodeMatchPolicyLabels {
					// node now matches policy's nodeSelector
					policy.syncPods(pods, APBR_MATCH_PODSEL|APBR_MATCH_NSSEL)
				}
			}
		}
		return true
	})
}

func (oc *Controller) syncAdminPBROnNamespaceChange(old, new interface{}) {
	newNs, ok := new.(*corev1.Namespace)
	if !ok {
		klog.Errorf("Not a namespace: %v", new)
		return
	}
	if !newNs.DeletionTimestamp.IsZero() {
		return
	}
	var oldNs *corev1.Namespace
	if old != nil {
		oldNs, ok = old.(*corev1.Namespace)
		if !ok {
			klog.Errorf("Not a namespace: %v", old)
			return
		}
	}
	if oldNs != nil && reflect.DeepEqual(oldNs.Labels, newNs.Labels) {
		return
	}
	podIndexer := oc.mc.watchFactory.PodInformer().GetIndexer()
	pods, err := podIndexer.ByIndex(types.CacheIndexPodByNamespace, newNs.Name)
	if err != nil {
		klog.Errorf("Failed to get pods by namespace %s: %v", newNs.Name, err)
		return
	}
	oc.adminPBRStore.Range(func(key interface{}, value interface{}) bool {
		policyMap := value.(map[string]*internalAdminPBRPolicy)
		for _, policy := range policyMap {
			if policy.namespaceSelector == nil {
				continue
			}
			if oldNs == nil {
				if policy.namespaceSelector.Matches(labels.Set(newNs.Labels)) {
					// new namespace matches selector
					policy.syncPods(pods, APBR_MATCH_PODSEL|APBR_MATCH_NODESEL)
				}
			} else {
				oldNsMatchPolicyLabels := policy.namespaceSelector.Matches(labels.Set(oldNs.Labels))
				newNsMatchPolicyLabels := policy.namespaceSelector.Matches(labels.Set(newNs.Labels))
				if oldNsMatchPolicyLabels && !newNsMatchPolicyLabels {
					// namespace doesn't match policy's nodeSelector anymore
					policy.disqualifyPods(pods)
				} else if !oldNsMatchPolicyLabels && newNsMatchPolicyLabels {
					// namespace now matches policy's namespaceSelector
					policy.syncPods(pods, APBR_MATCH_PODSEL|APBR_MATCH_NODESEL)
				}
			}
		}
		return true
	})
}

// delete ovn route policy if owning k8s object doesn't exist
func (oc *Controller) syncAdminPBRPeriodic() {
	klog.V(4).Infof("Start adminpbr sync for network %s", oc.nadInfo.NetName)
	// get all adminpbr policies from ovn
	ovnPolicies, err := oc.findPolicyBasedRoutes(strconv.Itoa(RoutePolicyPriorityAdminPBR))
	if err != nil {
		klog.Errorf("[%s] Failed to retrieve logical router policies from OVN: %v", oc.nadInfo.NetName, err)
		return
	}
	// group ovn policies by adminpbr name to avoid interleaving
	policyMapByAdminPBR := make(map[string][]*nbdb.LogicalRouterPolicy)
	for index := range ovnPolicies {
		policy := ovnPolicies[index]
		network := policy.ExternalIDs[types.ExternalIDNetAttachDef]
		if _, found := oc.nadInfo.NetAttachDefs.Load(network); !found {
			// not managed by this controller
			continue
		}
		owner := policy.ExternalIDs[types.ExternalIDK8sOwner]
		if owner == "" {
			continue
		}
		policies, ok := policyMapByAdminPBR[owner]
		if !ok {
			policies = []*nbdb.LogicalRouterPolicy{}
		}
		policies = append(policies, policy)
		policyMapByAdminPBR[owner] = policies
	}
	// check if a logical router policy in ovn has corresponding k8s adminpbr object or not
	// if not, the logical router policy will be considered stale and need cleanup
	for key, ovnPolicies := range policyMapByAdminPBR {
		apbrName := key[1:]                                            // remove leading "/"
		unlock := util.LockByKey.Acquire(lockNameOfAdminPBR(apbrName)) // acquire lock to avoid racing against handler
		adminpbr, err := oc.mc.watchFactory.GetAdminPBR(apbrName)
		if err != nil && !errors.IsNotFound(err) {
			// error happened, skip this round
			klog.Error("Failed to get adminpbr %s: %v", apbrName, err)
			unlock()
			continue
		}
		pbrNotFoundInK8s := true
		if adminpbr != nil && adminpbr.DeletionTimestamp.IsZero() {
			pbrNotFoundInK8s = false // adminpbr found in api server
		}
		for _, ovnPolicy := range ovnPolicies {
			hash := ovnPolicy.ExternalIDs[types.ExternalIDHash]
			var hashFoundInController bool
			value, _ := oc.adminPBRStore.Load(k8stypes.NamespacedName{Name: apbrName}.String())
			if value == nil {
				hashFoundInController = false
			} else {
				policyMap := value.(map[string]*internalAdminPBRPolicy)
				_, hashFoundInController = policyMap[hash]
			}
			klog.V(5).Infof("[%s] hash=%s, adminpbr not found: %v, hash found: %v", oc.nadInfo.NetName, hash, pbrNotFoundInK8s, hashFoundInController)
			if pbrNotFoundInK8s || !hashFoundInController {
				klog.V(4).Infof("[%s] Deleting stale logical router policy %s (%s)", oc.nadInfo.NetName, apbrName, hash)
				if err := oc.cleanupLogicalRouterPolicy(apbrName, internalPolicyOf(ovnPolicy)); err != nil {
					klog.Errorf("[%s] Failed to clean up stale logical router policy %s (%s): %v", oc.nadInfo.NetName, apbrName, hash, err)
				}
			}
		}
		unlock()
	}
	klog.V(4).Infof("Adminpbr sync completed for network %s", oc.nadInfo.NetName)
}

// if address set's member IP is not in k8s anymore, delete it from the set
func (oc *Controller) syncAddressSetPeriodic() {
	klog.V(4).Infof("Cleaning up IPs from all AdminPBR's address set for network %s", oc.nadInfo.NetName)
	oc.adminPBRStore.Range(func(key interface{}, value interface{}) bool {
		policyMap := value.(map[string]*internalAdminPBRPolicy)
		podIndexer := oc.mc.watchFactory.PodInformer().GetIndexer()
		for _, pol := range policyMap {
			pol.Lock()
			v4IPs, _ := pol.addressSet.GetIPs()
			stalev4IPs := make([]net.IP, 0)
			for _, ip := range v4IPs {
				// get pod by ip
				pods, err := podIndexer.ByIndex(types.CacheIndexPodByIP, ip)
				if err != nil {
					continue
				}
				// if pod doesn't exist or pod not qualified for the rule, remove ip from address set
				if len(pods) == 0 {
					stalev4IPs = append(stalev4IPs, net.ParseIP(ip))
				} else if !pol.filterPodsByAllSelectors(pods[0]) {
					stalev4IPs = append(stalev4IPs, net.ParseIP(ip))
				}
			}
			if len(stalev4IPs) != 0 {
				klog.V(4).Infof("[%s] Removing stale IPs (%s) from address set for policy %s", oc.nadInfo.NetName, stalev4IPs, pol.name)
				if err := pol.addressSet.DeleteIPs(stalev4IPs); err != nil {
					klog.V(4).Infof("[%s] Failed removing stale IPs for policy %s", oc.nadInfo.NetName, pol.name)
				}
			}
			pol.Unlock()
		}
		return true
	})
	klog.V(4).Infof("Address set cleanup completed for network %s", oc.nadInfo.NetName)
}

func setErrorForAdminPBR(oc *Controller, adminpbr *adminpbrapi.AdminPolicyBasedRoute, msg string) {
	klog.Errorf(msg)
	if err := oc.updateAdminPBRStatus(adminpbr, msg, types.OvnK8sStatusFailed); err != nil {
		klog.Errorf("Failed to set error for adminpbr %s: %v", adminpbr.Name, err)
	}
}

func (oc *Controller) retryAdminPBROperations() bool {
	item, quit := oc.adminPBRRetryQueue.Get()
	if quit {
		return false
	}
	oc.adminPBRRetryQueue.Done(item)
	retry, ok := item.(*retryRequest)
	if !ok {
		return true
	}
	klog.V(4).Infof("Retrying event: %v", retry)
	switch retry.action {
	case actionAddAdminPBR, actionUpdateAdminPBR:
		oc.onAdminPBRAddOrUpdate(retry.adminpbr)
	case actionAddPod4AdminPBR, actionUpdatePod4AdminPBR:
		// TODO: perhaps we need to get the retry.pod from the informer.
		if retry.policy.filterPodsByAllSelectors(retry.pod) {
			retry.policy.addPodToAddressSet(retry.pod)
		}
		// no retry for pod deletion
	}
	return true
}

func (oc *Controller) requeueAdminPBR(ra action, adminpbr *adminpbrapi.AdminPolicyBasedRoute) {
	if adminpbr == nil {
		klog.Errorf("Missing argument for retry")
		return
	}
	req := &retryRequest{
		action:   ra,
		adminpbr: adminpbr,
	}
	klog.V(4).Infof("Requeue adminpbr event to retry: %v", req)
	oc.adminPBRRetryQueue.AddAfter(req, time.Duration(3*time.Second))
}

func (oc *Controller) requeuePod4AdminPBR(ra action, policy *internalAdminPBRPolicy, pod *corev1.Pod) {
	if policy == nil || pod == nil {
		klog.Errorf("Missing argument for retry")
		return
	}
	req := &retryRequest{
		action: ra,
		policy: policy,
		pod:    pod,
	}
	klog.V(4).Infof("Requeue pod event for adminpbr to retry: %v", req)
	oc.adminPBRRetryQueue.AddAfter(req, time.Duration(3*time.Second))
}

func (oc *Controller) getAddressSetErrors(adminpbr *adminpbrapi.AdminPolicyBasedRoute) []string {
	policies := map[string]*internalAdminPBRPolicy{}
	if val, _ := oc.adminPBRStore.Load(util.NamespacedName(adminpbr)); val != nil {
		policies = val.(map[string]*internalAdminPBRPolicy)
	}
	errors := []string{}
	for _, policy := range policies {
		for pod, msg := range policy.addressSetErrors {
			errors = append(errors, fmt.Sprintf("%s - %s (%s)", messagePrefixAddressSet, msg, pod))
		}
	}
	return errors
}

func (oc *Controller) cleanupLogicalRouterPolicy(adminPBRName string, policy *internalAdminPBRPolicy) error {
	policy.detachPodHandler()
	prefixedAdminPBRName := adminPBRName
	if !strings.HasPrefix(adminPBRName, "/") {
		prefixedAdminPBRName = k8stypes.NamespacedName{Name: adminPBRName}.String()
	}
	if val, _ := oc.adminPBRStore.Load(prefixedAdminPBRName); val != nil {
		// adminpbr with same name created again
		policies := val.(map[string]*internalAdminPBRPolicy)
		if _, found := policies[policy.hash]; found {
			// new policy has the same rule, skip ovn cleanup
			return nil
		}
		// although adminpbr spec has the same name, rule is different, continue ovn cleanup
	}
	// remove logical route policy
	if err := libovsdbops.DeleteLogicalRouterPoliciesWithPredicate(oc.mc.nbClient, policy.logicalRouterName, func(item *nbdb.LogicalRouterPolicy) bool {
		return item.Priority == RoutePolicyPriorityAdminPBR &&
			item.ExternalIDs[types.ExternalIDK8sOwner] == prefixedAdminPBRName &&
			item.ExternalIDs[types.ExternalIDHash] == policy.hash &&
			util.HasExternalIDsForCluster(item.ExternalIDs)
	}); err != nil {
		return fmt.Errorf("failed to delete adminpbr %s, err: %v", prefixedAdminPBRName, err)
	}
	// remove address set
	if err := policy.deleteAddressSet(); err != nil {
		return fmt.Errorf("failed to delete address set %s, err: %v", policy.addressSetName(), err)
	}
	return nil
}

func (oc *Controller) deleteLogicalRouterPoliciesByPriority(priority int) error {
	return libovsdbops.DeleteLogicalRouterPoliciesWithPredicate(oc.mc.nbClient, util.GetClusterScopedName(types.OVNClusterRouter), func(item *nbdb.LogicalRouterPolicy) bool {
		return item.Priority == priority && util.HasExternalIDsForCluster(item.ExternalIDs)
	})
}

// join subnet is used by OVN for its internal purposes and packets destined to it
// should be kept within the cluster and shouldn't be subjected to AdminPBR rules
// and forwarded to Internet.
func (oc *Controller) noRerouteToJoinSubnet() error {
	// ensure that cluster subnets are in address set
	addrSet, err := oc.addressSetFactory.EnsureAddressSet(addressSetClusterSubnets)
	if err != nil {
		return fmt.Errorf("failed to create address set for cluster subnets: %v", err)
	}
	clusterSubnets := make([]*net.IPNet, 0, len(config.Default.ClusterSubnets))
	for _, clusterEntry := range config.Default.ClusterSubnets {
		clusterSubnets = append(clusterSubnets, clusterEntry.CIDR)
	}
	if err := addrSet.AddSubnets(clusterSubnets); err != nil {
		return err
	}
	// create or update logical router policy
	v4AddrSetName, _ := addrSet.GetASHashNames()
	match := fmt.Sprintf("ip4.src == {$%s} && ip4.dst == %s ", v4AddrSetName, config.Gateway.V4JoinSubnet)
	lrp := nbdb.LogicalRouterPolicy{
		Priority:    RoutePolicyPriorityNoRerouteJoinSubnet,
		Match:       match,
		Action:      nbdb.LogicalRouterPolicyActionAllow,
		ExternalIDs: util.CreateClusterScopedExternalIDs(),
	}
	p := func(item *nbdb.LogicalRouterPolicy) bool {
		return item.Priority == RoutePolicyPriorityNoRerouteJoinSubnet && item.Match == match &&
			util.HasExternalIDsForCluster(item.ExternalIDs)
	}

	if err = libovsdbops.CreateOrUpdateLogicalRouterPolicyWithPredicate(oc.mc.nbClient,
		util.GetClusterScopedName(types.OVNClusterRouter), &lrp, p,
		&lrp.Match, &lrp.Action, &lrp.Priority, &lrp.ExternalIDs); err != nil {
		return fmt.Errorf("unable to add logical router policy for join subnet %s: %v", config.Gateway.V4JoinSubnet, err)
	}
	return nil
}

func internalPolicyOf(ovnpbr *nbdb.LogicalRouterPolicy) *internalAdminPBRPolicy {
	return &internalAdminPBRPolicy{
		name:              ovnpbr.ExternalIDs[types.ExternalIDK8sOwner],
		hash:              ovnpbr.ExternalIDs[types.ExternalIDHash],
		logicalRouterName: ovnpbr.ExternalIDs[types.ExternalIDRouter],
		network:           ovnpbr.ExternalIDs[types.ExternalIDNetAttachDef],
	}
}

func lockNameOfAdminPBR(name string) string {
	return fmt.Sprintf("adminpbr/%s", name)
}
