// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package hostport

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/knftables"
)

func TestInitialSyncRetriesUDPCleanup(t *testing.T) {
	c, nft := newTestController(t)
	require.NoError(t, c.informer.GetStore().Add(testPod("deleted-while-down", "127.0.0.2", corev1.ProtocolUDP)))
	require.NoError(t, c.sync(context.Background()))

	restarted, _ := newTestController(t)
	restarted.nft = nft
	attempts := 0
	restarted.deleteUDPConntrack = func(context.Context, string, int32) error {
		attempts++
		require.NotContains(t, nft.Dump(), " dnat ")
		elements, err := nft.ListElements(context.Background(), "set", udpStateV4Set)
		require.NoError(t, err)
		require.Len(t, elements, 1, "keep the removed tuple durable until cleanup succeeds")
		if attempts == 1 {
			return fmt.Errorf("transient conntrack failure")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, restarted.syncWithRetry(ctx))
	require.Equal(t, 2, attempts)
	require.Empty(t, restarted.pendingUDP)
	elements, err := nft.ListElements(context.Background(), "set", udpStateV4Set)
	require.NoError(t, err)
	require.Empty(t, elements)
}

func TestInitialSyncRetryStops(t *testing.T) {
	for _, timeout := range []bool{true, false} {
		name := "shutdown"
		if timeout {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			c, nft := newTestController(t)
			var attempts atomic.Int32
			attempted := make(chan struct{}, 1)
			c.nft = &runFuncNFT{Interface: nft, run: func(context.Context, *knftables.Transaction) error {
				attempts.Add(1)
				select {
				case attempted <- struct{}{}:
				default:
				}
				return fmt.Errorf("persistent nftables failure")
			}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantErr := context.Canceled
			if timeout {
				var stopTimer context.CancelFunc
				ctx, stopTimer = context.WithTimeout(ctx, 100*time.Millisecond)
				defer stopTimer()
				wantErr = context.DeadlineExceeded
			}
			done := make(chan error, 1)
			go func() { done <- c.syncWithRetry(ctx) }()
			select {
			case <-attempted:
			case <-time.After(5 * time.Second):
				t.Fatal("initial reconciliation was not attempted")
			}
			if !timeout {
				cancel()
			}
			select {
			case err := <-done:
				require.ErrorIs(t, err, wantErr)
				require.ErrorContains(t, err, "persistent nftables failure")
			case <-time.After(time.Second):
				t.Fatal("initial reconciliation did not stop promptly")
			}
			require.Equal(t, int32(1), attempts.Load(), "do not retry after cancellation or the deadline")
		})
	}
}
