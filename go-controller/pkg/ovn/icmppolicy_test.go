package ovn

import (
	"context"
	"fmt"
	"strconv"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	addressset "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/address_set"
	ovntest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	util "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/urfave/cli/v2"

	icmpnetworkpolicyapi "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/icmpnetworkpolicy/v1alpha1"
	v1 "k8s.io/api/core/v1"
	knet "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apimachinerytypes "k8s.io/apimachinery/pkg/types"
)

type icmpNetworkPolicy struct{}

func newICMPNetworkPolicyMeta(name, namespace string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		UID:       apimachinerytypes.UID(namespace),
		Name:      name,
		Namespace: namespace,
		Labels: map[string]string{
			"name": name,
		},
	}
}

func newICMPNetworkPolicy(name, namespace string, podSelector metav1.LabelSelector, ingress []icmpnetworkpolicyapi.NetworkPolicyIngressRule, egress []icmpnetworkpolicyapi.NetworkPolicyEgressRule) *icmpnetworkpolicyapi.ICMPNetworkPolicy {
	return &icmpnetworkpolicyapi.ICMPNetworkPolicy{
		ObjectMeta: newICMPNetworkPolicyMeta(name, namespace),
		Spec: icmpnetworkpolicyapi.ICMPNetworkPolicySpec{
			PodSelector: podSelector,
			Ingress:     ingress,
			Egress:      egress,
		},
	}
}

func (n icmpNetworkPolicy) baseCmds(fexec *ovntest.FakeExec, icmpNetworkPolicy *icmpnetworkpolicyapi.ICMPNetworkPolicy) string {
	readableGroupName := fmt.Sprintf("%s_icmp_%s", icmpNetworkPolicy.Namespace, icmpNetworkPolicy.Name)
	return readableGroupName
}

func (n icmpNetworkPolicy) addDefaultDenyPGCmds(fexec *ovntest.FakeExec, icmpnetworkPolicy *icmpnetworkpolicyapi.ICMPNetworkPolicy) {
	pg_hash := hashedPortGroup(icmpnetworkPolicy.Namespace)
	fexec.AddFakeCmd(&ovntest.ExpectedCmd{
		Cmd:    "ovn-nbctl --timeout=15 --data=bare --no-heading --columns=_uuid find ACL match=\"outport == @" + pg_hash + "_" + ingressDenyPG + "\" action=drop external-ids:default-deny-policy-type=Ingress external_ids:network_name{=}[]",
		Output: fakeUUID,
	})
	fexec.AddFakeCmd(&ovntest.ExpectedCmd{
		Cmd:    "ovn-nbctl --timeout=15 --data=bare --no-heading --columns=_uuid find ACL match=\"outport == @" + pg_hash + "_" + ingressDenyPG + " && arp\" action=allow external-ids:default-deny-policy-type=Ingress external_ids:network_name{=}[]",
		Output: fakeUUID,
	})
	fexec.AddFakeCmd(&ovntest.ExpectedCmd{
		Cmd:    "ovn-nbctl --timeout=15 --data=bare --no-heading --columns=_uuid find ACL match=\"inport == @" + pg_hash + "_" + egressDenyPG + "\" action=drop external-ids:default-deny-policy-type=Egress external_ids:network_name{=}[]",
		Output: fakeUUID,
	})
	fexec.AddFakeCmd(&ovntest.ExpectedCmd{
		Cmd:    "ovn-nbctl --timeout=15 --data=bare --no-heading --columns=_uuid find ACL match=\"inport == @" + pg_hash + "_" + egressDenyPG + " && arp\" action=allow external-ids:default-deny-policy-type=Egress external_ids:network_name{=}[]",
		Output: fakeUUID,
	})
}

func (n icmpNetworkPolicy) addLocalPodCmds(fexec *ovntest.FakeExec, icmpnetworkPolicy *icmpnetworkpolicyapi.ICMPNetworkPolicy) {
	n.addDefaultDenyPGCmds(fexec, icmpnetworkPolicy)
}

func (n icmpNetworkPolicy) addNamespaceSelectorCmds(fexec *ovntest.FakeExec, icmpNetworkPolicy *icmpnetworkpolicyapi.ICMPNetworkPolicy, namespace string) {
	as := []string{}
	if namespace != "" {
		as = append(as, namespace)
	}

	for i := range icmpNetworkPolicy.Spec.Ingress {
		ingressAsMatch := asMatch(append(as, getAddressSetName(icmpNetworkPolicy.Namespace, "icmp_"+icmpNetworkPolicy.Name, knet.PolicyTypeIngress, i)))
		fexec.AddFakeCmdsNoOutputNoError([]string{
			fmt.Sprintf("ovn-nbctl --timeout=15 --data=bare --no-heading --columns=_uuid find ACL external-ids:l4Match=\"None\" external-ids:ipblock_cidr=false external-ids:namespace=%s external-ids:policy=icmp_%s external-ids:Ingress_num=%v external-ids:policy_type=Ingress external_ids:network_name{=}[]", icmpNetworkPolicy.Namespace, icmpNetworkPolicy.Name, i),
			"ovn-nbctl --timeout=15 --id=@acl create acl priority=" + types.DefaultAllowPriority + " direction=" + types.DirectionToLPort + " match=\"ip4.src == {" + ingressAsMatch + "} && outport == @a13918434151227952593\" action=allow-related log=false severity=info meter=acl-logging name=" + icmpNetworkPolicy.Namespace + "_" + "icmp_" + icmpNetworkPolicy.Name + "_" + strconv.Itoa(i) + " external-ids:l4Match=\"None\" external-ids:ipblock_cidr=false external-ids:namespace=namespace1 external-ids:policy=icmp_networkpolicy1 external-ids:Ingress_num=0 external-ids:policy_type=Ingress -- add port_group " + fakePgUUID + " acls @acl",
		})
	}
	for i := range icmpNetworkPolicy.Spec.Egress {
		egressAsMatch := asMatch(append(as, getAddressSetName(icmpNetworkPolicy.Namespace, "icmp_"+icmpNetworkPolicy.Name, knet.PolicyTypeEgress, i)))
		fexec.AddFakeCmdsNoOutputNoError([]string{
			fmt.Sprintf("ovn-nbctl --timeout=15 --data=bare --no-heading --columns=_uuid find ACL external-ids:l4Match=\"None\" external-ids:ipblock_cidr=false external-ids:namespace=%s external-ids:policy=icmp_%s external-ids:Egress_num=%v external-ids:policy_type=Egress external_ids:network_name{=}[]", icmpNetworkPolicy.Namespace, icmpNetworkPolicy.Name, i),
			"ovn-nbctl --timeout=15 --id=@acl create acl priority=" + types.DefaultAllowPriority + " direction=" + types.DirectionToLPort + " match=\"ip4.dst == {" + egressAsMatch + "} && inport == @a13918434151227952593\" action=allow-related log=false severity=info meter=acl-logging name=" + icmpNetworkPolicy.Namespace + "_" + "icmp_" + icmpNetworkPolicy.Name + "_" + strconv.Itoa(i) + " external-ids:l4Match=\"None\" external-ids:ipblock_cidr=false external-ids:namespace=namespace1 external-ids:policy=icmp_networkpolicy1 external-ids:Egress_num=0 external-ids:policy_type=Egress -- add port_group " + fakePgUUID + " acls @acl",
		})
	}
}

func (n icmpNetworkPolicy) addNamespaceSelectorCmdsExistingAcl(fexec *ovntest.FakeExec, icmpNetworkPolicy *icmpnetworkpolicyapi.ICMPNetworkPolicy, namespace string) {
	for i := range icmpNetworkPolicy.Spec.Ingress {
		ingressAsMatch := asMatch([]string{
			namespace,
			getAddressSetName(icmpNetworkPolicy.Namespace, "icmp_"+icmpNetworkPolicy.Name, knet.PolicyTypeIngress, i),
		})
		fexec.AddFakeCmd(&ovntest.ExpectedCmd{
			Cmd:    fmt.Sprintf("ovn-nbctl --timeout=15 --data=bare --no-heading --columns=_uuid find ACL external-ids:l4Match=\"None\" external-ids:ipblock_cidr=false external-ids:namespace=%s external-ids:policy=icmp_%s external-ids:Ingress_num=%v external-ids:policy_type=Ingress external_ids:network_name{=}[]", icmpNetworkPolicy.Namespace, icmpNetworkPolicy.Name, i),
			Output: fakeUUID,
		})
		fexec.AddFakeCmdsNoOutputNoError([]string{
			"ovn-nbctl --timeout=15 set acl " + fakeUUID + " match=\"ip4.src == {" + ingressAsMatch + "} && outport == @a13918434151227952593\" priority=" + types.DefaultAllowPriority + " direction=" + types.DirectionToLPort + " action=allow-related log=false severity=info meter=acl-logging name=" + icmpNetworkPolicy.Namespace + "_" + "icmp_" + icmpNetworkPolicy.Name + "_" + strconv.Itoa(i),
		})
	}
	for i := range icmpNetworkPolicy.Spec.Egress {
		egressAsMatch := asMatch([]string{
			namespace,
			getAddressSetName(icmpNetworkPolicy.Namespace, "icmp_"+icmpNetworkPolicy.Name, knet.PolicyTypeEgress, i),
		})
		fexec.AddFakeCmd(&ovntest.ExpectedCmd{
			Cmd:    fmt.Sprintf("ovn-nbctl --timeout=15 --data=bare --no-heading --columns=_uuid find ACL external-ids:l4Match=\"None\" external-ids:ipblock_cidr=false external-ids:namespace=%s external-ids:policy=icmp_%s external-ids:Egress_num=%v external-ids:policy_type=Egress", icmpNetworkPolicy.Namespace, icmpNetworkPolicy.Name, i),
			Output: fakeUUID,
		})
		fexec.AddFakeCmdsNoOutputNoError([]string{
			"ovn-nbctl --timeout=15 set acl " + fakeUUID + " match=\"ip4.dst == {" + egressAsMatch + "} && inport == @a13918434151227952593\" priority=" + types.DefaultAllowPriority + " direction=" + types.DirectionToLPort + " action=allow-related log=false severity=info meter=acl-logging name=" + icmpNetworkPolicy.Namespace + "_" + "icmp_" + icmpNetworkPolicy.Name + "_" + strconv.Itoa(i),
		})
	}
}

func exteventuallyExpectNoAddressSets(fakeOvn *FakeOVN, icmpnetworkPolicy *icmpnetworkpolicyapi.ICMPNetworkPolicy) {
	policyName := "icmp_" + icmpnetworkPolicy.Name
	for i := range icmpnetworkPolicy.Spec.Ingress {
		asName := getAddressSetName(icmpnetworkPolicy.Namespace, policyName, knet.PolicyTypeIngress, i)
		fakeOvn.asf.EventuallyExpectNoAddressSet(asName)
	}
	for i := range icmpnetworkPolicy.Spec.Egress {
		asName := getAddressSetName(icmpnetworkPolicy.Namespace, policyName, knet.PolicyTypeEgress, i)
		fakeOvn.asf.EventuallyExpectNoAddressSet(asName)
	}
}

func extexpectAddressSetsWithIP(fakeOvn *FakeOVN, icmpnetworkPolicy *icmpnetworkpolicyapi.ICMPNetworkPolicy, ip string) {
	policyName := "icmp_" + icmpnetworkPolicy.Name
	for i := range icmpnetworkPolicy.Spec.Ingress {
		asName := getAddressSetName(icmpnetworkPolicy.Namespace, policyName, knet.PolicyTypeIngress, i)
		fakeOvn.asf.ExpectAddressSetWithIPs(asName, []string{ip})
	}
	for i := range icmpnetworkPolicy.Spec.Egress {
		asName := getAddressSetName(icmpnetworkPolicy.Namespace, policyName, knet.PolicyTypeEgress, i)
		fakeOvn.asf.ExpectAddressSetWithIPs(asName, []string{ip})
	}
}

func exteventuallyExpectEmptyAddressSets(fakeOvn *FakeOVN, icmpnetworkPolicy *icmpnetworkpolicyapi.ICMPNetworkPolicy) {
	policyName := "icmp_" + icmpnetworkPolicy.Name
	for i := range icmpnetworkPolicy.Spec.Ingress {
		asName := getAddressSetName(icmpnetworkPolicy.Namespace, policyName, knet.PolicyTypeIngress, i)
		fakeOvn.asf.EventuallyExpectEmptyAddressSet(asName)
	}
	for i := range icmpnetworkPolicy.Spec.Egress {
		asName := getAddressSetName(icmpnetworkPolicy.Namespace, policyName, knet.PolicyTypeEgress, i)
		fakeOvn.asf.EventuallyExpectEmptyAddressSet(asName)
	}
}

var _ = ginkgo.Describe("OVN Ext NetworkPolicy Operations", func() {
	const (
		namespaceName1 = "namespace1"
		namespaceName2 = "namespace2"
	)
	var (
		app     *cli.App
		fakeOvn *FakeOVN
		fExec   *ovntest.FakeExec
	)

	ginkgo.BeforeEach(func() {
		// Restore global default values before each testcase
		config.PrepareTestConfig()
		config.OVNKubernetesFeature.EnableICMPNetworkPolicy = true

		app = cli.NewApp()
		app.Name = "test"
		app.Flags = config.Flags

		fExec = ovntest.NewLooseCompareFakeExec()
		fakeOvn = NewFakeOVN(fExec)
		ovntest.ResetNumMockExecutions()
	})

	ginkgo.AfterEach(func() {
		fakeOvn.shutdown()
	})

	ginkgo.Context("during execution", func() {

		ginkgo.It("correctly creates and deletes a icmpnetworkpolicy allowing a port to a local pod", func() {
			app.Action = func(ctx *cli.Context) error {
				npTest := icmpNetworkPolicy{}

				namespace1 := *newNamespace(namespaceName1)
				nPodTest := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespace1.Name,
				)
				nPod := newPod(nPodTest.namespace, nPodTest.podName, nPodTest.nodeName, nPodTest.podIP)

				const (
					labelName string = "pod-name"
					labelVal  string = "server"
					icmpType  int32  = 8
					icmpType2 int32  = 3
				)
				nPod.Labels[labelName] = labelVal

				icmpProtocol := "ICMP"
				icmpNetworkPolicy := newICMPNetworkPolicy("networkpolicy1", namespace1.Name,
					metav1.LabelSelector{
						MatchLabels: map[string]string{
							labelName: labelVal,
						},
					},
					[]icmpnetworkpolicyapi.NetworkPolicyIngressRule{{
						Protocols: []icmpnetworkpolicyapi.NetworkPolicyProtocol{{
							Type:     icmpType,
							Protocol: icmpProtocol,
						}},
					}},
					[]icmpnetworkpolicyapi.NetworkPolicyEgressRule{{
						Protocols: []icmpnetworkpolicyapi.NetworkPolicyProtocol{{
							Type:     icmpType,
							Protocol: icmpProtocol,
						}},
					}},
				)

				icmpNetworkPolicy2 := newICMPNetworkPolicy("networkpolicy2", namespace1.Name,
					metav1.LabelSelector{
						MatchLabels: map[string]string{
							labelName: labelVal,
						},
					},
					[]icmpnetworkpolicyapi.NetworkPolicyIngressRule{{
						Protocols: []icmpnetworkpolicyapi.NetworkPolicyProtocol{{
							Type:     icmpType2,
							Protocol: icmpProtocol,
						}},
					}},
					[]icmpnetworkpolicyapi.NetworkPolicyEgressRule{{
						Protocols: []icmpnetworkpolicyapi.NetworkPolicyProtocol{{
							Type:     icmpType2,
							Protocol: icmpProtocol,
						}},
					}},
				)

				nPodTest.baseCmds(fExec)
				npTest.baseCmds(fExec, icmpNetworkPolicy)
				npTest.addLocalPodCmds(fExec, icmpNetworkPolicy)

				//readableGroupName := fmt.Sprintf("%s_icmp_%s", icmpNetworkPolicy.Namespace, icmpNetworkPolicy.Name)
				fExec.AddFakeCmdsNoOutputNoError([]string{
					fmt.Sprintf("ovn-nbctl --timeout=15 --data=bare --no-heading --columns=_uuid find ACL external-ids:l4Match=\"icmp4 && icmp4.type == %d\" external-ids:ipblock_cidr=false external-ids:namespace=%s external-ids:policy=icmp_%s external-ids:Ingress_num=0 external-ids:policy_type=Ingress external_ids:network_name{=}[]", icmpType, icmpNetworkPolicy.Namespace, icmpNetworkPolicy.Name),
					fmt.Sprintf("ovn-nbctl --timeout=15 --id=@acl create acl priority="+types.DefaultAllowPriority+" direction="+types.DirectionToLPort+" match=\"ip4 && icmp4 && icmp4.type == %d && outport == @a13918434151227952593\" action=allow-related log=false severity=info meter=acl-logging name=%s_%s_0 external-ids:l4Match=\"icmp4 && icmp4.type == %d\" external-ids:ipblock_cidr=false external-ids:namespace=%s external-ids:policy=icmp_%s external-ids:Ingress_num=0 external-ids:policy_type=Ingress -- add port_group %s acls @acl", icmpType, icmpNetworkPolicy.Namespace, "icmp_"+icmpNetworkPolicy.Name, icmpType, icmpNetworkPolicy.Namespace, icmpNetworkPolicy.Name, fakePgUUID),
					fmt.Sprintf("ovn-nbctl --timeout=15 --data=bare --no-heading --columns=_uuid find ACL external-ids:l4Match=\"icmp4 && icmp4.type == %d\" external-ids:ipblock_cidr=false external-ids:namespace=%s external-ids:policy=icmp_%s external-ids:Egress_num=0 external-ids:policy_type=Egress external_ids:network_name{=}[]", icmpType, icmpNetworkPolicy.Namespace, icmpNetworkPolicy.Name),
					fmt.Sprintf("ovn-nbctl --timeout=15 --id=@acl create acl priority="+types.DefaultAllowPriority+" direction="+types.DirectionToLPort+" match=\"ip4 && icmp4 && icmp4.type == %d && inport == @a13918434151227952593\" action=allow-related log=false severity=info meter=acl-logging name=%s_%s_0 external-ids:l4Match=\"icmp4 && icmp4.type == %d\" external-ids:ipblock_cidr=false external-ids:namespace=%s external-ids:policy=icmp_%s external-ids:Egress_num=0 external-ids:policy_type=Egress -- add port_group %s acls @acl", icmpType, icmpNetworkPolicy.Namespace, "icmp_"+icmpNetworkPolicy.Name, icmpType, icmpNetworkPolicy.Namespace, icmpNetworkPolicy.Name, fakePgUUID),

					fmt.Sprintf("ovn-nbctl --timeout=15 --data=bare --no-heading --columns=_uuid find ACL external-ids:l4Match=\"icmp4 && icmp4.type == %d\" external-ids:ipblock_cidr=false external-ids:namespace=%s external-ids:policy=icmp_%s external-ids:Ingress_num=0 external-ids:policy_type=Ingress external_ids:network_name{=}[]", icmpType2, icmpNetworkPolicy2.Namespace, icmpNetworkPolicy2.Name),
					fmt.Sprintf("ovn-nbctl --timeout=15 --id=@acl create acl priority="+types.DefaultAllowPriority+" direction="+types.DirectionToLPort+" match=\"ip4 && icmp4 && icmp4.type == %d && outport == @a13918430852693067960\" action=allow-related log=false severity=info meter=acl-logging name=%s_%s_0 external-ids:l4Match=\"icmp4 && icmp4.type == %d\" external-ids:ipblock_cidr=false external-ids:namespace=%s external-ids:policy=icmp_%s external-ids:Ingress_num=0 external-ids:policy_type=Ingress -- add port_group %s acls @acl", icmpType2, icmpNetworkPolicy2.Namespace, "icmp_"+icmpNetworkPolicy2.Name, icmpType2, icmpNetworkPolicy2.Namespace, icmpNetworkPolicy2.Name, fakePgUUID),
					fmt.Sprintf("ovn-nbctl --timeout=15 --data=bare --no-heading --columns=_uuid find ACL external-ids:l4Match=\"icmp4 && icmp4.type == %d\" external-ids:ipblock_cidr=false external-ids:namespace=%s external-ids:policy=icmp_%s external-ids:Egress_num=0 external-ids:policy_type=Egress external_ids:network_name{=}[]", icmpType2, icmpNetworkPolicy2.Namespace, icmpNetworkPolicy2.Name),
					fmt.Sprintf("ovn-nbctl --timeout=15 --id=@acl create acl priority="+types.DefaultAllowPriority+" direction="+types.DirectionToLPort+" match=\"ip4 && icmp4 && icmp4.type == %d && inport == @a13918430852693067960\" action=allow-related log=false severity=info meter=acl-logging name=%s_%s_0 external-ids:l4Match=\"icmp4 && icmp4.type == %d\" external-ids:ipblock_cidr=false external-ids:namespace=%s external-ids:policy=icmp_%s external-ids:Egress_num=0 external-ids:policy_type=Egress -- add port_group %s acls @acl", icmpType2, icmpNetworkPolicy2.Namespace, "icmp_"+icmpNetworkPolicy2.Name, icmpType2, icmpNetworkPolicy2.Namespace, icmpNetworkPolicy2.Name, fakePgUUID),
				})

				fakeOvn.start(ctx,
					&v1.NamespaceList{
						Items: []v1.Namespace{namespace1},
					},
					&v1.PodList{
						Items: []v1.Pod{*nPod},
					},
					&icmpnetworkpolicyapi.ICMPNetworkPolicyList{
						Items: []icmpnetworkpolicyapi.ICMPNetworkPolicy{
							*icmpNetworkPolicy,
						},
					},
				)
				nPodTest.populateLogicalSwitchCache(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()
				fakeOvn.controller.WatchICMPNetworkPolicy()

				_, err := fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Get(context.TODO(), icmpNetworkPolicy.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				// gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
				fakeOvn.asf.ExpectAddressSetWithIPs(namespaceName1, []string{nPodTest.podIP})

				// assert that pod is in the default-deny portgroup

				// this helper function returns a function, because it's called behind
				// an
				getPGPorts := func(name string) func() ([]string, error) {
					return func() ([]string, error) {
						pg, err := fakeOvn.ovnNBClient.PortGroupGet(name)
						if err != nil {
							return nil, err
						}
						return pg.Ports, nil
					}
				}

				pgDefaultDenyName := defaultDenyPortGroup(namespace1.Name, "ingressDefaultDeny")
				gomega.Eventually(getPGPorts(pgDefaultDenyName)).Should(gomega.ConsistOf(fakeUUID))

				// assert that pod is in the NP's portgroup
				np1PG := hashedPortGroup(fmt.Sprintf("%s_icmp_%s", icmpNetworkPolicy.Namespace, icmpNetworkPolicy.Name))
				gomega.Eventually(getPGPorts(np1PG)).Should(gomega.ConsistOf(fakeUUID))
				fakeOvn.asf.ExpectAddressSetWithIPs(namespaceName1, []string{nPodTest.podIP})

				// Create a second NP
				ginkgo.By("Creating and deleting another policy that references that pod")

				_, err = fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Create(context.TODO(), icmpNetworkPolicy2, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				// Check that portgroups look sane
				np2PG := hashedPortGroup(fmt.Sprintf("%s_icmp_%s", icmpNetworkPolicy2.Namespace, icmpNetworkPolicy2.Name))
				gomega.Eventually(getPGPorts(pgDefaultDenyName)).Should(gomega.ConsistOf(fakeUUID))
				gomega.Eventually(getPGPorts(np2PG)).Should(gomega.ConsistOf(fakeUUID))

				// Delete the second network policy
				err = fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy2.Namespace).Delete(context.TODO(), icmpNetworkPolicy2.Name, metav1.DeleteOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				// Ensure the pod still has default deny
				gomega.Eventually(getPGPorts(pgDefaultDenyName)).Should(gomega.ConsistOf(fakeUUID))

				// Delete the first network policy
				ginkgo.By("Deleting that ICMPNetwork policy")
				err = fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Delete(context.TODO(), icmpNetworkPolicy.Name, metav1.DeleteOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				// Check that the default-deny portgroup is now deleted
				gomega.Eventually(func() error { _, err := getPGPorts(pgDefaultDenyName)(); return err }).Should(gomega.MatchError("object not found"))

				// fake exec checkup
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)

				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("reconciles a deleted namespace referenced by a icmpnetworkpolicy with a local running pod", func() {
			app.Action = func(ctx *cli.Context) error {

				npTest := icmpNetworkPolicy{}

				namespace1 := *newNamespace(namespaceName1)
				namespace2 := *newNamespace(namespaceName2)

				nPodTest := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespace1.Name,
				)

				icmpNetworkPolicy := newICMPNetworkPolicy("networkpolicy1", namespace1.Name,
					metav1.LabelSelector{},
					[]icmpnetworkpolicyapi.NetworkPolicyIngressRule{
						{
							From: []icmpnetworkpolicyapi.NetworkPolicyPeer{
								{
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": namespace2.Name,
										},
									},
								},
							},
						},
					},
					[]icmpnetworkpolicyapi.NetworkPolicyEgressRule{
						{
							To: []icmpnetworkpolicyapi.NetworkPolicyPeer{
								{
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": namespace2.Name,
										},
									},
								},
							},
						},
					})

				nPodTest.baseCmds(fExec)
				npTest.addNamespaceSelectorCmds(fExec, icmpNetworkPolicy, namespace2.Name)
				npTest.addLocalPodCmds(fExec, icmpNetworkPolicy)

				fakeOvn.start(ctx,
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespace1,
							namespace2,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPod(nPodTest.namespace, nPodTest.podName, nPodTest.nodeName, nPodTest.podIP),
						},
					},
					&icmpnetworkpolicyapi.ICMPNetworkPolicyList{
						Items: []icmpnetworkpolicyapi.ICMPNetworkPolicy{
							*icmpNetworkPolicy,
						},
					},
				)

				nPodTest.populateLogicalSwitchCache(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()
				fakeOvn.controller.WatchICMPNetworkPolicy()

				_, err := fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Get(context.TODO(), icmpNetworkPolicy.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
				fakeOvn.asf.ExpectAddressSetWithIPs(namespaceName1, []string{nPodTest.podIP})

				npTest.addNamespaceSelectorCmds(fExec, icmpNetworkPolicy, "")

				err = fakeOvn.fakeClient.KubeClient.CoreV1().Namespaces().Delete(context.TODO(), namespace2.Name, *metav1.NewDeleteOptions(0))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
				fakeOvn.asf.EventuallyExpectNoAddressSet(namespaceName2)

				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("reconciles a deleted namespace referenced by a icmpnetworkpolicy", func() {
			app.Action = func(ctx *cli.Context) error {

				npTest := icmpNetworkPolicy{}

				namespace1 := *newNamespace(namespaceName1)
				namespace2 := *newNamespace(namespaceName2)
				icmpNetworkPolicy := newICMPNetworkPolicy("networkpolicy1", namespace1.Name,
					metav1.LabelSelector{},
					[]icmpnetworkpolicyapi.NetworkPolicyIngressRule{
						{
							From: []icmpnetworkpolicyapi.NetworkPolicyPeer{
								{
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": namespace2.Name,
										},
									},
								},
							},
						},
					},
					[]icmpnetworkpolicyapi.NetworkPolicyEgressRule{
						{
							To: []icmpnetworkpolicyapi.NetworkPolicyPeer{
								{
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": namespace2.Name,
										},
									},
								},
							},
						},
					})

				npTest.addNamespaceSelectorCmds(fExec, icmpNetworkPolicy, namespace2.Name)
				npTest.addDefaultDenyPGCmds(fExec, icmpNetworkPolicy)

				fakeOvn.start(ctx,
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespace1,
							namespace2,
						},
					},
					&icmpnetworkpolicyapi.ICMPNetworkPolicyList{
						Items: []icmpnetworkpolicyapi.ICMPNetworkPolicy{
							*icmpNetworkPolicy,
						},
					},
				)

				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchICMPNetworkPolicy()

				_, err := fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Get(context.TODO(), icmpNetworkPolicy.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)

				npTest.addNamespaceSelectorCmds(fExec, icmpNetworkPolicy, "")

				err = fakeOvn.fakeClient.KubeClient.CoreV1().Namespaces().Delete(context.TODO(), namespace2.Name, *metav1.NewDeleteOptions(0))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("reconciles a deleted pod referenced by a icmpnetworkpolicy in its own namespace", func() {
			app.Action = func(ctx *cli.Context) error {

				npTest := icmpNetworkPolicy{}

				namespace1 := *newNamespace(namespaceName1)

				nPodTest := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespace1.Name,
				)
				icmpNetworkPolicy := newICMPNetworkPolicy("networkpolicy1", namespace1.Name,
					metav1.LabelSelector{},
					[]icmpnetworkpolicyapi.NetworkPolicyIngressRule{
						{
							From: []icmpnetworkpolicyapi.NetworkPolicyPeer{
								{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": nPodTest.podName,
										},
									},
								},
							},
						},
					},
					[]icmpnetworkpolicyapi.NetworkPolicyEgressRule{
						{
							To: []icmpnetworkpolicyapi.NetworkPolicyPeer{
								{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": nPodTest.podName,
										},
									},
								},
							},
						},
					})

				nPodTest.baseCmds(fExec)
				npTest.addNamespaceSelectorCmds(fExec, icmpNetworkPolicy, "")
				npTest.addLocalPodCmds(fExec, icmpNetworkPolicy)

				fakeOvn.start(ctx,
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespace1,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPod(nPodTest.namespace, nPodTest.podName, nPodTest.nodeName, nPodTest.podIP),
						},
					},
					&icmpnetworkpolicyapi.ICMPNetworkPolicyList{
						Items: []icmpnetworkpolicyapi.ICMPNetworkPolicy{
							*icmpNetworkPolicy,
						},
					},
				)

				nPodTest.populateLogicalSwitchCache(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()
				fakeOvn.controller.WatchICMPNetworkPolicy()

				extexpectAddressSetsWithIP(fakeOvn, icmpNetworkPolicy, nPodTest.podIP)
				fakeOvn.asf.ExpectAddressSetWithIPs(namespaceName1, []string{nPodTest.podIP})

				_, err := fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Get(context.TODO(), icmpNetworkPolicy.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)

				err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(nPodTest.namespace).Delete(context.TODO(), nPodTest.podName, *metav1.NewDeleteOptions(0))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)

				exteventuallyExpectEmptyAddressSets(fakeOvn, icmpNetworkPolicy)
				fakeOvn.asf.EventuallyExpectEmptyAddressSet(namespaceName1)
				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("reconciles a deleted pod referenced by a icmpnetworkpolicy in another namespace", func() {
			app.Action = func(ctx *cli.Context) error {

				npTest := icmpNetworkPolicy{}

				namespace1 := *newNamespace(namespaceName1)
				namespace2 := *newNamespace(namespaceName2)

				nPodTest := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespace2.Name,
				)
				icmpNetworkPolicy := newICMPNetworkPolicy("networkpolicy1", namespace1.Name,
					metav1.LabelSelector{},
					[]icmpnetworkpolicyapi.NetworkPolicyIngressRule{
						{
							From: []icmpnetworkpolicyapi.NetworkPolicyPeer{
								{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": nPodTest.podName,
										},
									},
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": nPodTest.namespace,
										},
									},
								},
							},
						},
					},
					[]icmpnetworkpolicyapi.NetworkPolicyEgressRule{
						{
							To: []icmpnetworkpolicyapi.NetworkPolicyPeer{
								{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": nPodTest.podName,
										},
									},
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": nPodTest.namespace,
										},
									},
								},
							},
						},
					})

				nPodTest.baseCmds(fExec)
				npTest.addNamespaceSelectorCmds(fExec, icmpNetworkPolicy, "")
				npTest.addDefaultDenyPGCmds(fExec, icmpNetworkPolicy)

				fakeOvn.start(ctx,
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespace1,
							namespace2,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPod(nPodTest.namespace, nPodTest.podName, nPodTest.nodeName, nPodTest.podIP),
						},
					},
					&icmpnetworkpolicyapi.ICMPNetworkPolicyList{
						Items: []icmpnetworkpolicyapi.ICMPNetworkPolicy{
							*icmpNetworkPolicy,
						},
					},
				)

				nPodTest.populateLogicalSwitchCache(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()
				fakeOvn.controller.WatchICMPNetworkPolicy()

				fakeOvn.asf.ExpectEmptyAddressSet(namespaceName1)
				extexpectAddressSetsWithIP(fakeOvn, icmpNetworkPolicy, nPodTest.podIP)
				fakeOvn.asf.ExpectAddressSetWithIPs(namespaceName2, []string{nPodTest.podIP})

				_, err := fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Get(context.TODO(), icmpNetworkPolicy.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)

				err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(nPodTest.namespace).Delete(context.TODO(), nPodTest.podName, *metav1.NewDeleteOptions(0))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)

				// After deleting the pod all address sets should be empty
				exteventuallyExpectEmptyAddressSets(fakeOvn, icmpNetworkPolicy)
				fakeOvn.asf.EventuallyExpectEmptyAddressSet(namespaceName1)

				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("reconciles an updated namespace label", func() {
			app.Action = func(ctx *cli.Context) error {

				npTest := icmpNetworkPolicy{}

				namespace1 := *newNamespace(namespaceName1)
				namespace2 := *newNamespace(namespaceName2)

				nPodTest := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespace2.Name,
				)
				icmpNetworkPolicy := newICMPNetworkPolicy("networkpolicy1", namespace1.Name,
					metav1.LabelSelector{},
					[]icmpnetworkpolicyapi.NetworkPolicyIngressRule{
						{
							From: []icmpnetworkpolicyapi.NetworkPolicyPeer{
								{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": nPodTest.podName,
										},
									},
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": nPodTest.namespace,
										},
									},
								},
							},
						},
					},
					[]icmpnetworkpolicyapi.NetworkPolicyEgressRule{
						{
							To: []icmpnetworkpolicyapi.NetworkPolicyPeer{
								{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": nPodTest.podName,
										},
									},
									NamespaceSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": nPodTest.namespace,
										},
									},
								},
							},
						},
					})

				nPodTest.baseCmds(fExec)
				npTest.addNamespaceSelectorCmds(fExec, icmpNetworkPolicy, "")
				npTest.addDefaultDenyPGCmds(fExec, icmpNetworkPolicy)

				fakeOvn.start(ctx,
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespace1,
							namespace2,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPod(nPodTest.namespace, nPodTest.podName, nPodTest.nodeName, nPodTest.podIP),
						},
					},
					&icmpnetworkpolicyapi.ICMPNetworkPolicyList{
						Items: []icmpnetworkpolicyapi.ICMPNetworkPolicy{
							*icmpNetworkPolicy,
						},
					},
				)
				nPodTest.populateLogicalSwitchCache(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()
				fakeOvn.controller.WatchICMPNetworkPolicy()

				fakeOvn.asf.ExpectEmptyAddressSet(namespaceName1)
				extexpectAddressSetsWithIP(fakeOvn, icmpNetworkPolicy, nPodTest.podIP)
				fakeOvn.asf.ExpectAddressSetWithIPs(namespaceName2, []string{nPodTest.podIP})

				_, err := fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Get(context.TODO(), icmpNetworkPolicy.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)

				namespace2.ObjectMeta.Labels = map[string]string{"labels": "test"}
				_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Namespaces().Update(context.TODO(), &namespace2, metav1.UpdateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)

				// After updating the namespace all address sets should be empty
				exteventuallyExpectEmptyAddressSets(fakeOvn, icmpNetworkPolicy)

				fakeOvn.asf.EventuallyExpectEmptyAddressSet(namespaceName1)

				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("reconciles a deleted icmpnetworkpolicy", func() {
			app.Action = func(ctx *cli.Context) error {

				npTest := icmpNetworkPolicy{}

				namespace1 := *newNamespace(namespaceName1)

				nPodTest := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespace1.Name,
				)
				icmpNetworkPolicy := newICMPNetworkPolicy("networkpolicy1", namespace1.Name,
					metav1.LabelSelector{},
					[]icmpnetworkpolicyapi.NetworkPolicyIngressRule{
						{
							From: []icmpnetworkpolicyapi.NetworkPolicyPeer{
								{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": nPodTest.podName,
										},
									},
								},
							},
						},
					},
					[]icmpnetworkpolicyapi.NetworkPolicyEgressRule{
						{
							To: []icmpnetworkpolicyapi.NetworkPolicyPeer{
								{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"name": nPodTest.podName,
										},
									},
								},
							},
						},
					})

				nPodTest.baseCmds(fExec)
				npTest.addNamespaceSelectorCmds(fExec, icmpNetworkPolicy, "")
				npTest.addLocalPodCmds(fExec, icmpNetworkPolicy)

				fakeOvn.start(ctx,
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespace1,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPod(nPodTest.namespace, nPodTest.podName, nPodTest.nodeName, nPodTest.podIP),
						},
					},
					&icmpnetworkpolicyapi.ICMPNetworkPolicyList{
						Items: []icmpnetworkpolicyapi.ICMPNetworkPolicy{
							*icmpNetworkPolicy,
						},
					},
				)

				nPodTest.populateLogicalSwitchCache(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()
				fakeOvn.controller.WatchICMPNetworkPolicy()

				_, err := fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Get(context.TODO(), icmpNetworkPolicy.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
				fakeOvn.asf.ExpectAddressSetWithIPs(namespaceName1, []string{nPodTest.podIP})

				err = fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Delete(context.TODO(), icmpNetworkPolicy.Name, *metav1.NewDeleteOptions(0))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
				exteventuallyExpectNoAddressSets(fakeOvn, icmpNetworkPolicy)

				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

	})
})

var _ = ginkgo.Describe("OVN ICMPNetworkPolicy Low-Level Operations", func() {
	var (
		fExec     *ovntest.FakeExec
		asFactory *addressset.FakeAddressSetFactory
	)

	ginkgo.BeforeEach(func() {
		// Restore global default values before each testcase
		config.PrepareTestConfig()
		fExec = ovntest.NewLooseCompareFakeExec()
		err := util.SetExec(fExec)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		asFactory = addressset.NewFakeAddressSetFactory()
		config.IPv4Mode = true
		config.IPv6Mode = false
	})

	ginkgo.It("computes match strings from address sets correctly", func() {
		const (
			pgUUID string = "pg-uuid"
			pgName string = "pg-name"
		)

		policy := &icmpnetworkpolicyapi.ICMPNetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				UID:       apimachinerytypes.UID("testing"),
				Name:      "policy",
				Namespace: "testing",
			},
		}
		policyName := "icmp_" + policy.Name
		netAttachInfo := &util.NetAttachDefInfo{NetNameInfo: util.NetNameInfo{NetName: types.DefaultNetworkName, Prefix: "", NotDefault: false}}
		gp := newGressPolicy(knet.PolicyType(icmpnetworkpolicyapi.PolicyTypeIngress), 0, policy.Namespace,
			policyName, netAttachInfo, false)
		err := gp.ensurePeerAddressSet(asFactory)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// asName := getIPv4ASName(gp.peerAddressSet.GetName())
		asName := gp.peerAddressSet.GetName()

		one := fmt.Sprintf("testing.policy.ingress.1")
		two := fmt.Sprintf("testing.policy.ingress.2")
		three := fmt.Sprintf("testing.policy.ingress.3")
		four := fmt.Sprintf("testing.policy.ingress.4")
		five := fmt.Sprintf("testing.policy.ingress.5")
		six := fmt.Sprintf("testing.policy.ingress.6")

		cur := addExpectedGressCmds(fExec, gp, pgName, []string{asName}, []string{asName, one})
		gomega.Expect(gp.addNamespaceAddressSet(one)).To(gomega.BeTrue())
		gp.localPodSetACL(pgName, pgUUID, defaultACLLoggingSeverity)
		gomega.Expect(fExec.CalledMatchesExpected()).To(gomega.BeTrue(), fExec.ErrorDesc)

		cur = addExpectedGressCmds(fExec, gp, pgName, cur, []string{asName, one, two})
		gomega.Expect(gp.addNamespaceAddressSet(two)).To(gomega.BeTrue())
		gp.localPodSetACL(pgName, pgUUID, defaultACLLoggingSeverity)
		gomega.Expect(fExec.CalledMatchesExpected()).To(gomega.BeTrue(), fExec.ErrorDesc)

		// address sets should be alphabetized
		cur = addExpectedGressCmds(fExec, gp, pgName, cur, []string{asName, one, two, three})
		gomega.Expect(gp.addNamespaceAddressSet(three)).To(gomega.BeTrue())
		gp.localPodSetACL(pgName, pgUUID, defaultACLLoggingSeverity)
		gomega.Expect(fExec.CalledMatchesExpected()).To(gomega.BeTrue(), fExec.ErrorDesc)

		// re-adding an existing set is a no-op
		gp.addNamespaceAddressSet(one)
		gomega.Expect(gp.addNamespaceAddressSet(three)).To(gomega.BeFalse())

		cur = addExpectedGressCmds(fExec, gp, pgName, cur, []string{asName, one, two, three, four})
		gomega.Expect(gp.addNamespaceAddressSet(four)).To(gomega.BeTrue())
		gp.localPodSetACL(pgName, pgUUID, defaultACLLoggingSeverity)
		gomega.Expect(fExec.CalledMatchesExpected()).To(gomega.BeTrue(), fExec.ErrorDesc)

		// now delete a set
		cur = addExpectedGressCmds(fExec, gp, pgName, cur, []string{asName, two, three, four})
		gomega.Expect(gp.delNamespaceAddressSet(one)).To(gomega.BeTrue())
		gp.localPodSetACL(pgName, pgUUID, defaultACLLoggingSeverity)
		gomega.Expect(fExec.CalledMatchesExpected()).To(gomega.BeTrue(), fExec.ErrorDesc)

		// deleting again is a no-op
		gp.delNamespaceAddressSet(one)
		gomega.Expect(fExec.CalledMatchesExpected()).To(gomega.BeTrue(), fExec.ErrorDesc)

		// add and delete some more...
		cur = addExpectedGressCmds(fExec, gp, pgName, cur, []string{asName, two, three, four, five})
		gomega.Expect(gp.addNamespaceAddressSet(five)).To(gomega.BeTrue())
		gp.localPodSetACL(pgName, pgUUID, defaultACLLoggingSeverity)
		gomega.Expect(fExec.CalledMatchesExpected()).To(gomega.BeTrue(), fExec.ErrorDesc)

		cur = addExpectedGressCmds(fExec, gp, pgName, cur, []string{asName, two, four, five})
		gomega.Expect(gp.delNamespaceAddressSet(three)).To(gomega.BeTrue())
		gp.localPodSetACL(pgName, pgUUID, defaultACLLoggingSeverity)
		gomega.Expect(fExec.CalledMatchesExpected()).To(gomega.BeTrue(), fExec.ErrorDesc)

		// deleting again is no-op
		gomega.Expect(gp.delNamespaceAddressSet(one)).To(gomega.BeFalse())

		cur = addExpectedGressCmds(fExec, gp, pgName, cur, []string{asName, two, four, five, six})
		gomega.Expect(gp.addNamespaceAddressSet(six)).To(gomega.BeTrue())
		gp.localPodSetACL(pgName, pgUUID, defaultACLLoggingSeverity)
		gomega.Expect(fExec.CalledMatchesExpected()).To(gomega.BeTrue(), fExec.ErrorDesc)

		cur = addExpectedGressCmds(fExec, gp, pgName, cur, []string{asName, four, five, six})
		gomega.Expect(gp.delNamespaceAddressSet(two)).To(gomega.BeTrue())
		gp.localPodSetACL(pgName, pgUUID, defaultACLLoggingSeverity)
		gomega.Expect(fExec.CalledMatchesExpected()).To(gomega.BeTrue(), fExec.ErrorDesc)

		cur = addExpectedGressCmds(fExec, gp, pgName, cur, []string{asName, four, six})
		gomega.Expect(gp.delNamespaceAddressSet(five)).To(gomega.BeTrue())
		gp.localPodSetACL(pgName, pgUUID, defaultACLLoggingSeverity)
		gomega.Expect(fExec.CalledMatchesExpected()).To(gomega.BeTrue(), fExec.ErrorDesc)

		cur = addExpectedGressCmds(fExec, gp, pgName, cur, []string{asName, four})
		gomega.Expect(gp.delNamespaceAddressSet(six)).To(gomega.BeTrue())
		gp.localPodSetACL(pgName, pgUUID, defaultACLLoggingSeverity)
		gomega.Expect(fExec.CalledMatchesExpected()).To(gomega.BeTrue(), fExec.ErrorDesc)

		cur = addExpectedGressCmds(fExec, gp, pgName, cur, []string{asName})
		gomega.Expect(gp.delNamespaceAddressSet(four)).To(gomega.BeTrue())
		gp.localPodSetACL(pgName, pgUUID, defaultACLLoggingSeverity)
		gomega.Expect(fExec.CalledMatchesExpected()).To(gomega.BeTrue(), fExec.ErrorDesc)

		// deleting again is no-op
		gomega.Expect(gp.delNamespaceAddressSet(four)).To(gomega.BeFalse())
	})
})
