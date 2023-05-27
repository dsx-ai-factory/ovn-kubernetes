package ovn

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	ovntest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	libovsdbtest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing/libovsdb"
)

var _ = ginkgo.Describe("Skip IPAM on a given network", func() {
	var (
		app       *cli.App
		fakeOvn   *FakeOVN
		initialDB libovsdbtest.TestSetup
	)

	ginkgo.BeforeEach(func() {
		// Restore global default values before each testcase
		config.PrepareTestConfig()

		app = cli.NewApp()
		app.Name = "test"
		app.Flags = config.Flags

		fakeOvn = NewFakeOVN()
		initialDB = libovsdbtest.TestSetup{
			NBData: []libovsdbtest.TestData{
				&nbdb.LogicalSwitch{
					Name: "node1",
				},
			},
		}

	})

	ginkgo.AfterEach(func() {
		fakeOvn.shutdown()
	})

	ginkgo.Context("skip ipam for a pod on given interface", func() {
		ginkgo.It("reconciles a pod with skip ipam annotation and floating ip", func() {
			app.Action = func(ctx *cli.Context) error {
				floatingIP := "10.193.13.5"
				nodeSecondarySubnet := "10.193.0.0/26"
				namespaceT := *newNamespace("namespace1")
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
				pod := newPod(t.namespace, t.podName, t.nodeName, t.podIP)
				pod.Annotations = map[string]string{
					"k8s.v1.cni.cncf.io/networks":     `[{"interface":"net1","name":"skip-ipam-nad","namespace":"default"}]`,
					"k8s.ovn.org/skip-ip-on-networks": "default/skip-ipam-nad",
					"k8s.ovn.org/port-security-info":  fmt.Sprintf(`{"default/skip-ipam-nad": {"ips": ["%s"]}}`, floatingIP),
				}
				fakeOvn.startWithDBSetup(initialDB,
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespaceT,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{},
					},
				)
				fakeOvn.controller.lsManager.AddNode(t.nodeName, t.nodeName+"-UUID", []*net.IPNet{ovntest.MustParseIPNet(t.nodeSubnet)})
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()
				skipIPAMController, err := fakeOvn.mhController.NewOvnController(
					&util.NetAttachDefInfo{
						NetNameInfo: util.NetNameInfo{
							NetName:     "default/skip-ipam-nad",
							IsSecondary: true,
						},
						NetCidr: "10.193.0.0/16",
						MTU:     1400,
					}, fakeOvn.asf)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				skipIPAMController.nadInfo.NetAttachDefs.Store("default/skip-ipam-nad", &util.NadConfig{MissRateLimitConfig: util.MissRateLimitConfig{MaxNewConnPPS: 10, MaxNewConnBurst: 100}})
				skipIPAMController.WatchPods()
				skipIPAMController.lsManager.AddNode(t.nodeName, t.nodeName+"-UUID", []*net.IPNet{ovntest.MustParseIPNet(nodeSecondarySubnet)})
				_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(t.namespace).Create(context.TODO(), pod, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				// check nbdb data is added
				gomega.Eventually(func() bool {
					lsp := &nbdb.LogicalSwitchPort{Name: "default.skip.ipam.nad_namespace1_myPod"}
					err = fakeOvn.nbClient.Get(context.TODO(), lsp)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(len(lsp.PortSecurity)).To(gomega.Equal(1))
					strs := strings.Split(lsp.PortSecurity[0], " ")
					gomega.Expect(len(strs)).To(gomega.Equal(2))
					gomega.Expect(strs[1]).To(gomega.Equal(floatingIP))
					return true
				}, 60*time.Second).Should(gomega.BeTrue())
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
		ginkgo.It("reconciles a pod with skip ipam annotation but no floating ip", func() {
			app.Action = func(ctx *cli.Context) error {
				nodeSecondarySubnet := "10.193.0.0/26"
				namespaceT := *newNamespace("namespace1")
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
				pod := newPod(t.namespace, t.podName, t.nodeName, t.podIP)
				pod.Annotations = map[string]string{
					"k8s.v1.cni.cncf.io/networks":     `[{"interface":"net1","name":"skip-ipam-nad","namespace":"default"}]`,
					"k8s.ovn.org/skip-ip-on-networks": "default/skip-ipam-nad",
				}
				fakeOvn.startWithDBSetup(initialDB,
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespaceT,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{},
					},
				)
				fakeOvn.controller.lsManager.AddNode(t.nodeName, t.nodeName+"-UUID", []*net.IPNet{ovntest.MustParseIPNet(t.nodeSubnet)})
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()
				skipIPAMController, err := fakeOvn.mhController.NewOvnController(
					&util.NetAttachDefInfo{
						NetNameInfo: util.NetNameInfo{
							NetName:     "default/skip-ipam-nad",
							IsSecondary: true,
						},
						NetCidr: "10.193.0.0/16",
						MTU:     1400,
					}, fakeOvn.asf)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				skipIPAMController.nadInfo.NetAttachDefs.Store("default/skip-ipam-nad", &util.NadConfig{MissRateLimitConfig: util.MissRateLimitConfig{MaxNewConnPPS: 10, MaxNewConnBurst: 100}})
				skipIPAMController.WatchPods()
				skipIPAMController.lsManager.AddNode(t.nodeName, t.nodeName+"-UUID", []*net.IPNet{ovntest.MustParseIPNet(nodeSecondarySubnet)})
				_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(t.namespace).Create(context.TODO(), pod, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				// check nbdb data is added
				gomega.Eventually(func() bool {
					lsp := &nbdb.LogicalSwitchPort{Name: "default.skip.ipam.nad_namespace1_myPod"}
					err = fakeOvn.nbClient.Get(context.TODO(), lsp)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(len(lsp.PortSecurity)).To(gomega.Equal(1))
					strs := strings.Split(lsp.PortSecurity[0], " ")
					gomega.Expect(len(strs)).To(gomega.Equal(1))
					return true
				}, 60*time.Second).Should(gomega.BeTrue())
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
	})
})
