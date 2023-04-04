package node

import (
	"fmt"
	"net"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	ctypes "github.com/containernetworking/cni/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni"
	cnitypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube/mocks"
	ovntest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing"
	linkMock "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing/mocks/github.com/vishvananda/netlink"
	v1mocks "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing/mocks/k8s.io/client-go/listers/core/v1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	utilMocks "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util/mocks"

	"github.com/stretchr/testify/mock"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func genOVSFindCmd(timeout, table, column, condition string) string {
	return fmt.Sprintf("ovs-vsctl --timeout=%s --no-heading --format=csv --data=bare --columns=%s find %s %s",
		timeout, column, table, condition)
}

func genOVSAddPortCmd(hostIfaceName, ifaceID, mac, ip, sandboxID, podUID string) string {
	return fmt.Sprintf("ovs-vsctl --timeout=30 --may-exist add-port br-int %s other_config:transient=true "+
		"-- set interface %s external_ids:attached_mac=%s "+
		"external_ids:iface-id=%s external_ids:iface-id-ver=%s external_ids:ip_addresses=%s external_ids:sandbox=%s "+
		"-- --if-exists remove interface %s external_ids network_name",
		hostIfaceName, hostIfaceName, mac, ifaceID, podUID, ip, sandboxID, hostIfaceName)
}

func genOVSDelPortCmd(portName string, timeout ...int) string {
	_timeout := 15
	if len(timeout) > 0 {
		_timeout = timeout[0]
	}
	return fmt.Sprintf("ovs-vsctl --timeout=%d --if-exists del-port br-int %s", _timeout, portName)
}

func genOVSGetCmd(table, record, column, key string) string {
	if key != "" {
		column = column + ":" + key
	}
	return fmt.Sprintf("ovs-vsctl --timeout=30 --if-exists get %s %s %s", table, record, column)
}

func genOfctlDumpFlowsCmd(queryStr string) string {
	return fmt.Sprintf("ovs-ofctl --timeout=10 --no-stats --strict dump-flows br-int %s", queryStr)
}

func genIfaceID(podNamespace, podName string) string {
	return fmt.Sprintf("%s_%s", podNamespace, podName)
}

func checkOVSPortPodInfo(execMock *ovntest.FakeExec, vfRep string, exists bool, timeout, sandbox string, nadName string) {
	output := ""
	if exists {
		output = fmt.Sprintf("sandbox=%s", sandbox)
		if nadName != types.DefaultNetworkName {
			output = output + " network_name=" + nadName
		}
	}
	execMock.AddFakeCmd(&ovntest.ExpectedCmd{
		Cmd:    genOVSFindCmd(timeout, "Interface", "external_ids", "name="+vfRep),
		Output: output,
	})
}

func newFakeKubeClientWithPod(pod *v1.Pod) *fake.Clientset {
	return fake.NewSimpleClientset(&v1.PodList{Items: []v1.Pod{*pod}})
}

var _ = Describe("Node DPU tests", func() {
	var sriovnetOpsMock utilMocks.SriovnetOps
	var netlinkOpsMock utilMocks.NetLinkOps
	var execMock *ovntest.FakeExec
	var kubeMock mocks.KubeInterface
	var pod v1.Pod
	var node OvnNode
	var podLister v1mocks.PodLister
	var podNamespaceLister v1mocks.PodNamespaceLister
	var nc *ovnNodeController

	origSriovnetOps := util.GetSriovnetOps()
	origNetlinkOps := util.GetNetLinkOps()

	BeforeEach(func() {
		sriovnetOpsMock = utilMocks.SriovnetOps{}
		netlinkOpsMock = utilMocks.NetLinkOps{}
		execMock = ovntest.NewFakeExec()

		util.SetSriovnetOpsInst(&sriovnetOpsMock)
		util.SetNetLinkOpMockInst(&netlinkOpsMock)
		err := util.SetExec(execMock)
		Expect(err).NotTo(HaveOccurred())
		err = cni.SetExec(execMock)
		Expect(err).NotTo(HaveOccurred())

		kubeMock = mocks.KubeInterface{}
		node = OvnNode{Kube: &kubeMock}
		netconf := &cnitypes.NetConf{
			NetConf: ctypes.NetConf{
				Name: types.DefaultNetworkName,
			},
			TopoType: types.Layer3AttachDefTopoType,
		}
		nadInfo, _ := util.NewNetAttachDefInfo(netconf)
		nadInfo.NetAttachDefs.Store("default", &util.NadConfig{MissRateLimitConfig: util.MissRateLimitConfig{MaxNewConnPPS: 0, MaxNewConnBurst: 0}})
		nc, _ = node.NewOvnNodeController(nadInfo)

		podNamespaceLister = v1mocks.PodNamespaceLister{}
		podLister = v1mocks.PodLister{}
		podLister.On("Pods", mock.AnythingOfType("string")).Return(&podNamespaceLister)

		pod = v1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:        "a-pod",
			Namespace:   "foo-ns",
			UID:         "a-pod",
			Annotations: map[string]string{},
		}}
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
		var vfLink *linkMock.Link
		var ifInfo *cni.PodInterfaceInfo
		var scd util.DPUConnectionDetails

		BeforeEach(func() {
			vfRep = "pf0vf9"
			vfLink = &linkMock.Link{}
			ifInfo = &cni.PodInterfaceInfo{
				PodAnnotation: util.PodAnnotation{},
				Ingress:       -1,
				Egress:        -1,
				IsDPUHostMode: true,
				PodUID:        "a-pod",
				NetNameInfo:   util.NetNameInfo{types.DefaultNetworkName, "", false},
				NadName:       types.DefaultNetworkName,
			}

			scd = util.DPUConnectionDetails{
				PfId:      "0",
				VfId:      "9",
				SandboxId: "a8d09931",
			}
			//sriovnetOpsMock.On("GetVfRepresentorDPU", "0", "9").Return(vfRep, nil)
			podAnnot := map[string]string{}
			err := util.MarshalPodDPUConnDetails(&podAnnot, &scd, types.DefaultNetworkName)
			Expect(err).ToNot(HaveOccurred())
			// set pod annotations
			pod.Annotations = podAnnot
			sriovnetOpsMock.On("SetRepresentorVFMissPktRate", vfRep, uint(0), uint(0)).Return(nil)
			sriovnetOpsMock.On("GetRepresentorVFMissPktDrops", vfRep).Return(uint64(0), nil)
			sriovnetOpsMock.On("GetRepresentorVFMissPktRate", vfRep).Return(uint64(0), uint64(0), nil)
		})

		It("Fails if GetVfRepresentorDPU fails", func() {
			sriovnetOpsMock.On("GetVfRepresentorDPU", "0", "9").Return("", fmt.Errorf("failed to get VF representor"))
			fakeClient := newFakeKubeClientWithPod(&pod)
			podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(pod, nil)

			// call addRepPort()
			err := nc.addRepPort(&pod, &scd, ifInfo, &podLister, fakeClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to get VF representor"))
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})

		It("Fails if configure OVS fails", func() {
			sriovnetOpsMock.On("GetVfRepresentorDPU", "0", "9").Return(vfRep, nil)
			// set ovs CMD output
			execMock.AddFakeCmd(&ovntest.ExpectedCmd{
				Cmd: genOVSFindCmd("30", "Interface", "name",
					"external-ids:iface-id="+genIfaceID(pod.Namespace, pod.Name)),
			})
			checkOVSPortPodInfo(execMock, vfRep, false, "30", "", "")
			execMock.AddFakeCmd(&ovntest.ExpectedCmd{
				Cmd:    genOVSGetCmd("Open_vSwitch", ".", "other_config", "hw-offload"),
				Output: "true",
			})
			// add-port, fail, del & retry
			execMock.AddFakeCmd(&ovntest.ExpectedCmd{
				Cmd: genOVSAddPortCmd(vfRep, genIfaceID(pod.Namespace, pod.Name), "", "", "a8d09931", string(pod.UID)),
				Err: fmt.Errorf("failed to run ovs command"),
			})
			execMock.AddFakeCmd(&ovntest.ExpectedCmd{
				Cmd:    genOVSDelPortCmd(vfRep, 30),
				Output: "true",
			})
			execMock.AddFakeCmd(&ovntest.ExpectedCmd{
				Cmd: genOVSAddPortCmd(vfRep, genIfaceID(pod.Namespace, pod.Name), "", "", "a8d09931", string(pod.UID)),
				Err: fmt.Errorf("failed to run ovs command"),
			})
			execMock.AddFakeCmd(&ovntest.ExpectedCmd{
				Cmd:    genOVSDelPortCmd(vfRep, 30),
				Output: "true",
			})
			execMock.AddFakeCmd(&ovntest.ExpectedCmd{
				Cmd: genOVSAddPortCmd(vfRep, genIfaceID(pod.Namespace, pod.Name), "", "", "a8d09931", string(pod.UID)),
				Err: fmt.Errorf("failed to run ovs command"),
			})
			execMock.AddFakeCmd(&ovntest.ExpectedCmd{
				Cmd:    genOVSDelPortCmd(vfRep, 30),
				Output: "true",
			})
			// Mock netlink/ovs calls for cleanup
			checkOVSPortPodInfo(execMock, vfRep, false, "15", "", "")
			fakeClient := newFakeKubeClientWithPod(&pod)
			//podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(pod, nil)

			// call addRepPort()
			err := nc.addRepPort(&pod, &scd, ifInfo, &podLister, fakeClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to run ovs command"))
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})

		Context("After successfully calling ConfigureOVS", func() {
			BeforeEach(func() {
				sriovnetOpsMock.On("GetVfRepresentorDPU", "0", "9").Return("pf0vf9", nil)
				// set ovs CMD output so cni.ConfigureOVS passes without error
				execMock.AddFakeCmd(&ovntest.ExpectedCmd{
					Cmd: genOVSFindCmd("30", "Interface", "name",
						"external-ids:iface-id="+genIfaceID(pod.Namespace, pod.Name)),
				})
				checkOVSPortPodInfo(execMock, vfRep, false, "30", "", "")
				execMock.AddFakeCmd(&ovntest.ExpectedCmd{
					Cmd:    genOVSGetCmd("Open_vSwitch", ".", "other_config", "hw-offload"),
					Output: "true",
				})
				execMock.AddFakeCmd(&ovntest.ExpectedCmd{
					Cmd: genOVSAddPortCmd(vfRep, genIfaceID(pod.Namespace, pod.Name), "", "", "a8d09931", string(pod.UID)),
				})
				// clearPodBandwidth
				execMock.AddFakeCmd(&ovntest.ExpectedCmd{
					Cmd: genOVSFindCmd("30", "interface", "name",
						"external-ids:sandbox=a8d09931"),
				})
				execMock.AddFakeCmd(&ovntest.ExpectedCmd{
					Cmd: genOVSFindCmd("30", "qos", "_uuid",
						"external-ids:sandbox=a8d09931"),
				})
				// getIfaceOFPort
				execMock.AddFakeCmd(&ovntest.ExpectedCmd{
					Cmd:    genOVSGetCmd("Interface", "pf0vf9", "ofport", ""),
					Output: "1",
				})
				// waitForPodFlows
				execMock.AddFakeCmd(&ovntest.ExpectedCmd{
					Cmd:    genOVSGetCmd("Interface", "pf0vf9", "external-ids", "iface-id"),
					Output: genIfaceID(pod.Namespace, pod.Name),
				})
				execMock.AddFakeCmd(&ovntest.ExpectedCmd{
					Cmd:    genOfctlDumpFlowsCmd("table=9,dl_src="),
					Output: "non-empty-output",
				})
				execMock.AddFakeCmd(&ovntest.ExpectedCmd{
					Cmd:    genOfctlDumpFlowsCmd("table=0,in_port=1"),
					Output: "non-empty-output",
				})
				sriovnetOpsMock.On("SetRepresentorPeerMacAddress", vfRep, net.HardwareAddr(nil)).Return(nil)
			})

			Context("Fails if link configuration fails on", func() {
				It("LinkByName()", func() {
					execMock := ovntest.NewFakeExec()
					e := util.SetExec(execMock)
					Expect(e).NotTo(HaveOccurred())
					e = cni.SetExec(execMock)
					Expect(e).NotTo(HaveOccurred())
					execMock.AddFakeCmd(&ovntest.ExpectedCmd{
						Cmd: genOVSFindCmd("30", "Interface", "name",
							"external-ids:iface-id="+genIfaceID(pod.Namespace, pod.Name)),
					})
					checkOVSPortPodInfo(execMock, vfRep, false, "30", "", "")
					execMock.AddFakeCmd(&ovntest.ExpectedCmd{
						Cmd:    genOVSGetCmd("Open_vSwitch", ".", "other_config", "hw-offload"),
						Output: "true",
					})
					// add-port, fail, del & retry
					execMock.AddFakeCmd(&ovntest.ExpectedCmd{
						Cmd: genOVSAddPortCmd(vfRep, genIfaceID(pod.Namespace, pod.Name), "", "", "a8d09931", string(pod.UID)),
					})
					execMock.AddFakeCmd(&ovntest.ExpectedCmd{
						Cmd:    genOVSDelPortCmd(vfRep, 30),
						Output: "true",
					})
					execMock.AddFakeCmd(&ovntest.ExpectedCmd{
						Cmd: genOVSAddPortCmd(vfRep, genIfaceID(pod.Namespace, pod.Name), "", "", "a8d09931", string(pod.UID)),
						Err: fmt.Errorf("failed to run ovs command"),
					})
					execMock.AddFakeCmd(&ovntest.ExpectedCmd{
						Cmd:    genOVSDelPortCmd(vfRep, 30),
						Output: "true",
					})
					execMock.AddFakeCmd(&ovntest.ExpectedCmd{
						Cmd: genOVSAddPortCmd(vfRep, genIfaceID(pod.Namespace, pod.Name), "", "", "a8d09931", string(pod.UID)),
						Err: fmt.Errorf("failed to run ovs command"),
					})
					execMock.AddFakeCmd(&ovntest.ExpectedCmd{
						Cmd:    genOVSDelPortCmd(vfRep, 30),
						Output: "true",
					})
					// Mock ovs calls for cleanup
					checkOVSPortPodInfo(execMock, vfRep, true, "15", scd.SandboxId, types.DefaultNetworkName)
					execMock.AddFakeCmd(&ovntest.ExpectedCmd{
						Cmd:    genOVSDelPortCmd(vfRep),
						Output: "true",
					})
					// fail LinkByName
					netlinkOpsMock.On("LinkByName", vfRep).Return(nil, fmt.Errorf("failed to get link"))
					fakeClient := newFakeKubeClientWithPod(&pod)
					podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(pod, nil)
					err := nc.addRepPort(&pod, &scd, ifInfo, &podLister, fakeClient)
					Expect(err).To(HaveOccurred())
					Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
				})

				It("LinkSetMTU()", func() {
					netlinkOpsMock.On("LinkByName", vfRep).Return(vfLink, nil)
					netlinkOpsMock.On("LinkSetMTU", vfLink, ifInfo.MTU).Return(fmt.Errorf("failed to set mtu"))
					// Mock netlink/ovs calls for cleanup
					checkOVSPortPodInfo(execMock, vfRep, true, "15", scd.SandboxId, types.DefaultNetworkName)
					netlinkOpsMock.On("LinkSetDown", vfLink).Return(nil)
					execMock.AddFakeCmd(&ovntest.ExpectedCmd{
						Cmd: genOVSDelPortCmd("pf0vf9"),
					})

					fakeClient := newFakeKubeClientWithPod(&pod)
					podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(pod, nil)

					err := nc.addRepPort(&pod, &scd, ifInfo, &podLister, fakeClient)
					Expect(err).To(HaveOccurred())
					Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
				})

				It("LinkSetUp()", func() {
					netlinkOpsMock.On("LinkByName", vfRep).Return(vfLink, nil)
					netlinkOpsMock.On("LinkSetMTU", vfLink, ifInfo.MTU).Return(nil)
					netlinkOpsMock.On("LinkSetUp", vfLink).Return(fmt.Errorf("failed to set link up"))
					// Mock netlink/ovs calls for cleanup
					checkOVSPortPodInfo(execMock, vfRep, true, "15", scd.SandboxId, types.DefaultNetworkName)
					netlinkOpsMock.On("LinkSetDown", vfLink).Return(nil)
					execMock.AddFakeCmd(&ovntest.ExpectedCmd{
						Cmd: genOVSDelPortCmd("pf0vf9"),
					})

					fakeClient := newFakeKubeClientWithPod(&pod)
					podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(pod, nil)

					err := nc.addRepPort(&pod, &scd, ifInfo, &podLister, fakeClient)
					Expect(err).To(HaveOccurred())
					Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
				})
			})

			It("Sets dpu.connection-status pod annotation on success", func() {
				netlinkOpsMock.On("LinkByName", vfRep).Return(vfLink, nil)
				netlinkOpsMock.On("LinkSetMTU", vfLink, ifInfo.MTU).Return(nil)
				netlinkOpsMock.On("LinkSetUp", vfLink).Return(nil)
				dcs := util.DPUConnectionStatus{
					Status: "Ready",
				}
				kubeMock.On("GetPod", pod.Namespace, pod.Name).Return(&pod, nil)
				cpod := pod.DeepCopy()
				err := util.MarshalPodDPUConnStatus(&cpod.Annotations, &dcs, types.DefaultNetworkName)
				Expect(err).ToNot(HaveOccurred())
				kubeMock.On("UpdatePod", cpod).Return(nil)

				fakeClient := newFakeKubeClientWithPod(&pod)
				podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(pod, nil)

				err = nc.addRepPort(&pod, &scd, ifInfo, &podLister, fakeClient)
				Expect(err).ToNot(HaveOccurred())
			})

			It("cleans up representor port if set pod annotation fails", func() {
				netlinkOpsMock.On("LinkByName", vfRep).Return(vfLink, nil)
				netlinkOpsMock.On("LinkSetMTU", vfLink, ifInfo.MTU).Return(nil)
				netlinkOpsMock.On("LinkSetUp", vfLink).Return(nil)
				dcs := util.DPUConnectionStatus{
					Status: "Ready",
				}
				kubeMock.On("GetPod", pod.Namespace, pod.Name).Return(&pod, nil)
				cpod := pod.DeepCopy()
				err := util.MarshalPodDPUConnStatus(&cpod.Annotations, &dcs, types.DefaultNetworkName)
				Expect(err).ToNot(HaveOccurred())
				kubeMock.On("UpdatePod", cpod).Return(fmt.Errorf("failed to set pod annotations"))
				// Mock netlink/ovs calls for cleanup
				checkOVSPortPodInfo(execMock, vfRep, true, "15", scd.SandboxId, types.DefaultNetworkName)
				netlinkOpsMock.On("LinkSetDown", vfLink).Return(nil)
				execMock.AddFakeCmd(&ovntest.ExpectedCmd{
					Cmd: genOVSDelPortCmd("pf0vf9"),
				})

				fakeClient := newFakeKubeClientWithPod(&pod)
				podNamespaceLister.On("Get", mock.AnythingOfType("string")).Return(pod, nil)

				err = nc.addRepPort(&pod, &scd, ifInfo, &podLister, fakeClient)
				Expect(err).To(HaveOccurred())
				Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
			})
		})
	})

	Context("delRepPort", func() {
		var vfRep, podDesc string
		var vfLink *linkMock.Link
		var scd util.DPUConnectionDetails

		BeforeEach(func() {
			podDesc = fmt.Sprintf("pod %s/%s nad %s", pod.Namespace, pod.Name, types.DefaultNetworkName)
			vfRep = "pf0vf9"
			vfLink = &linkMock.Link{}
			sriovnetOpsMock.On("SetRepresentorVFMissPktRate", vfRep, uint(0), uint(0)).Return(nil)
			scd = util.DPUConnectionDetails{
				PfId:      "0",
				VfId:      "9",
				SandboxId: "a8d09931",
			}
		})

		It("Attemps to remove VF representor of another sandbox from OVS, return failure", func() {
			checkOVSPortPodInfo(execMock, vfRep, true, "15", scd.SandboxId+"1", types.DefaultNetworkName)
			err := nc.delRepPort(&pod, &scd, vfRep, types.DefaultNetworkName, podDesc)
			Expect(err).To(HaveOccurred())
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})

		It("Attemps to remove VF representor of the same sandbox but different network from OVS, return failure", func() {
			checkOVSPortPodInfo(execMock, vfRep, true, "15", scd.SandboxId, "non-default")
			err := nc.delRepPort(&pod, &scd, vfRep, types.DefaultNetworkName, podDesc)
			Expect(err).To(HaveOccurred())
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})

		It("Sets link down for VF representor and removes VF representor from OVS", func() {
			checkOVSPortPodInfo(execMock, vfRep, true, "15", scd.SandboxId, types.DefaultNetworkName)
			netlinkOpsMock.On("LinkByName", vfRep).Return(vfLink, nil)
			netlinkOpsMock.On("LinkSetDown", vfLink).Return(nil)
			execMock.AddFakeCmd(&ovntest.ExpectedCmd{
				Cmd: fmt.Sprintf("ovs-vsctl --timeout=15 --if-exists del-port br-int %s", "pf0vf9"),
			})
			err := nc.delRepPort(&pod, &scd, vfRep, types.DefaultNetworkName, podDesc)
			Expect(err).ToNot(HaveOccurred())
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})

		It("Does not fail if LinkByName failed", func() {
			checkOVSPortPodInfo(execMock, vfRep, true, "15", scd.SandboxId, types.DefaultNetworkName)
			netlinkOpsMock.On("LinkByName", vfRep).Return(nil, fmt.Errorf("failed to get link"))
			execMock.AddFakeCmd(&ovntest.ExpectedCmd{
				Cmd: genOVSDelPortCmd("pf0vf9"),
			})
			err := nc.delRepPort(&pod, &scd, vfRep, types.DefaultNetworkName, podDesc)
			Expect(err).ToNot(HaveOccurred())
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})

		It("Does not fail if removal of VF representor from OVS fails once", func() {
			checkOVSPortPodInfo(execMock, vfRep, true, "15", scd.SandboxId, types.DefaultNetworkName)
			netlinkOpsMock.On("LinkByName", vfRep).Return(vfLink, nil)
			netlinkOpsMock.On("LinkSetDown", vfLink).Return(nil)
			// fail on first try
			execMock.AddFakeCmd(&ovntest.ExpectedCmd{
				Cmd: genOVSDelPortCmd("pf0vf9"),
				Err: fmt.Errorf("ovs command failed"),
			})
			// pass on the second
			execMock.AddFakeCmd(&ovntest.ExpectedCmd{
				Cmd: genOVSDelPortCmd("pf0vf9"),
				Err: nil,
			})
			// pass on the second
			err := nc.delRepPort(&pod, &scd, vfRep, types.DefaultNetworkName, podDesc)
			Expect(err).ToNot(HaveOccurred())
			Expect(execMock.CalledMatchesExpected()).To(BeTrue(), execMock.ErrorDesc())
		})
	})
})
