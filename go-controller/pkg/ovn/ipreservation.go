package ovn

import (
	"fmt"
	"net"
	"reflect"
	"time"

	ipallocator "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/allocator/ip"
	ipreservation "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/ipreservation/v1beta1"
	ipreservationscheme "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/ipreservation/v1beta1/apis/clientset/versioned/scheme"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/reference"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const (
	actionAddIPReservation    action = "add-ipreservation"
	actionDeleteIPReservation action = "delete-ipreservation"
)

type ipReservationRetryRequest struct {
	action    action
	resvIPObj *ipreservation.IPReservation
}

func getIPReservationLockKey(resvIPObj *ipreservation.IPReservation) string {
	return fmt.Sprintf("IPReservation/%s/%s", resvIPObj.Namespace, resvIPObj.Name)
}

func (bnc *BaseNetworkController) recordIPReservationEvent(reason string, err string, resvIP *ipreservation.IPReservation) {
	resvIPRef, refErr := reference.GetReference(ipreservationscheme.Scheme, resvIP)
	if refErr != nil {
		klog.Errorf("Couldn't get a reference to IPReservation %s/%s to post an event: '%v'",
			resvIP.Namespace, resvIP.Name, refErr)
	} else {
		klog.V(5).Infof("Posting a %s event for IPReservation %s/%s", kapi.EventTypeWarning, resvIP.Namespace, resvIP.Name)
		bnc.recorder.Eventf(resvIPRef, kapi.EventTypeWarning, reason, err)
	}
}

func (bnc *BaseNetworkController) updateIPReservationStatusWithRetry(namespace, name string, status ovntypes.OvnK8sStatus,
	messages []string, resvIPs []string) error {
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the latest version of IPReservation object to modify it
		latestResvIP, err := bnc.watchFactory.GetIPReservation(namespace, name)
		if err != nil {
			klog.Errorf("Unable to get IPReservation %s/%s for updating status, most likely it would be deleted",
				namespace, name)
			return err
		}

		latestResvIP = latestResvIP.DeepCopy()
		if status != "" {
			latestResvIP.Status.Status = status
		}
		if messages != nil {
			latestResvIP.Status.Messages = messages
		}
		if resvIPs != nil {
			latestResvIP.Status.ReservedIPs = resvIPs
		}
		return bnc.kube.UpdateIPReservationStatus(latestResvIP)
	})
	if retryErr != nil {
		return fmt.Errorf("error in updating status on IPReservation %s/%s: %v", namespace, name, retryErr)
	}
	return nil
}

func (oc *BaseSecondaryLayer2NetworkController) addIPReservation(resvIPObj *ipreservation.IPReservation) error {
	klog.Infof("Adding IPReservation %s/%s for network %s", resvIPObj.Namespace, resvIPObj.Name,
		resvIPObj.Spec.NetworkAttachmentName)
	// first check if we have already reserved IPs, and we are adding the object as part of ovnkube-master start
	if resvIPObj.Status.Status == ovntypes.OvnK8sStatusSucceeded {
		klog.Infof("IPReservation object %s/%s for network %s has already been handled during sync",
			resvIPObj.Namespace, resvIPObj.Name, resvIPObj.Spec.NetworkAttachmentName)
		return nil
	}

	var switchName string
	if oc.TopologyType() == ovntypes.LocalnetTopology {
		switchName = oc.GetNetworkScopedName(ovntypes.OVNLocalnetSwitch)
	} else {
		switchName = oc.GetNetworkScopedName(ovntypes.OVNLayer2Switch)
	}

	isIPv4 := resvIPObj.Spec.IPFamily == ipreservation.IPv4Protocol
	if !oc.lsManager.EnsureIPAMForIPFamily(switchName, isIPv4) {
		err := fmt.Errorf("there is no %s subnet for the given network %s for the IP allocator to reserve IPs",
			resvIPObj.Spec.IPFamily, resvIPObj.Spec.NetworkAttachmentName)
		tmpErr := oc.updateIPReservationStatusWithRetry(resvIPObj.Namespace, resvIPObj.Name, ovntypes.OvnK8sStatusFailed,
			[]string{err.Error()}, nil)
		if tmpErr != nil {
			klog.Errorf(tmpErr.Error())
		}
		return err
	}
	if isIPv4 {
		// currently an optimization only for IPv4 case;
		// it could be that after this check, the IPs got used by new pods that came up
		totalAvailableCount, err := oc.lsManager.AvailableIPsCount(switchName,
			resvIPObj.Spec.IPFamily == ipreservation.IPv4Protocol)
		if err != nil {
			return err
		}
		if totalAvailableCount < int64(resvIPObj.Spec.Count) {
			err := fmt.Errorf("for %s/%s, the number of available IPs in network %s - %d - is less than the requested number of IPs",
				resvIPObj.Namespace, resvIPObj.Name, resvIPObj.Spec.NetworkAttachmentName, totalAvailableCount)
			tmpErr := oc.updateIPReservationStatusWithRetry(resvIPObj.Namespace, resvIPObj.Name, ovntypes.OvnK8sStatusFailed,
				[]string{err.Error()}, nil)
			if tmpErr != nil {
				klog.Errorf(tmpErr.Error())
			}
			return err
		}
		klog.V(5).Infof("Network %s has sufficient number of IPs to reserve for IPReservation %s/%s object, count=%d",
			resvIPObj.Spec.NetworkAttachmentName, resvIPObj.Namespace, resvIPObj.Name, resvIPObj.Spec.Count)
	}

	resvIPNets, err := oc.lsManager.AllocateIPsByCount(switchName, isIPv4, resvIPObj.Spec.Count)
	if err != nil {
		err = fmt.Errorf("failed to reserve %d IPs for IPReservation %s/%s object for network %s - %v",
			resvIPObj.Spec.Count, resvIPObj.Namespace, resvIPObj.Name, resvIPObj.Spec.NetworkAttachmentName, err)
		tmpErr := oc.updateIPReservationStatusWithRetry(resvIPObj.Namespace, resvIPObj.Name, ovntypes.OvnK8sStatusFailed,
			[]string{err.Error()}, nil)
		if tmpErr != nil {
			klog.Errorf(tmpErr.Error())
		}
		if err != ipallocator.ErrFull {
			// there is no point in retrying if the IPAM is full, it is better for the user to free up the IPs and
			// recreate the CR
			oc.requeueIPReservation(actionAddIPReservation, resvIPObj)
		}
		return err
	}

	resvIPs := make([]string, len(resvIPNets))
	for i, resvIPNet := range resvIPNets {
		resvIPs[i] = resvIPNet.String()
	}

	err = oc.updateIPReservationStatusWithRetry(resvIPObj.Namespace, resvIPObj.Name, ovntypes.OvnK8sStatusSucceeded, nil,
		resvIPs)
	if err != nil {
		tmpErr := oc.lsManager.ReleaseIPs(switchName, resvIPNets)
		tmpRes := "success"
		if tmpErr != nil {
			tmpRes = fmt.Sprintf("failed - %v", tmpErr)
		}
		klog.Errorf("Failed to update the status of IPReservation object %s/%s with reservedIPs. "+
			"Attempting to release the reserved IPs - %v - %s", resvIPObj.Namespace, resvIPObj.Name, resvIPs, tmpRes)
		oc.requeueIPReservation(actionAddIPReservation, resvIPObj)
	}
	return err
}

func (oc *BaseSecondaryLayer2NetworkController) deleteIPReservation(resvIPObj *ipreservation.IPReservation) error {
	klog.Infof("Deleting IPReservation %s/%s for network %s", resvIPObj.Namespace, resvIPObj.Name, resvIPObj.Spec.NetworkAttachmentName)

	var switchName string
	if oc.TopologyType() == ovntypes.LocalnetTopology {
		switchName = oc.GetNetworkScopedName(ovntypes.OVNLocalnetSwitch)
	} else {
		switchName = oc.GetNetworkScopedName(ovntypes.OVNLayer2Switch)
	}

	resvIPNets := make([]*net.IPNet, 0, len(resvIPObj.Status.ReservedIPs))
	for _, resvIP := range resvIPObj.Status.ReservedIPs {
		ip, ipnet, _ := net.ParseCIDR(resvIP)
		ipnet.IP = ip
		resvIPNets = append(resvIPNets, ipnet)
	}

	// take care of the error case
	err := oc.lsManager.ReleaseIPs(switchName, resvIPNets)
	if err != nil {
		err = fmt.Errorf("failed relasing IPs for IPReservation %s/%s object for network %s, will retry - %v",
			resvIPObj.Namespace, resvIPObj.Name, resvIPObj.Spec.NetworkAttachmentName, err)
		oc.requeueIPReservation(actionDeleteIPReservation, resvIPObj)
	}
	return err
}

// Todo(gmoodalbail): if a NAD CR and IPReservation CR is applied at the same time, are there
// chances for race??

func (oc *BaseSecondaryLayer2NetworkController) syncIPReservationObjects(resvIPObjs []interface{}) error {
	for _, resvIPObjInterface := range resvIPObjs {
		resvIPObj, ok := resvIPObjInterface.(*ipreservation.IPReservation)
		if !ok {
			klog.Errorf("Spurious object in syncIPReservationObjects: %v", resvIPObjInterface)
			continue
		}
		// no IPs to reserve if the status is not succeeded
		if resvIPObj.Status.Status != ovntypes.OvnK8sStatusSucceeded {
			continue
		}
		var switchName string
		if oc.TopologyType() == ovntypes.LocalnetTopology {
			switchName = oc.GetNetworkScopedName(ovntypes.OVNLocalnetSwitch)
		} else {
			switchName = oc.GetNetworkScopedName(ovntypes.OVNLayer2Switch)
		}
		for _, resvIP := range resvIPObj.Status.ReservedIPs {
			ip, ipnet, _ := net.ParseCIDR(resvIP)
			ipnet.IP = ip
			err := oc.lsManager.AllocateIPs(switchName, []*net.IPNet{ipnet})
			if err != nil {
				if err == ipallocator.ErrAllocated {
					// This should not happen ever!! if it does, then something else - a Pod or another IPReservation
					// object took the IP from us!
					err = fmt.Errorf("failed to reserve the IP %s for IPReservation object %s/%s while syncing. "+
						"Something else has already reserved the IP", ipnet, resvIPObj.Namespace, resvIPObj.Name)
				} else {
					err = fmt.Errorf("failed to reserve the IP %s for IPReservation object %s/%s while syncing - %v",
						ipnet, resvIPObj.Namespace, resvIPObj.Name, err)
				}
				oc.recordIPReservationEvent("IPReservationSyncError", err.Error(), resvIPObj)
				return err
			} else {
				klog.V(5).Infof("Successfully reserved IP %s for IPReservation object %s/%s while syncing.",
					ipnet, resvIPObj.Namespace, resvIPObj.Name)
			}
		}
	}
	return nil
}

// WatchIPReservations starts the watching of ipreservation resources and calls
// back the appropriate handler logic
func (oc *BaseSecondaryLayer2NetworkController) WatchIPReservations() (err error) {
	if oc.ipReserveHandler != nil {
		// WatchIPReservations has succeeded and this is from retry, nothing to do
		return nil
	}
	start := time.Now()

	defer func() {
		if err != nil {
			if oc.ipReserveRetryQueue != nil {
				oc.ipReserveRetryQueue.ShutDown()
			}
			if oc.ipReserveHandler != nil {
				oc.watchFactory.RemoveIPReservationHandler(oc.ipReserveHandler)
			}
			oc.ipReserveRetryQueue = nil
			oc.ipReserveHandler = nil
		}
	}()

	oc.ipReserveRetryQueue = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "ipReserve")
	// filterIPReservation checks if the ipReservation's NAD belongs to this controller
	filterIPReservation := func(obj interface{}) bool {
		ipResv, ok := obj.(*ipreservation.IPReservation)
		if !ok {
			return false
		}
		return oc.HasNAD(ipResv.Spec.NetworkAttachmentName)
	}
	oc.ipReserveHandler, err = oc.watchFactory.AddHandlerWithFilterFunc(reflect.TypeOf(&ipreservation.IPReservation{}), filterIPReservation,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				resvIPObj := obj.(*ipreservation.IPReservation)
				unlock := util.LockByKey.Acquire(getIPReservationLockKey(resvIPObj))
				defer unlock()
				err := oc.addIPReservation(resvIPObj)
				if err != nil {
					klog.Errorf(err.Error())
					oc.recordIPReservationEvent("IPReservationAddError", err.Error(), resvIPObj)
				}
			},
			UpdateFunc: func(old, newer interface{}) {
				oldResvIPObj := old.(*ipreservation.IPReservation)
				newResvIPObj := newer.(*ipreservation.IPReservation)
				if !reflect.DeepEqual(oldResvIPObj.Spec, newResvIPObj.Spec) {
					oc.recordIPReservationEvent("IPReservationUpdateError", "Updating IPReservation object is not supported",
						oldResvIPObj)
				}
			},
			DeleteFunc: func(obj interface{}) {
				resvIPObj := obj.(*ipreservation.IPReservation)
				unlock := util.LockByKey.Acquire(getIPReservationLockKey(resvIPObj))
				defer unlock()
				err := oc.deleteIPReservation(resvIPObj)
				if err != nil {
					klog.Errorf(err.Error())
					oc.recordIPReservationEvent("IPReservationDeleteError", err.Error(), resvIPObj)
				}
			},
		}, oc.syncIPReservationObjects, 1 /* TBD: set priority */)
	if err != nil {
		return fmt.Errorf("failed to watch for IPReservations CRD for network %s", oc.GetNetworkName())
	}

	go func() {
		<-oc.stopChan
		oc.ipReserveRetryQueue.ShutDown()
	}()
	go func() {
		for oc.retryIPReservationOperations() {
		}
	}()

	klog.Infof("Bootstrapping existing ipreservations and cleaning stale ipreservations for network %s took %v",
		oc.GetNetworkName(), time.Since(start))
	return nil
}

func (oc *BaseSecondaryLayer2NetworkController) retryIPReservationOperations() bool {
	item, quit := oc.ipReserveRetryQueue.Get()
	if quit {
		return false
	}
	oc.ipReserveRetryQueue.Done(item)
	retryEvent, ok := item.(*ipReservationRetryRequest)
	if !ok {
		return true
	}
	klog.V(4).Infof("Retrying IPReservation event %s for %s/%s", retryEvent.action,
		retryEvent.resvIPObj.Namespace, retryEvent.resvIPObj.Name)
	unlock := util.LockByKey.Acquire(getIPReservationLockKey(retryEvent.resvIPObj))
	defer unlock()
	// it could be that the object has been removed from K8s while we were waiting on queue, so check
	// for existence of the object
	_, err := oc.watchFactory.GetIPReservation(retryEvent.resvIPObj.Namespace, retryEvent.resvIPObj.Name)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Stop retrying IPReservation event %v for  %s/%s as it does not exist", retryEvent,
				retryEvent.resvIPObj.Namespace, retryEvent.resvIPObj.Name)
		} else {
			// requeue
			oc.requeueIPReservation(retryEvent.action, retryEvent.resvIPObj)
		}
		return true
	}
	switch retryEvent.action {
	case actionAddIPReservation:
		err := oc.addIPReservation(retryEvent.resvIPObj)
		if err != nil {
			klog.Errorf("Failed adding IPReservation object %s/%s during retry",
				retryEvent.resvIPObj.Namespace, retryEvent.resvIPObj.Name)
		}
	case actionDeleteIPReservation:
		err := oc.deleteIPReservation(retryEvent.resvIPObj)
		if err != nil {
			klog.Errorf("Failed deleting IPReservation object %s/%s during retry",
				retryEvent.resvIPObj.Namespace, retryEvent.resvIPObj.Name)
		}
	}
	return true
}

func (oc *BaseSecondaryLayer2NetworkController) requeueIPReservation(ra action, resvIPObj *ipreservation.IPReservation) {
	req := &ipReservationRetryRequest{
		action:    ra,
		resvIPObj: resvIPObj,
	}
	klog.V(4).Infof("Requeue IPReservation %s/%s's %s event for retry", resvIPObj.Namespace, resvIPObj.Name, ra)
	oc.ipReserveRetryQueue.AddAfter(req, 3*time.Second)
}
