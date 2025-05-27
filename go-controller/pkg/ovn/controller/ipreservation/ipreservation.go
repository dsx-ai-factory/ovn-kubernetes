package ipreserv

import (
	"fmt"
	"net"
	"reflect"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/tools/reference"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	ipallocator "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/allocator/ip"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/allocator/ip/subnet"
	ipreservation "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/ipreservation/v1beta1"
	ipreservationscheme "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/ipreservation/v1beta1/apis/clientset/versioned/scheme"
	ipresvinformers "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/ipreservation/v1beta1/apis/informers/externalversions/ipreservation/v1beta1"
	ipresvlisters "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/ipreservation/v1beta1/apis/listers/ipreservation/v1beta1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/syncmap"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

const (
	// maxRetries is the number of times a object will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the
	// sequence of delays between successive queuings of an object.
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	// maxRetries = 15

	maxRetries = 10
)

// Controller manages selector-based service endpoints.
type Controller struct {
	util.ReconcilableNetInfo

	// libovsdb northbound client interface
	recorder record.EventRecorder

	watchFactory      *factory.WatchFactory
	kube              kube.InterfaceOVN
	ipresvInformer    ipresvinformers.IPReservationInformer
	ipresvLister      ipresvlisters.IPReservationLister
	ipresvCacheSynced cache.InformerSynced
	allocator         subnet.NamedAllocator
	controllerName    string
	ipresvCache       *syncmap.SyncMap[*ipReservationState]

	// Services that need to be updated. A channel is inappropriate here,
	// because it allows services with lots of pods to be serviced much
	// more often than services with few pods; it also would cause a
	// service that's inserted multiple times to be processed more than
	// necessary.
	queue workqueue.RateLimitingInterface

	stopChan <-chan struct{}
}

type ipReservationState struct {
	key         string
	spec        ipreservation.IPReservationSpec
	reservedIPs []string
}

// NewController returns a new IPReservation Controller.
func NewController(netInfo util.ReconcilableNetInfo,
	kube kube.InterfaceOVN,
	watchFactory *factory.WatchFactory,
	allocator subnet.NamedAllocator,
	recorder record.EventRecorder,
	stopChan <-chan struct{},
) (*Controller, error) {
	var err error
	controllerName := netInfo.GetNetworkName()
	c := &Controller{
		ReconcilableNetInfo: netInfo,
		queue:               workqueue.NewNamedRateLimitingQueue(workqueue.NewItemFastSlowRateLimiter(1*time.Second, 5*time.Second, 5), controllerName),
		kube:                kube,
		watchFactory:        watchFactory,
		ipresvInformer:      watchFactory.IPReservationInformer(),
		ipresvLister:        watchFactory.IPReservationInformer().Lister(),
		ipresvCacheSynced:   watchFactory.IPReservationInformer().Informer().HasSynced,
		recorder:            recorder,
		allocator:           allocator,
		controllerName:      controllerName,
		ipresvCache:         syncmap.NewSyncMap[*ipReservationState](),
		stopChan:            stopChan,
	}

	_, err = c.ipresvInformer.Informer().AddEventHandler(factory.WithUpdateHandlingForObjReplace(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onIPResvAdd,
		UpdateFunc: c.onIPResvUpdate,
		DeleteFunc: c.onIPResvDelete,
	}))

	if err != nil {
		return nil, err
	}
	// Wait for the caches to be synced
	klog.V(5).Info("Waiting for informer caches to sync")
	if !util.WaitForInformerCacheSyncWithTimeout(c.controllerName, stopChan, c.ipresvCacheSynced) {
		err = fmt.Errorf("timed out waiting for IP reservetion caches to sync")
		utilruntime.HandleError(err)
		return nil, err
	}

	// Run the sync function at startup so that we allocated all IPs already reserved
	err = c.syncIPReservations()
	if err != nil {
		err = fmt.Errorf("failed to sync existing IP reservations: %v", err)
		utilruntime.HandleError(err)
		return nil, err
	}

	return c, nil
}

func (c *Controller) queueIPResv(obj interface{}, event string) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("%s: couldn't get key for IPReservation object %+v: %v", c.controllerName, obj, err))
		return
	}
	klog.V(4).Infof("IP reservation %s: %v", key, event)
	c.queue.Add(key)
}

// onIPResvAdd queues the IPReservation for processing.
func (c *Controller) onIPResvAdd(obj interface{}) {
	c.queueIPResv(obj, "add")
}

// onIPResvUpdate updates the IPReservation State in the cache and queues the IPReservation for processing.
func (c *Controller) onIPResvUpdate(oldObj, newObj interface{}) {
	oldIPResv := oldObj.(*ipreservation.IPReservation)
	newIPResv := newObj.(*ipreservation.IPReservation)

	// don't process resync or objects that are marked for deletion
	if oldIPResv.ResourceVersion == newIPResv.ResourceVersion ||
		!newIPResv.GetDeletionTimestamp().IsZero() {
		return
	}
	if reflect.DeepEqual(oldIPResv.Spec, newIPResv.Spec) {
		c.queueIPResv(newIPResv, "update")
		return
	}
	c.recordIPReservationEvent("IPReservationUpdateError", "Updating IPReservation object is not supported",
		oldIPResv)
}

// onIPResvUpdate queues the IPReservation object for processing.
func (c *Controller) onIPResvDelete(obj interface{}) {
	c.queueIPResv(obj, "delete")
}

// Run will not return until stopCh is closed. workers determines how many
// objects (pods, namespaces, anps, banps) will be handled in parallel.
func (c *Controller) Run(threadiness int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()

	klog.Infof("Starting controller %s", c.controllerName)

	klog.Infof("Repairing IP reservetion")
	wg := &sync.WaitGroup{}
	// Start the workers after the repair loop to avoid races
	klog.V(5).Info("Starting IPReservation workers")
	for i := 0; i < threadiness; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wait.Until(func() {
				c.runWorker(wg)
			}, time.Second, stopCh)
		}()
	}

	<-stopCh

	klog.Infof("Shutting down IPReservation controller %s", c.controllerName)
	c.queue.ShutDown()
	wg.Wait()
}

// worker runs a worker thread that just dequeues items, processes them, and
// marks them done. You may run as many of these in parallel as you wish; the
// workqueue guarantees that they will not end up processing the same object
// at the same time.
func (c *Controller) runWorker(wg *sync.WaitGroup) {
	for c.processNextWorkItem(wg) {
	}
}

func (c *Controller) processNextWorkItem(wg *sync.WaitGroup) bool {
	wg.Add(1)
	defer wg.Done()
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	err := c.sync(key.(string))
	if err == nil {
		c.queue.Forget(key)
		return true
	}
	utilruntime.HandleError(fmt.Errorf("failed to process IPReservation %v: %v", key, err))

	if c.queue.NumRequeues(key) < maxRetries {
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)
	return true
}

func (c *Controller) recordIPReservationEvent(reason string, err string, resvIP *ipreservation.IPReservation) {
	resvIPRef, refErr := reference.GetReference(ipreservationscheme.Scheme, resvIP)
	if refErr != nil {
		klog.Errorf("Couldn't get a reference to IPReservation %s/%s to post an event: '%v'",
			resvIP.Namespace, resvIP.Name, refErr)
	} else {
		klog.Warningf("Posting a %s event for IPReservation %s/%s", corev1.EventTypeWarning, resvIP.Namespace, resvIP.Name)
		c.recorder.Eventf(resvIPRef, corev1.EventTypeWarning, reason, err)
	}
}

func (c *Controller) updateIPReservationStatusWithRetry(namespace, name string, status ovntypes.OvnK8sStatus,
	messages []string, resvIPs []string) error {
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the latest version of IPReservation object to modify it
		latestResvIP, err := c.watchFactory.GetIPReservation(namespace, name)
		if err != nil {
			klog.Errorf("Unable to get IPReservation %s/%s for updating status, most likely it would be deleted",
				namespace, name)
			return err
		}

		latestResvIP = latestResvIP.DeepCopy()
		if status != "" {
			latestResvIP.Status.Status = status
		}
		// clear the messages if the status is succeeded
		if messages != nil || status == ovntypes.OvnK8sStatusSucceeded {
			latestResvIP.Status.Messages = messages
		}
		if resvIPs != nil {
			latestResvIP.Status.ReservedIPs = resvIPs
		}
		return c.kube.UpdateIPReservationStatus(latestResvIP)
	})
	if retryErr != nil {
		return fmt.Errorf("error in updating status on IPReservation %s/%s: %v", namespace, name, retryErr)
	}
	return nil
}

func (c *Controller) sync(ipresvKey string) error {
	startTime := time.Now()
	ns, name, err := cache.SplitMetaNamespaceKey(ipresvKey)
	if err != nil {
		return err
	}
	klog.V(5).Infof("Processing sync for IP reservation %s", ipresvKey)

	defer func() {
		klog.V(5).Infof("Finished syncing IP reservation %s : %v", ipresvKey, time.Since(startTime))
	}()

	return c.ipresvCache.DoWithLock(ipresvKey, func(key string) error {
		ipresvObj, err := c.ipresvLister.IPReservations(ns).Get(name)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		exstingIPResvState, loaded := c.ipresvCache.Load(key)
		if ipresvObj == nil {
			// IP reservation is now deleted
			if !loaded {
				return nil
			}
			if err = c.deleteIPReservation(exstingIPResvState); err != nil {
				c.recordIPReservationEvent("IPReservationDeleteError", err.Error(),
					&ipreservation.IPReservation{})
				return err
			}
			c.ipresvCache.Delete(key)
			return nil
		}
		if exstingIPResvState != nil {
			if !reflect.DeepEqual(exstingIPResvState.spec, ipresvObj.Spec) {
				// old IP reservation has already been deleted and new one has been created
				// release the old reservation and create the new one
				if err = c.deleteIPReservation(exstingIPResvState); err != nil {
					return err
				}
				c.ipresvCache.Delete(key)
			}
		}
		resvIPs, err := c.ensureIPReservation(ipresvObj)
		if err != nil {
			klog.Errorf(err.Error())
			c.recordIPReservationEvent("IPReservationAddError", err.Error(), ipresvObj)
			return err
		}
		IPResvState := &ipReservationState{key, ipresvObj.Spec, resvIPs}
		c.ipresvCache.Store(key, IPResvState)
		return nil
	})
}

func (c *Controller) ensureIPReservation(resvIPObj *ipreservation.IPReservation) ([]string, error) {
	if !c.HasNAD(resvIPObj.Spec.NetworkAttachmentName) {
		// IP reservation does not apply to this network, do nothing and return success
		return nil, nil
	}

	klog.Infof("Adding IPReservation %s/%s for network %s", resvIPObj.Namespace, resvIPObj.Name,
		resvIPObj.Spec.NetworkAttachmentName)

	if c.allocator == nil {
		// IP reservation is not supported in this network, do nothing and return success
		msg := fmt.Errorf("the NAD %s of IPReservation %s/%s requested is not a layer2 or localnet network",
			resvIPObj.Spec.NetworkAttachmentName, resvIPObj.Namespace, resvIPObj.Name)
		klog.Errorf(msg.Error())
		err := c.updateIPReservationStatusWithRetry(resvIPObj.Namespace, resvIPObj.Name, ovntypes.OvnK8sStatusFailed,
			[]string{msg.Error()}, nil)
		if err != nil {
			klog.Errorf("Updated status of IPReservation %s/%s failed: %v", resvIPObj.Namespace, resvIPObj.Name, err)
		}
		return nil, err
	}

	// first check if we have already reserved IPs, and we are adding the object as part of ovnkube-master start
	if resvIPObj.Status.Status == ovntypes.OvnK8sStatusSucceeded {
		klog.Infof("IPReservation object %s/%s for network %s has already been handled during sync",
			resvIPObj.Namespace, resvIPObj.Name, resvIPObj.Spec.NetworkAttachmentName)
		return resvIPObj.Status.ReservedIPs, nil
	}

	isIPv4 := resvIPObj.Spec.IPFamily == ipreservation.IPv4Protocol
	if !c.allocator.EnsureIPAMForIPFamily(isIPv4) {
		err := fmt.Errorf("there is no %s subnet for the given network %s for the IP allocator to reserve IPs",
			resvIPObj.Spec.IPFamily, resvIPObj.Spec.NetworkAttachmentName)
		tmpErr := c.updateIPReservationStatusWithRetry(resvIPObj.Namespace, resvIPObj.Name, ovntypes.OvnK8sStatusFailed,
			[]string{err.Error()}, nil)
		if tmpErr != nil {
			klog.Errorf(tmpErr.Error())
		}
		return nil, err
	}
	if isIPv4 {
		// currently an optimization only for IPv4 case;
		// it could be that after this check, the IPs got used by new pods that came up
		totalAvailableCount, err := c.allocator.AvailableIPsCount(resvIPObj.Spec.IPFamily == ipreservation.IPv4Protocol)
		if err != nil {
			return nil, err
		}
		if totalAvailableCount < int64(resvIPObj.Spec.Count) {
			err := fmt.Errorf("for %s/%s, the number of available IPs in network %s - %d - is less than the requested number of IPs",
				resvIPObj.Namespace, resvIPObj.Name, resvIPObj.Spec.NetworkAttachmentName, totalAvailableCount)
			tmpErr := c.updateIPReservationStatusWithRetry(resvIPObj.Namespace, resvIPObj.Name, ovntypes.OvnK8sStatusFailed,
				[]string{err.Error()}, nil)
			if tmpErr != nil {
				klog.Errorf(tmpErr.Error())
			}
			return nil, err
		}
		klog.V(5).Infof("Network %s has sufficient number of IPs to reserve for IPReservation %s/%s object, count=%d",
			resvIPObj.Spec.NetworkAttachmentName, resvIPObj.Namespace, resvIPObj.Name, resvIPObj.Spec.Count)
	}

	resvIPNets, err := c.allocator.AllocateIPsByCount(isIPv4, resvIPObj.Spec.Count)
	if err != nil {
		err = fmt.Errorf("failed to reserve %d IPs for IPReservation %s/%s object for network %s - %v",
			resvIPObj.Spec.Count, resvIPObj.Namespace, resvIPObj.Name, resvIPObj.Spec.NetworkAttachmentName, err)
		tmpErr := c.updateIPReservationStatusWithRetry(resvIPObj.Namespace, resvIPObj.Name, ovntypes.OvnK8sStatusFailed,
			[]string{err.Error()}, nil)
		if tmpErr != nil {
			klog.Errorf(tmpErr.Error())
		}
		return nil, err
	}

	resvIPs := make([]string, len(resvIPNets))
	for i, resvIPNet := range resvIPNets {
		resvIPs[i] = resvIPNet.String()
	}

	err = c.updateIPReservationStatusWithRetry(resvIPObj.Namespace, resvIPObj.Name, ovntypes.OvnK8sStatusSucceeded, nil,
		resvIPs)
	if err != nil {
		tmpErr := c.allocator.ReleaseIPs(resvIPNets)
		tmpRes := "success"
		if tmpErr != nil {
			tmpRes = fmt.Sprintf("failed - %v", tmpErr)
		}
		resvIPs = nil
		klog.Errorf("Failed to update the status of IPReservation object %s/%s with reservedIPs. "+
			"Attempting to release the reserved IPs - %v - %s", resvIPObj.Namespace, resvIPObj.Name, resvIPs, tmpRes)
	}
	return resvIPs, err
}

func (c *Controller) deleteIPReservation(ipResvSate *ipReservationState) error {
	klog.Infof("Deleting IPReservation %s for network %s", ipResvSate.key, ipResvSate.spec.NetworkAttachmentName)

	ipCount := len(ipResvSate.reservedIPs)
	if ipCount == 0 {
		return nil
	}
	resvIPNets := make([]*net.IPNet, 0, ipCount)
	for _, resvIP := range ipResvSate.reservedIPs {
		ip, ipnet, _ := net.ParseCIDR(resvIP)
		ipnet.IP = ip
		resvIPNets = append(resvIPNets, ipnet)
	}

	// take care of the error case
	err := c.allocator.ReleaseIPs(resvIPNets)
	if err != nil {
		err = fmt.Errorf("failed relasing IPs for IPReservation %s object for network %s, will retry - %v",
			ipResvSate.key, ipResvSate.spec.NetworkAttachmentName, err)
	}
	return err
}

// Todo(gmoodalbail): if a NAD CR and IPReservation CR is applied at the same time, are there
// chances for race??

func (c *Controller) syncIPReservations() error {
	// This controller does not support IP reservation
	if c.allocator == nil {
		return nil
	}
	start := time.Now()
	defer func() {
		klog.Infof("Sync IP reservations took %v", time.Since(start))
	}()

	resvIPObjs, err := c.ipresvLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("unable to list all IP reservations: %v", err)
	}

	for _, resvIPObj := range resvIPObjs {
		key, err := cache.MetaNamespaceKeyFunc(resvIPObj)
		if err != nil {
			continue
		}
		// Check the IP reservation is for this network
		if !c.HasNAD(resvIPObj.Spec.NetworkAttachmentName) {
			continue
		}
		// no IPs to reserve if the status is not succeeded
		if resvIPObj.Status.Status != ovntypes.OvnK8sStatusSucceeded {
			continue
		}
		for _, resvIP := range resvIPObj.Status.ReservedIPs {
			ip, ipnet, _ := net.ParseCIDR(resvIP)
			ipnet.IP = ip
			err := c.allocator.AllocateIPs([]*net.IPNet{ipnet})
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
				c.recordIPReservationEvent("IPReservationSyncError", err.Error(), resvIPObj)
				return err
			} else {
				klog.V(5).Infof("Successfully reserved IP %s for IPReservation object %s/%s while syncing.",
					ipnet, resvIPObj.Namespace, resvIPObj.Name)
			}
		}
		IPResvState := &ipReservationState{key, resvIPObj.Spec, resvIPObj.Status.ReservedIPs}
		c.ipresvCache.Store(key, IPResvState)
	}
	return nil
}
