// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package hostport

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/knftables"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
)

// Run in a disposable network namespace with loopback up, nft, and NET_ADMIN.
// Keeping the backend alive proves that conntrack removal, rather than backend
// teardown or CNI DEL, stops the stale UDP translation.
func TestKernelUDPConntrackRestart(t *testing.T) {
	if os.Getenv("OVN_HOSTPORT_KERNEL_TEST") != "1" {
		t.Skip("requires Linux CAP_NET_ADMIN and nft")
	}
	for _, scenario := range []string{"persisted", "legacy", "cleanup-failure", "wildcard", "wildcard-v4"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			table := fmt.Sprintf("ovn-hostport-ct-%d", os.Getpid())
			nft, err := knftables.New(knftables.InetFamily, table)
			require.NoError(t, err)
			tx := nft.NewTransaction()
			tx.Add(&knftables.Table{})
			require.NoError(t, nft.Run(ctx, tx))
			t.Cleanup(func() {
				tx := nft.NewTransaction()
				tx.Delete(&knftables.Table{})
				require.NoError(t, nft.Run(ctx, tx))
			})

			startServer := func() int {
				server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.2")})
				require.NoError(t, err)
				done := make(chan struct{})
				go func() {
					defer close(done)
					buf := make([]byte, 128)
					for {
						n, addr, err := server.ReadFromUDP(buf)
						if err != nil {
							return
						}
						_, _ = server.WriteToUDP(buf[:n], addr)
					}
				}()
				t.Cleanup(func() { _ = server.Close(); <-done })
				return server.LocalAddr().(*net.UDPAddr).Port
			}
			port := startServer()
			c, _ := newTestController(t)
			c.nft = nft
			hostIP, destination := "127.0.0.1", "127.0.0.1"
			unrelatedPort := port
			if strings.HasPrefix(scenario, "wildcard") {
				hostIP, destination = "", "127.0.0.3"
				unrelatedPort = startServer()
				if scenario == "wildcard-v4" {
					hostIP = "0.0.0.0"
				}
			}
			pod := testPod("old", hostIP, corev1.ProtocolUDP)
			pod.Annotations[types.OvnPodAnnotationName] = strings.ReplaceAll(pod.Annotations[types.OvnPodAnnotationName], "10.128.0.3", "127.0.0.2")
			pod.Spec.Containers[0].Ports[0].HostPort = int32(port)
			pod.Spec.Containers[0].Ports[0].ContainerPort = int32(port)
			require.NoError(t, c.informer.GetStore().Add(pod))
			require.NoError(t, c.sync(context.Background()))
			if scenario == "legacy" {
				tx = nft.NewTransaction()
				tx.Delete(&knftables.Set{Name: udpStateV4Set})
				tx.Delete(&knftables.Set{Name: udpStateV6Set})
				require.NoError(t, nft.Run(ctx, tx))
			}

			dial := func(ip string, port int) *net.UDPConn {
				conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP(ip), Port: port})
				require.NoError(t, err)
				t.Cleanup(func() { _ = conn.Close() })
				return conn
			}
			probe := func(conn *net.UDPConn) error {
				if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
					return err
				}
				if _, err := conn.Write([]byte("ping")); err != nil {
					return err
				}
				buf := make([]byte, 128)
				_, err := conn.Read(buf)
				return err
			}
			flowExists := func(conn *net.UDPConn, destination string) bool {
				flows, err := netlink.ConntrackTableList(netlink.ConntrackTable, netlink.FAMILY_V4)
				require.NoError(t, err)
				for _, flow := range flows {
					if flow.Forward.Protocol == 17 && flow.Forward.DstPort == uint16(conn.RemoteAddr().(*net.UDPAddr).Port) &&
						flow.Forward.DstIP.Equal(net.ParseIP(destination)) && flow.Forward.SrcPort == uint16(conn.LocalAddr().(*net.UDPAddr).Port) {
						return true
					}
				}
				return false
			}
			client, unrelated := dial(destination, port), dial("127.0.0.2", unrelatedPort)
			require.NoError(t, probe(client))
			require.NoError(t, probe(unrelated))
			require.True(t, flowExists(client, destination))
			require.True(t, flowExists(unrelated, "127.0.0.2"))

			fresh, _ := newTestController(t)
			restart := func() *Controller {
				controller := NewController("node1", fresh.informer, nft)
				controller.readLegacyUDP = func(ctx context.Context) ([]udpTuple, error) { return readLegacyUDPTuples(ctx, table) }
				return controller
			}
			if scenario == "cleanup-failure" {
				failed := restart()
				failed.deleteUDPConntrack = func(context.Context, string, int32) error { return fmt.Errorf("injected cleanup failure") }
				require.ErrorContains(t, failed.sync(context.Background()), "injected cleanup failure")
				require.True(t, flowExists(client, destination))
				require.NoError(t, probe(client), "removing DNAT alone leaves the existing translation working")
			}
			require.NoError(t, restart().sync(context.Background()))
			require.False(t, flowExists(client, destination))
			require.True(t, flowExists(unrelated, "127.0.0.2"), "preserve UDP flows outside the removed mapping")
			require.Error(t, probe(client), "the same source tuple must no longer reach the old backend")
			require.NoError(t, probe(unrelated), "the backend remains alive")
		})
	}
}
