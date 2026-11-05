// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package ovn

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	informerfactory "k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	nodecontroller "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/controllers/node"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/networkmanager"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

type dcWideTestNodeHandler struct {
	nodecontroller.NodeHandler
	name      string
	reconcile func() error
	needed    func() bool
}

func (h *dcWideTestNodeHandler) GetNetworkName() string         { return h.name }
func (h *dcWideTestNodeHandler) SyncNodes([]*corev1.Node) error { return nil }

func (h *dcWideTestNodeHandler) ReconcileSNATExemptions() error {
	// Real network controllers check eligibility inside reconciliation too.
	if h.needed != nil && !h.needed() {
		return nil
	}
	return h.reconcile()
}

type dcWideTestUnsupportedHandler struct {
	nodecontroller.NodeHandler
	name string
}

func (h *dcWideTestUnsupportedHandler) GetNetworkName() string         { return h.name }
func (h *dcWideTestUnsupportedHandler) SyncNodes([]*corev1.Node) error { return nil }

// newDCWideTestNodeController exercises the production registry and lifecycle locks.
func newDCWideTestNodeController(t *testing.T, handlers map[string]nodecontroller.NodeHandler) *nodecontroller.NodeController {
	t.Helper()
	wf, err := factory.NewNodeWatchFactory(&util.OVNNodeClientset{KubeClient: fake.NewSimpleClientset()}, "test-node")
	if err != nil {
		t.Fatalf("creating watch factory for node registry: %v", err)
	}
	t.Cleanup(wf.Shutdown)
	c := nodecontroller.NewNodeController(wf, networkmanager.Default().Interface(), "test-node")
	t.Cleanup(c.Stop)
	for name, handler := range handlers {
		if h, ok := handler.(*dcWideTestNodeHandler); ok {
			h.name = name
		} else {
			handler = &dcWideTestUnsupportedHandler{name: name}
		}
		if err := c.RegisterNetworkController(handler); err != nil {
			t.Fatalf("registering network %s: %v", name, err)
		}
	}
	return c
}

// newDCWideTestController gives the shared informer a lifetime separate from its consumer.
func newDCWideTestController(t *testing.T, client clientset.Interface, nodes *nodecontroller.NodeController) *DCWideOverlaySubnetsController {
	t.Helper()
	factory := informerfactory.NewSharedInformerFactoryWithOptions(client, 0,
		informerfactory.WithNamespace(config.Kubernetes.OVNConfigNamespace))
	c := NewDCWideOverlaySubnetsController(factory.Core().V1().ConfigMaps(), nodes)
	stop := make(chan struct{})
	factory.Start(stop)
	t.Cleanup(func() { close(stop); factory.Shutdown() })
	return c
}

func TestDCWideOverlaySubnetsValidation(t *testing.T) {
	cidrs, err := parseDCWideOverlaySubnets(" 10.20.1.2/16, fd00:20::1/48,10.20.0.0/16\n")
	if err != nil || !slices.Equal(cidrs, []string{"10.20.0.0/16", "fd00:20::/48"}) {
		t.Fatalf("normalization failed: %v, %v", cidrs, err)
	}
	for _, value := range []string{"10.30.0.0/16,bad", "10.30.0.0/16,", "10.30.0.1", "::ffff:10.30.0.0/112"} {
		if _, err := parseDCWideOverlaySubnets(value); err == nil {
			t.Fatalf("expected %q to fail", value)
		}
	}
}

func TestDCWideOverlaySubnetsInformer(t *testing.T) {
	previousNamespace := config.Kubernetes.OVNConfigNamespace
	config.Kubernetes.OVNConfigNamespace = "custom-ovn"
	t.Cleanup(func() { config.Kubernetes.OVNConfigNamespace = previousNamespace })
	client := fake.NewSimpleClientset()
	setDCWideOverlaySubnets(nil)
	t.Cleanup(func() { setDCWideOverlaySubnets(nil) })
	var changes atomic.Int32
	var watchStarts atomic.Int32
	client.PrependWatchReactor("configmaps", func(action ktesting.Action) (bool, watch.Interface, error) {
		if action.GetNamespace() != config.Kubernetes.OVNConfigNamespace || action.(ktesting.WatchAction).GetWatchRestrictions().Fields.String() != "" {
			return true, nil, fmt.Errorf("unexpected ConfigMap watch scope")
		}
		watchStarts.Add(1)
		return false, nil, nil
	})
	registry := newDCWideTestNodeController(t, map[string]nodecontroller.NodeHandler{
		"test-network": &dcWideTestNodeHandler{reconcile: func() error { changes.Add(1); return nil }},
	})
	c := newDCWideTestController(t, client, registry)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Start(); err != nil {
		t.Fatalf("starting DC-wide overlay informer controller: %v", err)
	}
	defer c.Stop()
	waitFor := func(f func() bool) {
		t.Helper()
		for !f() {
			select {
			case <-ctx.Done():
				t.Fatal("timed out waiting for ConfigMap reconciliation")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	waitFor(func() bool { return watchStarts.Load() >= 1 })
	if len(getDCWideOverlaySubnets()) != 0 {
		t.Fatal("absent ConfigMap must give empty list")
	}
	cm, err := client.CoreV1().ConfigMaps(config.Kubernetes.OVNConfigNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: dcWideOverlaySubnetsConfigMapName},
		Data:       map[string]string{dcWideOverlaySubnetsKey: "10.20.0.0/16,fd00:20::/48"},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating initial DC-wide overlay subnets ConfigMap: %v", err)
	}
	waitFor(func() bool { return slices.Equal(getDCWideOverlaySubnets(), []string{"10.20.0.0/16", "fd00:20::/48"}) })
	// A shared informer sees other ConfigMaps, including unfiltered delete events.
	other := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: cm.Namespace},
		Data: map[string]string{dcWideOverlaySubnetsKey: "10.99.0.0/16"}}
	if _, err := client.CoreV1().ConfigMaps(cm.Namespace).Create(ctx, other, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating unrelated ConfigMap: %v", err)
	}
	otherKey := cm.Namespace + "/" + other.Name
	waitFor(func() bool { _, exists, _ := c.informer.GetStore().GetByKey(otherKey); return exists })
	if err := c.reconcile(otherKey); err != nil {
		t.Fatalf("ignoring unrelated ConfigMap update: %v", err)
	}
	if !slices.Equal(getDCWideOverlaySubnets(), []string{"10.20.0.0/16", "fd00:20::/48"}) {
		t.Fatal("unrelated ConfigMap changed exemptions")
	}
	if err := client.CoreV1().ConfigMaps(cm.Namespace).Delete(ctx, other.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("deleting unrelated ConfigMap: %v", err)
	}
	waitFor(func() bool { _, exists, _ := c.informer.GetStore().GetByKey(otherKey); return !exists })
	if err := c.reconcile(otherKey); err != nil {
		t.Fatalf("ignoring unrelated ConfigMap deletion: %v", err)
	}
	if !slices.Equal(getDCWideOverlaySubnets(), []string{"10.20.0.0/16", "fd00:20::/48"}) {
		t.Fatal("unrelated ConfigMap deletion cleared exemptions")
	}
	cm.Data[dcWideOverlaySubnetsKey] = "bad"
	if _, err := client.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("updating DC-wide overlay subnets ConfigMap with malformed input: %v", err)
	}
	// A later valid update must still work after malformed input.
	waitFor(func() bool {
		obj, exists, _ := c.informer.GetStore().GetByKey(config.Kubernetes.OVNConfigNamespace + "/" + cm.Name)
		return exists && obj.(*corev1.ConfigMap).Data[dcWideOverlaySubnetsKey] == "bad"
	})
	if err := c.sync(cm.Namespace + "/" + cm.Name); err != nil {
		t.Fatalf("malformed update must not trigger retries: %v", err)
	}
	if !slices.Equal(getDCWideOverlaySubnets(), []string{"10.20.0.0/16", "fd00:20::/48"}) {
		t.Fatal("malformed update changed the last valid list")
	}

	changesBefore := changes.Load()
	cm.Data[dcWideOverlaySubnetsKey] = "10.30.0.0/16"
	if _, err := client.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("updating DC-wide overlay subnets ConfigMap after malformed input: %v", err)
	}
	waitFor(func() bool {
		return slices.Equal(getDCWideOverlaySubnets(), []string{"10.30.0.0/16"}) && changes.Load() > changesBefore
	})
	if err := client.CoreV1().ConfigMaps(cm.Namespace).Delete(ctx, cm.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("deleting DC-wide overlay subnets ConfigMap: %v", err)
	}
	waitFor(func() bool { return len(getDCWideOverlaySubnets()) == 0 })
	cm.ResourceVersion = ""
	if _, err := client.CoreV1().ConfigMaps(cm.Namespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		t.Fatalf("recreating DC-wide overlay subnets ConfigMap: %v", err)
	}
	waitFor(func() bool { return slices.Equal(getDCWideOverlaySubnets(), []string{"10.30.0.0/16"}) })
	cm.Data = nil
	if _, err := client.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("clearing DC-wide overlay subnets ConfigMap data: %v", err)
	}
	waitFor(func() bool { return len(getDCWideOverlaySubnets()) == 0 })

}

func TestDCWideOverlaySubnetsInitialState(t *testing.T) {
	for _, value := range []string{"10.20.0.0/16,fd00:20::/48", "invalid"} {
		t.Run(value, func(t *testing.T) {
			client := fake.NewSimpleClientset(&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: dcWideOverlaySubnetsConfigMapName, Namespace: config.Kubernetes.OVNConfigNamespace},
				Data:       map[string]string{dcWideOverlaySubnetsKey: value},
			})
			setDCWideOverlaySubnets(nil)
			t.Cleanup(func() { setDCWideOverlaySubnets(nil) })
			c := newDCWideTestController(t, client, newDCWideTestNodeController(t, nil))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := c.Start()
			if err != nil {
				t.Fatalf("starting DC-wide overlay controller with initial ConfigMap data: %v", err)
			}
			defer c.Stop()
			if value == "invalid" {
				if len(getDCWideOverlaySubnets()) != 0 {
					t.Fatal("invalid initial list must be empty")
				}
				cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: dcWideOverlaySubnetsConfigMapName, Namespace: config.Kubernetes.OVNConfigNamespace}, Data: map[string]string{dcWideOverlaySubnetsKey: "10.30.0.0/16"}}
				if _, err := client.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
					t.Fatalf("publishing valid subnets after malformed ConfigMap startup: %v", err)
				}
				for !slices.Equal(getDCWideOverlaySubnets(), []string{"10.30.0.0/16"}) {
					select {
					case <-ctx.Done():
						t.Fatal("valid update after malformed startup was not applied")
					case <-time.After(10 * time.Millisecond):
					}
				}
			} else if !slices.Equal(getDCWideOverlaySubnets(), []string{"10.20.0.0/16", "fd00:20::/48"}) {
				t.Fatalf("initial cache was not ready at startup: %v", getDCWideOverlaySubnets())
			}
		})
	}
}

func TestDCWideOverlaySubnetsNetworkCallbacks(t *testing.T) {
	setDCWideOverlaySubnets(nil)
	t.Cleanup(func() { setDCWideOverlaySubnets(nil) })
	var failed, healthy atomic.Int32
	registry := newDCWideTestNodeController(t, map[string]nodecontroller.NodeHandler{
		"failing": &dcWideTestNodeHandler{reconcile: func() error {
			if failed.Add(1) < 3 {
				return fmt.Errorf("injected OVN write failure")
			}
			return nil
		}},
		"healthy":     &dcWideTestNodeHandler{reconcile: func() error { healthy.Add(1); return nil }},
		"unsupported": &struct{ nodecontroller.NodeHandler }{},
	})
	c := newDCWideTestController(t, fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: dcWideOverlaySubnetsConfigMapName, Namespace: config.Kubernetes.OVNConfigNamespace},
		Data:       map[string]string{dcWideOverlaySubnetsKey: "10.20.0.0/16"},
	}), registry)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Start(); err != nil {
		t.Fatalf("starting DC-wide overlay controller for independent network retries: %v", err)
	}
	defer c.Stop()
	for failed.Load() < 3 || healthy.Load() < 1 {
		select {
		case <-ctx.Done():
			t.Fatal("network callbacks did not run and retry independently")
		case <-time.After(10 * time.Millisecond):
		}
	}
	registry.DeregisterNetworkController("failing")
	count := failed.Load()
	if err := c.reconcile("network:failing"); err != nil {
		t.Fatalf("reconciling a removed network after pending retries: %v", err)
	}
	if failed.Load() != count {
		t.Fatal("removed network was reconciled")
	}
}

func TestDCWideOverlaySubnetsEligibility(t *testing.T) {
	setDCWideOverlaySubnets(nil)
	t.Cleanup(func() { setDCWideOverlaySubnets(nil) })
	var eligibleCalls, ineligibleCalls, ineligibleChecks atomic.Int32
	registry := newDCWideTestNodeController(t, map[string]nodecontroller.NodeHandler{
		"eligible":    &dcWideTestNodeHandler{reconcile: func() error { eligibleCalls.Add(1); return nil }},
		"ineligible":  &dcWideTestNodeHandler{needed: func() bool { ineligibleChecks.Add(1); return false }, reconcile: func() error { ineligibleCalls.Add(1); return nil }},
		"unsupported": &struct{ nodecontroller.NodeHandler }{},
	})
	c := newDCWideTestController(t, fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: dcWideOverlaySubnetsConfigMapName, Namespace: config.Kubernetes.OVNConfigNamespace},
		Data:       map[string]string{dcWideOverlaySubnetsKey: "10.20.0.0/16"},
	}), registry)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Start(); err != nil {
		t.Fatalf("starting DC-wide overlay controller for network eligibility checks: %v", err)
	}
	defer c.Stop()
	for eligibleCalls.Load() == 0 || ineligibleChecks.Load() == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("network keys were not processed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := c.reconcile(dcWideOverlayNetworkKeyPrefix + "unsupported"); err != nil {
		t.Fatalf("reconciling an unsupported network handler: %v", err)
	}
	// Eligibility can change while a network key is queued.
	if err := registry.WithNetworkHandler("eligible", func(handler nodecontroller.NodeHandler) error {
		handler.(*dcWideTestNodeHandler).needed = func() bool { return false }
		return nil
	}); err != nil {
		t.Fatalf("changing network eligibility under its handler lock: %v", err)
	}

	before := eligibleCalls.Load()
	if err := c.reconcile(dcWideOverlayNetworkKeyPrefix + "eligible"); err != nil {
		t.Fatalf("reconciling a network after eligibility changed: %v", err)
	}
	if eligibleCalls.Load() != before || ineligibleCalls.Load() != 0 {
		t.Fatal("execution did not recheck eligibility")
	}
}

func TestDCWideOverlaySubnetsBlockedNetwork(t *testing.T) {
	setDCWideOverlaySubnets(nil)
	t.Cleanup(func() { setDCWideOverlaySubnets(nil) })
	started, release := make(chan struct{}), make(chan struct{})
	var blockOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	seen := make(chan []string, 4)
	registry := newDCWideTestNodeController(t, map[string]nodecontroller.NodeHandler{
		"blocked": &dcWideTestNodeHandler{reconcile: func() error {
			blockOnce.Do(func() { close(started); <-release })
			return nil
		}},
		"fast": &dcWideTestNodeHandler{reconcile: func() error { seen <- getDCWideOverlaySubnets(); return nil }},
	})
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: dcWideOverlaySubnetsConfigMapName, Namespace: config.Kubernetes.OVNConfigNamespace},
		Data: map[string]string{dcWideOverlaySubnetsKey: "10.20.0.0/16"}}
	client := fake.NewSimpleClientset(cm)
	c := newDCWideTestController(t, client, registry)
	defer func() { unblock(); c.Stop() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Start(); err != nil {
		t.Fatalf("starting DC-wide overlay controller with a blocked network: %v", err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("blocked network did not start")
	}
	expect := func(want string) {
		t.Helper()
		select {
		case cidrs := <-seen:
			if !slices.Equal(cidrs, []string{want}) {
				t.Fatalf("expected %s, got %v", want, cidrs)
			}
		case <-ctx.Done():
			t.Fatal("a blocked network prevented other updates")
		}
	}
	expect("10.20.0.0/16")
	cm.Data[dcWideOverlaySubnetsKey] = "10.30.0.0/16"
	if _, err := client.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("updating DC-wide overlay subnets while a network is blocked: %v", err)
	}
	expect("10.30.0.0/16")
}
