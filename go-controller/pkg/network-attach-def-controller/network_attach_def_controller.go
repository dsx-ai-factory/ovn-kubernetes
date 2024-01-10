package networkAttachDefController

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	kapi "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	nadclientset "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/client/clientset/versioned"
	nadinformers "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/client/informers/externalversions"
	nadlisters "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/client/listers/k8s.cni.cncf.io/v1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/syncmap"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

const (
	// maxRetries is the number of times a object will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the
	// sequence of delays between successive queuings of an object.
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	// maxRetries = 15

	avoidResync     = 0
	numberOfWorkers = 2
	qps             = 15
	maxRetries      = 10
)

var ErrNetworkControllerTopologyNotManaged = errors.New("no cluster network controller to manage topology")

type BaseNetworkController interface {
	Start(ctx context.Context) error
	Stop()
	GetNetworkName() string
}

type NetworkController interface {
	InterConnectible
	BaseNetworkController
	CompareNetInfo(util.BasicNetInfo) bool
	AddNAD(string, *util.NADConfig)
	DeleteNAD(nadName string)
	HasNAD(nadName string) bool
	// Cleanup cleans up the given network, it could be called to clean up network controllers that are deleted when
	// ovn-k8s is down; so it's receiver could be a dummy network controller.
	Cleanup(string) error
}

type InterConnectible interface {
	NADToInterConnect() string
	// - Request the specified network to inter-connect/disconnect with the other network by its inter-connect information;
	// - Only the side returns NADToInterConnect() will intiate inter-connect and disconnect;
	// - Inter-connect will only be done when both Network controller are started successfully, and disconnect will be
	//     done when either one is stopped.
	StartInterConnect(icInfo *util.InterConnectInfo) error
	StopInterConnect(icInfo *util.InterConnectInfo) error
}

// NetworkControllerManager manages all network controllers
type NetworkControllerManager interface {
	NewNetworkController(netInfo util.NetInfo) (NetworkController, error)
	CleanupDeletedNetworks(allControllers []NetworkController) error
}

type networkNADInfo struct {
	nadNames  map[string]struct{}
	nc        NetworkController
	isStarted bool
}

type nadNetInfo struct {
	util.NetInfo
	nadConf *util.NADConfig
}

type interConnectInfo struct {
	// list of active NADs request to connect the passive NAD
	activeNADList map[string]struct{}
	activeICInfo  *util.InterConnectInfo
	passiveICInfo *util.InterConnectInfo
	isConnected   bool
}

type NetAttachDefinitionController struct {
	name               string
	recorder           record.EventRecorder
	ncm                NetworkControllerManager
	nadFactory         nadinformers.SharedInformerFactory
	netAttachDefLister nadlisters.NetworkAttachmentDefinitionLister
	netAttachDefSynced cache.InformerSynced
	queue              workqueue.RateLimitingInterface
	loopPeriod         time.Duration
	stopChan           chan struct{}
	wg                 sync.WaitGroup

	// key is nadName, value is nadNetInfo
	perNADNetInfo *syncmap.SyncMap[*nadNetInfo]
	// controller for all networks, key is netName of net-attach-def, value is networkNADInfo
	// this map is updated either at the very beginning of ovnkube controller when initializing the
	// default controller or when net-attach-def is added/deleted. All these are serialized by syncmap lock
	perNetworkNADInfo *syncmap.SyncMap[*networkNADInfo]

	passiveNADControllerInfoMutex sync.RWMutex
	// key is passive NAD name, entry is its associated inter-connection info
	passiveNADControllerInfoMap map[string]*interConnectInfo
}

func NewNetAttachDefinitionController(name string, ncm NetworkControllerManager, networkAttchDefClient nadclientset.Interface,
	recorder record.EventRecorder) (*NetAttachDefinitionController, error) {
	nadFactory := nadinformers.NewSharedInformerFactoryWithOptions(
		networkAttchDefClient,
		avoidResync,
	)
	netAttachDefInformer := nadFactory.K8sCniCncfIo().V1().NetworkAttachmentDefinitions()
	rateLimiter := workqueue.NewMaxOfRateLimiter(
		workqueue.NewItemExponentialFailureRateLimiter(5*time.Millisecond, 1000*time.Second),
		&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(qps), qps*5)})

	nadController := &NetAttachDefinitionController{
		name:                        name,
		recorder:                    recorder,
		ncm:                         ncm,
		nadFactory:                  nadFactory,
		netAttachDefLister:          netAttachDefInformer.Lister(),
		netAttachDefSynced:          netAttachDefInformer.Informer().HasSynced,
		queue:                       workqueue.NewNamedRateLimitingQueue(rateLimiter, "net-attach-def"),
		loopPeriod:                  time.Second,
		stopChan:                    make(chan struct{}),
		perNADNetInfo:               syncmap.NewSyncMap[*nadNetInfo](),
		perNetworkNADInfo:           syncmap.NewSyncMap[*networkNADInfo](),
		passiveNADControllerInfoMap: map[string]*interConnectInfo{},
	}
	_, err := netAttachDefInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    nadController.onNetworkAttachDefinitionAdd,
			UpdateFunc: nadController.onNetworkAttachDefinitionUpdate,
			DeleteFunc: nadController.onNetworkAttachDefinitionDelete,
		})
	if err != nil {
		return nil, err
	}
	return nadController, nil

}

func (nadController *NetAttachDefinitionController) Start() error {
	klog.Infof("Starting %s NAD controller", nadController.name)
	g := errgroup.Group{}
	// run on a goroutine so that we can be stopped if we happen to
	// be stuck waiting for cache sync
	g.Go(nadController.start)
	return g.Wait()
}

func (nadController *NetAttachDefinitionController) start() error {
	nadController.nadFactory.Start(nadController.stopChan)
	if !util.WaitForInformerCacheSyncWithTimeout(nadController.name, nadController.stopChan, nadController.netAttachDefSynced) {
		return fmt.Errorf("stop requested while syncing %s caches", nadController.name)
	}

	err := nadController.SyncNetworkControllers()
	if err != nil {
		return fmt.Errorf("failed to sync all existing NAD entries: %v", err)
	}

	// add a dummy default network net-attach-def to create/start default network controller in case no default network
	// net-attach-def exists.
	err = nadController.AddNetAttachDefCommon(nadController.ncm, types.DefaultNetworkName, &util.DefaultNetInfo{}, nil, nil, true)
	if err != nil && !errors.Is(err, ErrNetworkControllerTopologyNotManaged) {
		return fmt.Errorf("failed to start default network controller: %v", err)
	}

	klog.Infof("Starting workers for %s NAD controller", nadController.name)
	for i := 0; i < numberOfWorkers; i++ {
		nadController.wg.Add(1)
		go func() {
			defer nadController.wg.Done()
			wait.Until(nadController.worker, nadController.loopPeriod, nadController.stopChan)
		}()
	}

	return nil
}

func (nadController *NetAttachDefinitionController) Stop() {
	klog.Infof("Shutting down %s NAD controller", nadController.name)

	close(nadController.stopChan)
	nadController.queue.ShutDown()

	// wait for the workers to terminate
	nadController.wg.Wait()

	// stop each network controller
	for _, oc := range nadController.getAllNetworkControllers() {
		oc.Stop()
	}
}

func (nadController *NetAttachDefinitionController) SyncNetworkControllers() (err error) {
	startTime := time.Now()
	klog.V(4).Infof("Starting repairing loop for %s", nadController.name)
	defer func() {
		klog.V(4).Infof("Finished repairing loop for %s: %v err: %v", nadController.name,
			time.Since(startTime), err)
	}()

	err = nadController.AddNetAttachDefCommon(nadController.ncm, types.DefaultNetworkName, &util.DefaultNetInfo{}, nil, nil, false)
	if err != nil && !errors.Is(err, ErrNetworkControllerTopologyNotManaged) {
		return fmt.Errorf("failed to initialize default network controller: %v", err)
	}

	existingNADs, err := nadController.netAttachDefLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("failed to get list of all net-attach-def")
	}

	// Need to walk through all the NADs and create all network controllers and update their list of NADs.
	// The controller can only be started after all known NADs are added so as to avoid to the extent possible
	// the errors and retries that would result if the controller attempted to process pods attached with NADs
	// we wouldn't otherwise know about yet
	for _, nad := range existingNADs {
		err = nadController.AddNetAttachDef(nadController.ncm, nad, false)
		// Ignore the error if there is no network controller to manager a topology
		if err != nil && !errors.Is(err, ErrNetworkControllerTopologyNotManaged) {
			return err
		}
	}

	return nadController.ncm.CleanupDeletedNetworks(nadController.getAllNetworkControllers())
}

func (nadController *NetAttachDefinitionController) worker() {
	for nadController.processNextWorkItem() {
	}
}

func (nadController *NetAttachDefinitionController) processNextWorkItem() bool {
	key, quit := nadController.queue.Get()
	if quit {
		return false
	}
	defer nadController.queue.Done(key)

	err := nadController.sync(key.(string))
	nadController.handleErr(err, key)
	return true
}

func (nadController *NetAttachDefinitionController) sync(key string) error {
	startTime := time.Now()
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	klog.V(5).Infof("%s: Sync net-attach-def %s/%s", nadController.name, namespace, name)
	defer func() {
		klog.V(4).Infof("%s: Finished syncing net-attach-def %s/%s: %v", nadController.name, namespace, name, time.Since(startTime))
	}()

	nad, err := nadController.netAttachDefLister.NetworkAttachmentDefinitions(namespace).Get(name)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	if nad == nil {
		return nadController.DeleteNetAttachDef(key)
	} else {
		return nadController.AddNetAttachDef(nadController.ncm, nad, true)
	}
}

func (nadController *NetAttachDefinitionController) handleErr(err error, key interface{}) {
	ns, name, keyErr := cache.SplitMetaNamespaceKey(key.(string))
	if keyErr != nil {
		klog.ErrorS(err, "Failed to split meta namespace cache key", "key", key)
	}

	if errors.Is(err, ErrNetworkControllerTopologyNotManaged) {
		klog.V(2).Infof("%s: Dropping net-attach-def %q out of the queue. No network controller to manage it.", nadController.name, key)
		err = nil
	}

	if err == nil {
		nadController.queue.Forget(key)
		return
	}

	if nadController.queue.NumRequeues(key) < maxRetries {
		nadController.queue.AddRateLimited(key)
		klog.V(2).InfoS("Error syncing net-attach-def, retrying", "net-attach-def", klog.KRef(ns, name), "err", err)
		return
	}

	// NAD failed to be added/deleted after maxRetries, it is a fatal error, exit the program.
	klog.Fatalf("%s: Failed processing net-attach-def %q for %d times, drop it out of the queue: %v",
		nadController.name, key, maxRetries, err)
	nadController.queue.Forget(key)
	utilruntime.HandleError(err)
}

func (nadController *NetAttachDefinitionController) queueNetworkAttachDefinition(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("%s: couldn't get key for net-attach-def %+v: %v", nadController.name, obj, err))
		return
	}
	nadController.queue.Add(key)
}

func (nadController *NetAttachDefinitionController) onNetworkAttachDefinitionAdd(obj interface{}) {
	nad := obj.(*nettypes.NetworkAttachmentDefinition)
	if nad == nil {
		utilruntime.HandleError(fmt.Errorf("%s: invalid net-attach-def provided to onNetworkAttachDefinitionAdd()", nadController.name))
		return
	}

	klog.V(4).Infof("%s: Adding net-attach-def %s/%s", nadController.name, nad.Namespace, nad.Name)
	nadController.queueNetworkAttachDefinition(obj)
}

func (nadController *NetAttachDefinitionController) onNetworkAttachDefinitionUpdate(oldObj, newObj interface{}) {
	oldNAD := oldObj.(*nettypes.NetworkAttachmentDefinition)
	newNAD := newObj.(*nettypes.NetworkAttachmentDefinition)
	if oldNAD == nil || newNAD == nil {
		utilruntime.HandleError(fmt.Errorf("%s: invalid net-attach-def provided to onNetworkAttachDefinitionUpdate()", nadController.name))
		return
	}

	// don't process resync or objects that are marked for deletion
	if oldNAD.ResourceVersion == newNAD.ResourceVersion ||
		!newNAD.GetDeletionTimestamp().IsZero() {
		return
	}

	// only allow NADConfig update
	nInfo1, nadConf1, err1 := util.ParseNADInfo(oldNAD)
	if err1 == nil {
		nInfo2, nadConf2, err2 := util.ParseNADInfo(newNAD)
		if err2 == nil {
			if nInfo1.CompareNetInfo(nInfo2) {
				if util.IsNADConfSame(nadConf1, nadConf2) {
					return
				}
				nadController.queueNetworkAttachDefinition(newObj)
				return
			}
		}
	}

	err := fmt.Sprintf("%s: Updating net-attach-def %s/%s is not supported", nadController.name, newNAD.Namespace, newNAD.Name)
	nadRef := kapi.ObjectReference{
		Kind:      "NetworkAttachmentDefinition",
		Namespace: newNAD.Namespace,
		Name:      newNAD.Name,
	}
	nadController.recorder.Eventf(&nadRef, kapi.EventTypeWarning, "ErrorUpdatingResource", err)
	klog.Warningf(err)
}

func (nadController *NetAttachDefinitionController) onNetworkAttachDefinitionDelete(obj interface{}) {
	nad, ok := obj.(*nettypes.NetworkAttachmentDefinition)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		nad, ok = tombstone.Obj.(*nettypes.NetworkAttachmentDefinition)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a NetworkAttachmentDefinition object %#v", obj))
			return
		}
	}
	klog.V(4).Infof("%s: Deleting net-attach-def %s/%s", nadController.name, nad.Namespace, nad.Name)
	nadController.queueNetworkAttachDefinition(nad)
}

// getAllNetworkControllers returns a snapshot of all managed NAD associated network controllers.
// Caller needs to note that there are no guarantees the return results reflect the real time
// condition. There maybe more controllers being added, and returned controllers may be deleted
func (nadController *NetAttachDefinitionController) getAllNetworkControllers() []NetworkController {
	allNetworkNames := nadController.perNetworkNADInfo.GetKeys()
	allNetworkControllers := make([]NetworkController, 0, len(allNetworkNames))
	for _, netName := range allNetworkNames {
		nadController.perNetworkNADInfo.LockKey(netName)
		nni, ok := nadController.perNetworkNADInfo.Load(netName)
		if ok {
			allNetworkControllers = append(allNetworkControllers, nni.nc)
		}
		nadController.perNetworkNADInfo.UnlockKey(netName)
	}
	return allNetworkControllers
}

// AddNetAttachDef adds the given nad to the associated controller. It creates the controller if this
// is the first NAD of the network.
// Non-retriable errors (configuration error etc.) are just logged, and the function immediately returns nil.
func (nadController *NetAttachDefinitionController) AddNetAttachDef(ncm NetworkControllerManager,
	netattachdef *nettypes.NetworkAttachmentDefinition, doStart bool) error {

	netAttachDefName := util.GetNADName(netattachdef.Namespace, netattachdef.Name)
	klog.Infof("%s: Add net-attach-def %s", nadController.name, netAttachDefName)

	nInfo, nadConf, invalidNADErr := util.ParseNADInfo(netattachdef)
	return nadController.AddNetAttachDefCommon(ncm, netAttachDefName, nInfo, nadConf, invalidNADErr, doStart)
}

func (nadController *NetAttachDefinitionController) AddNetAttachDefCommon(ncm NetworkControllerManager,
	netAttachDefName string, nInfo util.NetInfo, nadConf *util.NADConfig, invalidNADErr error, doStart bool) error {
	var netName string
	var err error

	if invalidNADErr == nil {
		netName = nInfo.GetNetworkName()
	}
	return nadController.perNADNetInfo.DoWithLock(netAttachDefName, func(nadName string) error {
		nadNci, loaded := nadController.perNADNetInfo.LoadOrStore(nadName, &nadNetInfo{
			NetInfo: nInfo,
			nadConf: nadConf,
		})
		if !loaded {
			// first time to process this nad
			if invalidNADErr != nil {
				// invalid nad, nothing to do
				klog.Warningf("%s: net-attach-def %s is first seen and is invalid: %v", nadController.name, nadName, invalidNADErr)
				nadController.perNADNetInfo.Delete(nadName)
				return nil
			}
			klog.V(5).Infof("%s: net-attach-def %s network %s first seen", nadController.name, nadName, netName)
			err = nadController.addNADToController(ncm, nadName, nInfo, nadConf, doStart)
			if err != nil {
				klog.Errorf("%s: Failed to add net-attach-def %s to network %s: %v", nadController.name, nadName, netName, err)
				nadController.perNADNetInfo.Delete(nadName)
				return err
			}
		} else {
			klog.V(5).Infof("%s: net-attach-def %s network %s already exists", nadController.name, nadName, netName)
			nadUpdated := false
			if invalidNADErr != nil {
				nadUpdated = true
			} else if !nadNci.CompareNetInfo(nInfo) {
				// netconf spec changed
				klog.V(5).Infof("%s: net-attach-def %s spec has changed", nadController.name, nadName)
				nadUpdated = true
			}
			nadConfUpdated := !util.IsNADConfSame(nadNci.nadConf, nadConf)

			if !nadUpdated {
				// nothing changed, may still need to start the controller or update its NADConfig
				if !doStart && !nadConfUpdated {
					return nil
				}
				err = nadController.addNADToController(ncm, nadName, nInfo, nadConf, doStart)
				if err != nil {
					klog.Errorf("%s: Failed to add net-attach-def %s to network %s: %v", nadController.name, nadName, netName, err)
					return err
				}
				return nil
			}
			if nadUpdated {
				klog.V(5).Infof("%s: net-attach-def %s network %s updated", nadController.name, nadName, netName)
				// delete the NAD from the old network first
				oldNetName := nadNci.GetNetworkName()
				err := nadController.deleteNADFromController(nadNci, nadName)
				if err != nil {
					klog.Errorf("%s: Failed to delete net-attach-def %s from network %s: %v", nadController.name, nadName, oldNetName, err)
					return err
				}
				nadController.perNADNetInfo.Delete(nadName)
			}
			if invalidNADErr != nil {
				klog.Warningf("%s: net-attach-def %s is invalid: %v", nadController.name, nadName, invalidNADErr)
				return nil
			}
			if nadUpdated {
				// directly return error, not to update the new NAD to the controller, so that to avoid lock deadlock for inter-connection
				return fmt.Errorf("net-attach-def %s updated, retry...", nadName)
			}
			klog.V(5).Infof("%s: Add updated net-attach-def %s to network %s", nadController.name, nadName, netName)
			nadController.perNADNetInfo.LoadOrStore(nadName, &nadNetInfo{NetInfo: nInfo, nadConf: nadConf})
			err = nadController.addNADToController(ncm, nadName, nInfo, nadConf, doStart)
			if err != nil {
				klog.Errorf("%s: Failed to add net-attach-def %s to network %s: %v", nadController.name, nadName, netName, err)
				nadController.perNADNetInfo.Delete(nadName)
				return err
			}
			return nil
		}
		return nil
	})
}

// DeleteNetAttachDef deletes the given NAD from the associated controller. It delete the controller if this
// is the last NAD of the network
func (nadController *NetAttachDefinitionController) DeleteNetAttachDef(netAttachDefName string) error {
	klog.Infof("%s: Delete net-attach-def %s", nadController.name, netAttachDefName)
	return nadController.perNADNetInfo.DoWithLock(netAttachDefName, func(nadName string) error {
		existingNadNetInfo, found := nadController.perNADNetInfo.Load(nadName)
		if !found {
			klog.V(5).Infof("%s: net-attach-def %s not found for removal", nadController.name, nadName)
			return nil
		}
		netName := existingNadNetInfo.GetNetworkName()
		err := nadController.deleteNADFromController(existingNadNetInfo, nadName)
		if err != nil {
			klog.Errorf("%s: Failed to delete net-attach-def %s from network %s: %v", nadController.name, nadName, netName, err)
			return err
		}
		nadController.perNADNetInfo.Delete(nadName)
		return nil
	})
}

func (nadController *NetAttachDefinitionController) addNADToController(ncm NetworkControllerManager, nadName string,
	nInfo util.NetInfo, nadConf *util.NADConfig, doStart bool) (err error) {
	var oc NetworkController
	var nadExists, isStarted bool

	netName := nInfo.GetNetworkName()
	klog.V(5).Infof("%s: Add net-attach-def %s to network %s", nadController.name, nadName, netName)
	return nadController.perNetworkNADInfo.DoWithLock(netName, func(networkName string) error {
		nni, loaded := nadController.perNetworkNADInfo.LoadOrStore(networkName, &networkNADInfo{
			nadNames:  map[string]struct{}{},
			nc:        nil,
			isStarted: false,
		})
		if !loaded {
			defer func() {
				if err != nil {
					nadController.perNetworkNADInfo.Delete(networkName)
				}
			}()
			// first NAD for this network, create controller
			klog.V(5).Infof("%s: First net-attach-def %s of network %s added, create network controller", nadController.name, nadName, networkName)
			oc, err = ncm.NewNetworkController(nInfo)
			if err != nil {
				return err
			}
			nni.nc = oc
		} else {
			klog.V(5).Infof("%s: net-attach-def %s added to existing network %s", nadController.name, nadName, networkName)
			// controller of this network already exists
			oc = nni.nc
			isStarted = nni.isStarted
			_, nadExists = nni.nadNames[nadName]

			if !oc.CompareNetInfo(nInfo) {
				if nadExists {
					// this should not happen, continue to start the existing controller if requested
					return fmt.Errorf("%s: net-attach-def %s netconf spec changed, should not happen", nadController.name, networkName)
				} else {
					return fmt.Errorf("%s: NAD %s does not share the same CNI config with network %s",
						nadController.name, nadName, networkName)
				}
			}
		}
		if !nadExists {
			nni.nadNames[nadName] = struct{}{}
			nni.nc.AddNAD(nadName, nadConf)
			defer func() {
				if err != nil {
					delete(nni.nadNames, nadName)
					nni.nc.DeleteNAD(nadName)
				}
			}()
		} else {
			// update NADConf
			nni.nc.AddNAD(nadName, nadConf)
		}

		if !doStart || isStarted {
			return nil
		}

		klog.V(5).Infof("%s: Start network controller for network %s", nadController.name, networkName)
		// start the controller if requested
		err = oc.Start(context.TODO())
		if err != nil {
			return fmt.Errorf("network controller for network %s failed to be started: %v", networkName, err)
		}
		nni.isStarted = true
		if nadToConnect := oc.NADToInterConnect(); nadToConnect != "" {
			// active side
			if nadToConnect == nadName {
				klog.Errorf("Error: NAD %s is requested to inter-connect itself", nadName)
				return nil
			} else if nInfo.GetInterConnectInfo() == nil {
				klog.Errorf("Error: NAD %s does not support inter-connect", nadName)
				return nil
			}
			_ = nadController.perNADNetInfo.DoWithLock(nadToConnect, func(passiveNADName string) error {
				passiveNADNetInfo, passiveNADAdded := nadController.perNADNetInfo.Load(passiveNADName)
				klog.V(5).Infof("Active NAD %s is requested to connect to passive NAD %s, passive NAD found :%v",
					nadName, passiveNADName, passiveNADAdded)
				nadController.passiveNADControllerInfoMutex.Lock()
				defer nadController.passiveNADControllerInfoMutex.Unlock()
				icInfo, err := nadController.AddActive(passiveNADName, nadName, nInfo.GetInterConnectInfo(), passiveNADNetInfo)
				if err != nil {
					klog.Errorf("Error in inter-connect NAD %s to NAD %s: %v", nadName, passiveNADName, err)
					return nil
				}
				if !icInfo.isConnected && icInfo.passiveICInfo != nil {
					klog.V(5).Infof("Active NAD %s connects to passive NAD %s", nadName, passiveNADName)
					err = oc.StartInterConnect(icInfo.passiveICInfo)
					if err != nil {
						klog.Warningf("Network %s connect to %s failed: %v", oc.GetNetworkName(), passiveNADName)
					}
					icInfo.isConnected = (err == nil)
				}
				return nil
			})
		} else {
			// passive side
			nadController.passiveNADControllerInfoMutex.Lock()
			defer nadController.passiveNADControllerInfoMutex.Unlock()
			icInfo := nadController.passiveNADControllerInfoMap[nadName]
			if icInfo == nil || icInfo.isConnected {
				return nil
			}
			if nInfo.GetInterConnectInfo() == nil {
				// passive side does not support does not support interconnection
				klog.Errorf("NAD/network (%s)/%s is requested to inter-connect to network %s which it does not support", nadName, oc.GetNetworkName(), icInfo.activeICInfo.NetName)
				return nil
			}
			klog.V(5).Infof("Passive NAD %s is up and is to connect to active network %s",
				nadName, icInfo.activeICInfo.NetName)
			icInfo.passiveICInfo = nInfo.GetInterConnectInfo()
			err = oc.StartInterConnect(icInfo.activeICInfo)
			if err != nil {
				klog.Warningf("NAD %s connect to network %s failed: %v", nadName, icInfo.activeICInfo.NetName, err)
			}
			icInfo.isConnected = (err == nil)
		}
		return nil
	})
}

func (nadController *NetAttachDefinitionController) deleteNADFromController(nadNci *nadNetInfo, nadName string) error {
	netName := nadNci.GetNetworkName()
	klog.V(5).Infof("%s: Delete net-attach-def %s from network %s", nadController.name, nadName, netName)
	return nadController.perNetworkNADInfo.DoWithLock(netName, func(networkName string) error {
		nni, found := nadController.perNetworkNADInfo.Load(networkName)
		if !found {
			klog.V(5).Infof("%s: Network controller for network %s not found", nadController.name, networkName)
			return nil
		}

		_, nadExists := nni.nadNames[nadName]
		if !nadExists {
			klog.V(5).Infof("%s: Unable to remove NAD %s, does not exist on network %s", nadController.name, nadName, networkName)
			return nil
		}

		oc := nni.nc
		nadController.passiveNADControllerInfoMutex.Lock()
		if peerNADName := nadNci.NADToInterConnect(); peerNADName != "" {
			// active side
			icInfo := nadController.passiveNADControllerInfoMap[peerNADName]
			if icInfo != nil {
				if _, ok := icInfo.activeNADList[nadName]; ok {
					if len(icInfo.activeNADList) == 1 {
						// the last NAD of the active network connect to nadName
						if icInfo.isConnected {
							if err := oc.StopInterConnect(icInfo.passiveICInfo); err != nil {
								nadController.passiveNADControllerInfoMutex.Unlock()
								return fmt.Errorf("failed to inter-disconnect network controller %s with NAD %s: %v", networkName, peerNADName, err)
							}
						}
						delete(nadController.passiveNADControllerInfoMap, peerNADName)
					} else {
						delete(icInfo.activeNADList, nadName)
					}
				}
			}
		} else {
			// passive side
			icInfo := nadController.passiveNADControllerInfoMap[nadName]
			if icInfo != nil {
				if icInfo.isConnected {
					if err := oc.StopInterConnect(icInfo.activeICInfo); err != nil {
						nadController.passiveNADControllerInfoMutex.Unlock()
						return fmt.Errorf("failed to inter-disconnect network controller %s with NAD %s: %v", networkName, peerNADName, err)
					}
					icInfo.isConnected = false
				}
				icInfo.passiveICInfo = nil
			}
		}
		nadController.passiveNADControllerInfoMutex.Unlock()

		delete(nni.nadNames, nadName)
		if len(nni.nadNames) == 0 {
			klog.V(5).Infof("%s: The last NAD: %s of network %s has been deleted, stopping network controller", nadController.name, nadName, networkName)
			oc.Stop()
			nni.isStarted = false
			err := oc.Cleanup(oc.GetNetworkName())
			// set isStarted to false even stop failed, as the operation could be half-done.
			// So if a new NAD with the same netconf comes in, it can restart the controller.
			nni.isStarted = false
			if err != nil {
				nni.nadNames[nadName] = struct{}{}
				return fmt.Errorf("%s: failed to stop network controller for network %s: %v", nadController.name, networkName, err)
			}
			nadController.perNetworkNADInfo.Delete(networkName)
		}
		nni.nc.DeleteNAD(nadName)
		klog.V(5).Infof("%s: Delete NAD %s from controller of network %s", nadController.name, nadName, networkName)
		return nil
	})
}

// AddActive() update per-passiveNAD interConnectInfo map, where activeInterConnectInfo is always valid, but
// passiveNADNetInfo may be nil or invalid (passive NAD not handled yet)
func (nadController *NetAttachDefinitionController) AddActive(passiveNADName, activeNADName string,
	activeInterConnectInfo *util.InterConnectInfo, passiveNADNetInfo *nadNetInfo) (*interConnectInfo, error) {
	var passiveInterConnectInfo *util.InterConnectInfo
	activeNetName := activeInterConnectInfo.NetName
	if passiveNADNetInfo != nil {
		passiveInterConnectInfo = passiveNADNetInfo.GetInterConnectInfo()
		if passiveInterConnectInfo == nil {
			return nil, fmt.Errorf("NAD %s does not support inter-connect", passiveNADName)
		}
		if activeNetName == passiveInterConnectInfo.NetName {
			return nil, fmt.Errorf("NAD %s to NAD %s belongs to the same network %s",
				activeNADName, passiveNADName, activeNetName)
		}
	}
	info, ok := nadController.passiveNADControllerInfoMap[passiveNADName]
	if !ok {
		info = &interConnectInfo{
			activeNADList: map[string]struct{}{},
			activeICInfo:  activeInterConnectInfo,
			passiveICInfo: passiveInterConnectInfo,
			isConnected:   false,
		}
		nadController.passiveNADControllerInfoMap[passiveNADName] = info
	} else {
		if info.activeICInfo.NetName != activeInterConnectInfo.NetName {
			return nil, fmt.Errorf("NAD %s is requested to connect to two different networks %s and %s",
				passiveNADName, info.activeICInfo.NetName, activeInterConnectInfo.NetName)
		}
		info.passiveICInfo = passiveInterConnectInfo
	}
	info.activeNADList[activeNADName] = struct{}{}
	return info, nil
}
