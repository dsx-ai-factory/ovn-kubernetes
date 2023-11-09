package ovn

import (
	"context"
	"time"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	"github.com/urfave/cli/v2"
	v1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cnitypes "github.com/containernetworking/cni/pkg/types"
	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	ovncnitypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	adminpbrapi "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/adminpbr/v1beta1"
	libovsdbops "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdb/ops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	libovsdbtest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing/libovsdb"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

var (
	policyName        = "test-policy"
	nextHop           = "10.10.10.10"
	adminPBRNamespace = "admin-pbr"
	node1Name         = "node1"
	node1IP           = "10.192.1.11"
	node2Name         = "node2"
	node2IP           = "10.192.1.12"
	pod1Name          = "pod1"
	pod1IP            = "10.128.10.20"
	pod2Name          = "pod2"
	pod2IP            = "10.128.10.21"
	app1Name          = "app1"
	app2Name          = "app2"
)

var _ = ginkgo.Describe("AdminPBR", func() {
	var (
		app     *cli.App
		fakeOvn *FakeOVN
		nad     *nettypes.NetworkAttachmentDefinition
	)

	ginkgo.BeforeEach(func() {
		// Restore global default values before each testcase
		config.PrepareTestConfig()
		config.OVNKubernetesFeature.EnableAdminPolicyBasedRouting = true
		config.OVNKubernetesFeature.EnableEgressFirewall = false
		config.OVNKubernetesFeature.EnableEgressIP = false
		app = cli.NewApp()
		app.Name = "adminpbr"
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
		gomega.Expect(err).To(gomega.BeNil())
	})
	ginkgo.AfterEach(func() {
		ocInfo := fakeOvn.secondaryControllers["ovn-primary"]
		gomega.Expect(ocInfo).ToNot(gomega.BeNil())
		fakeOvn.shutdown()
	})

	ginkgo.Context("AdminPBR", func() {
		ginkgo.It("can create/delete logical_router_policy and address_set in ovn when AdminPolicyBasedRoute is created/deleted", func() {
			pbr := newAdminPBR(policyName, nextHop)
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.startWithDBSetup(
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalRouter{
								Name: "ovn.primary_" + ovntypes.OVNClusterRouter,
								UUID: "ovn.primary_" + ovntypes.OVNClusterRouter + "-UUID",
							},
						},
					},
					&nettypes.NetworkAttachmentDefinitionList{
						Items: []nettypes.NetworkAttachmentDefinition{*nad},
					},
					&adminpbrapi.AdminPolicyBasedRouteList{
						Items: []adminpbrapi.AdminPolicyBasedRoute{
							*pbr,
						},
					},
				)
				ocInfo := fakeOvn.secondaryControllers["ovn-primary"]
				gomega.Expect(ocInfo).ToNot(gomega.BeNil())
				err := ocInfo.bnc.WatchAdminPolicyBasedRoutes()
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				var asIndex *libovsdbops.DbObjectIDs
				gomega.Eventually(func() []string {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
						return lrp.ExternalIDs[ovntypes.ExternalIDK8sOwner] == util.NamespacedName(pbr)
					}).List(context.TODO(), &policies)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(policies)).To(gomega.Equal(1))
					asIndex = getAdminPBRAddrSetDbIDs(pbr.Name, policies[0].ExternalIDs[ovntypes.ExternalIDHash], ocInfo.bnc.controllerName)
					return policies[0].Nexthops
				}).Should(gomega.ContainElement(nextHop))
				ocInfo.asf.EventuallyExpectAddressSet(asIndex)
				err = fakeOvn.fakeClient.AdminPBRClient.K8sV1beta1().AdminPolicyBasedRoutes().Delete(context.TODO(), policyName, metav1.DeleteOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Eventually(func() []nbdb.LogicalRouterPolicy {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
						return lrp.ExternalIDs[ovntypes.ExternalIDK8sOwner] == util.NamespacedName(pbr)
					}).List(context.TODO(), &policies)
					gomega.Expect(err).To(gomega.BeNil())
					return policies
				}, time.Minute).Should(gomega.BeEmpty())
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("can add/remove Pod IPs to/from address set when Pod is created/deleted", func() {
			pbr := newAdminPBR(policyName, nextHop)
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.startWithDBSetup(
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalRouter{
								Name: "ovn.primary_" + ovntypes.OVNClusterRouter,
								UUID: "ovn.primary_" + ovntypes.OVNClusterRouter + "-UUID",
							},
						},
					},
					&nettypes.NetworkAttachmentDefinitionList{
						Items: []nettypes.NetworkAttachmentDefinition{*nad},
					},
					&adminpbrapi.AdminPolicyBasedRouteList{
						Items: []adminpbrapi.AdminPolicyBasedRoute{
							*pbr,
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							*newNamespaceWithLabels(adminPBRNamespace, map[string]string{}),
						},
					},
					&v1.NodeList{
						Items: []v1.Node{
							*newNodeWithLabels(node1Name, node1IP, map[string]string{"ngn2.nvidia.com/igw_vip": "G"}),
							*newNodeWithLabels(node2Name, node2IP, map[string]string{"ngn2.nvidia.com/igw_vip": "H"}),
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPodWithLabels(adminPBRNamespace, pod1Name, node1Name, pod1IP, map[string]string{"k8s.io/app": app1Name}, node1IP),
							*newPodWithLabels(adminPBRNamespace, pod2Name, node2Name, pod2IP, map[string]string{"k8s.io/app": app2Name}, node2IP),
						},
					},
				)
				ocInfo := fakeOvn.secondaryControllers["ovn-primary"]
				gomega.Expect(ocInfo).ToNot(gomega.BeNil())
				err := ocInfo.bnc.WatchAdminPolicyBasedRoutes()
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				var asIndex *libovsdbops.DbObjectIDs
				gomega.Eventually(func() []string {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
						return lrp.ExternalIDs[ovntypes.ExternalIDK8sOwner] == util.NamespacedName(pbr)
					}).List(context.TODO(), &policies)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(policies)).To(gomega.Equal(1))
					asIndex = getAdminPBRAddrSetDbIDs(pbr.Name, policies[0].ExternalIDs[ovntypes.ExternalIDHash], ocInfo.bnc.controllerName)
					return policies[0].Nexthops
				}).Should(gomega.ContainElement(nextHop))
				ocInfo.asf.EventuallyExpectAddressSet(asIndex)
				ocInfo.asf.ExpectAddressSetWithIPs(asIndex, []string{pod1IP})
				err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(adminPBRNamespace).Delete(context.TODO(), pod1Name, metav1.DeleteOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				ocInfo.asf.EventuallyExpectEmptyAddressSetExist(asIndex)
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("can add/remove Pod IPs to/from address set when policy matches/mismatches the node selector", func() {
			pbr := newAdminPBR(policyName, nextHop)
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.startWithDBSetup(
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalRouter{
								Name: "ovn.primary_" + ovntypes.OVNClusterRouter,
								UUID: "ovn.primary_" + ovntypes.OVNClusterRouter + "-UUID",
							},
						},
					},
					&nettypes.NetworkAttachmentDefinitionList{
						Items: []nettypes.NetworkAttachmentDefinition{*nad},
					},
					&adminpbrapi.AdminPolicyBasedRouteList{
						Items: []adminpbrapi.AdminPolicyBasedRoute{
							*pbr,
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							*newNamespaceWithLabels(adminPBRNamespace, map[string]string{}),
						},
					},
					&v1.NodeList{
						Items: []v1.Node{
							*newNodeWithLabels(node1Name, node1IP, map[string]string{"ngn2.nvidia.com/igw_vip": "G"}),
							*newNodeWithLabels(node2Name, node2IP, map[string]string{"ngn2.nvidia.com/igw_vip": "H"}),
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPodWithLabels(adminPBRNamespace, pod1Name, node1Name, pod1IP, map[string]string{"k8s.io/app": app1Name}, node1IP),
							*newPodWithLabels(adminPBRNamespace, pod2Name, node2Name, pod2IP, map[string]string{"k8s.io/app": app2Name}, node2IP),
						},
					},
				)
				ocInfo := fakeOvn.secondaryControllers["ovn-primary"]
				gomega.Expect(ocInfo).ToNot(gomega.BeNil())
				err := ocInfo.bnc.WatchAdminPolicyBasedRoutes()
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				var asIndex *libovsdbops.DbObjectIDs
				gomega.Eventually(func() []string {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
						return lrp.ExternalIDs[ovntypes.ExternalIDK8sOwner] == util.NamespacedName(pbr)
					}).List(context.TODO(), &policies)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(policies)).To(gomega.Equal(1))
					asIndex = getAdminPBRAddrSetDbIDs(pbr.Name, policies[0].ExternalIDs[ovntypes.ExternalIDHash], ocInfo.bnc.controllerName)
					return policies[0].Nexthops
				}).Should(gomega.ContainElement(nextHop))
				ocInfo.asf.EventuallyExpectAddressSet(asIndex)
				ocInfo.asf.ExpectAddressSetWithIPs(asIndex, []string{pod1IP})
				nodeDelta := newNodeWithLabels(node1Name, node1IP, map[string]string{"ngn2.nvidia.com/igw_vip": "H"})
				_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Nodes().Update(context.TODO(), nodeDelta, metav1.UpdateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				ocInfo.asf.EventuallyExpectAddressSetWithIPs(asIndex, nil)
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("can add/remove Pod IPs to/from address set when policy matches/mismatches the namespace selector", func() {
			pbr := newAdminPBR(policyName, nextHop)
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.startWithDBSetup(
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalRouter{
								Name: "ovn.primary_" + ovntypes.OVNClusterRouter,
								UUID: "ovn.primary_" + ovntypes.OVNClusterRouter + "-UUID",
							},
						},
					},
					&nettypes.NetworkAttachmentDefinitionList{
						Items: []nettypes.NetworkAttachmentDefinition{*nad},
					},
					&adminpbrapi.AdminPolicyBasedRouteList{
						Items: []adminpbrapi.AdminPolicyBasedRoute{
							*pbr,
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							*newNamespaceWithLabels(adminPBRNamespace, map[string]string{}),
						},
					},
					&v1.NodeList{
						Items: []v1.Node{
							*newNodeWithLabels(node1Name, node1IP, map[string]string{"ngn2.nvidia.com/igw_vip": "G"}),
							*newNodeWithLabels(node2Name, node2IP, map[string]string{"ngn2.nvidia.com/igw_vip": "H"}),
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPodWithLabels(adminPBRNamespace, pod1Name, node1Name, pod1IP, map[string]string{"k8s.io/app": app1Name}, node1IP),
							*newPodWithLabels(adminPBRNamespace, pod2Name, node2Name, pod2IP, map[string]string{"k8s.io/app": app2Name}, node2IP),
						},
					},
				)
				ocInfo := fakeOvn.secondaryControllers["ovn-primary"]
				gomega.Expect(ocInfo).ToNot(gomega.BeNil())
				err := ocInfo.bnc.WatchAdminPolicyBasedRoutes()
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				var asIndex *libovsdbops.DbObjectIDs
				gomega.Eventually(func() []string {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
						return lrp.ExternalIDs[ovntypes.ExternalIDK8sOwner] == util.NamespacedName(pbr)
					}).List(context.TODO(), &policies)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(policies)).To(gomega.Equal(1))
					asIndex = getAdminPBRAddrSetDbIDs(pbr.Name, policies[0].ExternalIDs[ovntypes.ExternalIDHash], ocInfo.bnc.controllerName)
					return policies[0].Nexthops
				}).Should(gomega.ContainElement(nextHop))
				ocInfo.asf.EventuallyExpectAddressSet(asIndex)
				ocInfo.asf.ExpectAddressSetWithIPs(asIndex, []string{pod1IP})
				nsDelta := newNamespaceWithLabels(adminPBRNamespace, map[string]string{
					"ngn.nvidia.com/infrastructure": "",
				})
				_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Namespaces().Update(context.TODO(), nsDelta, metav1.UpdateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				ocInfo.asf.EventuallyExpectAddressSetWithIPs(asIndex, nil)
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("can add/remove Pod IPs to/from address set when pods label changes", func() {
			pbr := newAdminPBR(policyName, nextHop)
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.startWithDBSetup(
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalRouter{
								Name: "ovn.primary_" + ovntypes.OVNClusterRouter,
								UUID: "ovn.primary_" + ovntypes.OVNClusterRouter + "-UUID",
							},
						},
					},
					&nettypes.NetworkAttachmentDefinitionList{
						Items: []nettypes.NetworkAttachmentDefinition{*nad},
					},
					&adminpbrapi.AdminPolicyBasedRouteList{
						Items: []adminpbrapi.AdminPolicyBasedRoute{
							*pbr,
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							*newNamespaceWithLabels(adminPBRNamespace, map[string]string{}),
						},
					},
					&v1.NodeList{
						Items: []v1.Node{
							*newNodeWithLabels(node1Name, node1IP, map[string]string{"ngn2.nvidia.com/igw_vip": "G"}),
							*newNodeWithLabels(node2Name, node2IP, map[string]string{"ngn2.nvidia.com/igw_vip": "G"}),
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPodWithLabels(adminPBRNamespace, pod1Name, node1Name, pod1IP, map[string]string{"k8s.io/app": app1Name}, node1IP),
							*newPodWithLabels(adminPBRNamespace, pod2Name, node2Name, pod2IP, map[string]string{"k8s.io/app": app2Name}, node2IP),
						},
					},
				)
				ocInfo := fakeOvn.secondaryControllers["ovn-primary"]
				gomega.Expect(ocInfo).ToNot(gomega.BeNil())
				err := ocInfo.bnc.WatchAdminPolicyBasedRoutes()
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				var asIndex *libovsdbops.DbObjectIDs
				gomega.Eventually(func() []string {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
						return lrp.ExternalIDs[ovntypes.ExternalIDK8sOwner] == util.NamespacedName(pbr)
					}).List(context.TODO(), &policies)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(policies)).To(gomega.Equal(1))
					asIndex = getAdminPBRAddrSetDbIDs(pbr.Name, policies[0].ExternalIDs[ovntypes.ExternalIDHash], ocInfo.bnc.controllerName)
					return policies[0].Nexthops
				}).Should(gomega.ContainElement(nextHop))
				ocInfo.asf.EventuallyExpectAddressSet(asIndex)
				ocInfo.asf.ExpectAddressSetWithIPs(asIndex, []string{pod1IP})
				podDelta := newPodWithLabels(adminPBRNamespace, pod1Name, node1Name, pod1IP, map[string]string{"k8s.io/app": "something_else"}, node1IP)
				_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(adminPBRNamespace).Update(context.TODO(), podDelta, metav1.UpdateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				podDelta = newPodWithLabels(adminPBRNamespace, pod2Name, node2Name, pod2IP, map[string]string{"k8s.io/app": app1Name}, node2IP)
				_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(adminPBRNamespace).Update(context.TODO(), podDelta, metav1.UpdateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				ocInfo.asf.EventuallyExpectAddressSetWithIPs(asIndex, []string{pod2IP})
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("will retry if ovn operation fails", func() {
			pbr := newAdminPBR(policyName, nextHop)
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.startWithDBSetup(libovsdbtest.TestSetup{},
					&nettypes.NetworkAttachmentDefinitionList{
						Items: []nettypes.NetworkAttachmentDefinition{*nad},
					},
					&adminpbrapi.AdminPolicyBasedRouteList{
						Items: []adminpbrapi.AdminPolicyBasedRoute{
							*pbr,
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							*newNamespaceWithLabels(adminPBRNamespace, map[string]string{}),
						},
					},
					&v1.NodeList{
						Items: []v1.Node{
							*newNodeWithLabels(node1Name, node1IP, map[string]string{"ngn2.nvidia.com/igw_vip": "G"}),
							*newNodeWithLabels(node2Name, node2IP, map[string]string{"ngn2.nvidia.com/igw_vip": "G"}),
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPodWithLabels(adminPBRNamespace, pod1Name, node1Name, pod1IP, map[string]string{"k8s.io/app": app1Name}, node1IP),
							*newPodWithLabels(adminPBRNamespace, pod2Name, node2Name, pod2IP, map[string]string{"k8s.io/app": app2Name}, node2IP),
						},
					},
				)
				ocInfo := fakeOvn.secondaryControllers["ovn-primary"]
				gomega.Expect(ocInfo).ToNot(gomega.BeNil())
				err := ocInfo.bnc.WatchAdminPolicyBasedRoutes()
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Eventually(func() []nbdb.LogicalRouterPolicy {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
						return lrp.ExternalIDs[ovntypes.ExternalIDK8sOwner] == util.NamespacedName(pbr)
					}).List(context.TODO(), &policies)
					gomega.Expect(err).To(gomega.BeNil())
					return policies
				}, 15*time.Second).Should(gomega.BeEmpty())
				ops, err := fakeOvn.nbClient.Create(&nbdb.LogicalRouter{
					Name: "ovn.primary_" + ovntypes.OVNClusterRouter,
					UUID: "ovn.primary_" + ovntypes.OVNClusterRouter + "-UUID",
				})
				gomega.Expect(err).To(gomega.BeNil())
				_, err = fakeOvn.nbClient.Transact(context.TODO(), ops...)
				gomega.Expect(err).To(gomega.BeNil())
				gomega.Eventually(func() []nbdb.LogicalRouterPolicy {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
						return lrp.ExternalIDs[ovntypes.ExternalIDK8sOwner] == util.NamespacedName(pbr)
					}).List(context.TODO(), &policies)
					gomega.Expect(err).To(gomega.BeNil())
					return policies
				}, 60*time.Second).Should(gomega.HaveLen(1))
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

	})
})

func newAdminPBR(name, nextHop string) *adminpbrapi.AdminPolicyBasedRoute {
	return &adminpbrapi.AdminPolicyBasedRoute{
		TypeMeta: metav1.TypeMeta{
			Kind:       "AdminPolicyBasedRoute",
			APIVersion: "k8s.ovn.org/v1beta1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: adminpbrapi.AdminPolicyBasedRouteSpec{
			NetworkAttachmentName: "default/ovn-primary",
			Policies: []adminpbrapi.RoutingPolicyRule{
				{
					From: adminpbrapi.RoutingPolicyMatch{
						NodeSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{
								"ngn2.nvidia.com/igw_vip": "G",
							},
						},
						NamespaceSelector: metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      "ngn.nvidia.com/infrastructure",
									Operator: metav1.LabelSelectorOpDoesNotExist,
								},
							},
						},
						PodSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{
								"k8s.io/app": app1Name,
							},
						},
					},
					NextHop: adminpbrapi.RoutingPolicyNextHop{
						NextHopIPs: []string{nextHop},
					},
					To: netv1.IPBlock{
						CIDR: "8.8.8.8",
					},
				},
			},
		},
	}
}

func newNodeWithLabels(nodeName, nodeIP string, labels map[string]string) *v1.Node {
	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodeName,
			Labels: map[string]string{},
		},
		Status: v1.NodeStatus{
			Addresses: []v1.NodeAddress{
				{
					Type:    v1.NodeInternalIP,
					Address: nodeIP,
				},
			},
			Conditions: []v1.NodeCondition{
				{
					Type:   v1.NodeReady,
					Status: v1.ConditionTrue,
				},
			},
		},
	}
	for k, v := range labels {
		node.Labels[k] = v
	}
	return node
}
