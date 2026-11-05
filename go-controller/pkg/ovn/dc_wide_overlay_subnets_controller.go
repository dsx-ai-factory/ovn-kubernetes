// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package ovn

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	controllerutil "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/controller"
	nodecontroller "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/controllers/node"
)

const (
	dcWideOverlaySubnetsConfigMapName    = "dc-wide-overlay-subnets-config"
	dcWideOverlaySubnetsKey              = "dc-wide-overlay-subnets"
	dcWideOverlayExemptionControllerName = "dc-wide-overlay-snat-exemptions"
	dcWideOverlayNetworkKeyPrefix        = "network:"
	dcWideOverlayWorkerCount             = 4
)

// Network bootstrap and update workers read the same protected desired CIDR list.
var dcWideOverlaySubnets struct {
	sync.RWMutex
	cidrs []string
}

// getDCWideOverlaySubnets returns a snapshot that callers can safely modify.
func getDCWideOverlaySubnets() []string {
	dcWideOverlaySubnets.RLock()
	defer dcWideOverlaySubnets.RUnlock()
	return slices.Clone(dcWideOverlaySubnets.cidrs)
}

// setDCWideOverlaySubnets publishes a copy and reports whether fan-out is needed.
func setDCWideOverlaySubnets(cidrs []string) bool {
	dcWideOverlaySubnets.Lock()
	defer dcWideOverlaySubnets.Unlock()
	if slices.Equal(dcWideOverlaySubnets.cidrs, cidrs) {
		return false
	}
	dcWideOverlaySubnets.cidrs = slices.Clone(cidrs)
	return true
}

// snatExemptionReconciler lets network controllers opt into SNAT exemption updates when needed.
type snatExemptionReconciler interface {
	ReconcileSNATExemptions() error
}

// DCWideOverlaySubnetsController maintains the shared CIDR list and queues network updates.
type DCWideOverlaySubnetsController struct {
	namespace      string
	informer       cache.SharedIndexInformer
	nodeController *nodecontroller.NodeController
	controller     controllerutil.Controller
}

// NewDCWideOverlaySubnetsController watches the ConfigMap for exemption changes.
func NewDCWideOverlaySubnetsController(informer coreinformers.ConfigMapInformer, nodeController *nodecontroller.NodeController) *DCWideOverlaySubnetsController {
	namespace := config.Kubernetes.OVNConfigNamespace
	c := &DCWideOverlaySubnetsController{
		namespace:      namespace,
		informer:       informer.Informer(),
		nodeController: nodeController,
	}
	// Informer events enqueue ConfigMap keys; workers pass both key types to c.reconcile.
	c.controller = controllerutil.NewController(dcWideOverlayExemptionControllerName, &controllerutil.ControllerConfig[corev1.ConfigMap]{
		ObjNeedsUpdate: func(old, next *corev1.ConfigMap) bool {
			return next.Namespace == namespace && next.Name == dcWideOverlaySubnetsConfigMapName &&
				(old == nil || !maps.Equal(old.Data, next.Data))
		},
		Informer:  c.informer,
		Reconcile: c.reconcile,
		// Handler locks serialize writes; unlike uplinkStateController, there are no worker-owned maps.
		Threadiness: dcWideOverlayWorkerCount,
		MaxAttempts: controllerutil.InfiniteAttempts,
		RateLimiter: controllerutil.DefaultRateLimiter[string](),
	})
	return c
}

// Start loads the initial CIDR list before network controllers start.
func (c *DCWideOverlaySubnetsController) Start() error {
	// Seed the shared list before network bootstrap can create exemption address sets.
	return controllerutil.StartWithInitialSync(func() error {
		key := c.namespace + "/" + dcWideOverlaySubnetsConfigMapName
		if err := c.sync(key); err != nil {
			klog.Errorf("DC-wide overlay subnets initial sync failed; starting with an empty list and retrying: %v", err)
			c.controller.ReconcileRateLimited(key)
		}
		return nil
	}, c.controller)
}

// Stop stops workers; the watch factory owns the shared informer.
func (c *DCWideOverlaySubnetsController) Stop() {
	controllerutil.Stop(c.controller)
}

// getSNATExemptionReconciler skips network controllers that do not manage OVN exemptions.
func getSNATExemptionReconciler(handler nodecontroller.NodeHandler, name string) snatExemptionReconciler {
	if reconciler, ok := handler.(snatExemptionReconciler); ok {
		return reconciler
	}
	klog.V(5).Infof("Skipping DC-wide SNAT exemptions for network %s: handler %T does not implement snatExemptionReconciler", name, handler)
	return nil
}

// reconcile dispatches work based on the queue key:
// "<namespace>/dc-wide-overlay-subnets-config" loads the CIDRs and queues network updates.
// "network:<network-name>" reconciles that network's exemption set using the current CIDRs.
func (c *DCWideOverlaySubnetsController) reconcile(key string) error {
	if name, isNetwork := strings.CutPrefix(key, dcWideOverlayNetworkKeyPrefix); isNetwork {
		// Resolve the live handler under its lock to serialize with node updates and teardown.
		return c.nodeController.WithNetworkHandler(name, func(handler nodecontroller.NodeHandler) error {
			reconciler := getSNATExemptionReconciler(handler, name)
			if reconciler != nil {
				// An error makes the workqueue retry this network key with backoff.
				return reconciler.ReconcileSNATExemptions()
			}
			return nil
		})
	}
	// Shared informer deletions bypass ObjNeedsUpdate, so discard other ConfigMap keys here too.
	if key != c.namespace+"/"+dcWideOverlaySubnetsConfigMapName {
		return nil
	}
	return c.sync(key)
}

// sync reads current informer state so retries never replay an obsolete ConfigMap value.
func (c *DCWideOverlaySubnetsController) sync(key string) error {
	obj, exists, err := c.informer.GetStore().GetByKey(key)
	if err != nil {
		return fmt.Errorf("failed to read DC-wide overlay subnets ConfigMap %q from cache: %w", key, err)
	}
	// An absent ConfigMap or data key explicitly clears the extra exemptions.
	value := ""
	if exists {
		value = obj.(*corev1.ConfigMap).Data[dcWideOverlaySubnetsKey]
	}
	desired, err := parseDCWideOverlaySubnets(value)
	if err != nil {
		// Only a ConfigMap edit can fix malformed input, so log without retrying.
		klog.Errorf("Invalid DC-wide overlay subnets in ConfigMap %s; retaining the last valid list: %v", key, err)
		return nil
	}
	c.updateSubnets(desired)
	return nil
}

// updateSubnets queues names without waiting for busy networks' handler locks.
func (c *DCWideOverlaySubnetsController) updateSubnets(desired []string) {
	// Compare normalized CIDRs so unchanged values do not queue network updates.
	if setDCWideOverlaySubnets(desired) {
		// Networks registered later read the current CIDRs during node bootstrap.
		for _, name := range c.nodeController.GetNetworkNames() {
			// This only queues work; workers call reconcile later and read the latest CIDRs.
			c.controller.Reconcile(dcWideOverlayNetworkKeyPrefix + name)
		}
	}
}

// parseDCWideOverlaySubnets normalizes and deduplicates CIDRs before comparing updates.
func parseDCWideOverlaySubnets(value string) ([]string, error) {
	cidrs := sets.New[string]()
	if strings.TrimSpace(value) != "" {
		for _, subnet := range strings.Split(value, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(subnet))
			if err != nil {
				return nil, fmt.Errorf("invalid DC-wide overlay subnet %q: %w", subnet, err)
			}
			if prefix.Addr().Is4In6() {
				return nil, fmt.Errorf("IPv4-mapped IPv6 DC-wide overlay subnet %q is not supported", subnet)
			}
			cidrs.Insert(prefix.Masked().String())
		}
	}
	return sets.List(cidrs), nil
}
