package ovn

import (
	"context"
	"net"
	"strings"
	"time"

	cnitypes "github.com/containernetworking/cni/pkg/types"
	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/urfave/cli/v2"

	ovncnitypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	virtualip "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/virtualip/v1beta1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	libovsdbtest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing/libovsdb"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	virtualIPName      = "virtualip1"
	virtualIPNamespace = "virtualip-namespace"
	virtualIPApp       = "app1"
	vipAddress         = "10.255.245.10"
	vipNetworkName     = "virtualip-nad"
	vipPortName        = "ovn_k8s_vport_virtualip-namespace_virtualip1"
)

var _ = ginkgo.Describe("VirtualIP", func() {
	var (
		app       *cli.App
		fakeOvn   *FakeOVN
		initialDB libovsdbtest.TestSetup
		nadName   string
		nad       *nettypes.NetworkAttachmentDefinition
	)

	ginkgo.BeforeEach(func() {
		var err error
		// Restore global default values before each testcase
		config.PrepareTestConfig()
		config.OVNKubernetesFeature.EnableVirtualIP = true
		app = cli.NewApp()
		app.Name = "VirtualIP"
		app.Flags = config.Flags

		fakeOvn = NewFakeOVN(false)
		initialDB = libovsdbtest.TestSetup{
			NBData: []libovsdbtest.TestData{
				&nbdb.LogicalSwitch{
					Name: "node1",
				},
				&nbdb.LogicalSwitch{
					Name: util.GetSecondaryNetworkPrefix(vipNetworkName) + ovntypes.OVNLayer2Switch,
				},
			},
		}
		nadName = util.GetNADName("default", vipNetworkName)
		nad, err = newNetworkAttachmentDefinition(
			"default",
			vipNetworkName,
			ovncnitypes.NetConf{
				NetConf: cnitypes.NetConf{
					Name: vipNetworkName,
					Type: "ovn-k8s-cni-overlay",
				},
				Topology:       ovntypes.Layer2Topology,
				NADName:        nadName,
				Subnets:        "10.255.245.0/24",
				ExcludeSubnets: "10.255.245.10/32",
			},
		)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})

	ginkgo.AfterEach(func() {
		fakeOvn.shutdown()
	})

	ginkgo.Context("VirtualIP", func() {
		ginkgo.It("can create/delete virtual port in ovn when virtualIP is created/deleted", func() {
			vip := newVirtualIP(virtualIPName, virtualIPNamespace, vipAddress, nadName)
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.startWithDBSetup(initialDB,
					&virtualip.VirtualIPList{
						Items: []virtualip.VirtualIP{
							*vip,
						},
					},
					&nettypes.NetworkAttachmentDefinitionList{
						Items: []nettypes.NetworkAttachmentDefinition{*nad},
					},
				)
				ocInfo := fakeOvn.secondaryControllers[vipNetworkName]
				subnet := ocInfo.bnc.Subnets()[0]
				err := ocInfo.bnc.lsManager.AddOrUpdateSwitch(ocInfo.bnc.GetNetworkScopedName(ovntypes.OVNLayer2Switch), []*net.IPNet{subnet.CIDR})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = ocInfo.bnc.WatchVirtualIPs()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(func() string {
					virtualLSP := []nbdb.LogicalSwitchPort{}
					err := fakeOvn.controller.nbClient.WhereCache(func(lsp *nbdb.LogicalSwitchPort) bool {
						return lsp.Name == getVirtualPortName(virtualIPNamespace, virtualIPName)
					}).List(context.TODO(), &virtualLSP)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(virtualLSP)).To(gomega.Equal(1))
					return virtualLSP[0].Name
				}).Should(gomega.Equal(vipPortName))
				err = fakeOvn.fakeClient.VirtualIPClient.K8sV1beta1().VirtualIPs(virtualIPNamespace).Delete(context.TODO(), virtualIPName, metav1.DeleteOptions{})
				gomega.Eventually(func() []nbdb.LogicalSwitchPort {
					virtualLSP := []nbdb.LogicalSwitchPort{}
					err := fakeOvn.controller.nbClient.WhereCache(func(lsp *nbdb.LogicalSwitchPort) bool {
						return lsp.Name == getVirtualPortName(virtualIPNamespace, virtualIPName)
					}).List(context.TODO(), &virtualLSP)
					gomega.Expect(err).To(gomega.BeNil())
					return virtualLSP
				}, time.Minute).Should(gomega.BeEmpty())
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("can add/delete pod logical switch port to virtual port virtual-parents field", func() {
			vip := newVirtualIP(virtualIPName, virtualIPNamespace, vipAddress, nadName)
			pod1 := newPodWithLabels(virtualIPNamespace, "pod1", "node1", "10.192.1.11", map[string]string{"k8s.io/app": virtualIPApp}, "10.128.1.12")
			pod1.Annotations = map[string]string{
				"k8s.v1.cni.cncf.io/networks": nadName,
			}
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.startWithDBSetup(initialDB,
					&v1.NodeList{
						Items: []v1.Node{
							*newNode("node1", "192.168.126.202/24"),
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							*newNamespaceWithLabels(virtualIPNamespace, map[string]string{}),
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*pod1,
						},
					},
					&virtualip.VirtualIPList{
						Items: []virtualip.VirtualIP{
							*vip,
						},
					},
					&nettypes.NetworkAttachmentDefinitionList{
						Items: []nettypes.NetworkAttachmentDefinition{*nad},
					},
				)
				ocInfo := fakeOvn.secondaryControllers[vipNetworkName]
				subnet := ocInfo.bnc.Subnets()[0]
				err := ocInfo.bnc.lsManager.AddOrUpdateSwitch(ocInfo.bnc.GetNetworkScopedName(ovntypes.OVNLayer2Switch), []*net.IPNet{subnet.CIDR})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				err = ocInfo.bnc.WatchPods()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = ocInfo.bnc.WatchVirtualIPs()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(func() string {
					virtualLSP := []nbdb.LogicalSwitchPort{}
					err := fakeOvn.controller.nbClient.WhereCache(func(lsp *nbdb.LogicalSwitchPort) bool {
						return lsp.Name == getVirtualPortName(virtualIPNamespace, virtualIPName)
					}).List(context.TODO(), &virtualLSP)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(virtualLSP)).To(gomega.Equal(1))
					return virtualLSP[0].Name
				}).Should(gomega.Equal(vipPortName))

				// check if virtual port virtual-parents options field has been updated with
				// pod logical switch port
				lspPod := util.GetSecondaryNetworkLogicalPortName(virtualIPNamespace, "pod1", nadName)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Eventually(func() string {
					virtualLSP := []nbdb.LogicalSwitchPort{}
					err := fakeOvn.controller.nbClient.WhereCache(func(lsp *nbdb.LogicalSwitchPort) bool {
						return lsp.Name == getVirtualPortName(virtualIPNamespace, virtualIPName)
					}).List(context.TODO(), &virtualLSP)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(virtualLSP)).To(gomega.Equal(1))
					vipParents := virtualLSP[0].Options[optionsVirtualIPParents]
					return vipParents
				}).Should(gomega.Equal(lspPod))

				podDelta := newPodWithLabels(virtualIPNamespace, "pod1", "node1", "10.192.1.11", map[string]string{"k8s.io/app": "something_else"}, "10.128.1.12")
				_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(virtualIPNamespace).Update(context.TODO(), podDelta, metav1.UpdateOptions{})
				// now virtual port virtual-parents field should be empty,
				// as pod1 label has been changed
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Eventually(func() string {
					virtualLSP := []nbdb.LogicalSwitchPort{}
					err := fakeOvn.controller.nbClient.WhereCache(func(lsp *nbdb.LogicalSwitchPort) bool {
						return lsp.Name == getVirtualPortName(virtualIPNamespace, virtualIPName)
					}).List(context.TODO(), &virtualLSP)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(virtualLSP)).To(gomega.Equal(1))
					vipParents := virtualLSP[0].Options[optionsVirtualIPParents]
					return vipParents
				}).Should(gomega.Equal(""))
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("can add/delete virtualip address to pod logical switch port port security field", func() {
			vip := newVirtualIP(virtualIPName, virtualIPNamespace, vipAddress, nadName)
			pod1 := newPodWithLabels(virtualIPNamespace, "pod1", "node1", "10.192.1.11", map[string]string{"k8s.io/app": virtualIPApp}, "10.128.1.12")
			pod1.Annotations = map[string]string{
				"k8s.v1.cni.cncf.io/networks": nadName,
			}
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.startWithDBSetup(initialDB,
					&v1.NodeList{
						Items: []v1.Node{
							*newNode("node1", "192.168.126.202/24"),
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							*newNamespaceWithLabels(virtualIPNamespace, map[string]string{}),
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*pod1,
						},
					},
					&virtualip.VirtualIPList{
						Items: []virtualip.VirtualIP{
							*vip,
						},
					},
					&nettypes.NetworkAttachmentDefinitionList{
						Items: []nettypes.NetworkAttachmentDefinition{*nad},
					},
				)
				ocInfo := fakeOvn.secondaryControllers[vipNetworkName]
				subnet := ocInfo.bnc.Subnets()[0]
				err := ocInfo.bnc.lsManager.AddOrUpdateSwitch(ocInfo.bnc.GetNetworkScopedName(ovntypes.OVNLayer2Switch), []*net.IPNet{subnet.CIDR})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				err = ocInfo.bnc.WatchPods()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = ocInfo.bnc.WatchVirtualIPs()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(func() string {
					virtualLSP := []nbdb.LogicalSwitchPort{}
					err := fakeOvn.controller.nbClient.WhereCache(func(lsp *nbdb.LogicalSwitchPort) bool {
						return lsp.Name == getVirtualPortName(virtualIPNamespace, virtualIPName)
					}).List(context.TODO(), &virtualLSP)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(virtualLSP)).To(gomega.Equal(1))
					return virtualLSP[0].Name
				}).Should(gomega.Equal(vipPortName))

				// check if pod1 logical switch port port security field has been updated to have
				// virtualIP address
				gomega.Eventually(func() string {
					podLSP := []nbdb.LogicalSwitchPort{}
					err := fakeOvn.controller.nbClient.WhereCache(func(lsp *nbdb.LogicalSwitchPort) bool {
						return lsp.Name == util.GetSecondaryNetworkLogicalPortName(virtualIPNamespace, "pod1", nadName)
					}).List(context.TODO(), &podLSP)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(podLSP)).To(gomega.Equal(1))
					addrList := podLSP[0].PortSecurity
					var portSecurityFields string
					for _, addr := range addrList {
						if strings.HasSuffix(addr, vipAddress) {
							portSecurityFields = addr
							break
						}
					}
					if portSecurityFields != "" {
						fields := strings.Split(portSecurityFields, " ")
						return fields[1]
					}
					return portSecurityFields
				}).Should(gomega.Equal(vipAddress))

				err = fakeOvn.fakeClient.VirtualIPClient.K8sV1beta1().VirtualIPs(virtualIPNamespace).Delete(context.TODO(), virtualIPName, metav1.DeleteOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Eventually(func() string {
					podLSP := []nbdb.LogicalSwitchPort{}
					err := fakeOvn.controller.nbClient.WhereCache(func(lsp *nbdb.LogicalSwitchPort) bool {
						return lsp.Name == util.GetSecondaryNetworkLogicalPortName(virtualIPNamespace, "pod1", nadName)
					}).List(context.TODO(), &podLSP)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(podLSP)).To(gomega.Equal(1))
					addrList := podLSP[0].PortSecurity
					var portSecurityFields string
					for _, addr := range addrList {
						if strings.HasSuffix(addr, vipAddress) {
							portSecurityFields = addr
							break
						}
					}
					if portSecurityFields != "" {
						fields := strings.Split(portSecurityFields, " ")
						return fields[1]
					}
					return portSecurityFields
				}).Should(gomega.Equal(""))
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
	})
})

func newVirtualIP(name, namespace string, vipAddress, nadName string) *virtualip.VirtualIP {
	return &virtualip.VirtualIP{
		TypeMeta: metav1.TypeMeta{
			Kind:       "VirtualIP",
			APIVersion: "k8s.ovn.org/v1beta1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: virtualip.VirtualIPSpec{
			VirtualIP:             vipAddress,
			NetworkAttachmentName: nadName,
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"k8s.io/app": virtualIPApp,
				},
			},
		},
	}
}
