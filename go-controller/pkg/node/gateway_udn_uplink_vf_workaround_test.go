// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"k8s.io/client-go/tools/cache"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	uplinklisters "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/uplink/v1alpha1/apis/listers/uplink/v1alpha1"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/node/iprulemanager"
	nodenft "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/node/nftables"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
)

func TestUplinkVFRecoveryBeforeGatewayResolution(t *testing.T) {
	for _, dualStack := range []bool{false, true} {
		name := "IPv4"
		if dualStack {
			name = "dual stack"
		}
		t.Run(name, func(t *testing.T) {
			if err := config.PrepareTestConfig(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = config.PrepareTestConfig() })
			t.Setenv(uplinkVFWorkaroundEnvVar, "true")
			config.OvnKubeNode.Mode = types.NodeModeDPU
			config.Gateway.Mode = config.GatewayModeShared
			config.Gateway.V4MasqueradeSubnet = "169.254.0.0/17"
			config.Gateway.V6MasqueradeSubnet = "fd69::/112"
			config.IPv4Mode = true
			config.IPv6Mode = dualStack
			config.OVNKubernetesFeature.EnableMultiNetwork = true
			config.OVNKubernetesFeature.EnableNetworkSegmentation = true
			config.OVNKubernetesFeature.EnableUplink = true
			state := resolvedGatewayUplinkState("uplink1", "node-a")
			state.Status.MACAddress = ""
			state.Status.IPAddresses = nil
			gw, ops, nodeLister := newUplinkGatewayReconcileHarness(t, state)
			config.OvnKubeNode.Mode = types.NodeModeDPUHost
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			if err := indexer.Add(state); err != nil {
				t.Fatal(err)
			}
			gw.uplinkStateLister = uplinklisters.NewUplinkStateLister(indexer)
			link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "eth0", Index: 7,
				HardwareAddr: net.HardwareAddr{2, 0x42, 0xac, 0x12, 0, 2}}}
			ops.On("LinkByName", "eth0").Return(link, nil)
			ops.On("LinkSetDown", link).Return(nil).Once()
			withdrawal := ops.On("AddrList", link, 0).Return([]netlink.Addr{}, nil).Once()
			ops.On("RouteListFiltered", netlink.FAMILY_ALL, &netlink.Route{Table: unix.RT_TABLE_MAIN}, uint64(netlink.RT_FILTER_TABLE)).Return([]netlink.Route{}, nil).Once()
			ops.On("LinkSetUp", link).Run(func(mock.Arguments) { link.Flags |= net.FlagUp }).Return(nil).Once()

			// This must raise the VF even though MAC/IP discovery has not completed.
			if _, _, err := gw.reconcileUplinkConfiguration(); err == nil || !strings.Contains(err.Error(), "after bringing interface eth0 up") {
				t.Fatalf("expected recovery before gateway resolution, got %v", err)
			}
			for range 2 {
				if _, _, err := gw.reconcileUplinkConfiguration(); err == nil || !strings.Contains(err.Error(), "failed to parse UplinkState MAC") {
					t.Fatalf("expected retry waiting for discovery, got %v", err)
				}
			}
			if gw.uplinkGatewayCleanupRequired {
				t.Fatal("waiting for discovery must not start gateway programming")
			}

			// A cached, complete UplinkState is insufficient while the live VF still
			// has no addresses. This also covers recovery after gateway replacement.
			restored := resolvedGatewayUplinkState("uplink1", "node-a")
			if dualStack {
				restored.Status.IPAddresses = append(restored.Status.IPAddresses, "2001:db8::10/64")
			}
			if err := indexer.Update(restored); err != nil {
				t.Fatal(err)
			}
			missing := ops.On("AddrList", link, 0).Return([]netlink.Addr{}, nil).Once().NotBefore(withdrawal)
			if _, _, err := gw.reconcileUplinkConfiguration(); err == nil || !strings.Contains(err.Error(), "waiting for Uplink uplink1 host addresses") {
				t.Fatalf("expected to wait for live configuration, got %v", err)
			}
			addresses := []netlink.Addr{}
			for _, cidr := range restored.Status.IPAddresses {
				address, err := netlink.ParseAddr(string(cidr))
				if err != nil {
					t.Fatal(err)
				}
				addresses = append(addresses, *address)
			}
			ops.On("AddrList", link, 0).Return(addresses, nil).Once().NotBefore(missing)
			// Stop at the first gateway-programming dependency, proving the recovered
			// state passes resolution without needing OVS or a privileged namespace.
			programming := errors.New("reached gateway programming")
			nodeLister.On("Get", "node-a").Return(nil, programming).Once()
			if _, _, err := gw.reconcileUplinkConfiguration(); err == nil || !strings.Contains(err.Error(), programming.Error()) {
				t.Fatalf("expected recovered state to reach programming, got %v", err)
			}
			ops.AssertNumberOfCalls(t, "LinkSetUp", 1)
			ops.AssertNumberOfCalls(t, "LinkSetDown", 1)
		})
	}
}

func TestUplinkVFRecoveryPreservesAttachedInterface(t *testing.T) {
	if err := config.PrepareTestConfig(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = config.PrepareTestConfig() })
	t.Setenv(uplinkVFWorkaroundEnvVar, "true")
	config.OvnKubeNode.Mode = types.NodeModeDPU
	config.Gateway.Mode = config.GatewayModeShared
	config.Gateway.V4MasqueradeSubnet = "169.254.0.0/17"
	config.OVNKubernetesFeature.EnableMultiNetwork = true
	config.OVNKubernetesFeature.EnableNetworkSegmentation = true
	config.OVNKubernetesFeature.EnableUplink = true
	state := resolvedGatewayUplinkState("uplink1", "node-a")
	gw, ops, _ := newUplinkGatewayReconcileHarness(t, state)
	config.OvnKubeNode.Mode = types.NodeModeDPUHost
	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "eth0", Index: 7, MasterIndex: 42}}
	ops.On("LinkByName", "eth0").Return(link, nil)
	if err := gw.ensureUplinkVFUp(state); err != nil {
		t.Fatal(err)
	}
	if err := gw.setUplinkVFDown(); err != nil {
		t.Fatal(err)
	}
	ops.AssertNotCalled(t, "LinkSetUp", mock.Anything)
	ops.AssertNotCalled(t, "LinkSetDown", mock.Anything)
}

func TestUplinkVFRecoveryDisabled(t *testing.T) {
	if err := config.PrepareTestConfig(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = config.PrepareTestConfig() })
	config.OvnKubeNode.Mode = types.NodeModeDPU
	config.Gateway.Mode = config.GatewayModeShared
	config.Gateway.V4MasqueradeSubnet = "169.254.0.0/17"
	config.OVNKubernetesFeature.EnableMultiNetwork = true
	config.OVNKubernetesFeature.EnableNetworkSegmentation = true
	config.OVNKubernetesFeature.EnableUplink = true
	state := resolvedGatewayUplinkState("uplink1", "node-a")
	gw, ops, _ := newUplinkGatewayReconcileHarness(t, state)
	config.OvnKubeNode.Mode = types.NodeModeDPUHost
	t.Setenv(uplinkVFWorkaroundEnvVar, "false")
	if err := gw.ensureUplinkVFUp(state); err != nil {
		t.Fatal(err)
	}
	if err := gw.setUplinkVFDown(); err != nil {
		t.Fatal(err)
	}
	ops.AssertNotCalled(t, "LinkByName", "eth0")
}

func TestUplinkVFRecoveryAfterGatewayReplacement(t *testing.T) {
	if err := config.PrepareTestConfig(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = config.PrepareTestConfig() })
	t.Setenv(uplinkVFWorkaroundEnvVar, "true")
	config.OvnKubeNode.Mode = types.NodeModeDPU
	config.Gateway.Mode = config.GatewayModeShared
	config.Gateway.V4MasqueradeSubnet = "169.254.0.0/17"
	config.IPv4Mode = true
	config.OVNKubernetesFeature.EnableMultiNetwork = true
	config.OVNKubernetesFeature.EnableNetworkSegmentation = true
	config.OVNKubernetesFeature.EnableUplink = true
	state := resolvedGatewayUplinkState("uplink1", "node-a")
	gw, ops, _ := newUplinkGatewayReconcileHarness(t, state)
	config.OvnKubeNode.Mode = types.NodeModeDPUHost
	gw.ruleManager = iprulemanager.NewController(true, false)
	nodenft.SetFakeNFTablesHelper()
	gw.uplinkGatewayCleanupRequired = true
	gw.uplinkFingerprint = uplinkGatewayFingerprintFromState(state)
	gw.uplinkFingerprint.ipAddresses = "192.0.2.9/24"
	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "eth0", Index: 7, MasterIndex: 42, Flags: net.FlagUp}}
	ops.On("LinkByName", "eth0").Return(link, nil)
	notFound := errors.New("VRF removed")
	ops.On("LinkByName", mock.MatchedBy(func(name string) bool { return name != "eth0" && name != "breth0" })).Run(func(mock.Arguments) { link.MasterIndex = 0 }).Return(nil, notFound).Once()
	ops.On("IsLinkNotFoundError", notFound).Return(true).Once()
	ops.On("LinkSetDown", link).Run(func(mock.Arguments) { link.Flags &^= net.FlagUp }).Return(nil).Once()
	if _, _, err := gw.reconcileUplinkConfiguration(); err == nil || !strings.Contains(err.Error(), "after gateway teardown") {
		t.Fatalf("expected replacement to defer rebuilding after teardown, got %v", err)
	}
	if gw.uplinkGatewayCleanupRequired {
		t.Fatal("replacement started programming before recovering the VF")
	}
	if link.Flags&net.FlagUp != 0 {
		t.Fatal("teardown did not take the VF down")
	}
	ops.On("LinkSetDown", link).Return(nil).Once()
	ops.On("AddrList", link, 0).Return([]netlink.Addr{}, nil).Once()
	ops.On("RouteListFiltered", netlink.FAMILY_ALL, &netlink.Route{Table: unix.RT_TABLE_MAIN}, uint64(netlink.RT_FILTER_TABLE)).Return([]netlink.Route{}, nil).Once()
	ops.On("LinkSetUp", link).Run(func(mock.Arguments) { link.Flags |= net.FlagUp }).Return(nil).Once()
	if _, _, err := gw.reconcileUplinkConfiguration(); err == nil || !strings.Contains(err.Error(), "after bringing interface eth0 up") {
		t.Fatalf("expected replacement to recover VF before using cached state, got %v", err)
	}
	if gw.uplinkGatewayCleanupRequired {
		t.Fatal("recovery used cached data to start programming immediately")
	}
}
