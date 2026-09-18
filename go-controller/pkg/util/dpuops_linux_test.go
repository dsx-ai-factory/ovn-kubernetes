// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build linux
// +build linux

package util

import (
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/k8snetworkplumbingwg/sriovnet"
	"github.com/onsi/gomega"

	libovsdbclient "github.com/ovn-kubernetes/libovsdb/client"

	ovsops "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/libovsdb/ops"
	ovntest "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/testing"
	libovsdbtest "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/testing/libovsdb"
	utilmocks "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util/mocks"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/vswitchd"
)

const (
	testHostPFMAC  = "00:73:58:6d:a1:b3"
	testHostVF0MAC = "00:07:3d:f2:76:4a"
)

// Captured from a DPU serving a host with eight quad-port ConnectX-9 cards.
// Every card presents pf0 through pf3 and every PF has its own VF index space,
// so pfID 0 and vfID 0 name eight different functions; the PF MAC and the
// eswitch device behind it are what tell them apart.
const (
	cx9CardBPFMAC  = "dc:73:fc:22:20:b0" // host PF enP1p3s0f0np0
	cx9CardBESwich = "0005:06:00.0"
	cx9CardBVF0Rep = "A56c1pf0vf0"

	cx9CardAPFMAC  = "dc:73:fc:22:20:a0" // host PF enP3p1s0f0np0
	cx9CardAESwich = "0005:03:00.0"
	cx9CardAVF0Rep = "A53c1pf0vf0"
)

// cx9PortParams builds the port parameters sriovnet derives from a ConnectX-9
// host PF MAC. Host PFs are always external functions of controller 1.
func cx9PortParams(eswitch string, pfNumber uint16) *sriovnet.RepresentorPortParams {
	return &sriovnet.RepresentorPortParams{
		ECPF:             eswitch,
		ControllerNumber: 1,
		PFNumber:         pfNumber,
	}
}

func TestResolveDPUConnectionRepresentorWithoutPFMAC(t *testing.T) {
	g := gomega.NewWithT(t)
	sriovOps := replaceSriovnetOps(t)
	// Connection details written before pfMac existed carry only the indices,
	// so the legacy lookup has to stay reachable. Registering none of the port
	// params calls makes the mock fail the test if the MAC path is taken.
	sriovOps.On("GetVfRepresentorDPU", "0", "9").Return("pf0vf9", nil)

	rep, err := ResolveDPUConnectionRepresentor(&DPUConnectionDetails{PfId: "0", VfId: "9"})
	g.Expect(err).NotTo(gomega.HaveOccurred(), "an empty PF MAC must fall back to the index lookup")
	g.Expect(rep).To(gomega.Equal("pf0vf9"))
}

func TestResolveDPUConnectionRepresentorAnchorsOnPFMAC(t *testing.T) {
	g := gomega.NewWithT(t)
	sriovOps := replaceSriovnetOps(t)
	sriovOps.On("GetPFRepresentorPortParamsFromMAC", ovntest.MustParseMAC(cx9CardBPFMAC)).
		Return(cx9PortParams(cx9CardBESwich, 0), nil)
	sriovOps.On("GetVfRepresentorFromPortParams", cx9PortParams(cx9CardBESwich, 0), uint32(0)).
		Return(cx9CardBVF0Rep, nil)

	rep, err := ResolveDPUConnectionRepresentor(&DPUConnectionDetails{PfId: "0", VfId: "0", PfMAC: cx9CardBPFMAC})
	g.Expect(err).NotTo(gomega.HaveOccurred(), "resolving a VF representor through the PF MAC must succeed")
	g.Expect(rep).To(gomega.Equal(cx9CardBVF0Rep))
}

// TestResolveDPUConnectionRepresentorDistinguishesRepeatedPFIndex is the
// regression test for quad-port ConnectX-9 hosts: two pods reporting the same
// pfID and vfID on different cards must resolve to different representors. The
// deprecated index lookup returns the same netdev for both.
func TestResolveDPUConnectionRepresentorDistinguishesRepeatedPFIndex(t *testing.T) {
	g := gomega.NewWithT(t)
	sriovOps := replaceSriovnetOps(t)
	sriovOps.On("GetPFRepresentorPortParamsFromMAC", ovntest.MustParseMAC(cx9CardBPFMAC)).
		Return(cx9PortParams(cx9CardBESwich, 0), nil)
	sriovOps.On("GetVfRepresentorFromPortParams", cx9PortParams(cx9CardBESwich, 0), uint32(0)).
		Return(cx9CardBVF0Rep, nil)
	sriovOps.On("GetPFRepresentorPortParamsFromMAC", ovntest.MustParseMAC(cx9CardAPFMAC)).
		Return(cx9PortParams(cx9CardAESwich, 0), nil)
	sriovOps.On("GetVfRepresentorFromPortParams", cx9PortParams(cx9CardAESwich, 0), uint32(0)).
		Return(cx9CardAVF0Rep, nil)

	repB, err := ResolveDPUConnectionRepresentor(&DPUConnectionDetails{PfId: "0", VfId: "0", PfMAC: cx9CardBPFMAC})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	repA, err := ResolveDPUConnectionRepresentor(&DPUConnectionDetails{PfId: "0", VfId: "0", PfMAC: cx9CardAPFMAC})
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(repB).To(gomega.Equal(cx9CardBVF0Rep))
	g.Expect(repA).To(gomega.Equal(cx9CardAVF0Rep))
	g.Expect(repB).NotTo(gomega.Equal(repA),
		"VF 0 of PF 0 on two different cards must not resolve to the same representor")
}

// TestResolveDPUConnectionRepresentorResolvesEveryPFIndex covers PF indices 2 and
// 3, which a quad-port card exposes and sriovnet's GetVfRepresentorDPU rejects
// outright.
func TestResolveDPUConnectionRepresentorResolvesEveryPFIndex(t *testing.T) {
	for pfIndex := uint16(0); pfIndex < 4; pfIndex++ {
		t.Run(fmt.Sprintf("pf%d", pfIndex), func(t *testing.T) {
			g := gomega.NewWithT(t)
			sriovOps := replaceSriovnetOps(t)
			pfMAC := fmt.Sprintf("dc:73:fc:22:20:b%d", pfIndex)
			eswitch := fmt.Sprintf("0005:06:00.%d", pfIndex)
			expectedRep := fmt.Sprintf("A56c1pf%dvf0", pfIndex)
			sriovOps.On("GetPFRepresentorPortParamsFromMAC", ovntest.MustParseMAC(pfMAC)).
				Return(cx9PortParams(eswitch, pfIndex), nil)
			sriovOps.On("GetVfRepresentorFromPortParams", cx9PortParams(eswitch, pfIndex), uint32(0)).
				Return(expectedRep, nil)

			rep, err := ResolveDPUConnectionRepresentor(&DPUConnectionDetails{PfId: strconv.Itoa(int(pfIndex)), VfId: "0", PfMAC: pfMAC})
			g.Expect(err).NotTo(gomega.HaveOccurred(), "every PF of a quad-port card must resolve")
			g.Expect(rep).To(gomega.Equal(expectedRep))
		})
	}
}

// TestResolveDPUConnectionRepresentorDoesNotFallBackOnMiss pins the fail-closed
// behaviour: the index lookup is ambiguous across cards, so falling back to it
// could plumb a representor belonging to a pod on another card.
func TestResolveDPUConnectionRepresentorDoesNotFallBackOnMiss(t *testing.T) {
	tests := []struct {
		name        string
		lookupErr   error
		sentinel    error
		errContains string
	}{
		{
			name:        "no port reports the MAC",
			lookupErr:   fmt.Errorf("no matching devlink port found: %w", sriovnet.ErrRepresentorNotFound),
			sentinel:    sriovnet.ErrRepresentorNotFound,
			errContains: "eswitch port of the host PF",
		},
		{
			name:        "several ports report the MAC",
			lookupErr:   fmt.Errorf("multiple matching devlink ports: %w", sriovnet.ErrMultipleRepresentorsFound),
			sentinel:    sriovnet.ErrMultipleRepresentorsFound,
			errContains: "eswitch port of the host PF",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			sriovOps := replaceSriovnetOps(t)
			// GetVfRepresentorDPU is deliberately not registered: the mock
			// fails the test if the ambiguous lookup is attempted.
			sriovOps.On("GetPFRepresentorPortParamsFromMAC", ovntest.MustParseMAC(cx9CardBPFMAC)).
				Return(nil, tt.lookupErr)

			_, err := ResolveDPUConnectionRepresentor(&DPUConnectionDetails{PfId: "0", VfId: "0", PfMAC: cx9CardBPFMAC})
			g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring(tt.errContains)))
			g.Expect(errors.Is(err, tt.sentinel)).To(gomega.BeTrue(),
				"the sriovnet sentinel must survive wrapping so callers can tell a miss from a duplicate")
		})
	}
}

func TestResolveDPUConnectionRepresentorRejectsMalformedInput(t *testing.T) {
	t.Run("malformed PF MAC", func(t *testing.T) {
		g := gomega.NewWithT(t)
		replaceSriovnetOps(t)

		_, err := ResolveDPUConnectionRepresentor(&DPUConnectionDetails{PfId: "0", VfId: "0", PfMAC: "not-a-mac"})
		g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("published host PF MAC")),
			"an unparseable MAC must not silently degrade to the index lookup")
	})

	t.Run("malformed VF index", func(t *testing.T) {
		g := gomega.NewWithT(t)
		sriovOps := replaceSriovnetOps(t)
		sriovOps.On("GetPFRepresentorPortParamsFromMAC", ovntest.MustParseMAC(cx9CardBPFMAC)).
			Return(cx9PortParams(cx9CardBESwich, 0), nil)

		_, err := ResolveDPUConnectionRepresentor(&DPUConnectionDetails{PfId: "0", VfId: "vf9", PfMAC: cx9CardBPFMAC})
		g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("parse VF index")))
	})
}

func TestSimulatedGetPortRepresentorByPFMACUnsupported(t *testing.T) {
	g := gomega.NewWithT(t)

	_, err := (&SimulatedDPUOps{}).GetPortRepresentorByPFMAC(ovntest.MustParseMAC(cx9CardBPFMAC), "0")
	g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("not supported in simulated DPU environments")),
		"simulated DPUs have no eswitch to query by MAC and resolve through the index pair instead")
}

// newVFBridgeHarness builds an OVS bridge holding a single host VF representor,
// which is the layout of an Uplink bridge on a BlueField style DPU.
func newVFBridgeHarness(t *testing.T) libovsdbclient.Client {
	t.Helper()
	g := gomega.NewWithT(t)

	ovsClient, testCtx, err := libovsdbtest.NewOVSTestHarness(libovsdbtest.TestSetup{
		OVSData: []libovsdbtest.TestData{
			&vswitchd.OpenvSwitch{UUID: "root-ovs", Bridges: []string{"br-vm-uuid"}},
			&vswitchd.Bridge{UUID: "br-vm-uuid", Name: "br-vm", Ports: []string{"pf0vf0-port-uuid"}},
			&vswitchd.Port{UUID: "pf0vf0-port-uuid", Name: "pf0vf0", Interfaces: []string{"pf0vf0-iface-uuid"}},
			&vswitchd.Interface{UUID: "pf0vf0-iface-uuid", Name: "pf0vf0", Type: "system"},
		},
	})
	g.Expect(err).NotTo(gomega.HaveOccurred(), "failed to build the OVS test harness for the VF bridge")
	t.Cleanup(testCtx.Cleanup)
	return ovsClient
}

func replaceSriovnetOps(t *testing.T) *utilmocks.SriovnetOps {
	t.Helper()
	sriovOps := utilmocks.NewSriovnetOps(t)
	orig := GetSriovnetOps()
	SetSriovnetOpsInst(sriovOps)
	t.Cleanup(func() {
		SetSriovnetOpsInst(orig)
	})
	return sriovOps
}

func mustGetBridge(t *testing.T, ovsClient libovsdbclient.Client, name string) *vswitchd.Bridge {
	t.Helper()
	g := gomega.NewWithT(t)
	bridge, err := ovsops.GetBridge(ovsClient, name)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "failed to get bridge %s from the test harness", name)
	return bridge
}

func TestSwitchdevFindHostRepresentorByPeerMACMatchesVF(t *testing.T) {
	g := gomega.NewWithT(t)
	ovsClient := newVFBridgeHarness(t)

	sriovOps := replaceSriovnetOps(t)
	sriovOps.On("GetRepresentorPortFlavour", "pf0vf0").
		Return(sriovnet.PortFlavour(sriovnet.PORT_FLAVOUR_PCI_VF), nil)
	sriovOps.On("GetDevlinkPortFunctionMacAddress", "pf0vf0").
		Return(ovntest.MustParseMAC(testHostVF0MAC), nil)

	rep, err := (&SwitchdevDPUOps{}).FindHostRepresentorByPeerMAC(
		ovsClient, mustGetBridge(t, ovsClient, "br-vm"), ovntest.MustParseMAC(testHostVF0MAC), "node-a")
	g.Expect(err).NotTo(gomega.HaveOccurred(), "looking up the VF representor by its host peer MAC must succeed")
	g.Expect(rep).To(gomega.Equal("pf0vf0"), "pf0vf0 is the VF representor peering with the host MAC")
}

func TestSwitchdevFindHostRepresentorByPeerMACMatchesSF(t *testing.T) {
	g := gomega.NewWithT(t)
	ovsClient := newVFBridgeHarness(t)

	sriovOps := replaceSriovnetOps(t)
	sriovOps.On("GetRepresentorPortFlavour", "pf0vf0").
		Return(sriovnet.PortFlavour(sriovnet.PORT_FLAVOUR_PCI_SF), nil)
	// SF representors resolve directly through devlink; there is no sriovnet
	// fallback to configure.
	sriovOps.On("GetDevlinkPortFunctionMacAddress", "pf0vf0").
		Return(ovntest.MustParseMAC(testHostVF0MAC), nil)

	rep, err := (&SwitchdevDPUOps{}).FindHostRepresentorByPeerMAC(
		ovsClient, mustGetBridge(t, ovsClient, "br-vm"), ovntest.MustParseMAC(testHostVF0MAC), "node-a")
	g.Expect(err).NotTo(gomega.HaveOccurred(), "looking up the SF representor by its host peer MAC must succeed")
	g.Expect(rep).To(gomega.Equal("pf0vf0"), "pf0vf0 is the SF representor peering with the host MAC")
}

func TestSwitchdevFindHostRepresentorByPeerMACRejectsZeroFallbackMAC(t *testing.T) {
	g := gomega.NewWithT(t)
	ovsClient := newVFBridgeHarness(t)

	sriovOps := replaceSriovnetOps(t)
	sriovOps.On("GetRepresentorPortFlavour", "pf0vf0").
		Return(sriovnet.PortFlavour(sriovnet.PORT_FLAVOUR_PCI_PF), nil)
	sriovOps.On("GetDevlinkPortFunctionMacAddress", "pf0vf0").
		Return(nil, fmt.Errorf("devlink unavailable"))
	// The sriovnet fallback can report an unset PF peer MAC as all zeroes with
	// a nil error; it must not be matched, even against a zero host MAC.
	sriovOps.On("GetRepresentorPeerMacAddress", "pf0vf0").
		Return(ovntest.MustParseMAC("00:00:00:00:00:00"), nil)

	_, err := (&SwitchdevDPUOps{}).FindHostRepresentorByPeerMAC(
		ovsClient, mustGetBridge(t, ovsClient, "br-vm"), ovntest.MustParseMAC("00:00:00:00:00:00"), "node-a")
	g.Expect(errors.Is(err, ErrHostRepresentorNotFound)).To(gomega.BeTrue(),
		"an all-zero fallback MAC must be treated as unset, not matched")
}

func TestSwitchdevFindHostRepresentorByPeerMACMissWrapsSentinel(t *testing.T) {
	g := gomega.NewWithT(t)
	ovsClient := newVFBridgeHarness(t)

	sriovOps := replaceSriovnetOps(t)
	sriovOps.On("GetRepresentorPortFlavour", "pf0vf0").
		Return(sriovnet.PortFlavour(sriovnet.PORT_FLAVOUR_PCI_VF), nil)
	sriovOps.On("GetDevlinkPortFunctionMacAddress", "pf0vf0").
		Return(ovntest.MustParseMAC(testHostVF0MAC), nil)

	_, err := (&SwitchdevDPUOps{}).FindHostRepresentorByPeerMAC(
		ovsClient, mustGetBridge(t, ovsClient, "br-vm"), ovntest.MustParseMAC(testHostPFMAC), "node-a")
	g.Expect(errors.Is(err, ErrHostRepresentorNotFound)).To(gomega.BeTrue(),
		"a clean miss must wrap ErrHostRepresentorNotFound so callers can keep searching")
}

func TestSwitchdevFindHostRepresentorByPeerMACSkipsUnreadableRepresentor(t *testing.T) {
	g := gomega.NewWithT(t)
	ovsClient, testCtx, err := libovsdbtest.NewOVSTestHarness(libovsdbtest.TestSetup{
		OVSData: []libovsdbtest.TestData{
			&vswitchd.OpenvSwitch{UUID: "root-ovs", Bridges: []string{"br-vm-uuid"}},
			&vswitchd.Bridge{UUID: "br-vm-uuid", Name: "br-vm", Ports: []string{"pf0vf0-port-uuid", "pf0vf7-port-uuid"}},
			&vswitchd.Port{UUID: "pf0vf0-port-uuid", Name: "pf0vf0", Interfaces: []string{"pf0vf0-iface-uuid"}},
			&vswitchd.Interface{UUID: "pf0vf0-iface-uuid", Name: "pf0vf0", Type: "system"},
			&vswitchd.Port{UUID: "pf0vf7-port-uuid", Name: "pf0vf7", Interfaces: []string{"pf0vf7-iface-uuid"}},
			&vswitchd.Interface{UUID: "pf0vf7-iface-uuid", Name: "pf0vf7", Type: "system"},
		},
	})
	g.Expect(err).NotTo(gomega.HaveOccurred(), "failed to build the OVS test harness for the two-representor bridge")
	t.Cleanup(testCtx.Cleanup)

	sriovOps := replaceSriovnetOps(t)
	// pf0vf0 is unreadable: a VF representor has no sriovnet fallback, so
	// its devlink failure must not abort the scan. The walk is in map order,
	// so when pf0vf7 happens to be visited first pf0vf0 is never probed;
	// hence the Maybe.
	sriovOps.On("GetRepresentorPortFlavour", "pf0vf0").
		Return(sriovnet.PortFlavour(sriovnet.PORT_FLAVOUR_PCI_VF), nil).
		Maybe()
	sriovOps.On("GetDevlinkPortFunctionMacAddress", "pf0vf0").
		Return(nil, fmt.Errorf("devlink unavailable")).
		Maybe()
	sriovOps.On("GetRepresentorPortFlavour", "pf0vf7").
		Return(sriovnet.PortFlavour(sriovnet.PORT_FLAVOUR_PCI_VF), nil)
	sriovOps.On("GetDevlinkPortFunctionMacAddress", "pf0vf7").
		Return(ovntest.MustParseMAC(testHostVF0MAC), nil)

	rep, err := (&SwitchdevDPUOps{}).FindHostRepresentorByPeerMAC(
		ovsClient, mustGetBridge(t, ovsClient, "br-vm"), ovntest.MustParseMAC(testHostVF0MAC), "node-a")
	g.Expect(err).NotTo(gomega.HaveOccurred(),
		"the unreadable representor must be skipped, not abort the scan")
	g.Expect(rep).To(gomega.Equal("pf0vf7"), "the matching representor after the unreadable one must be found")
}

// TestSwitchdevGetHostGatewayMACAddressStaysPFOnly guards the default gateway
// bridge path, which must keep resolving through the host PF representor only.
func TestSwitchdevGetHostGatewayMACAddressStaysPFOnly(t *testing.T) {
	g := gomega.NewWithT(t)
	ovsClient := newVFBridgeHarness(t)

	sriovOps := replaceSriovnetOps(t)
	sriovOps.On("GetRepresentorPortFlavour", "pf0vf0").
		Return(sriovnet.PortFlavour(sriovnet.PORT_FLAVOUR_PCI_VF), nil)

	_, err := (&SwitchdevDPUOps{}).GetHostGatewayMACAddress(ovsClient, "br-vm", "node-a")
	g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("dpu host interface was not found")))
}

func TestSimulatedFindHostRepresentorByPeerMAC(t *testing.T) {
	g := gomega.NewWithT(t)
	ovsClient, testCtx, err := libovsdbtest.NewOVSTestHarness(libovsdbtest.TestSetup{
		OVSData: []libovsdbtest.TestData{
			&vswitchd.OpenvSwitch{UUID: "root-ovs", Bridges: []string{"br-vm-uuid"}},
			&vswitchd.Bridge{UUID: "br-vm-uuid", Name: "br-vm", Ports: []string{"rep-port-uuid"}},
			&vswitchd.Port{UUID: "rep-port-uuid", Name: "rep0-1", Interfaces: []string{"rep-iface-uuid"}},
			&vswitchd.Interface{UUID: "rep-iface-uuid", Name: "rep0-1"},
		},
	})
	g.Expect(err).NotTo(gomega.HaveOccurred(), "failed to build the OVS test harness for the simulated bridge")
	t.Cleanup(testCtx.Cleanup)

	ops := &SimulatedDPUOps{}
	// The MAC the simulator derives for this bridge must be the same one the
	// reverse lookup accepts.
	hostMAC, err := ops.GetHostGatewayMACAddress(ovsClient, "br-vm", "node-a")
	g.Expect(err).NotTo(gomega.HaveOccurred(), "deriving the simulated host gateway MAC must succeed")

	rep, err := ops.FindHostRepresentorByPeerMAC(ovsClient, mustGetBridge(t, ovsClient, "br-vm"), hostMAC, "node-a")
	g.Expect(err).NotTo(gomega.HaveOccurred(), "the reverse lookup must accept the MAC the forward lookup derived")
	g.Expect(rep).To(gomega.Equal("rep0-1"), "rep0-1 is the only simulated representor on br-vm")

	// A different node derives a different MAC, so the lookup must miss.
	otherMAC, err := ops.GetHostGatewayMACAddress(ovsClient, "br-vm", "node-b")
	g.Expect(err).NotTo(gomega.HaveOccurred(), "deriving the simulated MAC for another node must succeed")
	_, err = ops.FindHostRepresentorByPeerMAC(ovsClient, mustGetBridge(t, ovsClient, "br-vm"), otherMAC, "node-a")
	g.Expect(errors.Is(err, ErrHostRepresentorNotFound)).To(gomega.BeTrue(),
		"a MAC derived for another node must be a clean miss")
}

func replaceFileSystemOps(t *testing.T) *utilmocks.FileSystemOps {
	t.Helper()
	fsOps := utilmocks.NewFileSystemOps(t)
	orig := GetFileSystemOps()
	SetFileSystemOps(fsOps)
	t.Cleanup(func() {
		SetFileSystemOps(orig)
	})
	return fsOps
}

func TestSwitchdevResolvePFIndex(t *testing.T) {
	const (
		pf1Netdev   = "enp4s0f1"
		pf1PCI      = "0000:03:00.1"
		vf7PCI      = "0000:03:02.3"
		nonSriovPCI = "0000:05:00.0"
	)

	t.Run("PF netdev resolves to its PCI function number", func(t *testing.T) {
		g := gomega.NewWithT(t)
		fsOps := replaceFileSystemOps(t)
		sriovOps := replaceSriovnetOps(t)
		fsOps.On("Readlink", "/sys/class/net/"+pf1Netdev+"/device").
			Return("../../"+pf1PCI, nil)
		sriovOps.On("GetPfPciFromVfPci", pf1PCI).
			Return("", fmt.Errorf("no physfn link, not a VF"))
		fsOps.On("PathExists", "/sys/bus/pci/devices/"+pf1PCI+"/sriov_totalvfs").
			Return(true, nil)

		pfIndex, err := (&SwitchdevDPUOps{}).ResolvePFIndex(pf1Netdev)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "resolving an SR-IOV capable PF must succeed")
		g.Expect(pfIndex).To(gomega.Equal(1), "the PF index is the PCI function number")
	})

	t.Run("VF is rejected", func(t *testing.T) {
		g := gomega.NewWithT(t)
		replaceFileSystemOps(t)
		sriovOps := replaceSriovnetOps(t)
		sriovOps.On("GetPfPciFromVfPci", vf7PCI).Return(pf1PCI, nil)

		_, err := (&SwitchdevDPUOps{}).ResolvePFIndex(vf7PCI)
		g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("virtual function")),
			"a VF has its own VF resolution path and must not resolve as a PF")
	})

	t.Run("non SR-IOV device is rejected", func(t *testing.T) {
		g := gomega.NewWithT(t)
		fsOps := replaceFileSystemOps(t)
		sriovOps := replaceSriovnetOps(t)
		sriovOps.On("GetPfPciFromVfPci", nonSriovPCI).
			Return("", fmt.Errorf("no physfn link, not a VF"))
		fsOps.On("PathExists", "/sys/bus/pci/devices/"+nonSriovPCI+"/sriov_totalvfs").
			Return(false, nil)

		_, err := (&SwitchdevDPUOps{}).ResolvePFIndex(nonSriovPCI)
		g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("not an SR-IOV capable")),
			"a plain NIC must not be treated as a PF with a DPU representor")
	})

	t.Run("unknown netdev is rejected", func(t *testing.T) {
		g := gomega.NewWithT(t)
		fsOps := replaceFileSystemOps(t)
		replaceSriovnetOps(t)
		fsOps.On("Readlink", "/sys/class/net/nosuch0/device").
			Return("", fmt.Errorf("no such file or directory"))

		_, err := (&SwitchdevDPUOps{}).ResolvePFIndex("nosuch0")
		g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("sysfs device link")),
			"a netdev with no sysfs device link must not resolve as a PF")
	})

	t.Run("SR-IOV capability check failure is reported", func(t *testing.T) {
		g := gomega.NewWithT(t)
		fsOps := replaceFileSystemOps(t)
		sriovOps := replaceSriovnetOps(t)
		sriovOps.On("GetPfPciFromVfPci", pf1PCI).
			Return("", fmt.Errorf("no physfn link, not a VF"))
		fsOps.On("PathExists", "/sys/bus/pci/devices/"+pf1PCI+"/sriov_totalvfs").
			Return(false, fmt.Errorf("permission denied"))

		_, err := (&SwitchdevDPUOps{}).ResolvePFIndex(pf1PCI)
		g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("SR-IOV capability")),
			"a failed capability check must surface as an error, not as a missing PF")
	})

	t.Run("non-PCI parent device is rejected", func(t *testing.T) {
		g := gomega.NewWithT(t)
		fsOps := replaceFileSystemOps(t)
		sriovOps := replaceSriovnetOps(t)
		fsOps.On("Readlink", "/sys/class/net/virt0/device").
			Return("../../virtio3", nil)
		sriovOps.On("GetPfPciFromVfPci", "virtio3").
			Return("", fmt.Errorf("no physfn link, not a VF"))
		fsOps.On("PathExists", "/sys/bus/pci/devices/virtio3/sriov_totalvfs").
			Return(true, nil)

		_, err := (&SwitchdevDPUOps{}).ResolvePFIndex("virt0")
		g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("failed to parse PCI address")),
			"a parent device that is not a PCI function must not resolve as a PF")
	})
}
