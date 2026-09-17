// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"

	"github.com/k8snetworkplumbingwg/sriovnet"
	"github.com/stretchr/testify/mock"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	libovsdbclient "github.com/ovn-kubernetes/libovsdb/client"
	"github.com/ovn-kubernetes/libovsdb/model"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/cni"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	adminpolicybasedrouteclient "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/adminpolicybasedroute/v1/apis/clientset/versioned/fake"
	factorymocks "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/factory/mocks"
	kubemocks "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/kube/mocks"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/node/routemanager"
	ovntest "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/testing"
	libovsdbtest "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/testing/libovsdb"
	linkMock "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/testing/mocks/github.com/vishvananda/netlink"
	coreinformermocks "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/testing/mocks/k8s.io/client-go/informers/core/v1"
	v1mocks "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/testing/mocks/k8s.io/client-go/listers/core/v1"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
	utilMocks "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util/mocks"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/vswitchd"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// failOnceClient wraps a libovsdb Client and makes the first Transact call
// return an error, then unblocks. All other methods are forwarded unchanged.
// Used to exercise retry loops without depending on real network/timing
// behavior from the test harness.
type failOnceClient struct {
	libovsdbclient.Client
	fired atomic.Bool
}

func (f *failOnceClient) Transact(ctx context.Context, ops ...ovsdb.Operation) ([]ovsdb.OperationResult, error) {
	if f.fired.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("injected transient ovsdb failure")
	}
	return f.Client.Transact(ctx, ops...)
}

// failInsertClient fails libovsdb transactions that create new OVS rows, matching
// ConfigureOVS add-port failures on the privileged ovnkube-node path while still
// allowing delete/cleanup transactions between retries.
type failInsertClient struct {
	libovsdbclient.Client
}

func (f *failInsertClient) Transact(ctx context.Context, ops ...ovsdb.Operation) ([]ovsdb.OperationResult, error) {
	for _, op := range ops {
		if op.Op == ovsdb.OperationInsert {
			return nil, fmt.Errorf("injected configure OVS failure")
		}
	}
	return f.Client.Transact(ctx, ops...)
}

type ovnInstalledClient struct {
	libovsdbclient.Client
	ifaceName string
}

func (c *ovnInstalledClient) Where(models ...model.Model) libovsdbclient.ConditionalAPI {
	return &ovnInstalledConditional{
		ConditionalAPI: c.Client.Where(models...),
		ifaceName:      c.ifaceName,
	}
}

type ovnInstalledConditional struct {
	libovsdbclient.ConditionalAPI
	ifaceName string
}

func (c *ovnInstalledConditional) List(ctx context.Context, result any) error {
	err := c.ConditionalAPI.List(ctx, result)
	if err != nil {
		return err
	}
	markOVNInstalled(result, c.ifaceName)
	return nil
}

func markOVNInstalled(result any, ifaceName string) {
	switch ifaces := result.(type) {
	case *[]*vswitchd.Interface:
		for _, iface := range *ifaces {
			markInterfaceOVNInstalled(iface, ifaceName)
		}
	case *[]vswitchd.Interface:
		for i := range *ifaces {
			markInterfaceOVNInstalled(&(*ifaces)[i], ifaceName)
		}
	}
}

func markInterfaceOVNInstalled(iface *vswitchd.Interface, ifaceName string) {
	if iface == nil || iface.Name != ifaceName {
		return
	}
	if iface.ExternalIDs == nil {
		iface.ExternalIDs = map[string]string{}
	}
	iface.ExternalIDs["ovn-installed"] = "true"
}

func genOVSFindCmd(timeout, table, column, condition string) string {
	return fmt.Sprintf("ovs-vsctl --timeout=%s --no-heading --format=csv --data=bare --columns=%s find %s %s",
		timeout, column, table, condition)
}

func checkOVSPortPodInfo(execMock *ovntest.FakeExec, vfRep string, exists bool, timeout, sandbox string, nadName string) {
	output := ""
	if exists {
		output = fmt.Sprintf("sandbox=%s", sandbox)
		if nadName != types.DefaultNetworkName {
			output = output + " k8s.ovn.org/nad=" + nadName
		}
	}
	execMock.AddFakeCmd(&ovntest.ExpectedCmd{
		Cmd:    genOVSFindCmd(timeout, "Interface", "external_ids", "name="+vfRep),
		Output: output,
	})
}

func newFakeKubeClientWithPod(pod *corev1.Pod) *fake.Clientset {
	return fake.NewSimpleClientset(&corev1.PodList{Items: []corev1.Pod{*pod}})
}

// A host with quad-port ConnectX-9 cards presents pf0 through pf3 on every
// card, and every PF has its own VF index space, so (pfId, vfId) names one
// function per card. These are two cards of such a host, as reported by the
// DPU serving it: the PF MAC and the eswitch device behind it are the only
// things that tell the cards apart.
const (
	cardOnePFMAC   = "dc:73:fc:22:20:b0"
	cardOneESwitch = "0005:06:00.0"
	cardOneVF9Rep  = "A56c1pf0vf9"

	cardTwoPFMAC   = "dc:73:fc:22:20:a0"
	cardTwoESwitch = "0005:03:00.0"
	cardTwoVF9Rep  = "A53c1pf0vf9"
)

// cx9PortParams builds the devlink port parameters sriovnet derives from a
// ConnectX-9 host PF MAC. Host PFs are external functions of controller 1.
func cx9PortParams(eswitch string, pfNumber uint16) *sriovnet.RepresentorPortParams {
	return &sriovnet.RepresentorPortParams{
		ECPF:             eswitch,
		ControllerNumber: 1,
		PFNumber:         pfNumber,
	}
}

var _ = Describe("Node DPU tests", func() {
	var sriovnetOpsMock utilMocks.SriovnetOps
	var netlinkOpsMock utilMocks.NetLinkOps
	var execMock *ovntest.FakeExec
	var kubeMock kubemocks.Interface
	var factoryMock factorymocks.NodeWatchFactory
	var pod corev1.Pod
	var dnnc *DefaultNodeNetworkController
	var podInformer coreinformermocks.PodInformer
	var podLister v1mocks.PodLister
	var podNamespaceLister v1mocks.PodNamespaceLister
	var clientset *cni.ClientSet
	var fakeIP string
	var routeManager *routemanager.Controller

	origSriovnetOps := util.GetSriovnetOps()
	origNetlinkOps := util.GetNetLinkOps()

	// expectRepLookupByPFMAC registers the DPU side representor lookup for a
	// pod whose connection details carry pfMac: the MAC resolves the card's
	// eswitch port, then the VF index is resolved within that card.
	expectRepLookupByPFMAC := func(pfMAC, eswitch string, vfIndex uint32, rep string) {
		portParams := cx9PortParams(eswitch, 0)
		sriovnetOpsMock.On("GetPFRepresentorPortParamsFromMAC", ovntest.MustParseMAC(pfMAC)).
			Return(portParams, nil)
		sriovnetOpsMock.On("GetVfRepresentorFromPortParams", portParams, vfIndex).
			Return(rep, nil)
	}

	BeforeEach(func() {
		Expect(config.PrepareTestConfig()).To(Succeed())
		sriovnetOpsMock = utilMocks.SriovnetOps{}
		netlinkOpsMock = utilMocks.NetLinkOps{}
		execMock = ovntest.NewFakeExec()

		util.SetSriovnetOpsInst(&sriovnetOpsMock)
		util.SetNetLinkOpMockInst(&netlinkOpsMock)
		err := util.SetExec(execMock)
		Expect(err).NotTo(HaveOccurred())
		err = cni.SetExec(execMock)
		Expect(err).NotTo(HaveOccurred())
		routeManager = routemanager.NewController()
		Expect(routeManager).NotTo(BeNil())

		kubeMock = kubemocks.Interface{}
		apbExternalRouteClient := adminpolicybasedrouteclient.NewSimpleClientset()
		factoryMock = factorymocks.NodeWatchFactory{}
		cnnci := newCommonNodeNetworkControllerInfo(nil, &kubeMock, apbExternalRouteClient, &factoryMock, nil, "", routeManager,
			"", "", []string{cardOnePFMAC, cardTwoPFMAC})
		dnnc = newDefaultNodeNetworkController(cnnci, nil, nil, routeManager, nil, nil)

		podInformer = coreinformermocks.PodInformer{}
		podNamespaceLister = v1mocks.PodNamespaceLister{}
		podLister = v1mocks.PodLister{}
		podLister.On("Pods", mock.AnythingOfType("string")).Return(&podNamespaceLister)

		pod = corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:        "a-pod",
			Namespace:   "foo-ns",
			UID:         "a-pod",
			Annotations: map[string]string{},
		}}
		fakeIP = "192.168.1.1/24"
	})

	AfterEach(func() {
		// Restore mocks so it does not affect other tests in the suite
		util.SetSriovnetOpsInst(origSriovnetOps)
		util.SetNetLinkOpMockInst(origNetlinkOps)
		cni.ResetRunner()
		util.ResetRunner()
	})

	Context("addRepPort", func() {
		var vfRep string
		var vfPciAddress string
		var vfLink *linkMock.Link
		var ifInfo *cni.PodInterfaceInfo
		var scd util.DPUConnectionDetails

		BeforeEach(func() {
			vfRep = cardOneVF9Rep
			vfPciAddress = "0000:03:00.0"
			vfLink = &linkMock.Link{}
			ip, ipnet, _ := net.ParseCIDR(fakeIP)
			ipnet.IP = ip
			ifInfo = &cni.PodInterfaceInfo{
				PodAnnotation: util.PodAnnotation{
					IPs: []*net.IPNet{ipnet},
				},
				Ingress:       -1,
				Egress:        -1,
				IsDPUHostMode: true,
				NetName:       types.DefaultNetworkName,
				NADKey:        types.DefaultNetworkName,
				PodUID:        "a-pod",
			}

			fakeClient := newFakeKubeClientWithPod(&pod)
			clientset = cni.NewClientSet(fakeClient, &podLister)
			scd = util.DPUConnectionDetails{
				PfId:      "0",
				VfId:      "9",
				PfMAC:     cardOnePFMAC,
				SandboxId: "a8d09931",
			}
			podAnnot, err := util.MarshalPodDPUConnDetails(nil, &scd, types.DefaultNetworkName)
			Expect(err).ToNot(HaveOccurred())
			// set pod annotations
			pod.Annotations = podAnnot
			sriovnetOpsMock.On("SetRepresentorVFMissPktRate", vfRep, uint(0), uint(0)).Return(nil)
			sriovnetOpsMock.On("GetRepresentorVFMissPktDrops", vfRep).Return(uint64(0), nil)
			sriovnetOpsMock.On("GetRepresentorVFMissPktRate", vfRep).Return(uint64(0), uint64(0), nil)
		})

		It("Fails if the VF representor lookup fails", func() {
			sriovnetOpsMock.On("GetPFRepresentorPortParamsFromMAC", ovntest.MustParseMAC(cardOnePFMAC)).
				Return(cx9PortParams(cardOneESwitch, 0), nil)
			sriovnetOpsMock.On("GetVfRepresentorFromPortParams", cx9PortParams(cardOneESwitch, 0), uint32(9)).
				Return("", fmt.Errorf("failed to get VF representor"))
			podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(&pod, nil)

			// call addRepPort()
			err := dnnc.addRepPort(context.Background(), &pod, &scd, types.DefaultNetworkName, ifInfo, clientset)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to get VF representor"))
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})

		// The card's eswitch port is looked up by the published PF MAC, so a
		// MAC no card reports must fail rather than degrade to the index
		// lookup, which would resolve some other card's representor.
		It("Fails without falling back to the index lookup if no card reports the PF MAC", func() {
			sriovnetOpsMock.On("GetPFRepresentorPortParamsFromMAC", ovntest.MustParseMAC(cardOnePFMAC)).
				Return(nil, fmt.Errorf("no matching devlink port found with MAC address %s", cardOnePFMAC))
			podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(&pod, nil)

			err := dnnc.addRepPort(context.Background(), &pod, &scd, types.DefaultNetworkName, ifInfo, clientset)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no matching devlink port"))
			sriovnetOpsMock.AssertNotCalled(GinkgoT(), "GetVfRepresentorDPU", mock.Anything, mock.Anything)
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})

		It("Fails if GetPCIFromDeviceName fails", func() {
			expectRepLookupByPFMAC(cardOnePFMAC, cardOneESwitch, 9, vfRep)
			sriovnetOpsMock.On("GetPCIFromDeviceName", vfRep).Return("", fmt.Errorf("could not find PCI Address"))
			podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(&pod, nil)

			// call addRepPort()
			err := dnnc.addRepPort(context.Background(), &pod, &scd, types.DefaultNetworkName, ifInfo, clientset)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("could not find PCI Address"))
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})

		// Connection details written by an ovnkube-node-dpu-host that predates
		// pfMac carry only the indices, so the legacy lookup must stay
		// reachable for pods that are already running across an upgrade.
		It("Uses the index lookup when pfMac is absent", func() {
			scd.PfMAC = ""
			sriovnetOpsMock.On("GetVfRepresentorDPU", "0", "9").
				Return("", fmt.Errorf("index lookup reached"))
			podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(&pod, nil)

			err := dnnc.addRepPort(context.Background(), &pod, &scd, types.DefaultNetworkName, ifInfo, clientset)
			Expect(err).To(MatchError(ContainSubstring("index lookup reached")))
			sriovnetOpsMock.AssertNotCalled(GinkgoT(), "GetPFRepresentorPortParamsFromMAC", mock.Anything)
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})

		// The regression test for quad-port ConnectX-9 hosts: pfId 0 / vfId 9
		// exists on every card, so two pods reporting the same pair must plumb
		// different representors. Both calls stop right after resolution,
		// which is recorded on the connection details.
		It("Resolves one pfId and vfId pair to a different representor per card", func() {
			cardTwoSCD := scd
			cardTwoSCD.PfMAC = cardTwoPFMAC

			expectRepLookupByPFMAC(cardOnePFMAC, cardOneESwitch, 9, cardOneVF9Rep)
			expectRepLookupByPFMAC(cardTwoPFMAC, cardTwoESwitch, 9, cardTwoVF9Rep)
			sriovnetOpsMock.On("GetPCIFromDeviceName", cardOneVF9Rep).
				Return("", fmt.Errorf("stop after resolution"))
			sriovnetOpsMock.On("GetPCIFromDeviceName", cardTwoVF9Rep).
				Return("", fmt.Errorf("stop after resolution"))
			podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(&pod, nil)

			Expect(dnnc.addRepPort(context.Background(), &pod, &scd, types.DefaultNetworkName, ifInfo, clientset)).NotTo(Succeed())
			Expect(dnnc.addRepPort(context.Background(), &pod, &cardTwoSCD, types.DefaultNetworkName, ifInfo, clientset)).NotTo(Succeed())

			Expect(scd.ConnPrivateInfo.ConnVFRepName).To(Equal(cardOneVF9Rep))
			Expect(cardTwoSCD.ConnPrivateInfo.ConnVFRepName).To(Equal(cardTwoVF9Rep))
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})

		It("Fails if configure OVS fails", func() {
			expectRepLookupByPFMAC(cardOnePFMAC, cardOneESwitch, 9, vfRep)
			sriovnetOpsMock.On("GetPCIFromDeviceName", vfRep).Return(vfPciAddress, nil)
			netlinkOpsMock.On("LinkByName", vfRep).Return(vfLink, nil)

			ovsClient, ovsCleanup, err := libovsdbtest.NewOVSTestHarness(libovsdbtest.TestSetup{
				OVSData: []libovsdbtest.TestData{
					&vswitchd.OpenvSwitch{UUID: "root-ovs", Bridges: []string{"br-int-uuid"}},
					&vswitchd.Bridge{UUID: "br-int-uuid", Name: "br-int"},
				},
			})
			Expect(err).NotTo(HaveOccurred())
			defer ovsCleanup.Cleanup()
			dnnc.ovsClient = &failInsertClient{Client: ovsClient}

			checkOVSPortPodInfo(execMock, vfRep, false, "15", "", "")

			podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(&pod, nil)

			// call addRepPort()
			err = dnnc.addRepPort(context.Background(), &pod, &scd, types.DefaultNetworkName, ifInfo, clientset)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failure in plugging pod interface"))
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})

		It("Fails if configure OVS fails but OVS interface is added", func() {
			expectRepLookupByPFMAC(cardOnePFMAC, cardOneESwitch, 9, vfRep)
			sriovnetOpsMock.On("GetPCIFromDeviceName", vfRep).Return(vfPciAddress, nil)
			// Seed the harness with a pre-existing OVS port for vfRep owned by
			// a different iface-id: cni.ConfigureOVS (libovsdb path) will fail
			// at the iface-id-conflict check, and the cleanup path runs
			// delRepPort which deletes the real port from the harness.
			ovsClient, ovsCleanup, err := libovsdbtest.NewOVSTestHarness(libovsdbtest.TestSetup{
				OVSData: []libovsdbtest.TestData{
					&vswitchd.OpenvSwitch{UUID: "root-ovs", Bridges: []string{"br-int-uuid"}},
					&vswitchd.Bridge{UUID: "br-int-uuid", Name: "br-int", Ports: []string{"vfrep-port-uuid"}},
					&vswitchd.Port{UUID: "vfrep-port-uuid", Name: vfRep, Interfaces: []string{"vfrep-iface-uuid"}},
					&vswitchd.Interface{
						UUID:        "vfrep-iface-uuid",
						Name:        vfRep,
						ExternalIDs: map[string]string{"iface-id": "someone-else"},
					},
				},
			})
			Expect(err).NotTo(HaveOccurred())
			defer ovsCleanup.Cleanup()
			dnnc.ovsClient = ovsClient

			// Cleanup path is shell-out for GetOVSPortPodInfo and netlink.
			checkOVSPortPodInfo(execMock, vfRep, true, "15", "a8d09931", "default")
			netlinkOpsMock.On("LinkByName", vfRep).Return(vfLink, nil)
			netlinkOpsMock.On("LinkSetDown", vfLink).Return(nil)
			netlinkOpsMock.On("LinkSetMTU", vfLink, 1500).Return(nil)
			podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(&pod, nil)

			err = dnnc.addRepPort(context.Background(), &pod, &scd, types.DefaultNetworkName, ifInfo, clientset)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("was added for iface-id"))
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})

		Context("After successfully calling ConfigureOVS", func() {
			var ovsCleanup *libovsdbtest.Context

			BeforeEach(func() {
				expectRepLookupByPFMAC(cardOnePFMAC, cardOneESwitch, 9, vfRep)
				sriovnetOpsMock.On("GetPCIFromDeviceName", vfRep).Return(vfPciAddress, nil)

				// Seed an OVSDB harness with an empty br-int so cni.ConfigureOVS
				// takes the libovsdb path: GetBridge succeeds, no stale ports
				// match, and CreateOrUpdatePodPort writes the new port. The
				// cleanup path's DeletePortWithInterfaces then finds and deletes
				// that real port.
				ovsClient, ctx, err := libovsdbtest.NewOVSTestHarness(libovsdbtest.TestSetup{
					OVSData: []libovsdbtest.TestData{
						&vswitchd.OpenvSwitch{UUID: "root-ovs", Bridges: []string{"br-int-uuid"}},
						&vswitchd.Bridge{UUID: "br-int-uuid", Name: "br-int"},
					},
				})
				Expect(err).NotTo(HaveOccurred())
				ovsCleanup = ctx
				dnnc.ovsClient = &ovnInstalledClient{
					Client:    ovsClient,
					ifaceName: vfRep,
				}

				// waitForPodInterface reads the libovsdb cache. The test client
				// marks the created interface as installed to model OVN setting
				// external_ids:ovn-installed=true.
				// ConfigureOVS calls LinkByName/LinkSetMTU/LinkSetUp when deviceID != ""
				netlinkOpsMock.On("LinkByName", vfRep).Return(vfLink, nil)
				netlinkOpsMock.On("LinkSetMTU", vfLink, ifInfo.MTU).Return(nil)
				netlinkOpsMock.On("LinkSetUp", vfLink).Return(nil)
				sriovnetOpsMock.On("SetRepresentorPeerMacAddress", vfRep, net.HardwareAddr(nil)).Return(nil)
			})

			AfterEach(func() {
				if ovsCleanup != nil {
					ovsCleanup.Cleanup()
					ovsCleanup = nil
				}
			})

			It("Sets dpu.connection-status pod annotation on success", func() {
				var err error
				dcs := util.DPUConnectionStatus{
					Status: "Ready",
				}
				cpod := pod.DeepCopy()
				cpod.Annotations, err = util.MarshalPodDPUConnStatus(cpod.Annotations, &dcs, types.DefaultNetworkName)
				Expect(err).ToNot(HaveOccurred())

				factoryMock.On("PodCoreInformer").Return(&podInformer)
				podInformer.On("Lister").Return(&podLister)
				podLister.On("Pods", mock.AnythingOfType("string")).Return(&podNamespaceLister)
				podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(&pod, nil)
				kubeMock.On("PatchPodStatusAnnotations", &pod, cpod).Return(nil)

				err = dnnc.addRepPort(context.Background(), &pod, &scd, types.DefaultNetworkName, ifInfo, clientset)
				Expect(err).ToNot(HaveOccurred())
				Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
			})

			It("cleans up representor port if set pod annotation fails", func() {
				var err error
				dcs := util.DPUConnectionStatus{
					Status: "Ready",
				}
				cpod := pod.DeepCopy()
				cpod.Annotations, err = util.MarshalPodDPUConnStatus(cpod.Annotations, &dcs, types.DefaultNetworkName)
				Expect(err).ToNot(HaveOccurred())
				// Mock netlink/ovs calls for cleanup
				checkOVSPortPodInfo(execMock, vfRep, true, "15", "a8d09931", "default")
				netlinkOpsMock.On("LinkSetDown", vfLink).Return(nil)
				netlinkOpsMock.On("LinkSetMTU", vfLink, 1500).Return(nil)

				factoryMock.On("PodCoreInformer").Return(&podInformer)
				podInformer.On("Lister").Return(&podLister)
				podLister.On("Pods", mock.AnythingOfType("string")).Return(&podNamespaceLister)
				podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(&pod, nil)
				kubeMock.On("PatchPodStatusAnnotations", &pod, cpod).Return(fmt.Errorf("failed to set pod annotations"))

				err = dnnc.addRepPort(context.Background(), &pod, &scd, types.DefaultNetworkName, ifInfo, clientset)
				Expect(err).To(HaveOccurred())
				Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
			})
		})
	})

	Context("delRepPort", func() {
		var vfRep string
		var vfLink *linkMock.Link
		var scd util.DPUConnectionDetails

		BeforeEach(func() {
			vfRep = "pf0vf9"
			vfLink = &linkMock.Link{}
			scd = util.DPUConnectionDetails{
				PfId:      "0",
				VfId:      "9",
				PfMAC:     cardOnePFMAC,
				SandboxId: "a8d09931",
			}
			sriovnetOpsMock.On("SetRepresentorVFMissPktRate", vfRep, uint(0), uint(0)).Return(nil)
		})

		It("Sets link down for VF representor and removes VF representor from OVS", func() {
			checkOVSPortPodInfo(execMock, vfRep, true, "15", scd.SandboxId, types.DefaultNetworkName)
			netlinkOpsMock.On("LinkByName", vfRep).Return(vfLink, nil)
			netlinkOpsMock.On("LinkSetDown", vfLink).Return(nil)
			netlinkOpsMock.On("LinkSetMTU", vfLink, 1500).Return(nil)
			ovsClient, ovsCleanup, err := libovsdbtest.NewOVSTestHarness(libovsdbtest.TestSetup{
				OVSData: []libovsdbtest.TestData{
					&vswitchd.OpenvSwitch{UUID: "root-ovs", Bridges: []string{"br-int-uuid"}},
					&vswitchd.Bridge{UUID: "br-int-uuid", Name: "br-int", Ports: []string{"vfrep-port-uuid"}},
					&vswitchd.Port{UUID: "vfrep-port-uuid", Name: "pf0vf9", Interfaces: []string{"vfrep-iface-uuid"}},
					&vswitchd.Interface{UUID: "vfrep-iface-uuid", Name: "pf0vf9"},
				},
			})
			Expect(err).ToNot(HaveOccurred())
			defer ovsCleanup.Cleanup()
			dnnc.ovsClient = ovsClient
			err = dnnc.delRepPort(&pod, &scd, vfRep, types.DefaultNetworkName)
			Expect(err).ToNot(HaveOccurred())
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})

		It("Does not fail if LinkByName failed", func() {
			checkOVSPortPodInfo(execMock, vfRep, true, "15", scd.SandboxId, types.DefaultNetworkName)
			netlinkOpsMock.On("LinkByName", vfRep).Return(nil, fmt.Errorf("failed to get link"))
			ovsClient, ovsCleanup, err := libovsdbtest.NewOVSTestHarness(libovsdbtest.TestSetup{
				OVSData: []libovsdbtest.TestData{
					&vswitchd.OpenvSwitch{UUID: "root-ovs", Bridges: []string{"br-int-uuid"}},
					&vswitchd.Bridge{UUID: "br-int-uuid", Name: "br-int", Ports: []string{"vfrep-port-uuid"}},
					&vswitchd.Port{UUID: "vfrep-port-uuid", Name: "pf0vf9", Interfaces: []string{"vfrep-iface-uuid"}},
					&vswitchd.Interface{UUID: "vfrep-iface-uuid", Name: "pf0vf9"},
				},
			})
			Expect(err).ToNot(HaveOccurred())
			defer ovsCleanup.Cleanup()
			dnnc.ovsClient = ovsClient
			err = dnnc.delRepPort(&pod, &scd, vfRep, types.DefaultNetworkName)
			Expect(err).ToNot(HaveOccurred())
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})

		It("Does not fail if removal of VF representor from OVS fails once", func() {
			// Wrap the harness client so the first Transact errors out; the
			// retry loop in delRepPort should recover on the second attempt.
			checkOVSPortPodInfo(execMock, vfRep, true, "15", scd.SandboxId, types.DefaultNetworkName)
			netlinkOpsMock.On("LinkByName", vfRep).Return(vfLink, nil)
			netlinkOpsMock.On("LinkSetDown", vfLink).Return(nil)
			netlinkOpsMock.On("LinkSetMTU", vfLink, 1500).Return(nil)
			ovsClient, ovsCleanup, err := libovsdbtest.NewOVSTestHarness(libovsdbtest.TestSetup{
				OVSData: []libovsdbtest.TestData{
					&vswitchd.OpenvSwitch{UUID: "root-ovs", Bridges: []string{"br-int-uuid"}},
					&vswitchd.Bridge{UUID: "br-int-uuid", Name: "br-int", Ports: []string{"vfrep-port-uuid"}},
					&vswitchd.Port{UUID: "vfrep-port-uuid", Name: "pf0vf9", Interfaces: []string{"vfrep-iface-uuid"}},
					&vswitchd.Interface{UUID: "vfrep-iface-uuid", Name: "pf0vf9"},
				},
			})
			Expect(err).ToNot(HaveOccurred())
			defer ovsCleanup.Cleanup()
			dnnc.ovsClient = &failOnceClient{Client: ovsClient}
			err = dnnc.delRepPort(&pod, &scd, vfRep, types.DefaultNetworkName)
			Expect(err).ToNot(HaveOccurred())
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})
	})

	Context("podReadyToAddDPU", func() {
		var scd util.DPUConnectionDetails

		BeforeEach(func() {
			scd = util.DPUConnectionDetails{
				PfId:      "0",
				VfId:      "9",
				PfMAC:     cardOnePFMAC,
				SandboxId: "a8d09931",
			}
		})

		It("Claims a pod whose PF MAC belongs to one of this DPU's host PFs", func() {
			annotations, err := util.MarshalPodDPUConnDetails(nil, &scd, types.DefaultNetworkName)
			Expect(err).ToNot(HaveOccurred())
			pod.Annotations = annotations

			Expect(dnnc.podReadyToAddDPU(&pod, types.DefaultNetworkName)).To(Equal(&scd))
		})

		// A host PF served by another DPU shares the pfId and vfId space with
		// this one, so the MAC is what keeps the two DPUs from both claiming
		// the pod.
		It("Ignores a pod whose PF MAC belongs to another DPU", func() {
			scd.PfMAC = "dc:73:fc:21:fc:60"
			annotations, err := util.MarshalPodDPUConnDetails(nil, &scd, types.DefaultNetworkName)
			Expect(err).ToNot(HaveOccurred())
			pod.Annotations = annotations

			Expect(dnnc.podReadyToAddDPU(&pod, types.DefaultNetworkName)).To(BeNil())
		})
	})

	Context("dpuConnectionDetailChanged", func() {
		base := util.DPUConnectionDetails{
			PfId:      "0",
			VfId:      "9",
			PfMAC:     cardOnePFMAC,
			SandboxId: "a8d09931",
		}
		withField := func(mutate func(*util.DPUConnectionDetails)) *util.DPUConnectionDetails {
			changed := base
			mutate(&changed)
			return &changed
		}

		DescribeTable("reports whether the pod needs replumbing",
			func(older, newer *util.DPUConnectionDetails, expected bool) {
				Expect(dpuConnectionDetailChanged(older, newer)).To(Equal(expected))
			},
			Entry("both absent", nil, nil, false),
			Entry("details appeared", nil, &base, true),
			Entry("details removed", &base, nil, true),
			Entry("identical details", &base, &base, false),
			// pfMac is the only field that changes when a pod moves to an
			// identically indexed VF on another card, so missing it would
			// leave the old card's representor plumbed.
			Entry("PF MAC changed", &base,
				withField(func(d *util.DPUConnectionDetails) { d.PfMAC = cardTwoPFMAC }), true),
			Entry("PF index changed", &base,
				withField(func(d *util.DPUConnectionDetails) { d.PfId = "1" }), true),
			Entry("VF index changed", &base,
				withField(func(d *util.DPUConnectionDetails) { d.VfId = "10" }), true),
			Entry("sandbox changed", &base,
				withField(func(d *util.DPUConnectionDetails) { d.SandboxId = "b9e1a042" }), true),
		)
	})
})
