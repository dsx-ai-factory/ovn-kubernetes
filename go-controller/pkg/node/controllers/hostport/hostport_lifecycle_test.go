// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package hostport

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/knftables"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

type runFuncNFT struct {
	knftables.Interface
	run func(context.Context, *knftables.Transaction) error
}

func (n *runFuncNFT) Run(ctx context.Context, tx *knftables.Transaction) error {
	return n.run(ctx, tx)
}

func TestShutdownCancelsReconciliation(t *testing.T) {
	for _, initial := range []bool{true, false} {
		name := "worker"
		if initial {
			name = "initial sync"
		}
		t.Run(name, func(t *testing.T) {
			_, nft := newTestController(t)
			var block atomic.Bool
			block.Store(initial)
			entered := make(chan struct{}, 1)
			blocked := &runFuncNFT{Interface: nft, run: func(ctx context.Context, tx *knftables.Transaction) error {
				if !block.Load() {
					return nft.Run(ctx, tx)
				}
				entered <- struct{}{}
				<-ctx.Done()
				return ctx.Err()
			}}
			informer := informers.NewSharedInformerFactory(fake.NewSimpleClientset(), 0).Core().V1().Pods().Informer()
			require.NoError(t, informer.AddIndexers(cache.Indexers{types.CacheIndexPodByNodeName: util.IndexPodByNodeName}))
			stop := make(chan struct{})
			var once sync.Once
			stopController := func() { once.Do(func() { close(stop) }) }
			t.Cleanup(stopController)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() { defer wg.Done(); informer.Run(stop) }()
			c := NewController("node1", informer, blocked)
			started := make(chan error, 1)
			go func() { started <- c.Start(stop, &wg) }()
			if !initial {
				require.NoError(t, <-started)
				block.Store(true)
				c.reconciler.Reconcile(reconcileKey)
			}
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("reconciliation did not reach nftables")
			}
			stopController()
			if initial {
				select {
				case err := <-started:
					require.ErrorIs(t, err, context.Canceled)
				case <-time.After(time.Second):
					t.Fatal("shutdown did not cancel initial reconciliation")
				}
			}
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("shutdown waited for the nftables timeout")
			}
		})
	}
}

func TestCanceledUDPCleanupSurvivesRestart(t *testing.T) {
	c, nft := newTestController(t)
	pod := testPod("old", "0.0.0.0", corev1.ProtocolUDP)
	require.NoError(t, c.informer.GetStore().Add(pod))
	require.NoError(t, c.sync(context.Background()))
	require.NoError(t, c.informer.GetStore().Delete(pod))
	addresses := func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("192.0.2.1"), Mask: net.CIDRMask(24, 32)},
			&net.IPNet{IP: net.ParseIP("192.0.2.2"), Mask: net.CIDRMask(24, 32)},
		}, nil
	}
	c.localAddresses = addresses
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cleared []string
	c.deleteUDPConntrack = func(ctx context.Context, ip string, _ int32) error {
		require.NoError(t, ctx.Err())
		cleared = append(cleared, ip)
		cancel()
		return nil
	}
	require.ErrorIs(t, c.sync(ctx), context.Canceled)
	require.Equal(t, []string{ipv4LoopbackCIDR}, cleared)
	require.Len(t, c.pendingUDP, 1)
	require.NotContains(t, nft.Dump(), " dnat ")
	elements, err := nft.ListElements(context.Background(), "set", udpStateV4Set)
	require.NoError(t, err)
	require.Len(t, elements, 1, "interrupted cleanup must remain durable")

	restarted, _ := newTestController(t)
	restarted.nft = nft
	restarted.localAddresses = addresses
	cleared = nil
	restarted.deleteUDPConntrack = func(_ context.Context, ip string, _ int32) error {
		cleared = append(cleared, ip)
		return nil
	}
	require.NoError(t, restarted.sync(context.Background()))
	require.ElementsMatch(t, []string{ipv4LoopbackCIDR, "192.0.2.1", "192.0.2.2"}, cleared)
	elements, err = nft.ListElements(context.Background(), "set", udpStateV4Set)
	require.NoError(t, err)
	require.Empty(t, elements)
}

func TestUDPCleanupFinalizationGetsFreshTimeout(t *testing.T) {
	c, nft := newTestController(t)
	require.NoError(t, c.informer.GetStore().Add(testPod("udp", "127.0.0.2", corev1.ProtocolUDP)))
	var transactions []context.Context
	c.nft = &runFuncNFT{Interface: nft, run: func(ctx context.Context, tx *knftables.Transaction) error {
		transactions = append(transactions, ctx)
		return nft.Run(ctx, tx)
	}}
	var cleanupFinished time.Time
	c.deleteUDPConntrack = func(context.Context, string, int32) error {
		require.Len(t, transactions, 1)
		require.ErrorIs(t, transactions[0].Err(), context.Canceled, "release the first transaction's timer before conntrack cleanup")
		cleanupFinished = time.Now()
		return nil
	}
	require.NoError(t, c.sync(context.Background()))
	require.Len(t, transactions, 2)
	deadline, ok := transactions[1].Deadline()
	require.True(t, ok)
	require.False(t, deadline.Before(cleanupFinished.Add(nftTimeout)), "finalization must get its full timeout after conntrack cleanup")
}

func TestConntrackCancellationStopsFurtherDeletes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	filter := &netlink.ConntrackFilter{}
	require.NoError(t, filter.AddProtocol(unix.IPPROTO_UDP))
	require.NoError(t, filter.AddPort(netlink.ConntrackOrigDstPort, 54323))
	cancellable := &cancellableConntrackFilter{ctx: ctx, filter: filter}
	flow := &netlink.ConntrackFlow{Forward: netlink.IPTuple{Protocol: unix.IPPROTO_UDP, DstPort: 54323}}
	require.True(t, cancellable.MatchConntrackFlow(flow))
	cancel()
	require.False(t, cancellable.MatchConntrackFlow(flow), "netlink must stop deleting flows once shutdown is requested")
	require.ErrorIs(t, deleteUDPConntrack(ctx, "127.0.0.2", 54323), context.Canceled)
}
