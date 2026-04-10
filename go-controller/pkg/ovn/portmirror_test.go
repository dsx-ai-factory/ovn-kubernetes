package ovn

import (
	"context"
	"net"
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
	portmirror "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/portmirror/v1beta1"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/nbdb"
	ovntest "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/testing"
	libovsdbtest "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/testing/libovsdb"
	ovntypes "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

var (
	portMirrorName      = "portmirror1"
	portMirrorNamespace = "portmirror-namespace"
	mirrorName          = "portmirror-namespace_portmirror1"
	portMirrorApp       = "app1"
)

var _ = ginkgo.Describe("PortMirror", func() {
	var (
		app     *cli.App
		fakeOvn *FakeOVN
		nad     *nettypes.NetworkAttachmentDefinition
	)

	ginkgo.BeforeEach(func() {
		// Restore global default values before each testcase
		gomega.Expect(config.PrepareTestConfig()).Should(gomega.Succeed())
		config.OVNKubernetesFeature.EnablePortMirror = true
		config.OVNKubernetesFeature.EnableMultiNetwork = true
		app = cli.NewApp()
		app.Name = "portmirror"
		app.Flags = config.Flags
		fakeOvn = NewFakeOVN(true)

		var err error
		nad, err = newNetworkAttachmentDefinition(
			"default",
			"ovn-primary",
			ovncnitypes.NetConf{
				NetConf: cnitypes.NetConf{
					Name: "ovn-primary",
					Type: "ovn-k8s-cni-overlay",
				},
				NADName:  util.GetNADName("default", "ovn-primary"),
				Topology: ovntypes.Layer3Topology,
				Subnets:  "10.193.0.0/16/26",
			},
		)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	})
	ginkgo.AfterEach(func() {
		ocInfo := fakeOvn.userDefinedNetworkControllers["ovn-primary"]
		gomega.Expect(ocInfo).ToNot(gomega.BeNil())
		fakeOvn.shutdown()
	})

	ginkgo.Context("PortMirror", func() {
		ginkgo.It("can create/delete mirror in ovn when portmirror is created/deleted", func() {
			pm := newPortMirror(portMirrorName, portMirrorNamespace, portmirror.PortMirrorDirectionBoth)
			app.Action = func(_ *cli.Context) error {
				fakeOvn.startWithDBSetup(
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{},
					},
					&portmirror.PortMirrorList{
						Items: []portmirror.PortMirror{
							*pm,
						},
					},
					&nettypes.NetworkAttachmentDefinitionList{
						Items: []nettypes.NetworkAttachmentDefinition{*nad},
					},
				)
				ocInfo := fakeOvn.userDefinedNetworkControllers["ovn-primary"]
				gomega.Expect(ocInfo).ToNot(gomega.BeNil())
				err := ocInfo.bnc.WatchPortMirrors()
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Eventually(func() string {
					mirrors := []nbdb.Mirror{}
					err := fakeOvn.nbClient.WhereCache(func(mirror *nbdb.Mirror) bool {
						return mirror.Name == util.GetPortMirrorOVNName(portMirrorNamespace, portMirrorName)
					}).List(context.TODO(), &mirrors)
					gomega.Expect(err).ToNot(gomega.HaveOccurred())
					gomega.Expect(mirrors).To(gomega.HaveLen(1))
					return mirrors[0].Name
				}).Should(gomega.Equal(mirrorName))
				err = fakeOvn.fakeClient.PortMirrorClient.K8sV1beta1().PortMirrors(portMirrorNamespace).Delete(context.TODO(), portMirrorName, metav1.DeleteOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Eventually(func() []nbdb.Mirror {
					mirrors := []nbdb.Mirror{}
					err := fakeOvn.nbClient.WhereCache(func(mirror *nbdb.Mirror) bool {
						return mirror.Name == util.GetPortMirrorOVNName(portMirrorNamespace, portMirrorName)
					}).List(context.TODO(), &mirrors)
					gomega.Expect(err).ToNot(gomega.HaveOccurred())
					return mirrors
				}, time.Minute).Should(gomega.BeEmpty())
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("can create/delete mirrorID to pod LSP in ovn when portmirror's are created/deleted", func() {
			pm := newPortMirror(portMirrorName, portMirrorNamespace, portmirror.PortMirrorDirectionBoth)
			namespaceT := *ovntest.NewNamespace(portMirrorNamespace)
			podT := ovntest.NewPodWithLabels(portMirrorNamespace, "pod1", "node1", "10.128.1.12", map[string]string{"k8s.io/app": portMirrorApp})
			podT.Annotations = map[string]string{
				"k8s.v1.cni.cncf.io/networks": "default/ovn-primary",
			}
			app.Action = func(_ *cli.Context) error {
				fakeOvn.startWithDBSetup(
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalSwitch{
								UUID: "node1",
								Name: "node1",
							},
							&nbdb.LogicalSwitch{
								UUID: "primary-node1",
								Name: "ovn.primary_node1",
							},
						},
					},
					&corev1.NamespaceList{
						Items: []corev1.Namespace{
							namespaceT,
						},
					},
					&corev1.NodeList{
						Items: []corev1.Node{
							*newNode("node1", "192.168.126.202/24"),
						},
					},
					&portmirror.PortMirrorList{
						Items: []portmirror.PortMirror{
							*pm,
						},
					},
					&corev1.PodList{
						Items: []corev1.Pod{
							*podT,
						},
					},
					&nettypes.NetworkAttachmentDefinitionList{
						Items: []nettypes.NetworkAttachmentDefinition{*nad},
					},
				)

				ocInfo := fakeOvn.userDefinedNetworkControllers["ovn-primary"]
				gomega.Expect(ocInfo).ToNot(gomega.BeNil())

				err := ocInfo.bnc.lsManager.AddOrUpdateSwitch(ocInfo.bnc.GetNetworkScopedName("node1"), []*net.IPNet{ovntest.MustParseIPNet("10.128.1.0/24")}, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = ocInfo.bnc.WatchPods()
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				err = ocInfo.bnc.WatchPortMirrors()
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				mirrors := []nbdb.Mirror{}
				err = fakeOvn.nbClient.WhereCache(func(mirror *nbdb.Mirror) bool {
					return mirror.Name == util.GetPortMirrorOVNName(portMirrorNamespace, portMirrorName)
				}).List(context.TODO(), &mirrors)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(mirrors).To(gomega.HaveLen(1))

				gomega.Eventually(func() []string {
					nadName := util.GetNADName("default", "ovn-primary")
					logicalPortName := util.GetUserDefinedNetworkLogicalPortName(portMirrorNamespace, "pod1", nadName)
					lsp := &nbdb.LogicalSwitchPort{Name: logicalPortName}
					err = fakeOvn.nbClient.Get(context.Background(), lsp)
					gomega.Expect(err).ToNot(gomega.HaveOccurred())
					return lsp.MirrorRules

				}, time.Minute).Should(gomega.ContainElement(mirrors[0].UUID))

				err = fakeOvn.fakeClient.PortMirrorClient.K8sV1beta1().PortMirrors(portMirrorNamespace).Delete(context.TODO(), portMirrorName, metav1.DeleteOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Eventually(func() []string {
					nadName := util.GetNADName("default", "ovn-primary")
					logicalPortName := util.GetUserDefinedNetworkLogicalPortName(portMirrorNamespace, "pod1", nadName)
					lsp := &nbdb.LogicalSwitchPort{Name: logicalPortName}
					err = fakeOvn.nbClient.Get(context.Background(), lsp)
					gomega.Expect(err).ToNot(gomega.HaveOccurred())
					return lsp.MirrorRules

				}, time.Minute).Should(gomega.BeEmpty())
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("can create/delete mirrorID to pod LSP in ovn when pod label changes", func() {
			pm := newPortMirror(portMirrorName, portMirrorNamespace, portmirror.PortMirrorDirectionBoth)
			namespaceT := *ovntest.NewNamespace(portMirrorNamespace)
			pod1 := ovntest.NewPodWithLabels(portMirrorNamespace, "pod1", "node1", "10.128.1.12", map[string]string{"k8s.io/app": portMirrorApp})
			pod1.Annotations = map[string]string{
				"k8s.v1.cni.cncf.io/networks": "default/ovn-primary",
			}
			app.Action = func(_ *cli.Context) error {
				fakeOvn.startWithDBSetup(
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalSwitch{
								UUID: "node1",
								Name: "node1",
							},
							&nbdb.LogicalSwitch{
								UUID: "primary-node1",
								Name: "ovn.primary_node1",
							},
						},
					},
					&corev1.NamespaceList{
						Items: []corev1.Namespace{
							namespaceT,
						},
					},
					&corev1.NodeList{
						Items: []corev1.Node{
							*newNode("node1", "192.168.126.202/24"),
						},
					},
					&portmirror.PortMirrorList{
						Items: []portmirror.PortMirror{
							*pm,
						},
					},
					&corev1.PodList{
						Items: []corev1.Pod{
							*pod1,
						},
					},
					&nettypes.NetworkAttachmentDefinitionList{
						Items: []nettypes.NetworkAttachmentDefinition{*nad},
					},
				)

				ocInfo := fakeOvn.userDefinedNetworkControllers["ovn-primary"]
				gomega.Expect(ocInfo).ToNot(gomega.BeNil())
				err := ocInfo.bnc.lsManager.AddOrUpdateSwitch(ocInfo.bnc.GetNetworkScopedName("node1"), []*net.IPNet{ovntest.MustParseIPNet("10.128.1.0/24")}, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = ocInfo.bnc.WatchPods()
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				err = ocInfo.bnc.WatchPortMirrors()
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				mirrors := []nbdb.Mirror{}
				err = fakeOvn.nbClient.WhereCache(func(mirror *nbdb.Mirror) bool {
					return mirror.Name == util.GetPortMirrorOVNName(portMirrorNamespace, portMirrorName)
				}).List(context.TODO(), &mirrors)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(mirrors).To(gomega.HaveLen(1))

				gomega.Eventually(func() []string {
					nadName := util.GetNADName("default", "ovn-primary")
					logicalPortName := util.GetUserDefinedNetworkLogicalPortName(portMirrorNamespace, "pod1", nadName)
					lsp := &nbdb.LogicalSwitchPort{Name: logicalPortName}
					err = fakeOvn.nbClient.Get(context.Background(), lsp)
					gomega.Expect(err).ToNot(gomega.HaveOccurred())
					return lsp.MirrorRules

				}, time.Minute).Should(gomega.ContainElement(mirrors[0].UUID))

				podDelta := ovntest.NewPodWithLabels(portMirrorNamespace, "pod1", "node1", "10.128.1.12", map[string]string{"k8s.io/app": "something_else"})
				_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(portMirrorNamespace).Update(context.TODO(), podDelta, metav1.UpdateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Eventually(func() []string {
					nadName := util.GetNADName("default", "ovn-primary")
					logicalPortName := util.GetUserDefinedNetworkLogicalPortName(portMirrorNamespace, "pod1", nadName)
					lsp := &nbdb.LogicalSwitchPort{Name: logicalPortName}
					err = fakeOvn.nbClient.Get(context.Background(), lsp)
					gomega.Expect(err).ToNot(gomega.HaveOccurred())
					return lsp.MirrorRules

				}, time.Minute).Should(gomega.BeEmpty())
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("Delete stale mirror in ovn when corresponding portmirror is not found", func() {
			app.Action = func(_ *cli.Context) error {
				fakeOvn.startWithDBSetup(
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.Mirror{
								Name: "stale_mirror",
								UUID: "stale_mirror",
							},
						},
					},
				)

				err := fakeOvn.controller.WatchPortMirrors()
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				// sleep for 30s so that periodic check will delete the stale mirror
				gomega.Eventually(func() []nbdb.Mirror {
					mirrors := []nbdb.Mirror{}
					err := fakeOvn.controller.nbClient.WhereCache(func(mirror *nbdb.Mirror) bool {
						return mirror.Name == "stale_mirror"
					}).List(context.TODO(), &mirrors)
					gomega.Expect(err).ToNot(gomega.HaveOccurred())
					return mirrors
				}, time.Minute).Should(gomega.BeEmpty())
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

	})
})

func newPortMirror(name, namespace string, mirrorDirection string) *portmirror.PortMirror {
	return &portmirror.PortMirror{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PortMirror",
			APIVersion: "k8s.ovn.org/v1beta1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: portmirror.PortMirrorSpec{
			MirrorDirection: mirrorDirection,
			Sources: []portmirror.PortMirrorSource{
				{
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"k8s.io/app": portMirrorApp,
						},
					},
					NetworkAttachmentName: []portmirror.NetworkAttachmentNameString{
						"default/ovn-primary",
					},
				},
			},
		},
	}
}
