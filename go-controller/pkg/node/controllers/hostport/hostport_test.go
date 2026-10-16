// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package hostport

import (
	"context"
	"fmt"
	"maps"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/knftables"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

func testPod(name, hostIP string, protocol corev1.Protocol) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test", Annotations: map[string]string{
			types.OvnPodAnnotationName: `{"default":{"ip_addresses":["10.128.0.3/24","fd00::3/64"],"mac_address":"0a:58:0a:80:00:03","role":"primary"}}`,
		}},
		Spec:   corev1.PodSpec{NodeName: "node1", Containers: []corev1.Container{{Name: "server", Ports: []corev1.ContainerPort{{HostIP: hostIP, HostPort: 54323, ContainerPort: 8080, Protocol: protocol}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func newTestController(t *testing.T) (*Controller, *knftables.Fake) {
	t.Helper()
	nft := knftables.NewFake(knftables.InetFamily, "ovn-kubernetes")
	tx := nft.NewTransaction()
	tx.Add(&knftables.Table{})
	tx.Add(&knftables.Chain{Name: "mgmtport-snat"})
	tx.Add(&knftables.Rule{Chain: "mgmtport-snat", Rule: "counter return"})
	require.NoError(t, nft.Run(context.Background(), tx))
	informer := cache.NewSharedIndexInformer(&cache.ListWatch{}, &corev1.Pod{}, 0, cache.Indexers{types.CacheIndexPodByNodeName: util.IndexPodByNodeName})
	c := NewController("node1", informer, nft)
	c.deleteUDPConntrack = func(context.Context, string, int32) error { return nil }
	return c, nft
}

func TestPodMappings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*corev1.Pod)
		want   int
	}{
		{"dual stack wildcard", func(*corev1.Pod) {}, 2},
		{"IPv4 wildcard", func(p *corev1.Pod) { p.Spec.Containers[0].Ports[0].HostIP = "0.0.0.0" }, 1},
		{"IPv6 wildcard", func(p *corev1.Pod) { p.Spec.Containers[0].Ports[0].HostIP = "::" }, 1},
		{"IPv4 localhost", func(p *corev1.Pod) { p.Spec.Containers[0].Ports[0].HostIP = "127.0.0.1" }, 1},
		{"IPv6 localhost unsupported", func(p *corev1.Pod) { p.Spec.Containers[0].Ports[0].HostIP = "::1" }, 0},
		{"remote node", func(p *corev1.Pod) { p.Spec.NodeName = "node2" }, 0},
		{"host network", func(p *corev1.Pod) { p.Spec.HostNetwork = true }, 0},
		{"completed", func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }, 0},
		{"failed", func(p *corev1.Pod) { p.Status.Phase = corev1.PodFailed }, 0},
		{"pending with allocated IP", func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending }, 2},
		{"unallocated", func(p *corev1.Pod) { p.Annotations = nil }, 0},
		{"malformed annotation", func(p *corev1.Pod) { p.Annotations[types.OvnPodAnnotationName] = "invalid" }, 0},
		{"UDN infrastructure address", func(p *corev1.Pod) {
			p.Annotations[types.OvnPodAnnotationName] = strings.ReplaceAll(p.Annotations[types.OvnPodAnnotationName], "primary", "infrastructure-locked")
		}, 0},
		{"invalid host IP", func(p *corev1.Pod) { p.Spec.Containers[0].Ports[0].HostIP = "invalid" }, 0},
		{"no host port", func(p *corev1.Pod) { p.Spec.Containers[0].Ports[0].HostPort = 0 }, 0},
		{"invalid protocol", func(p *corev1.Pod) { p.Spec.Containers[0].Ports[0].Protocol = "invalid" }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestController(t)
			p := testPod("pod", "", corev1.ProtocolTCP)
			tc.mutate(p)
			require.Len(t, c.podMappings(p), tc.want)
		})
	}
}

func TestReconcileMappingsAndCleanup(t *testing.T) {
	c, nft := newTestController(t)
	pods := []*corev1.Pod{
		testPod("tcp", "192.0.2.1", corev1.ProtocolTCP),
		testPod("udp", "192.0.2.1", corev1.ProtocolUDP),
		testPod("localhost", "127.0.0.1", corev1.ProtocolTCP),
		testPod("wildcard", "", corev1.ProtocolTCP),
		testPod("sctp", "2001:db8::1", corev1.ProtocolSCTP),
	}
	for _, pod := range pods {
		require.NoError(t, c.informer.GetStore().Add(pod))
	}
	require.NoError(t, c.sync(context.Background()))
	dump := nft.Dump()
	for _, rule := range []string{
		"ip daddr 192.0.2.1 tcp dport 54323 counter dnat ip to 10.128.0.3:8080",
		"ip daddr 192.0.2.1 udp dport 54323 counter dnat ip to 10.128.0.3:8080",
		"ip daddr 127.0.0.1 tcp dport 54323 counter dnat ip to 10.128.0.3:8080",
		"meta nfproto ipv4 tcp dport 54323 counter dnat ip to 10.128.0.3:8080",
		"meta nfproto ipv6 ip6 daddr != ::1 tcp dport 54323 counter dnat ip6 to [fd00::3]:8080",
		"ip6 daddr 2001:db8::1 sctp dport 54323 counter dnat ip6 to [fd00::3]:8080",
		"hostports-prerouting ip daddr 127.0.0.0/8 return",
		"hostports-output fib daddr type local jump hostports",
	} {
		require.Contains(t, dump, rule)
	}
	require.Less(t, strings.Index(dump, "ip daddr 127.0.0.1 tcp"), strings.Index(dump, "meta nfproto ipv4 tcp"))
	require.NoError(t, c.sync(context.Background()))
	require.Equal(t, dump, nft.Dump(), "reconciliation must not duplicate rules")

	// Replacing a pod's address must replace its old rules atomically.
	updated := pods[0].DeepCopy()
	updated.Annotations[types.OvnPodAnnotationName] = strings.ReplaceAll(updated.Annotations[types.OvnPodAnnotationName], "10.128.0.3", "10.128.0.8")
	require.NoError(t, c.informer.GetStore().Update(updated))
	require.NoError(t, c.sync(context.Background()))
	require.Contains(t, nft.Dump(), "ip daddr 192.0.2.1 tcp dport 54323 counter dnat ip to 10.128.0.8:8080")
	require.NotContains(t, nft.Dump(), "ip daddr 192.0.2.1 tcp dport 54323 counter dnat ip to 10.128.0.3:8080")

	for _, pod := range pods {
		require.NoError(t, c.informer.GetStore().Delete(pod))
	}
	require.NoError(t, c.sync(context.Background()))
	require.NotContains(t, nft.Dump(), " dnat ")
	require.NoError(t, Cleanup(nft))
	require.NoError(t, Cleanup(nft))
	require.NotContains(t, nft.Dump(), "hostports")
	require.Contains(t, nft.Dump(), "mgmtport-snat counter return")
}

func TestRestartRemovesStaleRulesAndRepairsMissingChains(t *testing.T) {
	c, nft := newTestController(t)
	require.NoError(t, c.informer.GetStore().Add(testPod("old", "192.0.2.1", corev1.ProtocolUDP)))
	require.NoError(t, c.sync(context.Background()))
	informer := cache.NewSharedIndexInformer(&cache.ListWatch{}, &corev1.Pod{}, 0, cache.Indexers{types.CacheIndexPodByNodeName: util.IndexPodByNodeName})
	restarted := NewController("node1", informer, nft)
	restarted.deleteUDPConntrack = func(context.Context, string, int32) error { return nil }
	require.NoError(t, restarted.sync(context.Background()))
	require.NotContains(t, nft.Dump(), " dnat ")
	require.NoError(t, Cleanup(nft))
	require.NoError(t, restarted.informer.GetStore().Add(testPod("new", "192.0.2.1", corev1.ProtocolTCP)))
	require.NoError(t, restarted.sync(context.Background()))
	require.Contains(t, nft.Dump(), " dnat ")
}

type failOnceNFT struct {
	knftables.Interface
	fail atomic.Bool
}

func (n *failOnceNFT) Run(ctx context.Context, tx *knftables.Transaction) error {
	if n.fail.Swap(false) {
		return fmt.Errorf("injected nftables failure")
	}
	return n.Interface.Run(ctx, tx)
}

func TestWatchReconcilesAndRetries(t *testing.T) {
	_, nft := newTestController(t)
	flaky := &failOnceNFT{Interface: nft}
	client := fake.NewSimpleClientset()
	informer := informers.NewSharedInformerFactory(client, 0).Core().V1().Pods().Informer()
	require.NoError(t, informer.AddIndexers(cache.Indexers{types.CacheIndexPodByNodeName: util.IndexPodByNodeName}))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); informer.Run(stop) }()
	defer func() { close(stop); wg.Wait() }()
	c := NewController("node1", informer, flaky)
	c.deleteUDPConntrack = func(context.Context, string, int32) error { return nil }
	flaky.fail.Store(true)
	require.NoError(t, c.Start(stop, &wg))
	flaky.fail.Store(true)
	pod := testPod("watched", "192.0.2.1", corev1.ProtocolUDP)
	_, err := client.CoreV1().Pods(pod.Namespace).Create(context.Background(), pod, metav1.CreateOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return strings.Contains(nft.Dump(), "udp dport 54323") }, 5*time.Second, 10*time.Millisecond)
	finished := pod.DeepCopy()
	finished.Status.Phase = corev1.PodSucceeded
	_, err = client.CoreV1().Pods(pod.Namespace).Update(context.Background(), finished, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !strings.Contains(nft.Dump(), " dnat ") }, 5*time.Second, 10*time.Millisecond)
	_, err = client.CoreV1().Pods(pod.Namespace).Update(context.Background(), pod, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return strings.Contains(nft.Dump(), " dnat ") }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, client.CoreV1().Pods(pod.Namespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{}))
	require.Eventually(t, func() bool { return !strings.Contains(nft.Dump(), " dnat ") }, 5*time.Second, 10*time.Millisecond)
}

// Opt-in integration check: validates the actual nft syntax/kernel API in an
// isolated table. Run as root on Linux with OVN_HOSTPORT_KERNEL_TEST=1.
func TestKernelRules(t *testing.T) {
	if os.Getenv("OVN_HOSTPORT_KERNEL_TEST") != "1" {
		t.Skip("requires Linux CAP_NET_ADMIN and nft")
	}
	table := fmt.Sprintf("ovn-hostport-test-%d", os.Getpid())
	nft, err := knftables.New(knftables.InetFamily, table)
	require.NoError(t, err)
	tx := nft.NewTransaction()
	tx.Add(&knftables.Table{})
	require.NoError(t, nft.Run(context.Background(), tx))
	t.Cleanup(func() {
		tx := nft.NewTransaction()
		tx.Delete(&knftables.Table{})
		require.NoError(t, nft.Run(context.Background(), tx))
	})
	c, _ := newTestController(t)
	c.nft = nft
	for _, p := range []*corev1.Pod{testPod("wild", "", corev1.ProtocolTCP), testPod("udp", "192.0.2.1", corev1.ProtocolUDP), testPod("udp6", "2001:db8::2", corev1.ProtocolUDP), testPod("udp-wildcard", "", corev1.ProtocolUDP), testPod("sctp", "2001:db8::1", corev1.ProtocolSCTP)} {
		require.NoError(t, c.informer.GetStore().Add(p))
	}
	require.NoError(t, c.sync(context.Background()))
	require.NoError(t, c.sync(context.Background()))
	expected := []udpTuple{{family: "ip", hostIP: "192.0.2.1", port: 54323}, {family: "ip6", hostIP: "2001:db8::2", port: 54323}, {family: "ip", port: 54323}, {family: "ip6", port: 54323}}
	legacy, err := readLegacyUDPTuples(context.Background(), table)
	require.NoError(t, err)
	require.ElementsMatch(t, expected, legacy)
	restarted, _ := newTestController(t)
	restarted.nft = nft
	require.NoError(t, restarted.restoreUDPState(context.Background()))
	require.ElementsMatch(t, expected, slices.Collect(maps.Keys(restarted.pendingUDP)))
	require.NoError(t, restarted.sync(context.Background()))
	require.NoError(t, Cleanup(nft))
}

func TestUDPConntrackCleanup(t *testing.T) {
	c, _ := newTestController(t)
	var cleared []string
	fail := false
	c.deleteUDPConntrack = func(_ context.Context, ip string, port int32) error {
		if fail {
			return fmt.Errorf("injected conntrack failure")
		}
		cleared = append(cleared, net.JoinHostPort(ip, fmt.Sprint(port)))
		return nil
	}
	c.localAddresses = func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP("192.0.2.1"), Mask: net.CIDRMask(24, 32)}, &net.IPNet{IP: net.ParseIP("2001:db8::1"), Mask: net.CIDRMask(64, 128)}}, nil
	}
	pod := testPod("udp", "", corev1.ProtocolUDP)
	require.NoError(t, c.informer.GetStore().Add(pod))
	require.NoError(t, c.sync(context.Background()))
	require.ElementsMatch(t, []string{"127.0.0.0/8:54323", "192.0.2.1:54323", "[2001:db8::1]:54323"}, cleared)
	cleared = nil
	require.NoError(t, c.sync(context.Background()))
	require.Empty(t, cleared, "unchanged mappings must not interrupt UDP flows")
	updated := pod.DeepCopy()
	updated.Annotations[types.OvnPodAnnotationName] = strings.ReplaceAll(updated.Annotations[types.OvnPodAnnotationName], "10.128.0.3", "10.128.0.8")
	require.NoError(t, c.informer.GetStore().Update(updated))
	fail = true
	require.ErrorContains(t, c.sync(context.Background()), "injected conntrack failure")
	// Deletion arriving while cleanup is pending must not lose that cleanup.
	require.NoError(t, c.informer.GetStore().Delete(pod))
	fail = false
	require.NoError(t, c.sync(context.Background()))
	require.ElementsMatch(t, []string{"127.0.0.0/8:54323", "192.0.2.1:54323", "[2001:db8::1]:54323"}, cleared)
	require.Empty(t, c.pendingUDP)
}

func TestUDPConntrackLeavesNonLocalDestinationsAlone(t *testing.T) {
	c, _ := newTestController(t)
	c.localAddresses = func() ([]net.Addr, error) { return nil, nil }
	var cleared []string
	c.deleteUDPConntrack = func(_ context.Context, ip string, _ int32) error { cleared = append(cleared, ip); return nil }
	require.NoError(t, c.informer.GetStore().Add(testPod("remote-ip", "203.0.113.1", corev1.ProtocolUDP)))
	require.NoError(t, c.informer.GetStore().Add(testPod("loopback", "127.0.0.2", corev1.ProtocolUDP)))
	require.NoError(t, c.sync(context.Background()))
	require.Equal(t, []string{"127.0.0.2"}, cleared)
}
