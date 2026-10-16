// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package hostport

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/knftables"
)

func TestRestartClearsUDPForDeletedPod(t *testing.T) {
	c, nft := newTestController(t)
	require.NoError(t, c.informer.GetStore().Add(testPod("deleted-while-down", "192.0.2.1", corev1.ProtocolUDP)))
	require.NoError(t, c.sync(context.Background()))

	// Keep the installed rules, but start with a fresh controller and no pods.
	restarted, _ := newTestController(t)
	restarted.nft = nft
	restarted.localAddresses = func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP("192.0.2.1"), Mask: net.CIDRMask(24, 32)}}, nil
	}
	var cleared []string
	restarted.deleteUDPConntrack = func(_ context.Context, ip string, port int32) error {
		require.NotContains(t, nft.Dump(), " dnat ", "remove stale rules before flushing conntrack")
		cleared = append(cleared, net.JoinHostPort(ip, strconv.Itoa(int(port))))
		require.Equal(t, int32(54323), port)
		return nil
	}
	require.NoError(t, restarted.sync(context.Background()))
	require.Equal(t, []string{"192.0.2.1:54323"}, cleared)
	require.NotContains(t, nft.Dump(), " dnat ")
}

func TestRestartRetainsFailedUDPCleanup(t *testing.T) {
	for _, hostIP := range []string{"", "0.0.0.0", "::", "192.0.2.1", "2001:db8::1", "127.0.0.2"} {
		t.Run(hostIP, func(t *testing.T) {
			c, nft := newTestController(t)
			pod := testPod("old", hostIP, corev1.ProtocolUDP)
			require.NoError(t, c.informer.GetStore().Add(pod))
			require.NoError(t, c.sync(context.Background()))
			addresses := func() ([]net.Addr, error) {
				return []net.Addr{
					&net.IPNet{IP: net.ParseIP("192.0.2.1"), Mask: net.CIDRMask(24, 32)},
					&net.IPNet{IP: net.ParseIP("2001:db8::1"), Mask: net.CIDRMask(64, 128)},
				}, nil
			}
			failed, _ := newTestController(t)
			failed.nft = nft
			failed.localAddresses = addresses
			failed.deleteUDPConntrack = func(context.Context, string, int32) error { return fmt.Errorf("injected cleanup failure") }
			require.ErrorContains(t, failed.sync(context.Background()), "injected cleanup failure")
			require.NotContains(t, nft.Dump(), " dnat ")

			// Lose all process memory after committing rule deletion. The next
			// startup must still discover and retry the unfinished cleanup.
			restarted, _ := newTestController(t)
			restarted.nft = nft
			restarted.localAddresses = addresses
			var cleared []string
			restarted.deleteUDPConntrack = func(_ context.Context, ip string, _ int32) error { cleared = append(cleared, ip); return nil }
			require.NoError(t, restarted.sync(context.Background()))
			expected := []string{hostIP}
			switch hostIP {
			case "":
				expected = []string{ipv4LoopbackCIDR, "192.0.2.1", "2001:db8::1"}
			case "0.0.0.0":
				expected = []string{ipv4LoopbackCIDR, "192.0.2.1"}
			case "::":
				expected = []string{"2001:db8::1"}
			}
			require.ElementsMatch(t, expected, cleared)
			for _, name := range []string{udpStateV4Set, udpStateV6Set} {
				elements, err := nft.ListElements(context.Background(), "set", name)
				require.NoError(t, err)
				require.Empty(t, elements, "forget removed tuples only after successful cleanup")
			}
			cleared = nil
			require.NoError(t, restarted.sync(context.Background()))
			require.Empty(t, cleared)
		})
	}
}

func TestRestartDoesNotFlushBeforeNFTCommit(t *testing.T) {
	c, nft := newTestController(t)
	require.NoError(t, c.informer.GetStore().Add(testPod("old", "127.0.0.2", corev1.ProtocolUDP)))
	require.NoError(t, c.sync(context.Background()))
	flaky := &failOnceNFT{Interface: nft}
	flaky.fail.Store(true)
	restarted, _ := newTestController(t)
	restarted.nft = flaky
	var cleared []string
	restarted.deleteUDPConntrack = func(_ context.Context, ip string, _ int32) error { cleared = append(cleared, ip); return nil }
	require.ErrorContains(t, restarted.sync(context.Background()), "injected nftables failure")
	require.Empty(t, cleared)
	require.Contains(t, nft.Dump(), " dnat ")
	require.NoError(t, restarted.sync(context.Background()))
	require.Equal(t, []string{"127.0.0.2"}, cleared)
}

type failUDPStateRead struct {
	knftables.Interface
	fail bool
}

func (f *failUDPStateRead) ListElements(ctx context.Context, kind, name string) ([]*knftables.Element, error) {
	if f.fail {
		return nil, fmt.Errorf("injected state read failure")
	}
	return f.Interface.ListElements(ctx, kind, name)
}

func TestRestartStateReadFailurePreservesRules(t *testing.T) {
	c, nft := newTestController(t)
	require.NoError(t, c.informer.GetStore().Add(testPod("old", "127.0.0.2", corev1.ProtocolUDP)))
	require.NoError(t, c.sync(context.Background()))
	before := nft.Dump()
	flaky := &failUDPStateRead{Interface: nft, fail: true}
	restarted, _ := newTestController(t)
	restarted.nft = flaky
	require.ErrorContains(t, restarted.sync(context.Background()), "injected state read failure")
	require.Equal(t, before, nft.Dump())
	flaky.fail = false
	var cleared []string
	restarted.deleteUDPConntrack = func(_ context.Context, ip string, _ int32) error { cleared = append(cleared, ip); return nil }
	require.NoError(t, restarted.sync(context.Background()))
	require.Equal(t, []string{"127.0.0.2"}, cleared)
}

func TestRestartRecoversLegacyRules(t *testing.T) {
	c, nft := newTestController(t)
	require.NoError(t, c.informer.GetStore().Add(testPod("old", "127.0.0.2", corev1.ProtocolUDP)))
	require.NoError(t, c.sync(context.Background()))
	tx := nft.NewTransaction()
	tx.Delete(&knftables.Set{Name: udpStateV4Set})
	tx.Delete(&knftables.Set{Name: udpStateV6Set})
	require.NoError(t, nft.Run(context.Background(), tx))
	restarted, _ := newTestController(t)
	restarted.nft = nft
	failRead := true
	restarted.readLegacyUDP = func(context.Context) ([]udpTuple, error) {
		if failRead {
			return nil, fmt.Errorf("injected legacy read failure")
		}
		require.Contains(t, nft.Dump(), " dnat ", "read legacy rules before replacing them")
		return []udpTuple{{family: "ip", hostIP: "127.0.0.2", port: 54323}}, nil
	}
	var cleared []string
	restarted.deleteUDPConntrack = func(_ context.Context, ip string, _ int32) error { cleared = append(cleared, ip); return nil }
	before := nft.Dump()
	require.ErrorContains(t, restarted.sync(context.Background()), "injected legacy read failure")
	require.Equal(t, before, nft.Dump())
	require.Empty(t, cleared)
	failRead = false
	require.NoError(t, restarted.sync(context.Background()))
	require.Equal(t, []string{"127.0.0.2"}, cleared)
}

func TestParseLegacyUDPTuples(t *testing.T) {
	data := []byte(`{"nftables":[
		{"metainfo":{"json_schema_version":1}},
		{"rule":{"chain":"hostports","expr":[{"match":{"op":"==","left":{"payload":{"protocol":"ip","field":"daddr"}},"right":"192.0.2.1"}},{"match":{"op":"==","left":{"payload":{"protocol":"udp","field":"dport"}},"right":54323}},{"counter":{"packets":1,"bytes":32}},{"dnat":{"family":"ip","addr":"10.128.0.3","port":8080}}]}},
		{"rule":{"chain":"hostports","expr":[{"match":{"op":"==","left":{"meta":{"key":"nfproto"}},"right":"ipv6"}},{"match":{"op":"!=","left":{"payload":{"protocol":"ip6","field":"daddr"}},"right":"::1"}},{"match":{"op":"==","left":{"payload":{"protocol":"udp","field":"dport"}},"right":53}},{"dnat":{"family":"ip6","addr":"fd00::3","port":8080}}]}},
		{"rule":{"chain":"hostports","expr":[{"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":54323}},{"dnat":{"family":"ip","addr":"10.128.0.3","port":8080}}]}},
		{"rule":{"chain":"unrelated","expr":[{"match":{"op":"==","left":{"payload":{"protocol":"udp","field":"dport"}},"right":9999}},{"dnat":{"family":"ip","addr":"10.128.0.4","port":8080}}]}}
	]}`)
	tuples, err := parseLegacyUDPTuples(data)
	require.NoError(t, err)
	require.ElementsMatch(t, []udpTuple{{family: "ip", hostIP: "192.0.2.1", port: 54323}, {family: "ip6", port: 53}}, tuples)
	_, err = parseLegacyUDPTuples([]byte(`{"nftables":`))
	require.Error(t, err)
	for _, input := range [][3]string{{"ip", "2001:db8::1", "53"}, {"ip6", "bad-ip", "53"}, {"ip", "", "0"}, {"ip", "", "65536"}} {
		_, err := parseUDPTuple(input[0], input[1], input[2])
		require.Error(t, err)
	}
}
