package ovn

import (
	"errors"
	"fmt"
	"time"

	kapi "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"

	libovsdbclient "github.com/ovn-org/libovsdb/client"
	portmirror "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/portmirror/v1beta1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	libovsdbops "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdb/ops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"k8s.io/klog/v2"
)

const (
	actionAddPortMirror    action = "add-portmirror"
	actionAddPortMirrorPod action = "add-pod-for-portmirror"
)

type portMirrorRetryRequest struct {
	action     action
	portMirror *portmirror.PortMirror
	pm         *util.PortMirror
	pod        *kapi.Pod
}

// deleteMirrorFromPod deletes the mirrorUUID form pod LSP
func (bnc *BaseNetworkController) deleteMirrorFromPod(portMirrorUUID, portName string) error {
	pmLogicalPortNameUnlock := util.GetLockByPMLogicalPortName(portName)
	defer pmLogicalPortNameUnlock()

	lsp := &nbdb.LogicalSwitchPort{Name: portName}
	podLSP, err := libovsdbops.GetLogicalSwitchPort(bnc.nbClient, lsp)
	if err != nil {
		if errors.Is(err, libovsdbclient.ErrNotFound) {
			// if we don't find the lsp it means it might have been deleted before so just return
			return nil
		}
		return fmt.Errorf("failed to get logical switch port %s info from NB DB (%v)", portName, err)
	}

	mirrorRuleList := make([]string, 0)
	for _, rule := range podLSP.MirrorRules {
		if rule == portMirrorUUID {
			continue
		}
		mirrorRuleList = append(mirrorRuleList, rule)
	}

	podLSP.MirrorRules = mirrorRuleList
	ops, err := bnc.nbClient.Where(podLSP).Update(podLSP, &podLSP.MirrorRules)
	if err != nil {
		return fmt.Errorf("could not create commands to update logical switch %s port mirror rules - %+v", portName, err)
	}

	_, err = libovsdbops.TransactAndCheckAndSetUUIDs(bnc.nbClient, podLSP, ops)
	if err != nil {
		return fmt.Errorf("could not perform update of logical switch port %s for port mirror rules - %+v", portName, err)
	}
	return nil
}

func (bnc *BaseNetworkController) handlePortMirrorSourcePodDelete(pm *util.PortMirror, pod *kapi.Pod) error {
	// portMirrorNameUnlock is used to serialize the portmirror status update
	portMirrorNameUnlock := util.GetLockByPMName(pm.Namespace, pm.Name)
	defer portMirrorNameUnlock()

	sourcePodKey := util.GetNamespacedName(pod.Namespace, pod.Name)

	// check if this pod is in PodRetry map that consists of pod's
	// whose networking isn't set yet.
	if _, ok := pm.SourceDetails.PodRetry.LoadAndDelete(sourcePodKey); ok {
		klog.V(5).Infof("%s/%s pod networking is not set yet, so nothing to do on portmirror side", pod.Namespace, pod.Name)
		return nil
	}

	val, ok := pm.SourceDetails.SourcePodInfo.LoadAndDelete(sourcePodKey)
	if !ok {
		klog.V(5).Infof("Pod %s/%s is already deleted or doesn't have any nad on this network %s",
			pod.Namespace, pod.Name, bnc.GetNetworkName())
		return nil
	}

	// pod is marked for deletion, so return
	if !pod.DeletionTimestamp.IsZero() {
		klog.V(5).Infof("Pod %s/%s is already marked for deletion, so nothing to do", pod.Namespace, pod.Name)
		return nil
	}

	podNadPortInfo := val.(map[string]string)
	for portName, nad := range podNadPortInfo {
		klog.Infof("Deleting mirror rules for pod %s/%s under portmirror %s/%s for nad %s",
			pod.Namespace, pod.Name, pm.Namespace, pm.Name, nad)
		err := bnc.deleteMirrorFromPod(pm.MirrorUUID, portName)
		if err != nil {
			klog.Errorf("Failed to delete mirror %s on pod logical port %s",
				util.GetPortMirrorOVNName(pm.Namespace, pm.Name), portName)
		}
	}
	return nil
}

// addMirrorToPod adds the portMirror UUID to the corresponding
// pod LSP.
func (bnc *BaseNetworkController) addMirrorToPod(pm *util.PortMirror, portName string) error {
	pmLogicalPortNameUnlock := util.GetLockByPMLogicalPortName(portName)
	defer pmLogicalPortNameUnlock()

	lsp := &nbdb.LogicalSwitchPort{Name: portName}
	podLSP, err := libovsdbops.GetLogicalSwitchPort(bnc.nbClient, lsp)
	if err != nil {
		return fmt.Errorf("unable to get the lsp %s from the nbdb: %s", portName, err)
	}

	// append mirror if it's not found in podLSP mirror list
	var found bool
	for _, rule := range podLSP.MirrorRules {
		// Nothing to do.
		if rule == pm.MirrorUUID {
			found = true
			break
		}
		// Check the sink in the existing rule, if it is different from the current, fail.
		// We don't support mirroring to multiple ports.
		lookupFunc := func(item *nbdb.Mirror) bool {
			return item.UUID == rule
		}
		mirror, err := libovsdbops.FindMirrorWithPredicate(bnc.nbClient, lookupFunc)
		if err != nil {
			return fmt.Errorf("unable to get mirror associated with existing rule %s from nbdb: (%v)", rule, err)
		}
		if mirror[0].Sink != pm.SinkLocalDetails.PortMirrorId {
			return fmt.Errorf("port %s already mirrored to a different Sink %s", portName, mirror[0].Sink)
		}
	}

	if found {
		klog.Infof("PortMirror %s mirror rule is already added for port %s",
			util.GetPortMirrorOVNName(pm.Namespace, pm.Name), portName)
		return nil
	}

	// add the mirrorUUID to the pod lsp
	podLSP.MirrorRules = append(podLSP.MirrorRules, pm.MirrorUUID)

	ops, err := bnc.nbClient.Where(podLSP).Update(podLSP, &podLSP.MirrorRules)
	if err != nil {
		return fmt.Errorf("could not create commands to update logical switch port %s - %+v", portName, err)
	}

	_, err = libovsdbops.TransactAndCheckAndSetUUIDs(bnc.nbClient, podLSP, ops)
	if err != nil {
		return fmt.Errorf("could not perform update of logical switch port %s - %+v", portName, err)
	}

	// update the status to success
	updateErr := util.UpdatePortMirrorStatusWithRetry(bnc.kube, pm.Namespace, pm.Name, ovntypes.OvnK8sStatusSucceeded, "", "")
	if updateErr != nil {
		klog.Errorf("Failed to update portmirror %s/%s status to success state :(%v)", pm.Namespace, pm.Name, updateErr)
	}
	return nil
}

func (bnc *BaseNetworkController) isNadToBeMirrored(nadName string, podNadInfo map[string]bool) bool {
	// if no network-attach-def's are mentioned in the portmirror spec for this pod(podSelector),
	// default behaviour is to mirror interfaces on default/primaty nad for the pods selected by a source podSelector.
	// currently, only default controller will be registered when no network-attach-def are defined in portmirror spec,
	// so no need to check whether it's primary network controller
	if len(podNadInfo) == 0 {
		return true
	}
	return podNadInfo[nadName]
}

func (bnc *BaseNetworkController) handlePortMirrorSourcePodAdd(pm *util.PortMirror, pod *kapi.Pod) error {
	portMirrorNameUnlock := util.GetLockByPMName(pm.Namespace, pm.Name)
	defer portMirrorNameUnlock()

	if pod.Spec.HostNetwork {
		// skip host network pods
		klog.Errorf("Source pods for portmirror %s/%s can't be host network pods", pm.Namespace, pm.Name)
		return nil
	}

	// sourcePodKeyForFailedOps is used as key for FailedPortMirrorOps and
	// is also used as cookie for clearing error messages from portmirror status
	sourcePodKeyForFailedOps := util.GetPortMirrorSourcePodKeyForFailedOps(pod)
	// clear any error messages from portmirror status if
	// addition of mirror rules to this pod was failed before
	if _, ok := pm.FailedPortMirrorOps[sourcePodKeyForFailedOps]; ok {
		util.ClearPortMirrorErrorMessage(pm, bnc.kube, sourcePodKeyForFailedOps)

		// check whether source pod and corresponding portmirror exists while being retried,
		// if either of them doesn't exists, stop retry.
		if _, err := bnc.watchFactory.GetPortMirror(pm.Namespace, pm.Name); err != nil {
			if apierrors.IsNotFound(err) {
				klog.Infof("Stop retrying pod %s/%s as corresponding portmirror %s/%s does not exist",
					pod.Namespace, pod.Name, pm.Namespace, pm.Name)
				return nil
			} else {
				// requeue
				bnc.requeuePodAddForPortMirror(actionAddPortMirrorPod, pm, pod)
				return fmt.Errorf("failed to retrieve portmirror %s/%s from informer cache during source pod %s/%s addition retry: (%v)",
					pm.Namespace, pm.Name, pod.Namespace, pod.Name, err)
			}
		}

		if _, err := bnc.watchFactory.GetPod(pod.Namespace, pod.Name); err != nil {
			if apierrors.IsNotFound(err) {
				klog.Infof("Stop retrying pod addition %s/%s as it does not exist",
					pod.Namespace, pod.Name)
				return nil
			} else {
				// requeue
				errMsg := fmt.Sprintf("%s - Failed to get %s from informer cache",
					util.MessagePrefixPortMirrorErr, sourcePodKeyForFailedOps)
				util.UpdatePortMirrorStatusOnError(pm, bnc.kube, errMsg, sourcePodKeyForFailedOps)
				bnc.requeuePodAddForPortMirror(actionAddPortMirrorPod, pm, pod)
				return fmt.Errorf("failed to retrieve pod %s/%s from informer cache for portmirror %s/%s: (%v)",
					pod.Namespace, pod.Name, pm.Namespace, pm.Name, err)
			}
		}
	}

	on, nseMap, err := util.GetPodNADToNetworkMapping(pod, bnc.NetInfo)
	if err != nil {
		return fmt.Errorf("failed to get network info map for pod %s/%s :(%v)", pod.Namespace, pod.Name, err)
	} else if !on {
		// pod is not attached to this specific network
		klog.V(5).Infof("Pod %s/%s is not attached to network %s ", pod.Namespace, pod.Name, bnc.GetNetworkName())
		return nil
	}

	var podPortInfo map[string]string
	sourcePodKey := util.GetNamespacedName(pod.Namespace, pod.Name)
	portInfo, ok := pm.SourceDetails.SourcePodInfo.Load(sourcePodKey)
	if ok {
		podPortInfo = portInfo.(map[string]string)
	} else {
		podPortInfo = make(map[string]string)
	}

	for nadName := range nseMap {
		for _, sInfo := range pm.SourceDetails.SourceNetInfo {
			// check if pod labels matches corresponding podSelector and
			// then check for the interface of pods that needs to be mirrored
			// by comparing with the nads.
			if !sInfo.PodSelector.Matches(labels.Set(pod.Labels)) {
				continue
			}

			// check if this nad is in the list of nad's to be mirrored for the pod.
			if !bnc.isNadToBeMirrored(nadName, sInfo.PodNetAttachDefMirrorInfo) {
				continue
			}

			klog.Infof("Adding mirror rules for pod %s/%s under portmirror %s/%s for NAD %s",
				pod.Namespace, pod.Name, pm.Namespace, pm.Name, nadName)
			portName := bnc.GetLogicalPortName(pod, nadName)
			// if portName is already found in podPortInfo map, it means
			// mirror rule has been already added for this pod port, so skip in this
			// iteration.
			if _, ok := podPortInfo[portName]; ok {
				continue
			}

			// for default network controller need to change the nadname to default
			if !bnc.IsSecondary() {
				nadName = ovntypes.DefaultNetworkName
			}
			// check if the network for pod is established
			portInfo, err := bnc.logicalPortCache.get(pod.Namespace, pod.Name, nadName)
			if err != nil {
				klog.Errorf("%s/%s pod networking is not set yet : (%v)", pod.Namespace, pod.Name, err)
				pm.SourceDetails.PodRetry.Store(sourcePodKey, true)
				return nil
			}

			// this is portInfo of the previous deleted Pod of the same name
			// wait for the next Pod update event
			if !portInfo.expires.IsZero() {
				klog.Errorf("Port %s is already marked for removal", portName)
				pm.SourceDetails.PodRetry.Store(sourcePodKey, true)
				return nil
			}

			// delete the pod from podRetry list as port plumbing has been successful
			if util.NeedsRetry(pod, pm) {
				pm.SourceDetails.PodRetry.Delete(sourcePodKey)
			}

			// add portMirrorID to pod lsp.
			err = bnc.addMirrorToPod(pm, portName)
			if err != nil {
				// store the podPortInfo for the ports where mirror addition
				// to lsp was successful to avoid the readdition during retry.
				pm.SourceDetails.SourcePodInfo.Store(sourcePodKey, podPortInfo)
				// use sourcePodKeyForFailedOps as cookie to find the errMsg while clearing errMessages
				errMsg := fmt.Sprintf("%s - Failed to add mirror rule for %s: %v",
					util.MessagePrefixPortMirrorErr, sourcePodKeyForFailedOps, err)
				util.UpdatePortMirrorStatusOnError(pm, bnc.kube, errMsg, sourcePodKeyForFailedOps)
				// add this pod to retryQueue
				bnc.requeuePodAddForPortMirror(actionAddPortMirrorPod, pm, pod)
				return fmt.Errorf("failed to add mirror rule to pod %s/%s for logical port name %s under portmirr %s/%s (%v)",
					pod.Namespace, pod.Name, portName, pm.Namespace, pm.Name, err)
			}
			// store the pod lsp in podPortInfo
			podPortInfo[portName] = nadName
		}
	}
	// store the pod Info of pod portName to be mirrored along
	// with corresponding nad in the SourcePodInfo.
	pm.SourceDetails.SourcePodInfo.Store(sourcePodKey, podPortInfo)
	return nil
}

func (bnc *BaseNetworkController) handleSourcePodSelectors(pm *util.PortMirror) {
	for _, sInfo := range pm.SourceDetails.SourceNetInfo {
		// if no network-attachment-def's are mentioned in the portmirror spec for this source,
		// default behaviour is to mirror interfaces on default/primaty nad for the pods selected by this source podSelector.
		// so, pod handler should be registered for only default network controller.
		var foundNad = false
		if len(sInfo.PodNetAttachDefMirrorInfo) == 0 && !bnc.IsSecondary() {
			foundNad = true
		}

		for nad := range sInfo.PodNetAttachDefMirrorInfo {
			if bnc.HasNAD(nad) {
				foundNad = true
				break
			}
		}

		// this portmirror source doesn't have any network-attachment-def to be mirrored on this controller
		if !foundNad {
			continue
		}

		var err error
		sInfo.PodHandler, err = bnc.watchFactory.AddFilteredPodHandler(pm.Namespace, sInfo.PodSelector,
			cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					pod := obj.(*kapi.Pod)
					if err := bnc.handlePortMirrorSourcePodAdd(pm, pod); err != nil {
						klog.Errorf(err.Error())
					}
				},
				DeleteFunc: func(obj interface{}) {
					pod := obj.(*kapi.Pod)
					if err := bnc.handlePortMirrorSourcePodDelete(pm, pod); err != nil {
						klog.Errorf(err.Error())
					}
				},
				UpdateFunc: func(oldObj, newObj interface{}) {
					newPod := newObj.(*kapi.Pod)
					if util.NeedsRetry(newPod, pm) {
						if err := bnc.handlePortMirrorSourcePodAdd(pm, newPod); err != nil {
							klog.Errorf(err.Error())
						}
					}
				}}, nil, 1)
		if err != nil {
			klog.Errorf("Error adding pod mirror handler for %v: (%v)", sInfo.PodSelector, err)
		}
	}
}

// createPortMirror creates a portmirror object in the nbdb
func (bnc *BaseNetworkController) createPortMirror(portMir *portmirror.PortMirror, pm *util.PortMirror) error {
	portMirrorName := util.GetPortMirrorOVNName(pm.Namespace, pm.Name)
	lookupFunc := func(item *nbdb.Mirror) bool {
		return item.Name == portMirrorName
	}

	mirrors, err := libovsdbops.FindMirrorWithPredicate(bnc.nbClient, lookupFunc)
	if err != nil && !errors.Is(err, libovsdbclient.ErrNotFound) {
		return fmt.Errorf("failed while checking for existence of mirror %s in the NB DB: (%v)",
			portMirrorName, err)
	}

	if len(mirrors) > 1 {
		return fmt.Errorf("more than one mirror found in nbdb for mirror name %s", portMirrorName)
	}

	// if mirror already exists, store mirror UUID
	if len(mirrors) == 1 {
		// get mirror UUID
		pm.MirrorUUID = mirrors[0].UUID
		klog.Infof("Mirror %s is already present in OVN", portMirrorName)
		return nil
	}

	// mirror doesn't exist and need to create one.
	// if mirrorDirection is not specified in the spec, use the default value as Both
	var portMirrorDirection nbdb.MirrorFilter
	if pm.MirrorDirection == "" || pm.MirrorDirection == portmirror.PortMirrorDirectionBoth {
		portMirrorDirection = nbdb.MirrorFilterBoth
	} else if pm.MirrorDirection == portmirror.PortMirrorDirectionOut {
		portMirrorDirection = nbdb.MirrorFilterFromLport
	} else if pm.MirrorDirection == portmirror.PortMirrorDirectionIn {
		portMirrorDirection = nbdb.MirrorFilterToLport
	}

	// Only local support now; when we add support for remote, we need to revisit this.
	portMirr := &nbdb.Mirror{
		Name:   portMirrorName,
		Sink:   pm.SinkLocalDetails.PortMirrorId,
		Type:   nbdb.MirrorTypeLocal,
		Filter: portMirrorDirection,
		ExternalIDs: map[string]string{
			ovntypes.ExternalIDK8sOwner:     util.GetNamespacedName(portMir.Namespace, portMir.Name),
			ovntypes.OvnK8sPrefix + "/kind": util.GroupKindOf(portMir),
			ovntypes.ExternalIDNamespace:    pm.Namespace,
			ovntypes.ExternalIDName:         pm.Name,
			ovntypes.ExternalIDUID:          string(pm.UID),
		},
	}

	if err := libovsdbops.CreateOrUpdateMirror(bnc.nbClient, portMirr); err != nil {
		return fmt.Errorf("failed to create a mirror %s for network %s, error: %v", portMirrorName, bnc.GetNetworkName(), err)
	}
	// store the portMirror UUID
	pm.MirrorUUID = portMirr.UUID
	klog.Infof("Mirror %s created successfully in NBDB", portMirrorName)
	return nil
}

func (bnc *BaseNetworkController) addPortMirror(portMir *portmirror.PortMirror) error {
	klog.Infof("Adding portmirror %s/%s for network %s", portMir.Namespace, portMir.Name, bnc.GetNetworkName())
	pm, err := util.NewPortMirror(portMir)
	if err != nil {
		return err
	}

	portMirrorKey := util.GetNamespacedName(pm.Namespace, pm.Name)
	// pmKeyForFailedOps is used as a key for FailedPortMirrorOps
	pmKeyForFailedOps := util.GetPortMirrorKeyForFailedOps(pm.Namespace, pm.Name)
	portMirrorNameUnlock := util.GetLockByPMName(pm.Namespace, pm.Name)

	// clear any error message for this portmirror
	// if there were any errors during portmirror creation in OVN
	if _, ok := pm.FailedPortMirrorOps[pmKeyForFailedOps]; ok {
		util.ClearPortMirrorErrorMessage(pm, bnc.kube, pmKeyForFailedOps)

		// check if the portmirror exists while being retried
		if _, err := bnc.watchFactory.GetPortMirror(portMir.Namespace, portMir.Name); err != nil {
			var updatedErr error
			if apierrors.IsNotFound(err) {
				klog.Infof("Stop retrying portmirror %s/%s as it does not exist",
					portMir.Namespace, portMir.Name)
			} else {
				// requeue
				errMsg := fmt.Sprintf("%s - Failed to retrieve %s from cache",
					util.MessagePrefixPortMirrorErr, pmKeyForFailedOps)
				util.UpdatePortMirrorStatusOnError(pm, bnc.kube, errMsg, pmKeyForFailedOps)
				bnc.requeuePortMirror(actionAddPortMirror, portMir)
				updatedErr = fmt.Errorf("failed in retrieving portmirror %s/%s from cache: %v",
					portMir.Namespace, portMir.Name, err)
			}
			portMirrorNameUnlock()
			return updatedErr
		}
	}

	// create mirror in nbdb
	err = bnc.createPortMirror(portMir, pm)
	if err != nil {
		// update the portMirror status with this error
		errMsg := fmt.Sprintf("%s - Failed to create mirror rule for %s in OVN", util.MessagePrefixPortMirrorErr, pmKeyForFailedOps)
		util.UpdatePortMirrorStatusOnError(pm, bnc.kube, errMsg, pmKeyForFailedOps)
		// add this operation to portmirrorRetryQueue
		bnc.requeuePortMirror(actionAddPortMirror, portMir)
		portMirrorNameUnlock()
		return fmt.Errorf("failed to create portmirror %s/%s: (%v)", pm.Namespace, pm.Name, err)
	}

	// update the status to success
	updateErr := util.UpdatePortMirrorStatusWithRetry(bnc.kube, pm.Namespace, pm.Name, ovntypes.OvnK8sStatusSucceeded, "", "")
	if updateErr != nil {
		klog.Errorf("Failed to update portmirror %s/%s status to success state :(%v)", pm.Namespace, pm.Name, updateErr)
	}
	// store in cache only if mirror creation is successful in nbdb
	bnc.portMirrors.Store(portMirrorKey, pm)
	portMirrorNameUnlock()

	// watch for podselector handlers
	bnc.handleSourcePodSelectors(pm)
	return nil
}

func (bnc *BaseNetworkController) deletePortMirror(portMir *portmirror.PortMirror) error {
	portMirrorKey := util.GetNamespacedName(portMir.Namespace, portMir.Name)
	p, ok := bnc.portMirrors.LoadAndDelete(portMirrorKey)
	if !ok {
		klog.Errorf("Deleting portmirror %s/%s which was not created successfully", portMir.Namespace, portMir.Name)
		return nil
	}
	klog.Infof("Deleting portmirror %s/%s for network %s", portMir.Namespace, portMir.Name, bnc.GetNetworkName())
	pm := p.(*util.PortMirror)

	portMirrorNameUnlock := util.GetLockByPMName(pm.Namespace, pm.Name)
	defer portMirrorNameUnlock()

	// delete the portmirror podhandlers
	for i := range pm.SourceDetails.SourceNetInfo {
		if pm.SourceDetails.SourceNetInfo[i].PodHandler != nil {
			bnc.watchFactory.RemovePodHandler(pm.SourceDetails.SourceNetInfo[i].PodHandler.(*factory.Handler))
		}
	}
	pm.SourceDetails.SourcePodInfo.Range(func(k, v interface{}) bool {
		podListPortInfo := v.(map[string]string)
		for portName, nad := range podListPortInfo {
			klog.Infof("Deleting mirror rules from pod logical switch port %s under portmirror %s/%s for nad %s",
				portName, pm.Namespace, pm.Name, nad)
			err := bnc.deleteMirrorFromPod(pm.MirrorUUID, portName)
			if err != nil {
				klog.Errorf("Failed to remove portmirror %s/%s mirror rule %s from logical switch port %s :(%v)",
					pm.Namespace, pm.Name, pm.MirrorUUID, portName, err)
			}
		}
		return true
	})

	// check if the mirror exists as it might be deleted by other network controller.
	// if it exists, delete mirror from nbdb.
	portMirrorName := util.GetPortMirrorOVNName(pm.Namespace, pm.Name)
	lookupFunc := func(item *nbdb.Mirror) bool {
		return item.Name == portMirrorName
	}

	mirrors, err := libovsdbops.FindMirrorWithPredicate(bnc.nbClient, lookupFunc)
	if err != nil && !errors.Is(err, libovsdbclient.ErrNotFound) {
		return fmt.Errorf("failed while checking for existence of mirror %s in the NB DB: (%v)",
			portMirrorName, err)
	}

	if len(mirrors) == 1 {
		if err := libovsdbops.DeleteMirror(bnc.nbClient, mirrors[0]); err != nil {
			return fmt.Errorf("failed to delete mirror %s for network %s, error: %v", portMirrorName, bnc.GetNetworkName(), err)
		}
		klog.Infof("Deleted mirror %s from NBDB", portMirrorName)
	}
	return nil
}

// syncPortMirrorsPeriodic deletes stale mirrors from NBDB
func (bnc *BaseNetworkController) syncPortMirrorsPeriodic() {
	// list out all mirrors
	mirrors, err := libovsdbops.ListMirrors(bnc.nbClient)
	if err != nil {
		klog.Errorf("Failed to list mirror: (%v)", err)
		return
	}

	for _, mirror := range mirrors {
		portMirrorName := mirror.ExternalIDs[ovntypes.ExternalIDName]
		portMirrorNamespace := mirror.ExternalIDs[ovntypes.ExternalIDNamespace]
		portmirrorUID := mirror.ExternalIDs[ovntypes.ExternalIDUID]
		portMirrorNameUnlock := util.GetLockByPMName(portMirrorNamespace, portMirrorName)

		pm, err := bnc.watchFactory.GetPortMirror(portMirrorNamespace, portMirrorName)
		if err != nil && !apierrors.IsNotFound(err) {
			// skip sync for mirror in this iteration
			klog.Errorf("Failed to get portmirror %s/%s from informer cache: (%v)",
				pm.Namespace, pm.Name, err)
			portMirrorNameUnlock()
			continue
		}

		// portmirror will be nil when we don't find pormirror object in informer cache
		if pm == nil || string(pm.UID) != portmirrorUID {
			// delete stale mirror
			klog.V(5).Infof("Deleting stale mirror %s from NBDB", mirror.Name)
			if err := libovsdbops.DeleteMirror(bnc.nbClient, mirror); err != nil {
				klog.Warningf("Failed to delete mirror %+v from NBDB: (%v)", mirror, err)
			}
		}
		portMirrorNameUnlock()
	}
}

// retryPortMirrorOperations retries the failed portmirror operations.
// currently, retrying only add operations during portmirror creation
// & adding a portmirror UUID to pod LSP operations.
func (bnc *BaseNetworkController) retryPortMirrorOperations() bool {
	item, quit := bnc.portMirrorRetryQueue.Get()
	if quit {
		return false
	}
	bnc.portMirrorRetryQueue.Done(item)
	retry, ok := item.(*portMirrorRetryRequest)
	if !ok {
		return true
	}
	klog.V(4).Infof("Retrying portmirror event: %+v", *retry)

	switch retry.action {
	case actionAddPortMirror:
		err := bnc.addPortMirror(retry.portMirror)
		if err != nil {
			klog.Errorf("Failed creating portmirror %s/%s during retry (%v)", retry.pm.Namespace, retry.pm.Name, err)
		}
	case actionAddPortMirrorPod:
		err := bnc.handlePortMirrorSourcePodAdd(retry.pm, retry.pod)
		if err != nil {
			klog.Errorf("Failed adding portmirror %s/%s rules to pod (%s) LSP during retry (%v)",
				retry.pm.Namespace, retry.pm.Name, util.GetNamespacedName(retry.pod.Namespace, retry.pod.Name), err)
		}
	}
	return true
}

func (bnc *BaseNetworkController) requeuePortMirror(ra action, portmirror *portmirror.PortMirror) {
	req := &portMirrorRetryRequest{
		action:     ra,
		portMirror: portmirror,
	}
	klog.V(4).Infof("Requeuing portmirror %s/%s Add event to retry", portmirror.Namespace, portmirror.Name)
	bnc.portMirrorRetryQueue.AddAfter(req, 3*time.Second)
}

func (bnc *BaseNetworkController) requeuePodAddForPortMirror(ra action, portmirror *util.PortMirror, pod *kapi.Pod) {
	req := &portMirrorRetryRequest{
		action: ra,
		pm:     portmirror,
		pod:    pod,
	}
	klog.V(4).Infof("Requeuing portmirror %s/%s pod %s/%s Add event to retry", portmirror.Namespace, portmirror.Name,
		pod.Namespace, pod.Name)
	bnc.portMirrorRetryQueue.AddAfter(req, 3*time.Second)
}
