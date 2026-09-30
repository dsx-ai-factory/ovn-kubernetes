// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package networkqos

import (
	"net"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	nqostype "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/networkqos/v1alpha1"
	nqoslister "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/networkqos/v1alpha1/apis/listers/networkqos/v1alpha1"
)

func TestPolicyEventsAdvanceHistoryGeneration(t *testing.T) {
	c, _ := newEventTestController(t)
	policy := &nqostype.NetworkQoS{ObjectMeta: metav1.ObjectMeta{Name: "qos", Namespace: "ns", ResourceVersion: "1"}}
	c.onNQOSAdd(policy)
	if c.historyGeneration.Load() != 1 {
		t.Fatal("policy creation must invalidate history")
	}
	seedHistory(c)
	updated := policy.DeepCopy()
	updated.ResourceVersion = "2"
	c.onNQOSUpdate(policy, updated)
	if c.historyGeneration.Load() != 1 {
		t.Fatal("status updates must not invalidate history")
	}
	c.onNQOSDelete(cache.DeletedFinalStateUnknown{Key: "ns/qos"})
	c.onNQOSAdd(policy)
	if c.historyGeneration.Load() != 3 || c.nqosQueue.Len() != 1 {
		t.Fatal("delete/recreate must advance generation even when the policy queue coalesces events")
	}
	if c.nqosPodQueue.Len() != 0 || c.nqosNamespaceQueue.Len() != 0 {
		t.Fatal("policy events must not enqueue history cleanup")
	}
	if c.podLabelsCache["ns/pod"].generation != 1 || c.nsLabelsCache["ns"].generation != 1 {
		t.Fatal("policy callbacks must leave worker history untouched")
	}
}

func TestDeletedObjectsWithoutHistoryResyncPolicies(t *testing.T) {
	for _, kind := range []string{"pod", "namespace"} {
		t.Run(kind, func(t *testing.T) {
			c, policies := newEventTestController(t)
			policy := &nqostype.NetworkQoS{
				ObjectMeta: metav1.ObjectMeta{Name: "qos", Namespace: "ns"},
				Spec: nqostype.Spec{
					PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "selected"}},
					Egress: []nqostype.Rule{{Classifier: nqostype.Classifier{To: []nqostype.Destination{{
						NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "selected"}},
					}}}}},
				},
			}
			if err := policies.Add(policy); err != nil {
				t.Fatal(err)
			}
			namespaces := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			c.nqosNamespaceLister = corelisters.NewNamespaceLister(namespaces)
			// The policy's initial scan could have selected this object before
			// its deletion. Neither worker has recorded its labels yet.
			if kind == "pod" {
				// Keep the Namespace present: this must exercise missing Pod
				// history, not the separate missing-Namespace fallback.
				if err := namespaces.Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}}); err != nil {
					t.Fatal(err)
				}
				c.onNQOSPodDelete(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
					Name: "pod", Namespace: "ns", Labels: map[string]string{"app": "selected"},
				}})
				key, _ := c.nqosPodQueue.Get()
				defer c.nqosPodQueue.Done(key)
				if err := c.syncNetworkQoSPod(key); err != nil {
					t.Fatal(err)
				}
			} else {
				c.onNQOSNamespaceDelete(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
					Name: "ns", Labels: map[string]string{"team": "selected"},
				}})
				key, _ := c.nqosNamespaceQueue.Get()
				defer c.nqosNamespaceQueue.Done(key)
				if err := c.syncNetworkQoSNamespace(key); err != nil {
					t.Fatal(err)
				}
			}
			if c.nqosQueue.Len() != 1 {
				t.Fatal("deletion without history must queue policy reconciliation")
			}
			key, _ := c.nqosQueue.Get()
			defer c.nqosQueue.Done(key)
			if key != "ns/qos" {
				t.Fatalf("unexpected policy key %q", key)
			}
		})
	}
}

func TestQueuedEventsWithoutPoliciesDoNotPopulateHistory(t *testing.T) {
	c, _ := newEventTestController(t)
	for i := 0; i < 10; i++ {
		if err := c.syncNetworkQoSPod("ns/pod"); err != nil {
			t.Fatal(err)
		}
		if err := c.syncNetworkQoSNamespace("ns"); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.podLabelsCache)+len(c.podIPCache)+len(c.nsLabelsCache) != 0 {
		t.Fatal("queued events populated history with no policies")
	}
}

func TestMissingHistoryResyncsNonMatchingObjects(t *testing.T) {
	c, _ := newHistoryTestController(t)
	for name, get := range map[string]func() (sets.Set[string], error){
		"pod":       func() (sets.Set[string], error) { return c.getNetworkQosForPodChange("ns/pod") },
		"namespace": func() (sets.Set[string], error) { return c.getNetworkQosForNamespaceChange("ns") },
	} {
		got, err := get()
		if err != nil || !got.Equal(sets.New("ns/qos")) {
			t.Fatalf("%s without history must resync policies, got %v, %v", name, got, err)
		}
		got, err = get()
		if err != nil || len(got) != 0 {
			t.Fatalf("%s with unchanged known history should be skipped, got %v, %v", name, got, err)
		}
	}
}

func TestRecreatedPolicyInvalidatesUnchangedHistory(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		name := "unchanged object"
		if deleted {
			name = "deleted object"
		}
		t.Run(name, func(t *testing.T) {
			c, policies := newHistoryTestController(t)
			// Record empty labels that do not match the policy. During a gap
			// without policies these objects could have gained matching labels,
			// then lost them (or disappeared) after the replacement's scan.
			if _, err := c.getNetworkQosForPodChange("ns/pod"); err != nil {
				t.Fatal(err)
			}
			if _, err := c.getNetworkQosForNamespaceChange("ns"); err != nil {
				t.Fatal(err)
			}
			policy := policies.List()[0].(*nqostype.NetworkQoS)
			if err := policies.Delete(policy); err != nil {
				t.Fatal(err)
			}
			c.onNQOSDelete(policy)
			if err := policies.Add(policy); err != nil {
				t.Fatal(err)
			}
			c.onNQOSAdd(policy)
			if deleted {
				c.nqosPodLister = corelisters.NewPodLister(cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}))
			}
			got, err := c.getNetworkQosForPodChange("ns/pod")
			if err != nil || !got.Equal(sets.New("ns/qos")) {
				t.Fatalf("stale Pod history must resync replacement policy, got %v, %v", got, err)
			}
			if deleted {
				c.nqosNamespaceLister = corelisters.NewNamespaceLister(cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{}))
			}
			got, err = c.getNetworkQosForNamespaceChange("ns")
			if err != nil || !got.Equal(sets.New("ns/qos")) {
				t.Fatalf("stale Namespace history must resync replacement policy, got %v, %v", got, err)
			}
			if deleted {
				if len(c.podLabelsCache)+len(c.podIPCache)+len(c.nsLabelsCache) != 0 {
					t.Fatal("deleted object history was retained")
				}
			} else if c.podLabelsCache["ns/pod"].generation != c.historyGeneration.Load() || c.nsLabelsCache["ns"].generation != c.historyGeneration.Load() {
				t.Fatal("new snapshots must record the current generation")
			}
		})
	}
}

func TestHistoryGenerationChangeDuringProcessingRequeuesObject(t *testing.T) {
	for _, kind := range []string{"pod", "namespace"} {
		for _, known := range []bool{false, true} {
			name := kind + "/missing history"
			if known {
				name = kind + "/unchanged known history"
			}
			t.Run(name, func(t *testing.T) {
				c, policies := newHistoryTestController(t)
				key, queue := "ns/pod", c.nqosPodQueue
				get := func() (sets.Set[string], error) { return c.getNetworkQosForPodChange(key) }
				history := func() labelHistory { return c.podLabelsCache[key] }
				if kind == "namespace" {
					key, queue = "ns", c.nqosNamespaceQueue
					get = func() (sets.Set[string], error) { return c.getNetworkQosForNamespaceChange(key) }
					history = func() labelHistory { return c.nsLabelsCache[key] }
				}
				if known {
					if _, err := get(); err != nil {
						t.Fatal(err)
					}
				}
				// Deliver a policy callback after the worker has captured its
				// generation and policy list, before it reads object history.
				originalLister := c.nqosLister
				c.nqosLister = policyListHook{NetworkQoSLister: originalLister, afterList: func() {
					c.onNQOSAdd(policies.List()[0])
				}}
				queue.Add(key)
				processing, _ := queue.Get()
				_, err := get()
				queue.Done(processing)
				if err != nil {
					t.Fatal(err)
				}
				if history().generation != 0 {
					t.Fatal("a snapshot must not be stamped with a generation that arrived during processing")
				}
				if queue.Len() != 1 {
					t.Fatal("generation change must schedule another pass, including after an unchanged-history early return")
				}
				c.nqosLister = originalLister
				processing, _ = queue.Get()
				got, err := get()
				queue.Done(processing)
				if err != nil || !got.Equal(sets.New("ns/qos")) || history().generation != 1 {
					t.Fatalf("retry must resync with fresh history, got %v, %v, history %+v", got, err, history())
				}
				if queue.Len() != 0 {
					t.Fatal("stable generation must not keep requeuing")
				}
			})
		}
	}
}

func TestMissingNamespaceDiscardsPodHistory(t *testing.T) {
	c, _ := newHistoryTestController(t)
	seedHistory(c)
	namespaces := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	c.nqosNamespaceLister = corelisters.NewNamespaceLister(namespaces)
	got, err := c.getNetworkQosForPodChange("ns/pod")
	if err != nil || !got.Equal(sets.New("ns/qos")) {
		t.Fatalf("missing Namespace must resync policies, got %v, %v", got, err)
	}
	if len(c.podLabelsCache) != 0 || len(c.podIPCache) != 0 {
		t.Fatal("Pod history must be discarded when its Namespace is missing")
	}
	// Once the Namespace is visible again, the old Pod history must not
	// suppress reconciliation, even though this generation is unchanged.
	if err := namespaces.Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}}); err != nil {
		t.Fatal(err)
	}
	got, err = c.getNetworkQosForPodChange("ns/pod")
	if err != nil || !got.Equal(sets.New("ns/qos")) {
		t.Fatalf("Pod with discarded history must resync policies, got %v, %v", got, err)
	}
}

func TestPolicyLookupErrorDoesNotRefreshHistory(t *testing.T) {
	c, _ := newHistoryTestController(t)
	seedHistory(c)
	c.historyGeneration.Add(1)
	c.nqosLister = failingNetworkQoSLister{}
	if _, err := c.getNetworkQosForPodChange("ns/pod"); err == nil {
		t.Fatal("expected Pod processing to propagate the policy lookup error")
	}
	if _, err := c.getNetworkQosForNamespaceChange("ns"); err == nil {
		t.Fatal("expected Namespace processing to propagate the policy lookup error")
	}
	pod, podFound := c.podLabelsCache["ns/pod"]
	ns, nsFound := c.nsLabelsCache["ns"]
	if !podFound || !nsFound || pod.generation != 0 || ns.generation != 0 ||
		pod.labels["app"] != "old" || ns.labels["team"] != "old" || len(c.podIPCache["ns/pod"]) != 1 {
		t.Fatal("failed lookup must leave stale history intact for retry")
	}
}

func seedHistory(c *Controller) {
	c.podLabelsCache["ns/pod"] = labelHistory{labels: map[string]string{"app": "old"}, generation: c.historyGeneration.Load()}
	c.podIPCache["ns/pod"] = []net.IP{net.ParseIP("192.0.2.1")}
	c.nsLabelsCache["ns"] = labelHistory{labels: map[string]string{"team": "old"}, generation: c.historyGeneration.Load()}
}

func newHistoryTestController(t *testing.T) (*Controller, cache.Indexer) {
	t.Helper()
	c, policies := newEventTestController(t)
	policy := &nqostype.NetworkQoS{
		ObjectMeta: metav1.ObjectMeta{Name: "qos", Namespace: "ns"},
		Spec: nqostype.Spec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "selected"}},
			Egress: []nqostype.Rule{{Classifier: nqostype.Classifier{To: []nqostype.Destination{{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "selected"}},
			}}}}},
		},
	}
	if err := policies.Add(policy); err != nil {
		t.Fatal(err)
	}
	pods := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	namespaces := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	// Empty labels do not match the policy. The first observed update may
	// nevertheless have removed labels that the policy previously selected.
	if err := pods.Add(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"}}); err != nil {
		t.Fatal(err)
	}
	if err := namespaces.Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}}); err != nil {
		t.Fatal(err)
	}
	c.nqosPodLister = corelisters.NewPodLister(pods)
	c.nqosNamespaceLister = corelisters.NewNamespaceLister(namespaces)
	return c, policies
}

type policyListHook struct {
	nqoslister.NetworkQoSLister
	afterList func()
}

func (l policyListHook) List(selector labels.Selector) ([]*nqostype.NetworkQoS, error) {
	policies, err := l.NetworkQoSLister.List(selector)
	l.afterList()
	return policies, err
}
