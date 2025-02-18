package networkmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	nadlisters "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/client/listers/k8s.cni.cncf.io/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/controller"
	ratypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/routeadvertisements/v1"
	ralisters "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/routeadvertisements/v1/apis/listers/routeadvertisements/v1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

func newNetworkController(name, zone, node string, cm ControllerManager, wf watchFactory) *networkController {
	nc := &networkController{
		name:               fmt.Sprintf("[%s network controller]", name),
		node:               node,
		zone:               zone,
		cm:                 cm,
		networks:           map[string]util.MutableNetInfo{},
		networkControllers: map[string]*networkControllerState{},

		enableInterConnect:      zone != "",
		perNetworkConnectToNADs: map[string]*interConnectState{},
	}

	// this controller does not feed from an informer, networks are manually
	// added to the queue for processing
	networkConfig := &controller.ReconcilerConfig{
		RateLimiter: workqueue.DefaultTypedControllerRateLimiter[string](),
		Reconcile:   nc.syncNetwork,
		Threadiness: 1,
	}
	nc.networkReconciler = controller.NewReconciler(
		nc.name,
		networkConfig,
	)

	// we don't care about route advertisements in cluster manager
	if nc.hasRouteAdvertisements() {
		nc.nadLister = wf.NADInformer().Lister()
		nc.raLister = wf.RouteAdvertisementsInformer().Lister()
		nc.nodeLister = wf.NodeCoreInformer().Lister()

		// ra controller
		raConfig := &controller.ControllerConfig[ratypes.RouteAdvertisements]{
			RateLimiter:    workqueue.DefaultTypedControllerRateLimiter[string](),
			Informer:       wf.RouteAdvertisementsInformer().Informer(),
			Lister:         nc.raLister.List,
			Reconcile:      func(string) error { return nc.syncRunningNetworks() },
			ObjNeedsUpdate: raNeedsUpdate,
			Threadiness:    1,
		}
		nc.raController = controller.NewController(
			nc.name,
			raConfig,
		)

		// node controller
		nodeConfig := &controller.ControllerConfig[corev1.Node]{
			RateLimiter:    workqueue.DefaultTypedControllerRateLimiter[string](),
			Informer:       wf.NodeCoreInformer().Informer(),
			Lister:         nc.nodeLister.List,
			Reconcile:      func(string) error { return nc.syncRunningNetworks() },
			ObjNeedsUpdate: nodeNeedsUpdate,
			Threadiness:    1,
		}
		nc.nodeController = controller.NewController(
			nc.name,
			nodeConfig,
		)
	}

	return nc
}

type networkControllerState struct {
	controller         NetworkController
	stoppedAndDeleting bool
}

type networkController struct {
	sync.RWMutex

	name string
	zone string
	node string

	nadLister  nadlisters.NetworkAttachmentDefinitionLister
	raLister   ralisters.RouteAdvertisementsLister
	nodeLister corelisters.NodeLister

	networkReconciler controller.Reconciler
	raController      controller.Controller
	nodeController    controller.Controller

	cm                 ControllerManager
	networks           map[string]util.MutableNetInfo
	networkControllers map[string]*networkControllerState

	// network inter-connect needed
	enableInterConnect bool
	// maps the networkName of the inter-connect state this network connects to.
	// For every such networkName, we capture the interconnection state.
	// key is network name of the active side (layer2 network),
	// value is passive side information (of default/layer3 network)
	perNetworkConnectToNADs map[string]*interConnectState
}

// Start will cleanup stale networks that have not been ensured via
// EnsuredNetwork before this call
func (c *networkController) Start() error {
	controllers := []controller.Reconciler{c.networkReconciler}
	if c.raController != nil {
		controllers = append(controllers, c.raController)
	}
	if c.nodeController != nil {
		controllers = append(controllers, c.nodeController)
	}
	return controller.StartWithInitialSync(
		c.syncAll,
		controllers...,
	)
}

func (c *networkController) Stop() {
	controllers := []controller.Reconciler{c.networkReconciler}
	if c.raController != nil {
		controllers = append(controllers, c.raController)
	}
	if c.nodeController != nil {
		controllers = append(controllers, c.nodeController)
	}
	controller.Stop(controllers...)
	for _, networkControllerState := range c.getAllNetworkStates() {
		if networkControllerState.controller.GetNetworkName() == types.DefaultNetworkName {
			// we don't own the lifecycle of the default network, so don't stop
			// it
			continue
		}
		networkControllerState.controller.Stop()
	}
}

func (c *networkController) EnsureNetwork(network util.MutableNetInfo) {
	c.setNetwork(network.GetNetworkName(), network)
	c.networkReconciler.Reconcile(network.GetNetworkName())
}

func (c *networkController) DeleteNetwork(network string) {
	switch network {
	case types.DefaultNetworkName:
		// for the default network however ensure it runs with the default
		// config
		c.setNetwork(network, &util.DefaultNetInfo{})
	default:
		c.setNetwork(network, nil)
	}
	c.networkReconciler.Reconcile(network)
}

func (c *networkController) setNetwork(network string, netInfo util.MutableNetInfo) {
	c.Lock()
	defer c.Unlock()
	if netInfo == nil {
		delete(c.networks, network)
		return
	}
	c.networks[network] = netInfo
}

func (c *networkController) getNetwork(network string) util.MutableNetInfo {
	c.RLock()
	defer c.RUnlock()
	return c.networks[network]
}

func (c *networkController) getAllNetworks() []util.NetInfo {
	c.RLock()
	defer c.RUnlock()
	networks := make([]util.NetInfo, 0, len(c.networks))
	for _, network := range c.networks {
		networks = append(networks, network)
	}
	return networks
}

func (c *networkController) setNetworkState(network string, state *networkControllerState) {
	c.Lock()
	defer c.Unlock()
	if state == nil {
		delete(c.networkControllers, network)
		return
	}
	c.networkControllers[network] = state
}

func (c *networkController) getNetworkState(network string) *networkControllerState {
	c.RLock()
	defer c.RUnlock()
	state := c.networkControllers[network]
	if state == nil {
		return &networkControllerState{}
	}
	return state
}

func (c *networkController) getReconcilableNetworkState(network string) (BaseNetworkController, bool) {
	if network == types.DefaultNetworkName {
		return c.cm.GetDefaultNetworkController(), false
	}
	state := c.getNetworkState(network)
	return state.controller, state.stoppedAndDeleting
}

func (c *networkController) getAllNetworkStates() []*networkControllerState {
	c.RLock()
	defer c.RUnlock()
	networkStates := make([]*networkControllerState, 0, len(c.networks))
	for _, state := range c.networkControllers {
		networkStates = append(networkStates, state)
	}
	return networkStates
}

func (c *networkController) syncAll() error {
	// as we sync upon start, consider networks that have not been ensured as
	// stale and clean them up
	validNetworks := c.getAllNetworks()
	if err := c.cm.CleanupStaleNetworks(validNetworks...); err != nil {
		return err
	}

	// sync all known networks. There is no informer for networks. Keys are added by NAD controller.
	// Certain downstream controllers that handle configuration for multiple networks depend on being
	// aware of all the existing networks on initialization. To achieve that, we need to start existing
	// networks synchronously. Otherwise, these controllers might incorrectly assess valid configuration
	// as stale.
	start := time.Now()
	klog.Infof("%s: syncing all networks", c.name)
	for _, network := range validNetworks {
		err := c.syncNetwork(network.GetNetworkName())
		if errors.Is(err, ErrNetworkControllerTopologyNotManaged) {
			klog.V(5).Infof("Ignoring network %q since %q does not manage it", network.GetNetworkName(), c.name)
			continue
		}
		if err != nil {
			return fmt.Errorf("failed to sync network %s: %w", network.GetNetworkName(), err)
		}
	}
	klog.Infof("%s: finished syncing all networks. Time taken: %s", c.name, time.Since(start))
	return nil
}

func (c *networkController) syncRunningNetworks() error {
	c.networkReconciler.Reconcile(types.DefaultNetworkName)
	for _, network := range c.getAllNetworkStates() {
		c.networkReconciler.Reconcile(network.controller.GetNetworkName())
	}

	return nil
}

// syncNetwork must be called with nm mutex locked
func (c *networkController) syncNetwork(network string) error {
	startTime := time.Now()
	klog.V(5).Infof("%s: sync network %s", c.name, network)
	defer func() {
		klog.V(4).Infof("%s: finished syncing network %s, took %v", c.name, network, time.Since(startTime))
	}()

	have, stoppedAndDeleting := c.getReconcilableNetworkState(network)
	want := c.getNetwork(network)

	compatible := util.AreNetworksCompatible(have, want)

	// we will dispose of the old network if deletion is in progress or if
	// non-reconcilable configuration changed
	dispose := stoppedAndDeleting || !compatible
	if dispose {
		err := c.deleteNetwork(network)
		if err != nil {
			return err
		}
		have = nil
	}

	// fetch other relevant network information
	err := c.gatherNetwork(want)
	if err != nil {
		return fmt.Errorf("failed to fetch other network information for network %s: %w", network, err)
	}

	ensureNetwork := !compatible || util.DoesNetworkNeedReconciliation(have, want)
	if !ensureNetwork {
		// no network changes
		return nil
	}

	// inform controller manager of upcoming changes so other controllers are
	// aware
	err = c.cm.Reconcile(network, have, want)
	if err != nil {
		return fmt.Errorf("failed to reconcile controller manager for network %s: %w", network, err)
	}

	// ensure the network controller
	err = c.ensureNetwork(want)
	if err != nil {
		return fmt.Errorf("%s: failed to ensure network %s: %w", c.name, network, err)
	}

	return nil
}

func (c *networkController) ensureNetwork(network util.MutableNetInfo) error {
	if network == nil {
		return nil
	}

	networkName := network.GetNetworkName()
	reconcilable, _ := c.getReconcilableNetworkState(networkName)

	// this might just be an update of reconcilable network configuration
	if reconcilable != nil {
		err := reconcilable.Reconcile(network)
		if err != nil {
			return fmt.Errorf("failed to reconcile controller for network %s: %w", networkName, err)
		}
		// update NADs of network
		c.passiveNADConnectToActiveNetwork(reconcilable, true)
		return nil
	}

	// otherwise setup & start the new network controller
	nc, err := c.cm.NewNetworkController(network)
	if err != nil {
		return fmt.Errorf("failed to create network %s: %w", networkName, err)
	}

	err = nc.Start(context.Background())
	if err != nil {
		return fmt.Errorf("failed to start network %s: %w", networkName, err)
	}
	c.setNetworkState(network.GetNetworkName(), &networkControllerState{controller: nc})
	// update NADs of network
	if nc.NADToInterConnect() != "" {
		c.activeNetworkConnectToPassiveNAD(nc)
	} else {
		c.passiveNADConnectToActiveNetwork(nc, false)
	}
	return nil
}

func (c *networkController) deleteNetwork(network string) error {
	have := c.getNetworkState(network)
	if have.controller == nil {
		return nil
	}

	if !have.stoppedAndDeleting {
		have.controller.Stop()
	}
	have.stoppedAndDeleting = true

	err := have.controller.Cleanup()
	if err != nil {
		return fmt.Errorf("%s: failed to cleanup network %s: %w", c.name, network, err)
	}

	c.networkDisconnect(have.controller)
	c.setNetworkState(network, nil)
	return nil
}

func (c *networkController) gatherNetwork(network util.MutableNetInfo) error {
	if network == nil {
		return nil
	}
	return c.setAdvertisements(network)
}

func (c *networkController) setAdvertisements(network util.MutableNetInfo) error {
	if !network.IsDefault() && !network.IsPrimaryNetwork() {
		return nil
	}
	if !c.hasRouteAdvertisements() {
		return nil
	}

	raNames := sets.New[string]()
	for _, nadNamespacedName := range network.GetNADs() {
		namespace, name, err := cache.SplitMetaNamespaceKey(nadNamespacedName)
		if err != nil {
			return err
		}

		nad, err := c.nadLister.NetworkAttachmentDefinitions(namespace).Get(name)
		if err != nil {
			return err
		}

		var nadRANames []string
		if nad.Annotations[types.OvnRouteAdvertisementsKey] != "" {
			err = json.Unmarshal([]byte(nad.Annotations[types.OvnRouteAdvertisementsKey]), &nadRANames)
			if err != nil {
				return err
			}
		}

		raNames.Insert(nadRANames...)
	}

	podAdvertisements := map[string][]string{}
	eipAdvertisements := map[string][]string{}
	for raName := range raNames {
		ra, err := c.raLister.Get(raName)
		if err != nil {
			return err
		}

		advertisements := sets.New(ra.Spec.Advertisements...)
		if !advertisements.Has(ratypes.PodNetwork) {
			continue
		}

		accepted := meta.FindStatusCondition(ra.Status.Conditions, "Accepted")
		if accepted == nil {
			// if there is no status we can safely ignore
			continue
		}
		if accepted.Status != metav1.ConditionTrue || accepted.ObservedGeneration != ra.Generation {
			// if the RA is not accepted, we commit to no change, best to
			// preserve the old config while we can't validate new config
			return fmt.Errorf("failed to reconcile network %q: RouteAdvertisements %q not in accepted status", network.GetNetworkName(), ra.Name)
		}

		nodeSelector, err := metav1.LabelSelectorAsSelector(&ra.Spec.NodeSelector)
		if err != nil {
			return err
		}

		nodes, err := c.nodeLister.List(nodeSelector)
		if err != nil {
			return err
		}

		vrf := ra.Spec.TargetVRF
		if vrf == "" {
			vrf = types.DefaultNetworkName
		}

		for _, node := range nodes {
			if !c.isNodeManaged(node) {
				continue
			}
			if advertisements.Has(ratypes.PodNetwork) {
				podAdvertisements[node.Name] = append(podAdvertisements[node.Name], vrf)
			}
			if advertisements.Has(ratypes.EgressIP) {
				eipAdvertisements[node.Name] = append(eipAdvertisements[node.Name], vrf)
			}
		}
	}
	network.SetPodNetworkAdvertisedVRFs(podAdvertisements)
	network.SetEgressIPAdvertisedVRFs(eipAdvertisements)
	return nil
}

func (c *networkController) hasRouteAdvertisements() bool {
	return util.IsRouteAdvertisementsEnabled()
}

func (c *networkController) isNodeManaged(node *corev1.Node) bool {
	switch {
	case c.node == "" && c.zone == "":
		// cluster manager manages all nodes
		return true
	case util.GetNodeZone(node) == c.zone:
		// ovnkube-controller manages nodes of its zone
		return true
	case node.Name == c.node:
		// ovnkube-node only manages a specific node
		return true
	}
	return false
}

func raNeedsUpdate(oldRA, newRA *ratypes.RouteAdvertisements) bool {
	if oldRA == nil || newRA == nil {
		// handle RA add/delete through the NAD annotation update
		return false
	}

	// don't process resync or objects that are marked for deletion
	if oldRA.ResourceVersion == newRA.ResourceVersion ||
		!newRA.GetDeletionTimestamp().IsZero() {
		return false
	}

	// the RA needs to be accepted on its current generation, otherwise we
	// commit to no change
	newCondition := meta.FindStatusCondition(newRA.Status.Conditions, "Accepted")
	if newCondition == nil {
		return false
	}
	if newCondition.Status != metav1.ConditionTrue {
		return false
	}
	if newCondition.ObservedGeneration != newRA.Generation {
		return false
	}
	// there had to be a change on observed generation or status, otherwise we
	// already processed this RA in its current form
	oldCondition := meta.FindStatusCondition(oldRA.Status.Conditions, "Accepted")
	if oldCondition == nil {
		return true
	}
	return oldCondition.ObservedGeneration != newCondition.ObservedGeneration || oldCondition.Status != newCondition.Status
}

func nodeNeedsUpdate(oldNode, newNode *corev1.Node) bool {
	if oldNode == nil || newNode == nil {
		return true
	}

	// don't process resync or objects that are marked for deletion
	if oldNode.ResourceVersion == newNode.ResourceVersion ||
		!newNode.GetDeletionTimestamp().IsZero() {
		return false
	}

	return !reflect.DeepEqual(oldNode.Labels, newNode.Labels) || oldNode.Annotations[util.OvnNodeZoneName] != newNode.Annotations[util.OvnNodeZoneName]
}

func (c *networkController) getRunningNetwork(id int) string {
	if id == DefaultNetworkID {
		return types.DefaultNetworkName
	}
	for _, state := range c.getAllNetworkStates() {
		if state.controller.GetNetworkID() == id {
			return state.controller.GetNetworkName()
		}
	}
	return ""
}

type NetworkInterConnectInfo struct {
	// the logical entity to connect to, it is either a logical router or a logical switch
	LogicalEntityToConnect interface{}
	StartInterConnect      func(BaseNetworkController, interface{}) error
	StopInterConnect       func(BaseNetworkController, interface{}) error
}

type icstate string

const (
	ICSTATE_INIT      icstate = "Init"
	ICSTATE_INVALID   icstate = "Invalid"
	ICSTATE_CONNECTED icstate = "Connected"
)

type interConnectState struct {
	// NAD name this network is requested to connect to
	nadToConnect string
	// network name of network to which "nadName" belongs
	peerNetName string
	state       icstate
	err         error
}

func (ics *interConnectState) init(nadToConnect string) {
	ics.nadToConnect = nadToConnect
	ics.peerNetName = ""
	ics.state = ICSTATE_INIT
	ics.err = nil
}

func (c *networkController) activeNetworkConnectToPassiveNAD(activeController BaseNetworkController) {
	if !c.enableInterConnect {
		return
	}
	nadToConnect := activeController.NADToInterConnect()
	if nadToConnect == "" {
		return
	}

	c.Lock()
	defer c.Unlock()
	netName := activeController.GetNetworkName()

	ics, ok := c.perNetworkConnectToNADs[netName]
	if !ok {
		ics = &interConnectState{}
		ics.init(nadToConnect)
		c.perNetworkConnectToNADs[netName] = ics

	}
	if ics.state == ICSTATE_CONNECTED || ics.state == ICSTATE_INVALID {
		if ics.state == ICSTATE_INVALID {
			klog.Errorf("Error: invalid inter-connect request between network %s and %s: %s",
				netName, nadToConnect, ics.err)
		}
		return
	}

	if activeController.HasNAD(ics.nadToConnect) {
		ics.state = ICSTATE_INVALID
		ics.err = fmt.Errorf("network %s is requested to inter-connect to itself (NAD %s)", netName, nadToConnect)
		klog.Error(ics.err)
		return
	}
	activeNetworkICInfo := activeController.GetNetworkInterConnectInfo()
	if activeNetworkICInfo == nil || activeNetworkICInfo.StartInterConnect == nil {
		ics.state = ICSTATE_INVALID
		ics.err = fmt.Errorf("network %s inter-connect to NAD %s failed: network %s does not support inter-connect",
			netName, nadToConnect, netName)
		klog.Error(ics.err)
		return
	}
	klog.V(5).Infof("Attempt to connect network %s to NAD %s", netName, nadToConnect)

	passiveControllerList := []BaseNetworkController{c.cm.GetDefaultNetworkController()}
	for _, state := range c.networkControllers {
		if !state.stoppedAndDeleting {
			passiveControllerList = append(passiveControllerList, state.controller)
		}
	}
	for _, passiveController := range passiveControllerList {
		if passiveController.HasNAD(nadToConnect) {
			passiveNetworkICInfo := passiveController.GetNetworkInterConnectInfo()
			if passiveNetworkICInfo == nil || passiveNetworkICInfo.StartInterConnect != nil {
				ics.state = ICSTATE_INVALID
				ics.err = fmt.Errorf("network %s inter-connect to NAD %s failed: network %s does not support inter-connect",
					netName, nadToConnect, passiveController.GetNetworkName())
				klog.Error(ics.err)
				break
			}
			ics.peerNetName = passiveController.GetNetworkName()
			err := activeNetworkICInfo.StartInterConnect(activeController, passiveNetworkICInfo.LogicalEntityToConnect)
			if err != nil {
				klog.Errorf("Inter-connect request between network %s and NAD %s failed: %v",
					activeController.GetNetworkName(), nadToConnect, err)
				break
			}
			ics.state = ICSTATE_CONNECTED
			break
		}
	}
}

// Attempt to connect/disconnect passive network to the active network
// if update is true, this is NADs change of passive network, we may need to disconnect with
// already connected active network
func (c *networkController) passiveNADConnectToActiveNetwork(passiveController BaseNetworkController, update bool) {
	errors := []error{}
	if !c.enableInterConnect {
		return
	}
	c.Lock()
	defer c.Unlock()
	passiveNetworkICInfo := passiveController.GetNetworkInterConnectInfo()
	for activeNetName, ics := range c.perNetworkConnectToNADs {
		// get active controller first
		state := c.networkControllers[activeNetName]
		activeController := state.controller
		if activeController == nil {
			klog.Errorf("This should never happend, failed to find active network controller for %s", activeNetName)
			continue
		}
		activeNetworkICInfo := activeController.GetNetworkInterConnectInfo()
		if activeNetworkICInfo == nil {
			klog.Errorf("This should never happend, active network controller of %s does not support inter-connect", activeNetName)
			continue
		}

		nadToConnect := ics.nadToConnect
		if !passiveController.HasNAD(nadToConnect) {
			if update {
				// check if this active network was connected to this passive network, if so disconnect
				if ics.peerNetName == passiveController.GetNetworkName() {
					klog.V(5).Infof("NAD %s is gone, try to disconnected from network %s", nadToConnect, activeNetName)
					if ics.state == ICSTATE_CONNECTED {
						err := activeNetworkICInfo.StopInterConnect(activeController, passiveNetworkICInfo.LogicalEntityToConnect)
						if err == nil {
							klog.Infof("Inter-disconnect request between network %s and NAD %s successed", activeNetName, nadToConnect)
						} else {
							errors = append(errors, fmt.Errorf("inter-disconnect request between network %s and NAD %s failed: %v",
								activeNetName, nadToConnect, err))
						}
					}
					ics.init(nadToConnect)
				}
			}
			continue
		}
		klog.V(5).Infof("NAD %s is requested to be connected to network %s", nadToConnect, activeNetName)
		if ics.state == ICSTATE_CONNECTED || ics.state == ICSTATE_INVALID {
			if ics.state == ICSTATE_INVALID {
				klog.Errorf("Error: invalid inter-connect request between network %s and %s: %s",
					activeNetName, nadToConnect, ics.err)
			}
			continue
		}
		if passiveNetworkICInfo == nil || passiveNetworkICInfo.StartInterConnect != nil {
			ics.state = ICSTATE_INVALID
			ics.err = fmt.Errorf("network %s inter-connect to NAD %s failed: NAD %s does not support inter-connect",
				activeNetName, nadToConnect, passiveController.GetNetworkName())
			klog.Error(ics.err)
			continue
		}
		ics.peerNetName = passiveController.GetNetworkName()
		err := activeNetworkICInfo.StartInterConnect(activeController, passiveNetworkICInfo.LogicalEntityToConnect)
		if err == nil {
			klog.Infof("Inter-connect request between network %s and NAD %s successed", activeNetName, nadToConnect)
			ics.state = ICSTATE_CONNECTED
		} else {
			errors = append(errors, fmt.Errorf("inter-connect request between network %s and NAD %s failed: %v",
				activeNetName, nadToConnect, err))
		}
	}
	if len(errors) > 0 {
		klog.Error(kerrors.NewAggregate(errors))
	}
}

func (c *networkController) networkDisconnect(bnc BaseNetworkController) {
	if !c.enableInterConnect {
		return
	}

	c.Lock()
	defer c.Unlock()
	netName := bnc.GetNetworkName()

	ics, ok := c.perNetworkConnectToNADs[netName]
	if ok {
		// this network is active network, try to see if it needs to disconnect
		klog.Infof("Network %s stopped, disconnect-interconnect request between network %s and NAD %s", netName, netName, ics.nadToConnect)
		if ics.state == ICSTATE_CONNECTED {
			activeNetworkICInfo := bnc.GetNetworkInterConnectInfo()
			passiveController := c.cm.GetDefaultNetworkController()

			if ics.peerNetName != types.DefaultNetworkName {
				state := c.networkControllers[ics.peerNetName]
				if state != nil {
					passiveController = state.controller
				}
			}
			if passiveController != nil && passiveController.GetNetworkInterConnectInfo() != nil {
				klog.V(5).Infof("Network %s stopped, try to disconnected from network %s", netName, ics.peerNetName)
				err := activeNetworkICInfo.StopInterConnect(bnc, passiveController.GetNetworkInterConnectInfo().LogicalEntityToConnect)
				if err == nil {
					klog.Infof("Inter-disconnect request between network %s and network %s successed", netName, ics.peerNetName)
				} else {
					klog.Errorf("Inter-disconnect request between network %s and network %s failed: %v", netName, ics.peerNetName, err)
				}
			}
		}
		// then delete the state from the map
		delete(c.perNetworkConnectToNADs, netName)
	} else {
		// this network may be a passive network, again, try to see if it needs to disconnect
		for activeNetName, ics := range c.perNetworkConnectToNADs {
			if ics.peerNetName == netName {
				// this network is passive network, and it has already connected to a passive network, try to disconnect
				klog.Infof("Network %s stopped, disconnect-interconnect request between network %s and network %s", netName, netName, activeNetName)
				if ics.state == ICSTATE_CONNECTED && bnc.GetNetworkInterConnectInfo() != nil {
					state := c.networkControllers[activeNetName]
					if state != nil {
						activeController := state.controller
						activeNetworkICInfo := activeController.GetNetworkInterConnectInfo()
						err := activeNetworkICInfo.StopInterConnect(activeController, bnc.GetNetworkInterConnectInfo().LogicalEntityToConnect)
						if err == nil {
							klog.Infof("Inter-disconnect request between network %s and network %s successed", activeNetName, netName)
						} else {
							klog.Errorf("Inter-disconnect request between network %s and network %s failed: %v", activeNetName, netName, err)
						}
					}
				}
				ics.init(ics.nadToConnect)
			}
		}
	}
}
