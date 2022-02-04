package ovn

import (
	"context"
	"fmt"
	"strconv"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/format"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	icmpnetworkpolicyapi "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/icmpnetworkpolicy/v1alpha1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	addressset "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/address_set"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/libovsdbops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing/libovsdb"
	libovsdbtest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing/libovsdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	util "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	"github.com/urfave/cli/v2"

	v1 "k8s.io/api/core/v1"
	knet "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apimachinerytypes "k8s.io/apimachinery/pkg/types"
)

type kIcmpNetworkPolicy struct{}

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

func (n kIcmpNetworkPolicy) getDefaultDenyData(networkPolicy *icmpnetworkpolicyapi.ICMPNetworkPolicy, ports []string, logSeverity nbdb.ACLSeverity) []libovsdb.TestData {
	pgHash := hashedPortGroup(networkPolicy.Namespace)
	policyName := "icmp_" + networkPolicy.Name
	shouldBeLogged := logSeverity != nbdb.ACLSeverityInfo
	egressDenyACL := libovsdbops.BuildACL(
		networkPolicy.Namespace+"_"+policyName,
		nbdb.ACLDirectionToLport,
		types.DefaultDenyPriority,
		"inport == @"+pgHash+"_"+egressDenyPG,
		nbdb.ACLActionDrop,
		types.OvnACLLoggingMeter,
		logSeverity,
		shouldBeLogged,
		map[string]string{
			defaultDenyPolicyTypeACLExtIdKey: string(knet.PolicyTypeEgress),
		},
	)
	egressDenyACL.UUID = libovsdbops.BuildNamedUUID()

	egressAllowACL := libovsdbops.BuildACL(
		networkPolicy.Namespace+"_ARPallowPolicy",
		nbdb.ACLDirectionToLport,
		types.DefaultAllowPriority,
		"inport == @"+pgHash+"_"+egressDenyPG+" && arp",
		nbdb.ACLActionAllow,
		types.OvnACLLoggingMeter,
		nbdb.ACLSeverityInfo,
		false,
		map[string]string{
			defaultDenyPolicyTypeACLExtIdKey: string(knet.PolicyTypeEgress),
		},
	)
	egressAllowACL.UUID = libovsdbops.BuildNamedUUID()

	ingressDenyACL := libovsdbops.BuildACL(
		networkPolicy.Namespace+"_"+policyName,
		nbdb.ACLDirectionToLport,
		types.DefaultDenyPriority,
		"outport == @"+pgHash+"_"+ingressDenyPG,
		nbdb.ACLActionDrop,
		types.OvnACLLoggingMeter,
		logSeverity,
		shouldBeLogged,
		map[string]string{
			defaultDenyPolicyTypeACLExtIdKey: string(knet.PolicyTypeIngress),
		},
	)
	ingressDenyACL.UUID = libovsdbops.BuildNamedUUID()

	ingressAllowACL := libovsdbops.BuildACL(
		networkPolicy.Namespace+"_ARPallowPolicy",
		nbdb.ACLDirectionToLport,
		types.DefaultAllowPriority,
		"outport == @"+pgHash+"_"+ingressDenyPG+" && arp",
		nbdb.ACLActionAllow,
		types.OvnACLLoggingMeter,
		nbdb.ACLSeverityInfo,
		false,
		map[string]string{
			defaultDenyPolicyTypeACLExtIdKey: string(knet.PolicyTypeIngress),
		},
	)
	ingressAllowACL.UUID = libovsdbops.BuildNamedUUID()

	lsps := []*nbdb.LogicalSwitchPort{}
	for _, uuid := range ports {
		lsps = append(lsps, &nbdb.LogicalSwitchPort{UUID: uuid})
	}

	egressDenyPG := buildPortGroup(
		pgHash+"_"+egressDenyPG,
		pgHash+"_"+egressDenyPG,
		lsps,
		[]*nbdb.ACL{egressDenyACL, egressAllowACL},
		util.NetNameInfo{types.DefaultNetworkName, "", false},
	)
	egressDenyPG.UUID = libovsdbops.BuildNamedUUID()

	ingressDenyPG := buildPortGroup(
		pgHash+"_"+ingressDenyPG,
		pgHash+"_"+ingressDenyPG,
		lsps,
		[]*nbdb.ACL{ingressDenyACL, ingressAllowACL},
		util.NetNameInfo{types.DefaultNetworkName, "", false},
	)
	ingressDenyPG.UUID = libovsdbops.BuildNamedUUID()

	return []libovsdb.TestData{
		egressDenyACL,
		egressAllowACL,
		ingressDenyACL,
		ingressAllowACL,
		egressDenyPG,
		ingressDenyPG,
	}
}

func icmpEventuallyExpectNoAddressSets(fakeOvn *FakeOVN, networkPolicy *icmpnetworkpolicyapi.ICMPNetworkPolicy) {
	for i := range networkPolicy.Spec.Ingress {
		asName := getAddressSetName(networkPolicy.Namespace, "icmp_"+networkPolicy.Name, knet.PolicyTypeIngress, i)
		fakeOvn.asf.EventuallyExpectNoAddressSet(asName)
	}
	for i := range networkPolicy.Spec.Egress {
		asName := getAddressSetName(networkPolicy.Namespace, "icmp_"+networkPolicy.Name, knet.PolicyTypeEgress, i)
		fakeOvn.asf.EventuallyExpectNoAddressSet(asName)
	}
}

func icmpEventuallyExpectAddressSetsWithIP(fakeOvn *FakeOVN, networkPolicy *icmpnetworkpolicyapi.ICMPNetworkPolicy, ip string) {
	for i := range networkPolicy.Spec.Ingress {
		asName := getAddressSetName(networkPolicy.Namespace, "icmp_"+networkPolicy.Name, knet.PolicyTypeIngress, i)
		fakeOvn.asf.EventuallyExpectAddressSetWithIPs(asName, []string{ip})
	}
	for i := range networkPolicy.Spec.Egress {
		asName := getAddressSetName(networkPolicy.Namespace, "icmp_"+networkPolicy.Name, knet.PolicyTypeEgress, i)
		fakeOvn.asf.EventuallyExpectAddressSetWithIPs(asName, []string{ip})
	}
}

func icmpEventuallyExpectEmptyAddressSetsExist(fakeOvn *FakeOVN, networkPolicy *icmpnetworkpolicyapi.ICMPNetworkPolicy) {
	for i := range networkPolicy.Spec.Ingress {
		asName := getAddressSetName(networkPolicy.Namespace, "icmp_"+networkPolicy.Name, knet.PolicyTypeIngress, i)
		fakeOvn.asf.EventuallyExpectEmptyAddressSetExist(asName)
	}
	for i := range networkPolicy.Spec.Egress {
		asName := getAddressSetName(networkPolicy.Namespace, "icmp_"+networkPolicy.Name, knet.PolicyTypeEgress, i)
		fakeOvn.asf.EventuallyExpectEmptyAddressSetExist(asName)
	}
}

func (n kIcmpNetworkPolicy) getPolicyData(networkPolicy *icmpnetworkpolicyapi.ICMPNetworkPolicy, policyPorts []string, peerNamespaces []string, icmpTypes []int32, logSeverity nbdb.ACLSeverity) []libovsdb.TestData {
	policyName := "icmp_" + networkPolicy.Name
	pgName := networkPolicy.Namespace + "_" + policyName
	pgHash := hashedPortGroup(pgName)
	acls := []*nbdb.ACL{}

	shouldBeLogged := logSeverity != nbdb.ACLSeverityInfo
	for i := range networkPolicy.Spec.Ingress {
		aclName := networkPolicy.Namespace + "_" + policyName + "_" + strconv.Itoa(i)
		policyType := string(knet.PolicyTypeIngress)
		if peerNamespaces != nil {
			ingressAsMatch := asMatch(append(peerNamespaces, getAddressSetName(networkPolicy.Namespace, policyName, knet.PolicyTypeIngress, i)))
			acl := libovsdbops.BuildACL(
				aclName,
				nbdb.ACLDirectionToLport,
				types.DefaultAllowPriority,
				fmt.Sprintf("ip4.src == {%s} && outport == @%s", ingressAsMatch, pgHash),
				nbdb.ACLActionAllowRelated,
				types.OvnACLLoggingMeter,
				logSeverity,
				shouldBeLogged,
				map[string]string{
					l4MatchACLExtIdKey:     "None",
					ipBlockCIDRACLExtIdKey: "false",
					namespaceACLExtIdKey:   networkPolicy.Namespace,
					policyACLExtIdKey:      policyName,
					policyTypeACLExtIdKey:  policyType,
					policyType + "_num":    strconv.Itoa(i),
				},
			)
			acl.UUID = libovsdbops.BuildNamedUUID()
			acls = append(acls, acl)
		}
		for _, v := range icmpTypes {
			acl := libovsdbops.BuildACL(
				aclName,
				nbdb.ACLDirectionToLport,
				types.DefaultAllowPriority,
				fmt.Sprintf("ip4 && icmp4 && icmp4.type == %d && outport == @%s", v, pgHash),
				nbdb.ACLActionAllowRelated,
				types.OvnACLLoggingMeter,
				logSeverity,
				shouldBeLogged,
				map[string]string{
					l4MatchACLExtIdKey:     fmt.Sprintf("icmp4 && icmp4.type == %d", v),
					ipBlockCIDRACLExtIdKey: "false",
					namespaceACLExtIdKey:   networkPolicy.Namespace,
					policyACLExtIdKey:      policyName,
					policyTypeACLExtIdKey:  policyType,
					policyType + "_num":    strconv.Itoa(i),
				},
			)
			acl.UUID = libovsdbops.BuildNamedUUID()
			acls = append(acls, acl)
		}
	}

	for i := range networkPolicy.Spec.Egress {
		aclName := networkPolicy.Namespace + "_" + policyName + "_" + strconv.Itoa(i)
		policyType := string(knet.PolicyTypeEgress)
		if peerNamespaces != nil {
			egressAsMatch := asMatch(append(peerNamespaces, getAddressSetName(networkPolicy.Namespace, policyName, knet.PolicyTypeEgress, i)))
			acl := libovsdbops.BuildACL(
				aclName,
				nbdb.ACLDirectionToLport,
				types.DefaultAllowPriority,
				fmt.Sprintf("ip4.dst == {%s} && inport == @%s", egressAsMatch, pgHash),
				nbdb.ACLActionAllowRelated,
				types.OvnACLLoggingMeter,
				logSeverity,
				shouldBeLogged,
				map[string]string{
					l4MatchACLExtIdKey:     "None",
					ipBlockCIDRACLExtIdKey: "false",
					namespaceACLExtIdKey:   networkPolicy.Namespace,
					policyACLExtIdKey:      policyName,
					policyTypeACLExtIdKey:  policyType,
					policyType + "_num":    strconv.Itoa(i),
				},
			)
			acl.UUID = libovsdbops.BuildNamedUUID()
			acls = append(acls, acl)
		}
		for _, v := range icmpTypes {
			acl := libovsdbops.BuildACL(
				aclName,
				nbdb.ACLDirectionToLport,
				types.DefaultAllowPriority,
				fmt.Sprintf("ip4 && icmp4 && icmp4.type == %d && inport == @%s", v, pgHash),
				nbdb.ACLActionAllowRelated,
				types.OvnACLLoggingMeter,
				logSeverity,
				shouldBeLogged,
				map[string]string{
					l4MatchACLExtIdKey:     fmt.Sprintf("icmp4 && icmp4.type == %d", v),
					ipBlockCIDRACLExtIdKey: "false",
					namespaceACLExtIdKey:   networkPolicy.Namespace,
					policyACLExtIdKey:      policyName,
					policyTypeACLExtIdKey:  policyType,
					policyType + "_num":    strconv.Itoa(i),
				},
			)
			acl.UUID = libovsdbops.BuildNamedUUID()
			acls = append(acls, acl)
		}
	}

	lsps := []*nbdb.LogicalSwitchPort{}
	for _, uuid := range policyPorts {
		lsps = append(lsps, &nbdb.LogicalSwitchPort{UUID: uuid})
	}

	pg := buildPortGroup(
		pgHash,
		pgName,
		lsps,
		acls,
		util.NetNameInfo{types.DefaultNetworkName, "", false},
	)
	pg.UUID = libovsdbops.BuildNamedUUID()

	data := []libovsdb.TestData{}
	for _, acl := range acls {
		data = append(data, acl)
	}
	data = append(data, pg)
	return data
}

var _ = ginkgo.Describe("OVN Ext NetworkPolicy Operations", func() {
	const (
		namespaceName1 = "namespace1"
		namespaceName2 = "namespace2"
	)
	var (
		app       *cli.App
		fakeOvn   *FakeOVN
		initialDB libovsdb.TestSetup

		gomegaFormatMaxLength int
	)

	ginkgo.BeforeEach(func() {
		// Restore global default values before each testcase
		config.PrepareTestConfig()
		config.OVNKubernetesFeature.EnableICMPNetworkPolicy = true

		app = cli.NewApp()
		app.Name = "test"
		app.Flags = config.Flags

		fakeOvn = NewFakeOVN()

		gomegaFormatMaxLength = format.MaxLength
		format.MaxLength = 0
		initialDB = libovsdb.TestSetup{
			NBData: []libovsdb.TestData{
				&nbdb.LogicalSwitch{
					Name: "node1",
				},
			},
		}
	})

	ginkgo.AfterEach(func() {
		fakeOvn.shutdown()
		format.MaxLength = gomegaFormatMaxLength
	})

	ginkgo.Context("during execution", func() {

		ginkgo.It("correctly creates and deletes a icmpnetworkpolicy allowing a port to a local pod", func() {
			app.Action = func(ctx *cli.Context) error {
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

				npTest := kIcmpNetworkPolicy{}
				gressPolicy1ExpectedData := npTest.getPolicyData(icmpNetworkPolicy, []string{nPodTest.portUUID}, nil, []int32{icmpType}, nbdb.ACLSeverityInfo)
				defaultDenyExpectedData := npTest.getDefaultDenyData(icmpNetworkPolicy, []string{nPodTest.portUUID}, nbdb.ACLSeverityInfo)
				expectedData := []libovsdb.TestData{}
				expectedData = append(expectedData, gressPolicy1ExpectedData...)
				expectedData = append(expectedData, defaultDenyExpectedData...)
				expectedData = append(expectedData, getExpectedDataPodsAndSwitches([]testPod{nPodTest}, []string{"node1"})...)

				fakeOvn.startWithDBSetup(initialDB,
					&v1.NamespaceList{
						Items: []v1.Namespace{namespace1},
					},
					&v1.PodList{
						Items: []v1.Pod{*nPod},
					},
					&icmpnetworkpolicyapi.ICMPNetworkPolicyList{
						Items: []icmpnetworkpolicyapi.ICMPNetworkPolicy{*icmpNetworkPolicy},
					},
				)
				nPodTest.populateLogicalSwitchCache(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()
				fakeOvn.controller.WatchICMPNetworkPolicy()

				ginkgo.By("Creating a ICMP network policy that applies to a pod")

				_, err := fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Get(context.TODO(), icmpNetworkPolicy.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				fakeOvn.asf.ExpectAddressSetWithIPs(namespaceName1, []string{nPodTest.podIP})
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(expectedData...))

				// Create a second NP
				ginkgo.By("Creating and deleting another ICMP network policy that references that pod")

				_, err = fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Create(context.TODO(), icmpNetworkPolicy2, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				gressPolicy2ExpectedData := npTest.getPolicyData(icmpNetworkPolicy2, []string{nPodTest.portUUID}, nil, []int32{icmpType2}, nbdb.ACLSeverityInfo)
				expectedDataWithPolicy2 := append(expectedData, gressPolicy2ExpectedData...)
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(expectedDataWithPolicy2...))

				// Delete the second network policy
				err = fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy2.Namespace).Delete(context.TODO(), icmpNetworkPolicy2.Name, metav1.DeleteOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				// TODO: test server does not garbage collect ACLs, so we just expect policy2 portgroup to be removed
				expectedDataWithoutPolicy2 := append(expectedData, gressPolicy2ExpectedData[:len(gressPolicy2ExpectedData)-1]...)
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(expectedDataWithoutPolicy2...))

				// Delete the first network policy
				ginkgo.By("Deleting that ICMP network policy")
				err = fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Delete(context.TODO(), icmpNetworkPolicy.Name, metav1.DeleteOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				// TODO: test server does not garbage collect ACLs, so we just expect policy & deny portgroups to be removed
				acls := []libovsdb.TestData{}
				acls = append(acls, gressPolicy1ExpectedData[:len(gressPolicy1ExpectedData)-1]...)
				acls = append(acls, gressPolicy2ExpectedData[:len(gressPolicy2ExpectedData)-1]...)
				acls = append(acls, defaultDenyExpectedData[:len(defaultDenyExpectedData)-2]...)
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(append(acls, getExpectedDataPodsAndSwitches([]testPod{nPodTest}, []string{"node1"})...)))

				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("reconciles a deleted namespace referenced by a icmpnetworkpolicy with a local running pod", func() {
			app.Action = func(ctx *cli.Context) error {
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

				npTest := kIcmpNetworkPolicy{}
				gressPolicyExpectedData := npTest.getPolicyData(icmpNetworkPolicy, []string{nPodTest.portUUID}, []string{namespace2.Name}, nil, nbdb.ACLSeverityInfo)
				defaultDenyExpectedData := npTest.getDefaultDenyData(icmpNetworkPolicy, []string{nPodTest.portUUID}, nbdb.ACLSeverityInfo)
				expectedData := []libovsdb.TestData{}
				expectedData = append(expectedData, gressPolicyExpectedData...)
				expectedData = append(expectedData, defaultDenyExpectedData...)
				expectedData = append(expectedData, getExpectedDataPodsAndSwitches([]testPod{nPodTest}, []string{"node1"})...)

				fakeOvn.startWithDBSetup(initialDB,
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
				fakeOvn.asf.ExpectAddressSetWithIPs(namespaceName1, []string{nPodTest.podIP})
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(expectedData...))

				expectedData = []libovsdb.TestData{}
				gressPolicyExpectedData = npTest.getPolicyData(icmpNetworkPolicy, []string{nPodTest.portUUID}, []string{}, nil, nbdb.ACLSeverityInfo)
				expectedData = append(expectedData, gressPolicyExpectedData...)
				expectedData = append(expectedData, defaultDenyExpectedData...)
				expectedData = append(expectedData, getExpectedDataPodsAndSwitches([]testPod{nPodTest}, []string{"node1"})...)

				err = fakeOvn.fakeClient.KubeClient.CoreV1().Namespaces().Delete(context.TODO(), namespace2.Name, *metav1.NewDeleteOptions(0))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				fakeOvn.asf.EventuallyExpectNoAddressSet(namespaceName2)
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(expectedData...))

				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("reconciles a deleted namespace referenced by a icmpnetworkpolicy", func() {
			app.Action = func(ctx *cli.Context) error {
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

				npTest := kIcmpNetworkPolicy{}
				gressPolicyExpectedData := npTest.getPolicyData(icmpNetworkPolicy, nil, []string{namespace2.Name}, nil, nbdb.ACLSeverityInfo)
				defaultDenyExpectedData := npTest.getDefaultDenyData(icmpNetworkPolicy, nil, nbdb.ACLSeverityInfo)
				expectedData := []libovsdb.TestData{}
				expectedData = append(expectedData, gressPolicyExpectedData...)
				expectedData = append(expectedData, defaultDenyExpectedData...)
				expectedData = append(expectedData, &nbdb.LogicalSwitch{
					UUID: libovsdbops.BuildNamedUUID(),
					Name: "node1",
				})

				fakeOvn.startWithDBSetup(initialDB,
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
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(expectedData...))

				expectedData = []libovsdb.TestData{}
				gressPolicyExpectedData = npTest.getPolicyData(icmpNetworkPolicy, nil, []string{}, nil, nbdb.ACLSeverityInfo)
				expectedData = append(expectedData, gressPolicyExpectedData...)
				expectedData = append(expectedData, defaultDenyExpectedData...)
				expectedData = append(expectedData, &nbdb.LogicalSwitch{
					UUID: libovsdbops.BuildNamedUUID(),
					Name: "node1",
				})

				err = fakeOvn.fakeClient.KubeClient.CoreV1().Namespaces().Delete(context.TODO(), namespace2.Name, *metav1.NewDeleteOptions(0))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(expectedData...))

				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("reconciles a deleted pod referenced by a icmpnetworkpolicy in its own namespace", func() {
			app.Action = func(ctx *cli.Context) error {
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

				npTest := kIcmpNetworkPolicy{}
				gressPolicyExpectedData := npTest.getPolicyData(icmpNetworkPolicy, []string{nPodTest.portUUID}, []string{}, nil, nbdb.ACLSeverityInfo)
				defaultDenyExpectedData := npTest.getDefaultDenyData(icmpNetworkPolicy, []string{nPodTest.portUUID}, nbdb.ACLSeverityInfo)
				expectedData := []libovsdb.TestData{}
				expectedData = append(expectedData, gressPolicyExpectedData...)
				expectedData = append(expectedData, defaultDenyExpectedData...)
				expectedData = append(expectedData, getExpectedDataPodsAndSwitches([]testPod{nPodTest}, []string{"node1"})...)

				fakeOvn.startWithDBSetup(initialDB,
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

				icmpEventuallyExpectAddressSetsWithIP(fakeOvn, icmpNetworkPolicy, nPodTest.podIP)
				fakeOvn.asf.ExpectAddressSetWithIPs(namespaceName1, []string{nPodTest.podIP})

				_, err := fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Get(context.TODO(), icmpNetworkPolicy.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(expectedData...))

				err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(nPodTest.namespace).Delete(context.TODO(), nPodTest.podName, *metav1.NewDeleteOptions(0))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				icmpEventuallyExpectEmptyAddressSetsExist(fakeOvn, icmpNetworkPolicy)
				fakeOvn.asf.EventuallyExpectEmptyAddressSetExist(namespaceName1)

				gressPolicyExpectedData = npTest.getPolicyData(icmpNetworkPolicy, nil, []string{}, nil, nbdb.ACLSeverityInfo)
				defaultDenyExpectedData = npTest.getDefaultDenyData(icmpNetworkPolicy, nil, nbdb.ACLSeverityInfo)
				expectedData = []libovsdb.TestData{}
				expectedData = append(expectedData, gressPolicyExpectedData...)
				expectedData = append(expectedData, defaultDenyExpectedData...)
				expectedData = append(expectedData, &nbdb.LogicalSwitch{
					UUID: libovsdbops.BuildNamedUUID(),
					Name: "node1",
				})
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(expectedData...))

				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("reconciles a deleted pod referenced by a icmpnetworkpolicy in another namespace", func() {
			app.Action = func(ctx *cli.Context) error {
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

				npTest := kIcmpNetworkPolicy{}
				gressPolicyExpectedData := npTest.getPolicyData(icmpNetworkPolicy, nil, []string{}, nil, nbdb.ACLSeverityInfo)
				defaultDenyExpectedData := npTest.getDefaultDenyData(icmpNetworkPolicy, nil, nbdb.ACLSeverityInfo)
				expectedData := []libovsdb.TestData{}
				expectedData = append(expectedData, gressPolicyExpectedData...)
				expectedData = append(expectedData, defaultDenyExpectedData...)
				expectedData = append(expectedData, getExpectedDataPodsAndSwitches([]testPod{nPodTest}, []string{"node1"})...)

				fakeOvn.startWithDBSetup(initialDB,
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
				icmpEventuallyExpectAddressSetsWithIP(fakeOvn, icmpNetworkPolicy, nPodTest.podIP)
				fakeOvn.asf.ExpectAddressSetWithIPs(namespaceName2, []string{nPodTest.podIP})

				_, err := fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Get(context.TODO(), icmpNetworkPolicy.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(expectedData...))

				err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(nPodTest.namespace).Delete(context.TODO(), nPodTest.podName, *metav1.NewDeleteOptions(0))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(expectedData...))

				// After deleting the pod all address sets should be empty
				icmpEventuallyExpectEmptyAddressSetsExist(fakeOvn, icmpNetworkPolicy)
				fakeOvn.asf.EventuallyExpectEmptyAddressSetExist(namespaceName1)

				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("reconciles an updated namespace label", func() {
			app.Action = func(ctx *cli.Context) error {
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

				npTest := kIcmpNetworkPolicy{}
				gressPolicyExpectedData := npTest.getPolicyData(icmpNetworkPolicy, nil, []string{}, nil, nbdb.ACLSeverityInfo)
				defaultDenyExpectedData := npTest.getDefaultDenyData(icmpNetworkPolicy, nil, nbdb.ACLSeverityInfo)
				expectedData := []libovsdb.TestData{}
				expectedData = append(expectedData, gressPolicyExpectedData...)
				expectedData = append(expectedData, defaultDenyExpectedData...)
				expectedData = append(expectedData, getExpectedDataPodsAndSwitches([]testPod{nPodTest}, []string{"node1"})...)

				fakeOvn.startWithDBSetup(initialDB,
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
				icmpEventuallyExpectAddressSetsWithIP(fakeOvn, icmpNetworkPolicy, nPodTest.podIP)
				fakeOvn.asf.ExpectAddressSetWithIPs(namespaceName2, []string{nPodTest.podIP})

				_, err := fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Get(context.TODO(), icmpNetworkPolicy.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(expectedData...))

				namespace2.ObjectMeta.Labels = map[string]string{"labels": "test"}
				_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Namespaces().Update(context.TODO(), &namespace2, metav1.UpdateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(expectedData...))

				// After updating the namespace all address sets should be empty
				icmpEventuallyExpectEmptyAddressSetsExist(fakeOvn, icmpNetworkPolicy)

				fakeOvn.asf.EventuallyExpectEmptyAddressSetExist(namespaceName1)

				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("reconciles a deleted icmpnetworkpolicy", func() {
			app.Action = func(ctx *cli.Context) error {
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

				npTest := kIcmpNetworkPolicy{}
				gressPolicyExpectedData := npTest.getPolicyData(icmpNetworkPolicy, []string{nPodTest.portUUID}, []string{}, nil, nbdb.ACLSeverityInfo)
				defaultDenyExpectedData := npTest.getDefaultDenyData(icmpNetworkPolicy, []string{nPodTest.portUUID}, nbdb.ACLSeverityInfo)
				expectedData := []libovsdb.TestData{}
				expectedData = append(expectedData, gressPolicyExpectedData...)
				expectedData = append(expectedData, defaultDenyExpectedData...)
				expectedData = append(expectedData, getExpectedDataPodsAndSwitches([]testPod{nPodTest}, []string{"node1"})...)

				fakeOvn.startWithDBSetup(initialDB,
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
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(expectedData...))
				fakeOvn.asf.ExpectAddressSetWithIPs(namespaceName1, []string{nPodTest.podIP})

				err = fakeOvn.fakeClient.ICMPNetworkPolicyClient.K8sV1alpha1().ICMPNetworkPolicies(icmpNetworkPolicy.Namespace).Delete(context.TODO(), icmpNetworkPolicy.Name, *metav1.NewDeleteOptions(0))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				icmpEventuallyExpectNoAddressSets(fakeOvn, icmpNetworkPolicy)

				acls := []libovsdb.TestData{}
				acls = append(acls, gressPolicyExpectedData[:len(gressPolicyExpectedData)-1]...)
				acls = append(acls, defaultDenyExpectedData[:len(defaultDenyExpectedData)-2]...)
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(append(acls, getExpectedDataPodsAndSwitches([]testPod{nPodTest}, []string{"node1"})...)))

				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

	})
})

var _ = ginkgo.Describe("OVN ICMPNetworkPolicy Low-Level Operations", func() {
	var (
		asFactory *addressset.FakeAddressSetFactory
		nbCleanup *libovsdbtest.Cleanup
	)

	ginkgo.BeforeEach(func() {
		nbCleanup = nil
	})

	ginkgo.AfterEach(func() {
		if nbCleanup != nil {
			nbCleanup.Cleanup()
		}
	})

	ginkgo.It("computes match strings from address sets correctly", func() {
		const (
			pgName string = "pg-name"
		)
		// Restore global default values before each testcase
		config.PrepareTestConfig()

		asFactory = addressset.NewFakeAddressSetFactory()
		config.IPv4Mode = true
		config.IPv6Mode = false

		policy := &icmpnetworkpolicyapi.ICMPNetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				UID:       apimachinerytypes.UID("testing"),
				Name:      "policy",
				Namespace: "testing",
			},
		}
		policyName := "icmp_" + policy.Name
		netAttachInfo := &util.NetAttachDefInfo{NetNameInfo: util.NetNameInfo{NetName: types.DefaultNetworkName, Prefix: "", IsSecondary: false}}
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

		gomega.Expect(gp.addNamespaceAddressSet(one)).To(gomega.BeTrue())
		expected := buildExpectedACL(gp, pgName, []string{asName, one})
		actual := gp.buildLocalPodACLs(pgName, defaultACLLoggingSeverity)
		gomega.Expect(actual).To(gomega.ConsistOf(libovsdb.EqualIgnoringUUIDs(expected)))

		gomega.Expect(gp.addNamespaceAddressSet(two)).To(gomega.BeTrue())
		expected = buildExpectedACL(gp, pgName, []string{asName, one, two})
		actual = gp.buildLocalPodACLs(pgName, defaultACLLoggingSeverity)
		gomega.Expect(actual).To(gomega.ConsistOf(libovsdb.EqualIgnoringUUIDs(expected)))

		// address sets should be alphabetized
		gomega.Expect(gp.addNamespaceAddressSet(three)).To(gomega.BeTrue())
		expected = buildExpectedACL(gp, pgName, []string{asName, one, two, three})
		actual = gp.buildLocalPodACLs(pgName, defaultACLLoggingSeverity)
		gomega.Expect(actual).To(gomega.ConsistOf(libovsdb.EqualIgnoringUUIDs(expected)))

		// re-adding an existing set is a no-op
		gomega.Expect(gp.addNamespaceAddressSet(three)).To(gomega.BeFalse())

		gomega.Expect(gp.addNamespaceAddressSet(four)).To(gomega.BeTrue())
		expected = buildExpectedACL(gp, pgName, []string{asName, one, two, three, four})
		actual = gp.buildLocalPodACLs(pgName, defaultACLLoggingSeverity)
		gomega.Expect(actual).To(gomega.ConsistOf(libovsdb.EqualIgnoringUUIDs(expected)))

		// now delete a set
		gomega.Expect(gp.delNamespaceAddressSet(one)).To(gomega.BeTrue())
		expected = buildExpectedACL(gp, pgName, []string{asName, two, three, four})
		actual = gp.buildLocalPodACLs(pgName, defaultACLLoggingSeverity)
		gomega.Expect(actual).To(gomega.ConsistOf(libovsdb.EqualIgnoringUUIDs(expected)))

		// deleting again is a no-op
		gomega.Expect(gp.delNamespaceAddressSet(one)).To(gomega.BeFalse())

		// add and delete some more...
		gomega.Expect(gp.addNamespaceAddressSet(five)).To(gomega.BeTrue())
		expected = buildExpectedACL(gp, pgName, []string{asName, two, three, four, five})
		actual = gp.buildLocalPodACLs(pgName, defaultACLLoggingSeverity)
		gomega.Expect(actual).To(gomega.ConsistOf(libovsdb.EqualIgnoringUUIDs(expected)))

		gomega.Expect(gp.delNamespaceAddressSet(three)).To(gomega.BeTrue())
		expected = buildExpectedACL(gp, pgName, []string{asName, two, four, five})
		actual = gp.buildLocalPodACLs(pgName, defaultACLLoggingSeverity)
		gomega.Expect(actual).To(gomega.ConsistOf(libovsdb.EqualIgnoringUUIDs(expected)))

		// deleting again is no-op
		gomega.Expect(gp.delNamespaceAddressSet(one)).To(gomega.BeFalse())

		gomega.Expect(gp.addNamespaceAddressSet(six)).To(gomega.BeTrue())
		expected = buildExpectedACL(gp, pgName, []string{asName, two, four, five, six})
		actual = gp.buildLocalPodACLs(pgName, defaultACLLoggingSeverity)
		gomega.Expect(actual).To(gomega.ConsistOf(libovsdb.EqualIgnoringUUIDs(expected)))

		gomega.Expect(gp.delNamespaceAddressSet(two)).To(gomega.BeTrue())
		expected = buildExpectedACL(gp, pgName, []string{asName, four, five, six})
		actual = gp.buildLocalPodACLs(pgName, defaultACLLoggingSeverity)
		gomega.Expect(actual).To(gomega.ConsistOf(libovsdb.EqualIgnoringUUIDs(expected)))

		gomega.Expect(gp.delNamespaceAddressSet(five)).To(gomega.BeTrue())
		expected = buildExpectedACL(gp, pgName, []string{asName, four, six})
		actual = gp.buildLocalPodACLs(pgName, defaultACLLoggingSeverity)
		gomega.Expect(actual).To(gomega.ConsistOf(libovsdb.EqualIgnoringUUIDs(expected)))

		gomega.Expect(gp.delNamespaceAddressSet(six)).To(gomega.BeTrue())
		expected = buildExpectedACL(gp, pgName, []string{asName, four})
		actual = gp.buildLocalPodACLs(pgName, defaultACLLoggingSeverity)
		gomega.Expect(actual).To(gomega.ConsistOf(libovsdb.EqualIgnoringUUIDs(expected)))

		gomega.Expect(gp.delNamespaceAddressSet(four)).To(gomega.BeTrue())
		expected = buildExpectedACL(gp, pgName, []string{asName})
		actual = gp.buildLocalPodACLs(pgName, defaultACLLoggingSeverity)
		gomega.Expect(actual).To(gomega.ConsistOf(libovsdb.EqualIgnoringUUIDs(expected)))

		// deleting again is no-op
		gomega.Expect(gp.delNamespaceAddressSet(four)).To(gomega.BeFalse())
	})
})
