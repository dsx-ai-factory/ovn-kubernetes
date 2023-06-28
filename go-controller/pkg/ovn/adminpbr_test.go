package ovn

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	"github.com/urfave/cli/v2"
	v1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	adminpbrapi "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/adminpbr/v1beta1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	addrset "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/address_set"
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
		fakeOvn = NewFakeOVN()
	})
	ginkgo.AfterEach(func() {
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
								Name: ovntypes.OVNClusterRouter,
								UUID: ovntypes.OVNClusterRouter + "-UUID",
							},
						},
					},
					&adminpbrapi.AdminPolicyBasedRouteList{
						Items: []adminpbrapi.AdminPolicyBasedRoute{
							*pbr,
						},
					},
				)
				fakeOvn.controller.nadInfo.TopoType = ovntypes.Layer3AttachDefTopoType
				fakeOvn.controller.WatchAdminPolicyBasedRoutes()
				gomega.Eventually(fakeOvn.controller).ShouldNot(gomega.BeNil())
				addressSetName := ""
				gomega.Eventually(func() []string {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.controller.mc.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
						return lrp.ExternalIDs[ovntypes.ExternalIDK8sOwner] == util.NamespacedName(pbr)
					}).List(context.TODO(), &policies)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(policies)).To(gomega.Equal(1))
					addressSetName = fmt.Sprintf("%s-%s", pbr.Name, policies[0].ExternalIDs[ovntypes.ExternalIDHash])
					return policies[0].Nexthops
				}).Should(gomega.ContainElement(nextHop))
				gomega.Expect(addressSetName).NotTo(gomega.BeEmpty())
				v4AddressSetName, _ := addrset.MakeAddressSetName(addressSetName)
				fakeOvn.asf.ExpectAddressSetExist(v4AddressSetName)
				err := fakeOvn.fakeClient.AdminPBRClient.K8sV1beta1().AdminPolicyBasedRoutes().Delete(context.TODO(), policyName, metav1.DeleteOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Eventually(func() []nbdb.LogicalRouterPolicy {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.controller.mc.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
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
								Name: ovntypes.OVNClusterRouter,
								UUID: ovntypes.OVNClusterRouter + "-UUID",
							},
						},
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
				fakeOvn.controller.nadInfo.TopoType = ovntypes.Layer3AttachDefTopoType
				fakeOvn.controller.WatchAdminPolicyBasedRoutes()
				gomega.Eventually(fakeOvn.controller).ShouldNot(gomega.BeNil())
				addressSetName := ""
				gomega.Eventually(func() []string {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.controller.mc.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
						return lrp.ExternalIDs[ovntypes.ExternalIDK8sOwner] == util.NamespacedName(pbr)
					}).List(context.TODO(), &policies)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(policies)).To(gomega.Equal(1))
					addressSetName = fmt.Sprintf("%s-%s", pbr.Name, policies[0].ExternalIDs[ovntypes.ExternalIDHash])
					return policies[0].Nexthops
				}).Should(gomega.ContainElement(nextHop))
				gomega.Expect(addressSetName).NotTo(gomega.BeEmpty())
				v4AddressSetName, _ := addrset.MakeAddressSetName(addressSetName)
				fakeOvn.asf.ExpectAddressSetExist(v4AddressSetName)
				fakeOvn.asf.ExpectAddressSetWithIPs(addressSetName, []string{pod1IP})
				err := fakeOvn.fakeClient.KubeClient.CoreV1().Pods(adminPBRNamespace).Delete(context.TODO(), pod1Name, metav1.DeleteOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				fakeOvn.asf.EventuallyExpectEmptyAddressSetExist(addressSetName)
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
								Name: ovntypes.OVNClusterRouter,
								UUID: ovntypes.OVNClusterRouter + "-UUID",
							},
						},
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
				fakeOvn.controller.nadInfo.TopoType = ovntypes.Layer3AttachDefTopoType
				fakeOvn.controller.WatchAdminPolicyBasedRoutes()
				gomega.Eventually(fakeOvn.controller).ShouldNot(gomega.BeNil())
				addressSetName := ""
				gomega.Eventually(func() []string {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.controller.mc.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
						return lrp.ExternalIDs[ovntypes.ExternalIDK8sOwner] == util.NamespacedName(pbr)
					}).List(context.TODO(), &policies)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(policies)).To(gomega.Equal(1))
					addressSetName = fmt.Sprintf("%s-%s", pbr.Name, policies[0].ExternalIDs[ovntypes.ExternalIDHash])
					return policies[0].Nexthops
				}).Should(gomega.ContainElement(nextHop))
				gomega.Expect(addressSetName).NotTo(gomega.BeEmpty())
				v4AddressSetName, _ := addrset.MakeAddressSetName(addressSetName)
				fakeOvn.asf.ExpectAddressSetExist(v4AddressSetName)
				fakeOvn.asf.ExpectAddressSetWithIPs(addressSetName, []string{pod1IP})
				nodeDelta := newNodeWithLabels(node1Name, node1IP, map[string]string{"ngn2.nvidia.com/igw_vip": "H"})
				_, err := fakeOvn.fakeClient.KubeClient.CoreV1().Nodes().Update(context.TODO(), nodeDelta, metav1.UpdateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				fakeOvn.asf.EventuallyExpectAddressSetWithIPs(addressSetName, nil)
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
								Name: ovntypes.OVNClusterRouter,
								UUID: ovntypes.OVNClusterRouter + "-UUID",
							},
						},
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
				fakeOvn.controller.nadInfo.TopoType = ovntypes.Layer3AttachDefTopoType
				fakeOvn.controller.WatchAdminPolicyBasedRoutes()
				gomega.Eventually(fakeOvn.controller).ShouldNot(gomega.BeNil())
				addressSetName := ""
				gomega.Eventually(func() []string {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.controller.mc.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
						return lrp.ExternalIDs[ovntypes.ExternalIDK8sOwner] == util.NamespacedName(pbr)
					}).List(context.TODO(), &policies)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(policies)).To(gomega.Equal(1))
					addressSetName = fmt.Sprintf("%s-%s", pbr.Name, policies[0].ExternalIDs[ovntypes.ExternalIDHash])
					return policies[0].Nexthops
				}).Should(gomega.ContainElement(nextHop))
				gomega.Expect(addressSetName).NotTo(gomega.BeEmpty())
				v4AddressSetName, _ := addrset.MakeAddressSetName(addressSetName)
				fakeOvn.asf.ExpectAddressSetExist(v4AddressSetName)
				fakeOvn.asf.ExpectAddressSetWithIPs(addressSetName, []string{pod1IP})
				nsDelta := newNamespaceWithLabels(adminPBRNamespace, map[string]string{
					"ngn.nvidia.com/infrastructure": "",
				})
				_, err := fakeOvn.fakeClient.KubeClient.CoreV1().Namespaces().Update(context.TODO(), nsDelta, metav1.UpdateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				fakeOvn.asf.EventuallyExpectAddressSetWithIPs(addressSetName, nil)
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
								Name: ovntypes.OVNClusterRouter,
								UUID: ovntypes.OVNClusterRouter + "-UUID",
							},
						},
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
				fakeOvn.controller.nadInfo.TopoType = ovntypes.Layer3AttachDefTopoType
				fakeOvn.controller.WatchAdminPolicyBasedRoutes()
				gomega.Eventually(fakeOvn.controller).ShouldNot(gomega.BeNil())
				addressSetName := ""
				gomega.Eventually(func() []string {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.controller.mc.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
						return lrp.ExternalIDs[ovntypes.ExternalIDK8sOwner] == util.NamespacedName(pbr)
					}).List(context.TODO(), &policies)
					gomega.Expect(err).To(gomega.BeNil())
					gomega.Expect(len(policies)).To(gomega.Equal(1))
					addressSetName = fmt.Sprintf("%s-%s", pbr.Name, policies[0].ExternalIDs[ovntypes.ExternalIDHash])
					return policies[0].Nexthops
				}).Should(gomega.ContainElement(nextHop))
				gomega.Expect(addressSetName).NotTo(gomega.BeEmpty())
				v4AddressSetName, _ := addrset.MakeAddressSetName(addressSetName)
				fakeOvn.asf.ExpectAddressSetExist(v4AddressSetName)
				fakeOvn.asf.ExpectAddressSetWithIPs(addressSetName, []string{pod1IP})
				podDelta := newPodWithLabels(adminPBRNamespace, pod1Name, node1Name, pod1IP, map[string]string{"k8s.io/app": "something_else"}, node1IP)
				_, err := fakeOvn.fakeClient.KubeClient.CoreV1().Pods(adminPBRNamespace).Update(context.TODO(), podDelta, metav1.UpdateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				podDelta = newPodWithLabels(adminPBRNamespace, pod2Name, node2Name, pod2IP, map[string]string{"k8s.io/app": app1Name}, node2IP)
				_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(adminPBRNamespace).Update(context.TODO(), podDelta, metav1.UpdateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				fakeOvn.asf.EventuallyExpectAddressSetWithIPs(addressSetName, []string{pod2IP})
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("will retry if ovn operation fails", func() {
			pbr := newAdminPBR(policyName, nextHop)
			app.Action = func(ctx *cli.Context) error {
				fakeOvn.startWithDBSetup(libovsdbtest.TestSetup{},
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
				// set to secondary to bypass OVN operations which rely on logical router which is absent at this point
				fakeOvn.controller.nadInfo.IsSecondary = true
				fakeOvn.controller.nadInfo.TopoType = ovntypes.Layer3AttachDefTopoType
				fakeOvn.controller.WatchAdminPolicyBasedRoutes()
				gomega.Eventually(fakeOvn.controller).ShouldNot(gomega.BeNil())
				gomega.Eventually(func() []nbdb.LogicalRouterPolicy {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.controller.mc.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
						return lrp.ExternalIDs[ovntypes.ExternalIDK8sOwner] == util.NamespacedName(pbr)
					}).List(context.TODO(), &policies)
					gomega.Expect(err).To(gomega.BeNil())
					return policies
				}, 15*time.Second).Should(gomega.BeEmpty())
				ops, err := fakeOvn.nbClient.Create(&nbdb.LogicalRouter{
					Name: ovntypes.OVNClusterRouter,
					UUID: ovntypes.OVNClusterRouter + "-UUID",
				})
				gomega.Expect(err).To(gomega.BeNil())
				_, err = fakeOvn.nbClient.Transact(context.TODO(), ops...)
				gomega.Expect(err).To(gomega.BeNil())
				ginkgo.By(fmt.Sprintf("Cathy expect 1 LogicalRouterPolicy of %s external-ids to be %s", ovntypes.ExternalIDK8sOwner, util.NamespacedName(pbr)))
				gomega.Eventually(func() []nbdb.LogicalRouterPolicy {
					policies := []nbdb.LogicalRouterPolicy{}
					err := fakeOvn.controller.mc.nbClient.WhereCache(func(lrp *nbdb.LogicalRouterPolicy) bool {
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
