package ovn

import (
	"context"
	"net"
	"time"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	"github.com/urfave/cli/v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	portmirror "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/portmirror/v1beta1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	ovntest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing"
	libovsdbtest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing/libovsdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
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
	)

	ginkgo.BeforeEach(func() {
		// Restore global default values before each testcase
		config.PrepareTestConfig()
		config.OVNKubernetesFeature.EnablePortMirror = true
		app = cli.NewApp()
		app.Name = "portmirror"
		app.Flags = config.Flags
		fakeOvn = NewFakeOVN()
	})
	ginkgo.AfterEach(func() {
		fakeOvn.shutdown()
	})

	ginkgo.Context("PortMirror", func() {
		ginkgo.It("can create/delete mirror in ovn when portmirror is created/deleted", func() {
			pm := newPortMirror(portMirrorName, portMirrorNamespace, portmirror.PortMirrorDirectionBoth)
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.startWithDBSetup(
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{},
					},
					&portmirror.PortMirrorList{
						Items: []portmirror.PortMirror{
							*pm,
						},
					},
				)
				fakeOvn.controller.WatchPortMirrors()
				gomega.Eventually(fakeOvn.controller).ShouldNot(gomega.BeNil())
				gomega.Eventually(func() string {
					mirrors := []nbdb.Mirror{}
					err := fakeOvn.controller.mc.nbClient.WhereCache(func(mirror *nbdb.Mirror) bool {
						return mirror.Name == util.GetPortMirrorOVNName(portMirrorNamespace, portMirrorName)
					}).List(context.TODO(), &mirrors)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(mirrors)).To(gomega.Equal(1))
					return mirrors[0].Name
				}).Should(gomega.Equal(mirrorName))
				err := fakeOvn.fakeClient.PortMirrorClient.K8sV1beta1().PortMirrors(portMirrorNamespace).Delete(context.TODO(), portMirrorName, metav1.DeleteOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Eventually(func() []nbdb.Mirror {
					mirrors := []nbdb.Mirror{}
					err := fakeOvn.controller.mc.nbClient.WhereCache(func(mirror *nbdb.Mirror) bool {
						return mirror.Name == util.GetPortMirrorOVNName(portMirrorNamespace, portMirrorName)
					}).List(context.TODO(), &mirrors)
					gomega.Expect(err).To(gomega.BeNil())
					return mirrors
				}, time.Minute).Should(gomega.BeEmpty())
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("can create/delete mirrorID to pod LSP in ovn when portmirror's are created/deleted", func() {
			pm := newPortMirror(portMirrorName, portMirrorNamespace, portmirror.PortMirrorDirectionBoth)
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.startWithDBSetup(
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalSwitch{
								UUID: "node1",
								Name: "node1",
							},
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							*newNamespaceWithLabels(portMirrorNamespace, map[string]string{}),
						},
					},
					&portmirror.PortMirrorList{
						Items: []portmirror.PortMirror{
							*pm,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPodWithLabels(portMirrorNamespace, "pod1", "node1", "10.128.1.12", map[string]string{"k8s.io/app": portMirrorApp}, "10.192.1.11"),
						},
					},
				)

				switchUUID := getLogicalSwitchUUID(fakeOvn.nbClient, "node1")
				fakeOvn.controller.lsManager.AddNode("node1", switchUUID, []*net.IPNet{ovntest.MustParseIPNet("10.128.1.0/24")})
				fakeOvn.controller.WatchPods()
				fakeOvn.controller.WatchPortMirrors()
				gomega.Eventually(fakeOvn.controller).ShouldNot(gomega.BeNil())

				mirrors := []nbdb.Mirror{}
				err := fakeOvn.controller.mc.nbClient.WhereCache(func(mirror *nbdb.Mirror) bool {
					return mirror.Name == util.GetPortMirrorOVNName(portMirrorNamespace, portMirrorName)
				}).List(context.TODO(), &mirrors)
				gomega.Expect(err).To(gomega.BeNil())
				gomega.Expect(len(mirrors)).To(gomega.Equal(1))

				gomega.Eventually(func() []string {
					logicalPortName := util.GetLogicalPortName(portMirrorNamespace, "pod1", "", true)
					lsp := &nbdb.LogicalSwitchPort{Name: logicalPortName}
					fakeOvn.controller.mc.nbClient.Get(context.Background(), lsp)
					return lsp.MirrorRules

				}, time.Minute).Should(gomega.ContainElement(mirrors[0].UUID))

				err = fakeOvn.fakeClient.PortMirrorClient.K8sV1beta1().PortMirrors(portMirrorNamespace).Delete(context.TODO(), portMirrorName, metav1.DeleteOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Eventually(func() []string {
					logicalPortName := util.GetLogicalPortName(portMirrorNamespace, "pod1", "", true)
					lsp := &nbdb.LogicalSwitchPort{Name: logicalPortName}
					fakeOvn.controller.mc.nbClient.Get(context.Background(), lsp)
					return lsp.MirrorRules

				}, time.Minute).Should(gomega.BeEmpty())
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("can create/delete mirrorID to pod LSP in ovn when pod label changes", func() {
			pm := newPortMirror(portMirrorName, portMirrorNamespace, portmirror.PortMirrorDirectionBoth)
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.startWithDBSetup(
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalSwitch{
								UUID: "node1",
								Name: "node1",
							},
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							*newNamespaceWithLabels(portMirrorNamespace, map[string]string{}),
						},
					},
					&portmirror.PortMirrorList{
						Items: []portmirror.PortMirror{
							*pm,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPodWithLabels(portMirrorNamespace, "pod1", "node1", "10.128.1.12", map[string]string{"k8s.io/app": portMirrorApp}, "10.192.1.11"),
						},
					},
				)

				switchUUID := getLogicalSwitchUUID(fakeOvn.nbClient, "node1")
				fakeOvn.controller.lsManager.AddNode("node1", switchUUID, []*net.IPNet{ovntest.MustParseIPNet("10.128.1.0/24")})
				fakeOvn.controller.WatchPods()
				fakeOvn.controller.WatchPortMirrors()
				gomega.Eventually(fakeOvn.controller).ShouldNot(gomega.BeNil())

				mirrors := []nbdb.Mirror{}
				err := fakeOvn.controller.mc.nbClient.WhereCache(func(mirror *nbdb.Mirror) bool {
					return mirror.Name == util.GetPortMirrorOVNName(portMirrorNamespace, portMirrorName)
				}).List(context.TODO(), &mirrors)
				gomega.Expect(err).To(gomega.BeNil())
				gomega.Expect(len(mirrors)).To(gomega.Equal(1))

				gomega.Eventually(func() []string {
					logicalPortName := util.GetLogicalPortName(portMirrorNamespace, "pod1", "", true)
					lsp := &nbdb.LogicalSwitchPort{Name: logicalPortName}
					fakeOvn.controller.mc.nbClient.Get(context.Background(), lsp)
					return lsp.MirrorRules

				}, time.Minute).Should(gomega.ContainElement(mirrors[0].UUID))

				podDelta := newPodWithLabels(portMirrorNamespace, "pod1", "node1", "10.128.1.12", map[string]string{"k8s.io/app": "something_else"}, "10.192.1.11")
				_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(portMirrorNamespace).Update(context.TODO(), podDelta, metav1.UpdateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Eventually(func() []string {
					logicalPortName := util.GetLogicalPortName(portMirrorNamespace, "pod1", "", true)
					lsp := &nbdb.LogicalSwitchPort{Name: logicalPortName}
					fakeOvn.controller.mc.nbClient.Get(context.Background(), lsp)
					return lsp.MirrorRules

				}, time.Minute).Should(gomega.BeEmpty())
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("Delete stale mirror in ovn when corresponding portmirror is not found", func() {
			app.Action = func(ctx *cli.Context) error {
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
				fakeOvn.controller.WatchPortMirrors()
				gomega.Eventually(fakeOvn.controller).ShouldNot(gomega.BeNil())
				// sleep for 30s so that periodic check will delete the stale mirror
				gomega.Eventually(func() []nbdb.Mirror {
					mirrors := []nbdb.Mirror{}
					err := fakeOvn.controller.mc.nbClient.WhereCache(func(mirror *nbdb.Mirror) bool {
						return mirror.Name == "stale_mirror"
					}).List(context.TODO(), &mirrors)
					gomega.Expect(err).To(gomega.BeNil())
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
