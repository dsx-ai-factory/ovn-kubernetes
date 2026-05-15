// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ovn

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	cnitypes "github.com/containernetworking/cni/pkg/types"
	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/urfave/cli/v2"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ovncnitypes "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/cni/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/nbdb"
	ovntest "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/testing"
	libovsdbtest "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/testing/libovsdb"
	ovntypes "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

var _ = ginkgo.Describe("Skip IPAM on a given network", func() {
	var (
		app       *cli.App
		fakeOvn   *FakeOVN
		initialDB libovsdbtest.TestSetup
		nad       *nettypes.NetworkAttachmentDefinition
		err       error
	)

	ginkgo.BeforeEach(func() {
		// Restore global default values before each testcase
		gomega.Expect(config.PrepareTestConfig()).To(gomega.Succeed())
		config.OVNKubernetesFeature.EnableMultiNetwork = true

		app = cli.NewApp()
		app.Name = "test"
		app.Flags = config.Flags

		fakeOvn = NewFakeOVN(false)
		initialDB = libovsdbtest.TestSetup{
			NBData: []libovsdbtest.TestData{
				&nbdb.LogicalSwitch{
					Name: "node1",
				},
				&nbdb.LogicalSwitch{
					Name: "skip.ipam.nad_ovn_layer2_switch",
				},
			},
		}
		nad, err = newNetworkAttachmentDefinition(
			"default",
			"skip-ipam-nad",
			ovncnitypes.NetConf{
				NetConf: cnitypes.NetConf{
					Name: "skip-ipam-nad",
					Type: "ovn-k8s-cni-overlay",
				},
				Topology: ovntypes.Layer2Topology,
				NADName:  util.GetNADName("default", "skip-ipam-nad"),
				Subnets:  "10.193.0.0/16",
			},
		)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})

	ginkgo.AfterEach(func() {
		fakeOvn.shutdown()
	})

	ginkgo.Context("skip ipam for a pod on given interface", func() {
		ginkgo.It("reconciles a pod with skip ipam annotation and floating ip", func() {
			app.Action = func(_ *cli.Context) error {
				floatingIP := "10.193.13.5"
				//nodeSecondarySubnet := "10.193.0.0/26"
				namespaceT := *ovntest.NewNamespace("namespace1")
				t := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespaceT.Name,
				)
				pod := ovntest.NewPod(t.namespace, t.podName, t.nodeName, t.podIP)
				pod.Annotations = map[string]string{
					"k8s.v1.cni.cncf.io/networks":     `[{"interface":"net1","name":"skip-ipam-nad","namespace":"default"}]`,
					"k8s.ovn.org/skip-ip-on-networks": "default/skip-ipam-nad",
					"k8s.ovn.org/port-security-info":  fmt.Sprintf(`{"default/skip-ipam-nad": {"ips": ["%s"]}}`, floatingIP),
				}
				// simulate cluster manager setting a MAC-only pod annotation (with tunnel ID) for the skip-ipam network
				hwAddr, _ := net.ParseMAC("0a:58:0a:80:01:03")
				pod.Annotations, err = util.MarshalPodAnnotation(pod.Annotations,
					&util.PodAnnotation{MAC: hwAddr, TunnelID: 1},
					util.GetNADName("default", "skip-ipam-nad"),
				)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				fakeOvn.startWithDBSetup(initialDB,
					&corev1.NamespaceList{
						Items: []corev1.Namespace{
							namespaceT,
						},
					},
					&corev1.NodeList{
						Items: []corev1.Node{
							*newNode(t.nodeName, "192.168.126.202/24"),
						},
					},
					&corev1.PodList{
						Items: []corev1.Pod{},
					},
					&nettypes.NetworkAttachmentDefinitionList{
						Items: []nettypes.NetworkAttachmentDefinition{*nad},
					},
				)
				ocInfo := fakeOvn.userDefinedNetworkControllers["skip-ipam-nad"]
				subnet := ocInfo.bnc.Subnets()[0]
				err = ocInfo.bnc.lsManager.AddOrUpdateSwitch(ocInfo.bnc.GetNetworkScopedName(ovntypes.OVNLayer2Switch), []*net.IPNet{subnet.CIDR}, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = ocInfo.bnc.WatchNamespaces()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = ocInfo.bnc.WatchPods()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(t.namespace).Create(context.TODO(), pod, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				// check nbdb data is added
				gomega.Eventually(func() bool {
					lsp := &nbdb.LogicalSwitchPort{Name: "default.skip.ipam.nad_namespace1_myPod"}
					err = fakeOvn.nbClient.Get(context.TODO(), lsp)
					if err != nil && err.Error() == "object not found" {
						return false
					}
					gomega.Expect(lsp.PortSecurity).To(gomega.HaveLen(1))
					strs := strings.Split(lsp.PortSecurity[0], " ")
					gomega.Expect(strs).To(gomega.HaveLen(2))
					gomega.Expect(strs[1]).To(gomega.Equal(floatingIP))
					return true
				}, 60*time.Second).Should(gomega.BeTrue())
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
		ginkgo.It("reconciles a pod with skip ipam annotation but no floating ip", func() {
			app.Action = func(_ *cli.Context) error {
				namespaceT := *ovntest.NewNamespace("namespace1")
				t := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespaceT.Name,
				)
				pod := ovntest.NewPod(t.namespace, t.podName, t.nodeName, t.podIP)
				pod.Annotations = map[string]string{
					"k8s.v1.cni.cncf.io/networks":     `[{"interface":"net1","name":"skip-ipam-nad","namespace":"default"}]`,
					"k8s.ovn.org/skip-ip-on-networks": "default/skip-ipam-nad",
				}
				// simulate cluster manager setting a MAC-only pod annotation (with tunnel ID) for the skip-ipam network
				hwAddr, _ := net.ParseMAC("0a:58:0a:80:01:03")
				pod.Annotations, err = util.MarshalPodAnnotation(pod.Annotations,
					&util.PodAnnotation{MAC: hwAddr, TunnelID: 1},
					util.GetNADName("default", "skip-ipam-nad"),
				)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				fakeOvn.startWithDBSetup(initialDB,
					&corev1.NamespaceList{
						Items: []corev1.Namespace{
							namespaceT,
						},
					},
					&corev1.NodeList{
						Items: []corev1.Node{
							*newNode(t.nodeName, "192.168.126.202/24"),
						},
					},
					&corev1.PodList{
						Items: []corev1.Pod{},
					},
					&nettypes.NetworkAttachmentDefinitionList{
						Items: []nettypes.NetworkAttachmentDefinition{*nad},
					},
				)
				ocInfo := fakeOvn.userDefinedNetworkControllers["skip-ipam-nad"]
				subnet := ocInfo.bnc.Subnets()[0]
				err = ocInfo.bnc.lsManager.AddOrUpdateSwitch(ocInfo.bnc.GetNetworkScopedName(ovntypes.OVNLayer2Switch), []*net.IPNet{subnet.CIDR}, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = ocInfo.bnc.WatchNamespaces()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = ocInfo.bnc.WatchPods()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(t.namespace).Create(context.TODO(), pod, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				// check nbdb data is added
				gomega.Eventually(func() bool {
					lsp := &nbdb.LogicalSwitchPort{Name: "default.skip.ipam.nad_namespace1_myPod"}
					err = fakeOvn.nbClient.Get(context.TODO(), lsp)
					if err != nil && err.Error() == "object not found" {
						return false
					}
					gomega.Expect(lsp.PortSecurity).To(gomega.HaveLen(1))
					strs := strings.Split(lsp.PortSecurity[0], " ")
					gomega.Expect(strs).To(gomega.HaveLen(1))
					return true
				}, 60*time.Second).Should(gomega.BeTrue())
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
	})
})
