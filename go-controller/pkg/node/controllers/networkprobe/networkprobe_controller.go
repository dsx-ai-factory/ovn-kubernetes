package networkprobe

import (
	"fmt"
	"reflect"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	v1coreinformers "k8s.io/client-go/informers/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	networkprobe "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/networkprobe/v1beta1"
	networkprobeclientset "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/networkprobe/v1beta1/apis/clientset/versioned"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/networkprobe/v1beta1/apis/informers/externalversions/networkprobe/v1beta1"
	listers "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/networkprobe/v1beta1/apis/listers/networkprobe/v1beta1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

// Controller manages NetworkProbe resources
type Controller struct {
	networkProbeLister    listers.NetworkProbeLister
	networkProbesSynced   cache.InformerSynced
	networkProbeCache     map[string]*NetworkProbeState
	networkProbeClientSet networkprobeclientset.Interface
	networkProbeQueue     workqueue.RateLimitingInterface
	networkProbeMutex     sync.Mutex

	nodeLister corelisters.NodeLister
	nodeQueue  workqueue.RateLimitingInterface
	nodeSynced cache.InformerSynced

	configMapLister corelisters.ConfigMapLister
	configMapSynced cache.InformerSynced

	secretLister corelisters.SecretLister
	secretSynced cache.InformerSynced

	nodeName      string
	stopCh        <-chan struct{}
	eventRecorder record.EventRecorder
}

func NewController(
	networkProbeClient networkprobeclientset.Interface,
	stopCh <-chan struct{},
	networkProbeInformer v1beta1.NetworkProbeInformer,
	nodeInformer v1coreinformers.NodeInformer,
	configMapInformer v1coreinformers.ConfigMapInformer,
	secretInformer v1coreinformers.SecretInformer,
	nodeName string,
	recorder record.EventRecorder) (*Controller, error) {

	c := &Controller{
		networkProbeClientSet: networkProbeClient,
		stopCh:                stopCh,
		configMapLister:       configMapInformer.Lister(),
		secretLister:          secretInformer.Lister(),
		networkProbeCache:     make(map[string]*NetworkProbeState),
		nodeName:              nodeName,
		eventRecorder:         recorder,
	}
	klog.Infof("Setting up event handlers for networkprobe controller")

	c.networkProbeLister = networkProbeInformer.Lister()
	c.networkProbeQueue = workqueue.NewRateLimitingQueueWithConfig(
		workqueue.NewItemFastSlowRateLimiter(1*time.Second, 5*time.Second, 5),
		workqueue.RateLimitingQueueConfig{Name: "NetworkProbes"},
	)

	c.networkProbesSynced = networkProbeInformer.Informer().HasSynced

	_, err := networkProbeInformer.Informer().AddEventHandler(factory.WithUpdateHandlingForObjReplace(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onNetworkProbeAdd,
		UpdateFunc: c.onNetworkProbeUpdate,
		DeleteFunc: c.onNetworkProbeDelete,
	}))

	if err != nil {
		return nil, fmt.Errorf("could not add Event Handler for networkprobeInformer during networkprobeController initialization, %w", err)
	}

	c.nodeLister = nodeInformer.Lister()
	c.nodeQueue = workqueue.NewRateLimitingQueueWithConfig(
		workqueue.NewItemFastSlowRateLimiter(1*time.Second, 5*time.Second, 5),
		workqueue.RateLimitingQueueConfig{Name: "NetworkProbeNodes"},
	)
	c.nodeSynced = nodeInformer.Informer().HasSynced

	_, err = nodeInformer.Informer().AddEventHandler(factory.WithUpdateHandlingForObjReplace(cache.ResourceEventHandlerFuncs{
		AddFunc:    nil,
		UpdateFunc: c.onNetworkProbeNodeUpdate,
		DeleteFunc: nil,
	}))

	if err != nil {
		return nil, fmt.Errorf("could not add Event Handler for nodeInformer during networkprobeController initialization, %w", err)
	}

	c.configMapSynced = configMapInformer.Informer().HasSynced
	c.secretSynced = secretInformer.Informer().HasSynced

	return c, nil
}

// Run starts the Controller
func (c *Controller) Run(wg *sync.WaitGroup, workers int) error {
	defer utilruntime.HandleCrash()

	klog.Infof("Starting networkprobe Controller")

	if !util.WaitForInformerCacheSyncWithTimeout("networkprobes", c.stopCh, c.networkProbesSynced, c.nodeSynced, c.configMapSynced, c.secretSynced) {
		return fmt.Errorf("timed out waiting for all caches to sync")
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wait.Until(
				func() {
					c.runNetworkProbeWorker(wg)
				}, time.Second, c.stopCh)
		}()
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wait.Until(
				func() {
					c.runNetworkProbeNodeWorker(wg)
				}, time.Second, c.stopCh)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		// wait until we're told to stop
		<-c.stopCh

		defer c.deleteAllNetworkProbes()
		defer c.networkProbeQueue.ShutDown()
		defer c.nodeQueue.ShutDown()
	}()

	return nil
}

func (c *Controller) runNetworkProbeNodeWorker(wg *sync.WaitGroup) {
	for c.processNextNetworkProbeNodeWorkItem(wg) {
	}
}

func (c *Controller) processNextNetworkProbeNodeWorkItem(wg *sync.WaitGroup) bool {
	wg.Add(1)
	defer wg.Done()

	key, quit := c.nodeQueue.Get()
	if quit {
		return false
	}

	defer c.nodeQueue.Done(key)

	err := c.syncNetworkProbeNode(key.(string))
	if err == nil {
		c.nodeQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("%v failed with : %v", key, err))

	if c.nodeQueue.NumRequeues(key) < maxRetries {
		c.nodeQueue.AddRateLimited(key)
		return true
	}

	c.nodeQueue.Forget(key)
	return true
}

func (c *Controller) processNextNetworkProbeWorkItem(wg *sync.WaitGroup) bool {
	wg.Add(1)
	defer wg.Done()

	key, quit := c.networkProbeQueue.Get()
	if quit {
		return false
	}

	defer c.networkProbeQueue.Done(key)

	err := c.syncNetworkProbe(key.(string))
	if err == nil {
		c.networkProbeQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("%v failed with : %v", key, err))

	if c.networkProbeQueue.NumRequeues(key) < maxRetries {
		c.networkProbeQueue.AddRateLimited(key)
		return true
	}

	c.networkProbeQueue.Forget(key)
	return true
}

func (c *Controller) runNetworkProbeWorker(wg *sync.WaitGroup) {
	for c.processNextNetworkProbeWorkItem(wg) {
	}
}

// onNetworkProbeAdd queues the network probe for processing.
func (c *Controller) onNetworkProbeAdd(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %+v: %v", obj, err))
		return
	}
	c.networkProbeQueue.Add(key)
}

// onNetworkProbeUpdate queues the network probe for processing.
func (c *Controller) onNetworkProbeUpdate(oldObj, newObj interface{}) {
	oldNetworkProbe := oldObj.(*networkprobe.NetworkProbe)
	newNetworkProbe := newObj.(*networkprobe.NetworkProbe)

	if oldNetworkProbe.ResourceVersion == newNetworkProbe.ResourceVersion ||
		!newNetworkProbe.GetDeletionTimestamp().IsZero() {
		return
	}

	if reflect.DeepEqual(oldNetworkProbe.Spec, newNetworkProbe.Spec) {
		return
	}

	key, err := cache.MetaNamespaceKeyFunc(newObj)
	if err == nil {
		c.networkProbeQueue.Add(key)
	}
}

// onNetworkProbeDelete queues the network probe for processing.
func (c *Controller) onNetworkProbeDelete(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %+v: %v", obj, err))
		return
	}
	c.networkProbeQueue.Add(key)
}

// onNetworkProbeUpdate queues the node for processing.
func (c *Controller) onNetworkProbeNodeUpdate(oldObj, newObj interface{}) {
	oldNode := oldObj.(*corev1.Node)
	newNode := newObj.(*corev1.Node)

	if oldNode.ResourceVersion == newNode.ResourceVersion ||
		!newNode.GetDeletionTimestamp().IsZero() {
		return
	}

	// don't process the update if node name doesn't match
	if oldNode.Name != c.nodeName || newNode.Name != c.nodeName {
		return
	}

	oldNodeLabels := labels.Set(oldNode.Labels)
	newNodeLabels := labels.Set(newNode.Labels)
	if labels.Equals(oldNodeLabels, newNodeLabels) {
		return
	}

	key, err := cache.MetaNamespaceKeyFunc(newObj)
	if err == nil {
		c.nodeQueue.Add(key)
	}
}

func (c *Controller) syncNetworkProbeNode(key string) error {
	c.networkProbeMutex.Lock()
	defer c.networkProbeMutex.Unlock()

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	startTime := time.Now()
	klog.V(4).Infof("Processing sync for node %s in networkprobe controller", key)
	defer func() {
		klog.V(4).Infof("Finished syncing node %s in networkprobe controller: %v", key, time.Since(startTime))
	}()

	node, err := c.nodeLister.Get(name)
	if err != nil {
		// return gracefully if node is not found
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	existingNetworkProbes, err := c.networkProbeLister.NetworkProbes("").List(labels.Everything())
	if err != nil {
		return err
	}

	nodeLabels := labels.Set(node.Labels)
	for _, networkProbe := range existingNetworkProbes {
		probeKey, err := cache.MetaNamespaceKeyFunc(networkProbe)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("couldn't get key for object %+v: %v", networkProbe, err))
			continue
		}
		probeState, loaded := c.networkProbeCache[probeKey]
		if !loaded {
			continue
		}

		// This case is for node label change
		if probeState.isProbeRunningOnThisNode {
			klog.Infof("Node %s used to match networkprobe %s, so requeing", name, probeKey)
			c.networkProbeQueue.Add(probeKey)
		}

		// when a new node labels matches networkProbe node selector
		if probeState.nodeSelector.Matches(nodeLabels) {
			klog.Infof("Node %s started to match networkprobe %s, so requeing", name, probeKey)
			c.networkProbeQueue.Add(probeKey)
		}
	}
	return nil
}

func (c *Controller) syncNetworkProbe(key string) error {
	c.networkProbeMutex.Lock()
	defer c.networkProbeMutex.Unlock()

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	startTime := time.Now()
	klog.V(4).Infof("Processing sync for networkprobe %s/%s", namespace, name)
	defer func() {
		klog.V(4).Infof("Finished syncing networkprobe %s/%s: %v", namespace, name, time.Since(startTime))
	}()

	networkProbe, err := c.networkProbeLister.NetworkProbes(namespace).Get(name)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if networkProbe == nil {
		// probe was deleted; let's clear up all the related resources to that
		err = c.deleteNetworkProbe(key)
		if err != nil {
			return err
		}
		return nil
	}

	statusMessage, statusReason, err := c.handleNetworkProbe(networkProbe, key)
	if err != nil {
		_ = c.UpdateNetworkProbeStatus(networkProbe.Namespace, networkProbe.Name, c.nodeName, err.Error(), statusReason)
		return err
	}
	return c.UpdateNetworkProbeStatus(networkProbe.Namespace, networkProbe.Name, c.nodeName, statusMessage, statusReason)
}

func (c *Controller) ShouldRunProbeOnNode(networkProbe *NetworkProbeState) (bool, error) {
	node, err := c.nodeLister.Get(c.nodeName)
	if err != nil {
		return false, fmt.Errorf("failed to get node %s :(%v)", c.nodeName, err)
	}

	// Check if the node labels matches the network probe node selector
	return networkProbe.nodeSelector.Matches(labels.Set(node.Labels)), nil
}
