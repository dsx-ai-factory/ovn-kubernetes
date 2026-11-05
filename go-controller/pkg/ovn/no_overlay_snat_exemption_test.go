// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package ovn

import (
	"context"
	"net"
	"sync"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilnet "k8s.io/utils/net"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	nodecontroller "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/controllers/node"
	libovsdbops "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/libovsdb/ops"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/nbdb"
	addressset "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/ovn/address_set"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

type snatGuardTestNetInfo struct{ util.ReconcilableNetInfo }

func (*snatGuardTestNetInfo) TopologyType() string       { return types.Layer2Topology }
func (*snatGuardTestNetInfo) Transport() string          { return types.NetworkTransportNoOverlay }
func (*snatGuardTestNetInfo) OutboundSNAT() string       { return types.NoOverlaySNATEnabled }
func (*snatGuardTestNetInfo) IsUserDefinedNetwork() bool { return true }

type dcWideBaseNodeHandler struct {
	*BaseNetworkController
	onSync func()
}

func (h *dcWideBaseNodeHandler) SyncNodes(_ []*corev1.Node) error {
	if h.onSync != nil {
		h.onSync()
	}
	return nil
}

func (h *dcWideBaseNodeHandler) ReconcileNode(_, node *corev1.Node, _, _ *nodecontroller.NodeAnnotationState) error {
	if node == nil {
		return nil
	}
	return h.ReconcileSNATExemptions()
}

var _ = ginkgo.Describe("No-Overlay SNAT Exemption Address Set", func() {
	var (
		fakeOvn           *FakeOVN
		addressSetFactory addressset.AddressSetFactory
		netInfo           *testNetInfo
		controllerName    = types.DefaultNetworkControllerName
	)

	ginkgo.BeforeEach(func() {
		gomega.Expect(config.PrepareTestConfig()).To(gomega.Succeed())
		setDCWideOverlaySubnets(nil)
		fakeOvn = NewFakeOVN(false, "node1")
		fakeOvn.start()
		addressSetFactory = fakeOvn.controller.addressSetFactory
		netInfo = &testNetInfo{
			NetInfo: fakeOvn.controller.GetNetInfo(),
		}
	})

	ginkgo.AfterEach(func() {
		fakeOvn.shutdown()
		setDCWideOverlaySubnets(nil)
	})

	ginkgo.It("updates existing exemptions through registered node handlers without changing NAT matches", func() {
		config.NoOverlay.EnableDCWideOverlaySNATExemptions = true
		config.Gateway.Mode = config.GatewayModeShared
		config.Default.Transport = types.NetworkTransportNoOverlay
		config.NoOverlay.OutboundSNAT = types.NoOverlaySNATEnabled
		config.IPv6Mode = true
		netInfo.transport = types.NetworkTransportNoOverlay
		netInfo.outboundSNAT = types.NoOverlaySNATEnabled
		netInfo.topology = types.Layer3Topology
		netInfo.ipMode = &[2]bool{true, true}
		_, subnet, err := net.ParseCIDR("10.128.0.0/14")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		netInfo.subnets = []config.CIDRNetworkEntry{{CIDR: subnet, HostSubnetLength: 24}}
		config.Default.ClusterSubnets = netInfo.subnets
		addressSetFactory = addressset.NewOvnAddressSetFactory(fakeOvn.nbClient, true, true)
		gomega.Expect(syncNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName, []string{"192.0.2.10"})).To(gomega.Succeed())
		as, err := getNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		v4UUID, _ := as.GetASUUID()
		router := &nbdb.LogicalRouter{Name: "GR_dc-wide-test"}
		gomega.Expect(libovsdbops.CreateOrUpdateLogicalRouter(fakeOvn.nbClient, router)).To(gomega.Succeed())
		match, err := GetNetworkScopedClusterSubnetSNATMatch(fakeOvn.nbClient, netInfo, "node1", true, utilnet.IPv4, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(match).To(gomega.BeEmpty())
		nat := &nbdb.NAT{Type: nbdb.NATTypeSNAT, LogicalIP: "10.128.0.0/14", ExternalIP: "192.0.2.10", Match: match, ExemptedExtIPs: &v4UUID}
		gomega.Expect(libovsdbops.CreateOrUpdateNATs(fakeOvn.nbClient, router, nat)).To(gomega.Succeed())
		nats := func() []*nbdb.NAT {
			result, err := libovsdbops.FindNATsWithPredicate(fakeOvn.nbClient, func(nat *nbdb.NAT) bool { return nat.ExternalIP == "192.0.2.10" })
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			return result
		}
		originalNATs := nats()
		gomega.Expect(originalNATs).To(gomega.HaveLen(1))
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Annotations: map[string]string{"k8s.ovn.org/host-cidrs": `["192.0.2.10/24"]`}}}
		_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Nodes().Create(context.Background(), node, metav1.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(func() error { _, err := fakeOvn.watcher.GetNode("node1"); return err }).Should(gomega.Succeed())
		base := &fakeOvn.controller.BaseNetworkController
		base.addressSetFactory = addressSetFactory
		gomega.Expect(base.nodeReconciler.RegisterNetworkController(&dcWideBaseNodeHandler{BaseNetworkController: base})).To(gomega.Succeed())
		defer base.nodeReconciler.DeregisterNetworkController(base.GetNetworkName())
		c := NewDCWideOverlaySubnetsController(fakeOvn.watcher.ConfigMapCoreInformer(), base.nodeReconciler)
		// The fake OVN factory is already running; start the newly requested ConfigMap informer.
		gomega.Expect(fakeOvn.watcher.Start()).To(gomega.Succeed())
		gomega.Expect(c.Start()).To(gomega.Succeed())
		defer c.Stop()
		addresses := func() []string { v4, v6 := as.GetAddresses(); return append(v4, v6...) }
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: dcWideOverlaySubnetsConfigMapName, Namespace: config.Kubernetes.OVNConfigNamespace},
			Data: map[string]string{dcWideOverlaySubnetsKey: "10.20.0.0/16,fd00:20::/48,10.128.0.0/14"}}
		_, err = fakeOvn.fakeClient.KubeClient.CoreV1().ConfigMaps(cm.Namespace).Create(context.Background(), cm, metav1.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(addresses).Should(gomega.ConsistOf("10.128.0.0/14", "192.0.2.10", "10.20.0.0/16", "fd00:20::/48"))
		// An unrelated node sync must preserve the configured prefixes.
		gomega.Expect(syncNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName, []string{"192.0.2.10"})).To(gomega.Succeed())
		gomega.Expect(addresses()).To(gomega.ConsistOf("10.128.0.0/14", "192.0.2.10", "10.20.0.0/16", "fd00:20::/48"))
		cm.Data[dcWideOverlaySubnetsKey] = "10.30.0.0/16"
		_, err = fakeOvn.fakeClient.KubeClient.CoreV1().ConfigMaps(cm.Namespace).Update(context.Background(), cm, metav1.UpdateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(addresses).Should(gomega.ConsistOf("10.128.0.0/14", "192.0.2.10", "10.30.0.0/16"))
		cm.Data[dcWideOverlaySubnetsKey] = "invalid"
		_, err = fakeOvn.fakeClient.KubeClient.CoreV1().ConfigMaps(cm.Namespace).Update(context.Background(), cm, metav1.UpdateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(func() string {
			obj, exists, _ := c.informer.GetStore().GetByKey(cm.Namespace + "/" + cm.Name)
			if !exists {
				return ""
			}
			return obj.(*corev1.ConfigMap).Data[dcWideOverlaySubnetsKey]
		}).Should(gomega.Equal("invalid"))
		gomega.Expect(c.sync(cm.Namespace + "/" + cm.Name)).To(gomega.Succeed())
		gomega.Expect(addresses()).To(gomega.ConsistOf("10.128.0.0/14", "192.0.2.10", "10.30.0.0/16"))
		cm.Data[dcWideOverlaySubnetsKey] = "10.40.0.0/16"
		_, err = fakeOvn.fakeClient.KubeClient.CoreV1().ConfigMaps(cm.Namespace).Update(context.Background(), cm, metav1.UpdateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(addresses).Should(gomega.ConsistOf("10.128.0.0/14", "192.0.2.10", "10.40.0.0/16"))

		gomega.Expect(fakeOvn.fakeClient.KubeClient.CoreV1().ConfigMaps(cm.Namespace).Delete(context.Background(), cm.Name, metav1.DeleteOptions{})).To(gomega.Succeed())
		gomega.Eventually(addresses).Should(gomega.ConsistOf("10.128.0.0/14", "192.0.2.10"))
		gomega.Expect(nats()).To(gomega.ConsistOf(originalNATs))
	})

	ginkgo.It("skips Layer 2 no-overlay networks without creating an exemption set", func() {
		info := &snatGuardTestNetInfo{ReconcilableNetInfo: fakeOvn.controller.ReconcilableNetInfo}
		base := &BaseNetworkController{ReconcilableNetInfo: info}
		gomega.Expect(base.ReconcileSNATExemptions()).To(gomega.Succeed())
	})

	ginkgo.DescribeTable("converges when network lifecycle overlaps CIDR publication", func(duringBootstrap bool) {
		config.NoOverlay.EnableDCWideOverlaySNATExemptions = true
		config.Gateway.Mode = config.GatewayModeShared
		config.Default.Transport = types.NetworkTransportNoOverlay
		config.NoOverlay.OutboundSNAT = types.NoOverlaySNATEnabled
		config.IPv6Mode = true
		_, subnet, err := net.ParseCIDR("10.128.0.0/14")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		config.Default.ClusterSubnets = []config.CIDRNetworkEntry{{CIDR: subnet, HostSubnetLength: 24}}
		base := &fakeOvn.controller.BaseNetworkController
		base.addressSetFactory = addressset.NewOvnAddressSetFactory(fakeOvn.nbClient, true, true)
		ctx := context.Background()
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Annotations: map[string]string{"k8s.ovn.org/host-cidrs": `["192.0.2.10/24"]`}}}
		_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(func() error { _, err := fakeOvn.watcher.GetNode("node1"); return err }).Should(gomega.Succeed())
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: dcWideOverlaySubnetsConfigMapName, Namespace: config.Kubernetes.OVNConfigNamespace},
			Data: map[string]string{dcWideOverlaySubnetsKey: "10.20.0.0/16,fd00:20::/48"}}
		publish := func() {
			_, err := fakeOvn.fakeClient.KubeClient.CoreV1().ConfigMaps(cm.Namespace).Create(ctx, cm, metav1.CreateOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if !duringBootstrap {
			publish()
		}
		c := NewDCWideOverlaySubnetsController(fakeOvn.watcher.ConfigMapCoreInformer(), base.nodeReconciler)
		gomega.Expect(fakeOvn.watcher.Start()).To(gomega.Succeed())
		gomega.Expect(c.Start()).To(gomega.Succeed())
		defer c.Stop()
		gomega.Expect(base.nodeReconciler.Start()).To(gomega.Succeed())
		defer base.nodeReconciler.DeregisterNetworkController(base.GetNetworkName())
		handler := &dcWideBaseNodeHandler{BaseNetworkController: base}
		if duringBootstrap {
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			handler.onSync = func() { close(started); <-release }
			registered := make(chan error, 1)
			go func() { registered <- base.nodeReconciler.RegisterNetworkController(handler) }()
			gomega.Eventually(started).Should(gomega.BeClosed())
			publish()
			gomega.Eventually(getDCWideOverlaySubnets).Should(gomega.ConsistOf("10.20.0.0/16", "fd00:20::/48"))
			unblock()
			gomega.Eventually(registered).Should(gomega.Receive(gomega.BeNil()))
		} else {
			gomega.Expect(base.nodeReconciler.RegisterNetworkController(handler)).To(gomega.Succeed())
		}
		addresses := func() []string {
			as, err := getNoOverlaySNATExemptionAddressSet(base.addressSetFactory, base.GetNetInfo(), base.controllerName)
			if err != nil || as == nil {
				return nil
			}
			v4, v6 := as.GetAddresses()
			return append(v4, v6...)
		}
		gomega.Eventually(addresses).Should(gomega.ConsistOf("10.128.0.0/14", "192.0.2.10", "10.20.0.0/16", "fd00:20::/48"))

		// Hold an in-flight node reconciliation while the CIDRs change and deletion starts.
		locked, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		defer unblock()
		inFlight := make(chan error, 1)
		go func() {
			inFlight <- base.nodeReconciler.WithNetworkHandler(base.GetNetworkName(), func(nodecontroller.NodeHandler) error {
				close(locked)
				<-release
				return nil
			})
		}()
		gomega.Eventually(locked).Should(gomega.BeClosed())
		cm.Data[dcWideOverlaySubnetsKey] = "10.30.0.0/16"
		_, err = fakeOvn.fakeClient.KubeClient.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, metav1.UpdateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(getDCWideOverlaySubnets).Should(gomega.ConsistOf("10.30.0.0/16"))
		removed := make(chan struct{})
		go func() { base.nodeReconciler.DeregisterNetworkController(base.GetNetworkName()); close(removed) }()
		unblock()
		gomega.Eventually(inFlight).Should(gomega.Receive(gomega.BeNil()))
		gomega.Eventually(removed).Should(gomega.BeClosed())
		gomega.Expect(cleanupNoOverlaySNATExemptionAddressSet(base.addressSetFactory, base.GetNetInfo(), base.controllerName)).To(gomega.Succeed())
		// Pending work for the removed network must not recreate its deleted address set.
		gomega.Expect(c.reconcile(dcWideOverlayNetworkKeyPrefix + base.GetNetworkName())).To(gomega.Succeed())
		gomega.Expect(addresses()).To(gomega.BeEmpty())

		// Reusing the name must target the new handler and bootstrap from the current list.
		gomega.Expect(base.nodeReconciler.RegisterNetworkController(&dcWideBaseNodeHandler{BaseNetworkController: base})).To(gomega.Succeed())
		gomega.Expect(c.reconcile(dcWideOverlayNetworkKeyPrefix + base.GetNetworkName())).To(gomega.Succeed())
		gomega.Eventually(addresses).Should(gomega.ConsistOf("10.128.0.0/14", "192.0.2.10", "10.30.0.0/16"))
	}, ginkgo.Entry("CIDRs published before registration", false), ginkgo.Entry("CIDRs published during bootstrap", true))

	ginkgo.Context("syncNoOverlaySNATExemptionAddressSet", func() {
		ginkgo.BeforeEach(func() {
			// Set up cluster subnets for testing
			_, cidr, err := net.ParseCIDR("10.128.0.0/14")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			netInfo.subnets = []config.CIDRNetworkEntry{
				{CIDR: cidr, HostSubnetLength: 24},
			}

			_, err = ensureNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("syncs cluster subnets and node IPs to the address set", func() {
			nodeIPs := []string{"192.168.1.10", "192.168.1.11"}
			err := syncNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName, nodeIPs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// Verify subnets and node IPs were added
			dbIDs := libovsdbops.NewDbObjectIDs(
				libovsdbops.AddressSetNoOverlaySNATExemption,
				controllerName,
				map[libovsdbops.ExternalIDKey]string{
					libovsdbops.ObjectNameKey: asName,
					libovsdbops.NetworkKey:    netInfo.GetNetworkName(),
				},
			)
			as, err := addressSetFactory.GetAddressSet(dbIDs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			ipv4Addrs, ipv6Addrs := as.GetAddresses()
			// Check that addresses match the cluster subnets plus node IPs
			expectedAddrs := []string{}
			for _, subnet := range netInfo.Subnets() {
				expectedAddrs = append(expectedAddrs, subnet.CIDR.String())
			}
			expectedAddrs = append(expectedAddrs, nodeIPs...)
			allAddrs := make([]string, 0, len(ipv4Addrs)+len(ipv6Addrs))
			allAddrs = append(allAddrs, ipv4Addrs...)
			allAddrs = append(allAddrs, ipv6Addrs...)
			gomega.Expect(allAddrs).To(gomega.ConsistOf(expectedAddrs))
		})

		ginkgo.It("filters DC-wide prefixes by family and ignores them when disabled", func() {
			setDCWideOverlaySubnets([]string{"10.20.0.0/16", "fd00:20::/48"})
			config.NoOverlay.EnableDCWideOverlaySNATExemptions = true
			netInfo.ipMode = &[2]bool{true, false}
			gomega.Expect(syncNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName, []string{"192.0.2.10"})).To(gomega.Succeed())
			as, err := getNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			v4, v6 := as.GetAddresses()
			gomega.Expect(v4).To(gomega.ConsistOf("10.128.0.0/14", "192.0.2.10", "10.20.0.0/16"))
			gomega.Expect(v6).To(gomega.BeEmpty())
			config.NoOverlay.EnableDCWideOverlaySNATExemptions = false
			gomega.Expect(syncNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName, []string{"192.0.2.10"})).To(gomega.Succeed())
			v4, v6 = as.GetAddresses()
			gomega.Expect(v4).To(gomega.ConsistOf("10.128.0.0/14", "192.0.2.10"))
			gomega.Expect(v6).To(gomega.BeEmpty())
		})

		ginkgo.It("handles idempotent syncs correctly", func() {
			nodeIPs := []string{"192.168.1.10", "192.168.1.11"}

			// First sync
			err := syncNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName, nodeIPs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// Get the addresses after first sync
			dbIDs := libovsdbops.NewDbObjectIDs(
				libovsdbops.AddressSetNoOverlaySNATExemption,
				controllerName,
				map[libovsdbops.ExternalIDKey]string{
					libovsdbops.ObjectNameKey: asName,
					libovsdbops.NetworkKey:    netInfo.GetNetworkName(),
				},
			)
			as, err := addressSetFactory.GetAddressSet(dbIDs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			firstIPv4, firstIPv6 := as.GetAddresses()

			// Second sync with same data
			err = syncNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName, nodeIPs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// Verify addresses remain the same
			secondIPv4, secondIPv6 := as.GetAddresses()
			gomega.Expect(secondIPv4).To(gomega.Equal(firstIPv4))
			gomega.Expect(secondIPv6).To(gomega.Equal(firstIPv6))
		})

		ginkgo.It("creates a missing address-set family during a dual-stack upgrade", func() {
			as, err := getNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			originalV4UUID, originalV6UUID := as.GetASUUID()
			gomega.Expect(originalV4UUID).NotTo(gomega.BeEmpty())
			gomega.Expect(originalV6UUID).To(gomega.BeEmpty())

			_, ipv6CIDR, err := net.ParseCIDR("fd00:10:128::/48")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			netInfo.subnets = append(netInfo.subnets, config.CIDRNetworkEntry{
				CIDR:             ipv6CIDR,
				HostSubnetLength: 64,
			})
			netInfo.ipMode = &[2]bool{true, true}
			dualStackFactory := addressset.NewOvnAddressSetFactory(fakeOvn.nbClient, true, true)
			nodeIPs := []string{"192.168.1.10", "fd00::10"}

			err = syncNoOverlaySNATExemptionAddressSet(dualStackFactory, netInfo, controllerName, nodeIPs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			as, err = getNoOverlaySNATExemptionAddressSet(dualStackFactory, netInfo, controllerName)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			v4UUID, v6UUID := as.GetASUUID()
			gomega.Expect(v4UUID).To(gomega.Equal(originalV4UUID))
			gomega.Expect(v6UUID).NotTo(gomega.BeEmpty())

			ipv4Addrs, ipv6Addrs := as.GetAddresses()
			gomega.Expect(ipv4Addrs).To(gomega.ConsistOf("10.128.0.0/14", "192.168.1.10"))
			gomega.Expect(ipv6Addrs).To(gomega.ConsistOf("fd00:10:128::/48", "fd00::10"))

			_, _, err = getNoOverlaySNATExemptionAsUUID(dualStackFactory, netInfo, controllerName)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("updates node IPs when they change", func() {
			// First sync with initial node IPs
			initialNodeIPs := []string{"192.168.1.10"}
			err := syncNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName, initialNodeIPs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// Verify initial state
			dbIDs := libovsdbops.NewDbObjectIDs(
				libovsdbops.AddressSetNoOverlaySNATExemption,
				controllerName,
				map[libovsdbops.ExternalIDKey]string{
					libovsdbops.ObjectNameKey: asName,
					libovsdbops.NetworkKey:    netInfo.GetNetworkName(),
				},
			)
			as, err := addressSetFactory.GetAddressSet(dbIDs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			ipv4Addrs, ipv6Addrs := as.GetAddresses()
			initialExpected := []string{}
			for _, subnet := range netInfo.Subnets() {
				initialExpected = append(initialExpected, subnet.CIDR.String())
			}
			initialExpected = append(initialExpected, initialNodeIPs...)
			allAddrs := make([]string, 0, len(ipv4Addrs)+len(ipv6Addrs))
			allAddrs = append(allAddrs, ipv4Addrs...)
			allAddrs = append(allAddrs, ipv6Addrs...)
			gomega.Expect(allAddrs).To(gomega.ConsistOf(initialExpected))

			// Sync with updated node IPs
			updatedNodeIPs := []string{"192.168.1.10", "192.168.1.11", "192.168.1.12"}
			err = syncNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName, updatedNodeIPs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// Verify updated state
			ipv4Addrs, ipv6Addrs = as.GetAddresses()
			updatedExpected := []string{}
			for _, subnet := range netInfo.Subnets() {
				updatedExpected = append(updatedExpected, subnet.CIDR.String())
			}
			updatedExpected = append(updatedExpected, updatedNodeIPs...)
			allAddrs = make([]string, 0, len(ipv4Addrs)+len(ipv6Addrs))
			allAddrs = append(allAddrs, ipv4Addrs...)
			allAddrs = append(allAddrs, ipv6Addrs...)
			gomega.Expect(allAddrs).To(gomega.ConsistOf(updatedExpected))
		})
	})

	ginkgo.Context("cleanupNoOverlaySNATExemptionAddressSet", func() {
		ginkgo.It("removes the address set", func() {
			_, err := ensureNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// Verify address set exists before cleanup
			dbIDs := libovsdbops.NewDbObjectIDs(
				libovsdbops.AddressSetNoOverlaySNATExemption,
				controllerName,
				map[libovsdbops.ExternalIDKey]string{
					libovsdbops.ObjectNameKey: asName,
					libovsdbops.NetworkKey:    netInfo.GetNetworkName(),
				},
			)
			as, err := addressSetFactory.GetAddressSet(dbIDs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(as).NotTo(gomega.BeNil())

			// Verify UUIDs exist before cleanup
			v4UUID, v6UUID := as.GetASUUID()
			if config.IPv4Mode {
				gomega.Expect(v4UUID).NotTo(gomega.BeEmpty())
			}
			if config.IPv6Mode {
				gomega.Expect(v6UUID).NotTo(gomega.BeEmpty())
			}

			// Cleanup
			err = cleanupNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// Verify address set was removed - UUIDs should be empty after cleanup
			as, err = addressSetFactory.GetAddressSet(dbIDs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			v4UUID, v6UUID = as.GetASUUID()
			gomega.Expect(v4UUID).To(gomega.BeEmpty())
			gomega.Expect(v6UUID).To(gomega.BeEmpty())
		})
	})

	ginkgo.Context("getNoOverlaySNATExemptionAsUUID", func() {
		ginkgo.It("returns UUIDs for IPv4 and IPv6 address sets when it exists", func() {
			_, err := ensureNoOverlaySNATExemptionAddressSet(addressSetFactory, netInfo, controllerName)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			v4UUID, v6UUID, err := getNoOverlaySNATExemptionAsUUID(addressSetFactory, netInfo, controllerName)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// For IPv4 mode, v4UUID should be set
			if config.IPv4Mode {
				gomega.Expect(v4UUID).NotTo(gomega.BeEmpty())
			}
			// For IPv6 mode, v6UUID should be set
			if config.IPv6Mode {
				gomega.Expect(v6UUID).NotTo(gomega.BeEmpty())
			}
		})

		ginkgo.It("returns an error when it doesn't exist", func() {
			_, _, err := getNoOverlaySNATExemptionAsUUID(addressSetFactory, netInfo, controllerName)
			gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("no-overlay SNAT exemption address set")))
		})

		ginkgo.It("does not require the missing family for a single-stack network on a dual-stack cluster", func() {
			// A dual-stack cluster with an IPv4-only network: the network's
			// address set only ever holds the IPv4 family, so the missing IPv6
			// UUID must not be treated as an error.
			config.IPv4Mode = true
			config.IPv6Mode = true
			netInfo.ipMode = &[2]bool{true, false}
			singleStackFactory := addressset.NewOvnAddressSetFactory(fakeOvn.nbClient, true, false)

			_, err := ensureNoOverlaySNATExemptionAddressSet(singleStackFactory, netInfo, controllerName)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			v4UUID, v6UUID, err := getNoOverlaySNATExemptionAsUUID(singleStackFactory, netInfo, controllerName)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(v4UUID).NotTo(gomega.BeEmpty())
			gomega.Expect(v6UUID).To(gomega.BeEmpty())
		})
	})
})
