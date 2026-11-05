// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	rav1 "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/routeadvertisements/v1"
	raclientset "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/routeadvertisements/v1/apis/clientset/versioned"
	apitypes "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/types"
	udnv1 "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/userdefinednetwork/v1"
	udnclientset "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/userdefinednetwork/v1/apis/clientset/versioned"
	ovnutil "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
	"github.com/ovn-kubernetes/ovn-kubernetes/test/e2e/allocators"
	"github.com/ovn-kubernetes/ovn-kubernetes/test/e2e/deploymentconfig"
	"github.com/ovn-kubernetes/ovn-kubernetes/test/e2e/infraprovider"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"
	e2enode "k8s.io/kubernetes/test/e2e/framework/node"
	e2epodoutput "k8s.io/kubernetes/test/e2e/framework/pod/output"
	utilnet "k8s.io/utils/net"
)

// expectDCWideOverlaySourceIPs checks new connections on the pod's actual primary network.
func expectDCWideOverlaySourceIPs(f *framework.Framework, pod *corev1.Pod, attachment string, serverIPs []string, exempt, consistent bool) {
	ginkgo.GinkgoHelper()
	node, err := f.ClientSet.CoreV1().Nodes().Get(context.Background(), pod.Spec.NodeName, metav1.GetOptions{})
	framework.ExpectNoError(err, "reading node %s for pod %s/%s source-IP expectations", pod.Spec.NodeName, pod.Namespace, pod.Name)
	nodeIPs := e2enode.GetAddresses(node, corev1.NodeInternalIP)
	for _, serverIP := range serverIPs {
		family := utilnet.IPFamilyOfString(serverIP)
		expected := getFirstIPStringOfFamily(family, nodeIPs)
		if exempt {
			expected, err = getPodAnnotationIPsForAttachmentByIPFamily(f.ClientSet, pod.Namespace, pod.Name, attachment, family)
			framework.ExpectNoError(err, "reading %s address for pod %s/%s attachment %s", family, pod.Namespace, pod.Name, attachment)
		}
		gomega.Expect(net.ParseIP(expected)).NotTo(gomega.BeNil(), "expected a valid source IP for pod %s/%s attachment %s on node %s, destination %s, exempt=%t", pod.Namespace, pod.Name, attachment, pod.Spec.NodeName, serverIP, exempt)
		readSource := func() (string, error) {
			output, err := e2epodoutput.RunHostCmd(pod.Namespace, pod.Name,
				fmt.Sprintf("curl --max-time 10 -g -q -fsS http://%s/clientip", net.JoinHostPort(serverIP, fmt.Sprint(netexecPort))))
			if err != nil {
				return "", fmt.Errorf("requesting client IP from %s using pod %s/%s attachment %s: %w", serverIP, pod.Namespace, pod.Name, attachment, err)
			}
			source, _, err := net.SplitHostPort(strings.TrimSpace(output))
			if err != nil {
				return "", fmt.Errorf("parsing client-IP response %q from %s for pod %s/%s: %w", output, serverIP, pod.Namespace, pod.Name, err)
			}
			parsed := net.ParseIP(source)
			if parsed == nil {
				return "", fmt.Errorf("invalid source IP in response %q from %s for pod %s/%s", output, serverIP, pod.Namespace, pod.Name)
			}
			return parsed.String(), nil
		}
		if consistent {
			gomega.Consistently(readSource, 5*time.Second, time.Second).Should(gomega.Equal(net.ParseIP(expected).String()), "pod %s/%s attachment %s on node %s must reach %s with source %s, exempt=%t", pod.Namespace, pod.Name, attachment, pod.Spec.NodeName, serverIP, expected, exempt)
		} else {
			gomega.Eventually(readSource, 60*time.Second, time.Second).Should(gomega.Equal(net.ParseIP(expected).String()), "pod %s/%s attachment %s on node %s must reach %s with source %s, exempt=%t", pod.Namespace, pod.Name, attachment, pod.Spec.NodeName, serverIP, expected, exempt)
		}
	}
}

// dcWideOverlayControllerPod selects the owning node's controller and NBDB in standard Kind deployments.
func dcWideOverlayControllerPod(f *framework.Framework, nodeName string) (*corev1.Pod, error) {
	pods, err := f.ClientSet.CoreV1().Pods(deploymentconfig.Get().OVNKubernetesNamespace()).List(context.Background(), metav1.ListOptions{
		LabelSelector: "app=ovnkube-node", FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		return nil, fmt.Errorf("listing ovnkube-node pods on node %s: %w", nodeName, err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil {
			continue
		}
		controller, nbdb := false, false
		for _, container := range pod.Spec.Containers {
			controller = controller || container.Name == "ovnkube-controller"
			nbdb = nbdb || container.Name == "nb-ovsdb"
		}
		if controller && nbdb {
			return pod, nil
		}
	}
	return nil, fmt.Errorf("node %s requires node-local ovnkube-controller and nb-ovsdb containers", nodeName)
}

// requireDCWideOverlayLocalController verifies the layout required by the Kind-only lifecycle cases.
func requireDCWideOverlayLocalController(f *framework.Framework, nodeName string) {
	ginkgo.GinkgoHelper()
	_, err := dcWideOverlayControllerPod(f, nodeName)
	framework.ExpectNoError(err, "locating the controller and NBDB on node %s", nodeName)
}

// dcWideOverlayNBCTL inspects the same zone that owns the test pod's exemption address sets.
func dcWideOverlayNBCTL(f *framework.Framework, nodeName string, args ...string) (string, error) {
	pod, err := dcWideOverlayControllerPod(f, nodeName)
	if err != nil {
		return "", err
	}
	command := append([]string{"ovn-nbctl"}, args...)
	stdout, stderr, err := ExecCommandInContainerWithFullOutput(f, pod.Namespace, pod.Name, "nb-ovsdb", command...)
	if err != nil {
		return "", fmt.Errorf("ovn-nbctl %v on node %s in pod %s/%s: %w; stderr: %s", args, nodeName, pod.Namespace, pod.Name, err, stderr)
	}
	return strings.TrimSpace(stdout), nil
}

// dcWideOverlayAddressSets identifies both IP families without depending on hashed OVN names.
func dcWideOverlayAddressSets(f *framework.Framework, nodeName, networkName string) ([]string, error) {
	output, err := dcWideOverlayNBCTL(f, nodeName, "--data=bare", "--no-heading", "--columns=_uuid", "find", "Address_Set",
		"external_ids:k8s.ovn.org/name=no-overlay-snat-exemption", "external_ids:network="+networkName)
	if err != nil {
		return nil, fmt.Errorf("finding SNAT exemption address sets for network %s on node %s: %w", networkName, nodeName, err)
	}
	return strings.Fields(output), nil
}

// testDCWideOverlayStartup checks exemption retention, startup fallback, and recovery after ConfigMap changes.
func testDCWideOverlayStartup(f *framework.Framework, pod *corev1.Pod, serverIPs []string, scenario, matchingSubnets string, setValue func(string), deleteOwned func() error) {
	ginkgo.GinkgoHelper()
	requireDCWideOverlayLocalController(f, pod.Spec.NodeName)
	setValue(matchingSubnets)
	expectDCWideOverlaySourceIPs(f, pod, "default", serverIPs, true, false)
	switch scenario {
	case "startup-invalid":
		ginkgo.By("keeping the previous valid exemptions until the controller restarts with malformed data")
		setValue(matchingSubnets + ",invalid")
		expectDCWideOverlaySourceIPs(f, pod, "default", serverIPs, true, true)
	case "startup-absent":
		ginkgo.By("removing the ConfigMap before the controller restarts")
		framework.ExpectNoError(deleteOwned(), "removing exemptions ConfigMap before scenario %s", scenario)
		expectDCWideOverlaySourceIPs(f, pod, "default", serverIPs, false, false)
	}
	ginkgo.By("restarting the controller with the ConfigMap state unchanged")
	framework.ExpectNoError(restartOVNKubeNodePod(f.ClientSet, deploymentconfig.Get().OVNKubernetesNamespace(), pod.Spec.NodeName), "restarting ovnkube-node on node %s for scenario %s", pod.Spec.NodeName, scenario)
	expectDCWideOverlaySourceIPs(f, pod, "default", serverIPs, false, false)

	ginkgo.By("applying a valid value after startup")
	setValue(matchingSubnets)
	expectDCWideOverlaySourceIPs(f, pod, "default", serverIPs, true, false)
}

// dcWideOverlayTestNetwork owns a namespace and a primary no-overlay CUDN that can be recreated.
type dcWideOverlayTestNetwork struct {
	f                     *framework.Framework
	name, namespace, node string
	subnets               []udnv1.Layer3Subnet
	network               *udnv1.ClusterUserDefinedNetwork
	pod                   *corev1.Pod
	raCreated             bool
}

// newDCWideOverlayTestNetwork registers cleanup before creating resources that may fail readiness.
func newDCWideOverlayTestNetwork(f *framework.Framework, nodeName string) *dcWideOverlayTestNetwork {
	ginkgo.GinkgoHelper()
	allocation, err := allocators.AllocateBGP(f, infraprovider.Get().NewTestContext())
	framework.ExpectNoError(err, "allocating non-overlapping CUDN subnets on node %s", nodeName)
	namespace, err := f.CreateNamespace(context.Background(), "dc-wide-cudn", map[string]string{RequiredUDNNamespaceLabel: ""})
	framework.ExpectNoError(err, "creating a primary-CUDN test namespace on node %s", nodeName)
	n := &dcWideOverlayTestNetwork{f: f, name: "dc-wide-" + framework.RandomSuffix(), namespace: namespace.Name, node: nodeName,
		subnets: filterL3Subnets(f.ClientSet, []udnv1.Layer3Subnet{
			{CIDR: udnv1.CIDR(allocation.UDNSubnet), HostSubnet: 24},
			{CIDR: udnv1.CIDR(allocation.UDNSubnet6), HostSubnet: 64},
		})}
	ginkgo.DeferCleanup(func() {
		if n.raCreated {
			client, err := raclientset.NewForConfig(f.ClientConfig())
			framework.ExpectNoError(err, "creating RouteAdvertisements client to clean up %s", n.name)
			err = client.K8sV1().RouteAdvertisements().Delete(context.Background(), n.name, metav1.DeleteOptions{})
			if !apierrors.IsNotFound(err) {
				framework.ExpectNoError(err, "deleting RouteAdvertisements %s during CUDN cleanup", n.name)
			}
		}
	})
	ginkgo.DeferCleanup(n.delete)
	n.create()
	return n
}

// create reuses the namespace and advertisement to test a fresh controller with the same network name.
func (n *dcWideOverlayTestNetwork) create() {
	ginkgo.GinkgoHelper()
	client, err := udnclientset.NewForConfig(n.f.ClientConfig())
	framework.ExpectNoError(err, "creating Kubernetes UDN client for CUDN %s", n.name)
	network, err := client.K8sV1().ClusterUserDefinedNetworks().Create(context.Background(), &udnv1.ClusterUserDefinedNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: n.name, Labels: map[string]string{"dc-wide-exemption-network": n.name}},
		Spec: udnv1.ClusterUserDefinedNetworkSpec{
			NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": n.namespace}},
			Network: udnv1.NetworkSpec{Topology: udnv1.NetworkTopologyLayer3,
				Layer3:    &udnv1.Layer3Config{Role: udnv1.NetworkRolePrimary, Subnets: n.subnets},
				Transport: udnv1.TransportOptionNoOverlay,
				NoOverlay: &udnv1.NoOverlayConfig{OutboundSNAT: udnv1.SNATEnabled, Routing: udnv1.RoutingUnmanaged}},
		},
	}, metav1.CreateOptions{})
	framework.ExpectNoError(err, "creating primary no-overlay CUDN %s in namespace %s on node %s", n.name, n.namespace, n.node)
	n.network = network
	gomega.Eventually(clusterUserDefinedNetworkReadyFunc(n.f.DynamicClient, n.name), 60*time.Second, time.Second).Should(gomega.Succeed(), "waiting for CUDN %s in namespace %s to become ready", n.name, n.namespace)
	// Dynamic UDN allocation needs a workload before the network can be advertised.
	n.pod, err = createPod(n.f, "dc-wide-client", n.node, n.namespace, []string{"bash", "-c", "sleep infinity"}, nil)
	framework.ExpectNoError(err, "creating client pod %s/dc-wide-client for CUDN %s on node %s", n.namespace, n.name, n.node)
	raClient, err := raclientset.NewForConfig(n.f.ClientConfig())
	framework.ExpectNoError(err, "creating RouteAdvertisements client for CUDN %s", n.name)
	if !n.raCreated {
		_, err = raClient.K8sV1().RouteAdvertisements().Create(context.Background(), &rav1.RouteAdvertisements{
			ObjectMeta: metav1.ObjectMeta{Name: n.name},
			Spec: rav1.RouteAdvertisementsSpec{
				NetworkSelectors: apitypes.NetworkSelectors{{NetworkSelectionType: apitypes.ClusterUserDefinedNetworks,
					ClusterUserDefinedNetworkSelector: &apitypes.ClusterUserDefinedNetworkSelector{
						NetworkSelector: metav1.LabelSelector{MatchLabels: map[string]string{"dc-wide-exemption-network": n.name}}}}},
				NodeSelector: metav1.LabelSelector{}, FRRConfigurationSelector: metav1.LabelSelector{},
				Advertisements: []rav1.AdvertisementType{rav1.PodNetwork},
			},
		}, metav1.CreateOptions{})
		framework.ExpectNoError(err, "creating RouteAdvertisements %s for CUDN %s", n.name, n.name)
		n.raCreated = true
	}
	gomega.Eventually(routeAdvertisementsReadyFunc(*raClient, n.name), 60*time.Second, time.Second).Should(gomega.Succeed(), "waiting for RouteAdvertisements %s for CUDN %s to become ready", n.name, n.name)
	gomega.Eventually(func() ([]string, error) {
		return dcWideOverlayAddressSets(n.f, n.node, ovnutil.GenerateCUDNNetworkName(n.name))
	}, 60*time.Second, time.Second).ShouldNot(gomega.BeEmpty(), "CUDN %s must have exemption address sets on node %s", n.name, n.node)
}

// delete waits for workload and OVN cleanup before another network can reuse the name.
func (n *dcWideOverlayTestNetwork) delete() {
	ginkgo.GinkgoHelper()
	if n.network == nil {
		return
	}
	ctx := context.Background()
	pods := n.f.ClientSet.CoreV1().Pods(n.namespace)
	err := pods.DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	if !apierrors.IsNotFound(err) {
		framework.ExpectNoError(err, "deleting workload pods in namespace %s for CUDN %s", n.namespace, n.name)
	}
	gomega.Eventually(func() (int, error) {
		list, err := pods.List(ctx, metav1.ListOptions{})
		if apierrors.IsNotFound(err) {
			return 0, nil
		}
		if err != nil {
			return 0, fmt.Errorf("listing remaining pods in namespace %s for CUDN %s: %w", n.namespace, n.name, err)
		}
		return len(list.Items), nil
	}, 90*time.Second, time.Second).Should(gomega.BeZero(), "workload pods in namespace %s must terminate before deleting CUDN %s", n.namespace, n.name)
	client, err := udnclientset.NewForConfig(n.f.ClientConfig())
	framework.ExpectNoError(err, "creating Kubernetes UDN client to delete CUDN %s", n.name)
	networks := client.K8sV1().ClusterUserDefinedNetworks()
	err = networks.Delete(ctx, n.name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &n.network.UID}})
	if !apierrors.IsNotFound(err) {
		framework.ExpectNoError(err, "deleting CUDN %s with UID %s", n.name, n.network.UID)
	}
	gomega.Eventually(func() error {
		remaining, err := networks.Get(ctx, n.name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("checking deletion of CUDN %s: %w", n.name, err)
		}
		return fmt.Errorf("CUDN %s still exists with finalizers %v", n.name, remaining.Finalizers)
	}, 90*time.Second, time.Second).Should(gomega.Succeed(), "CUDN %s must finish deletion before its name is reused", n.name)
	n.network, n.pod = nil, nil
	gomega.Eventually(func() ([]string, error) {
		return dcWideOverlayAddressSets(n.f, n.node, ovnutil.GenerateCUDNNetworkName(n.name))
	}, 60*time.Second, time.Second).Should(gomega.BeEmpty(), "deleted CUDN %s must leave no exemption address sets on node %s", n.name, n.node)
}

// expectDeletedDCWideOverlayNetwork checks that a later fan-out does not recreate orphan address sets.
func expectDeletedDCWideOverlayNetwork(n *dcWideOverlayTestNetwork) {
	ginkgo.GinkgoHelper()
	gomega.Consistently(func() ([]string, error) {
		return dcWideOverlayAddressSets(n.f, n.node, ovnutil.GenerateCUDNNetworkName(n.name))
	}, 5*time.Second, time.Second).Should(gomega.BeEmpty(), "later ConfigMap updates must not recreate exemption address sets for deleted CUDN %s on node %s", n.name, n.node)
}

// testDCWideOverlayNetworkLifecycle checks initial state, fan-out, and teardown across independent networks.
func testDCWideOverlayNetworkLifecycle(f *framework.Framework, defaultPod *corev1.Pod, serverIPs []string, matchingSubnets string, setValue func(string)) {
	ginkgo.GinkgoHelper()
	requireDCWideOverlayLocalController(f, defaultPod.Spec.NodeName)
	ginkgo.By("publishing exemptions before either CUDN exists")
	setValue(matchingSubnets)
	expectDCWideOverlaySourceIPs(f, defaultPod, "default", serverIPs, true, false)
	ginkgo.By("creating two primary Layer 3 CUDNs without another ConfigMap change")
	a := newDCWideOverlayTestNetwork(f, defaultPod.Spec.NodeName)
	b := newDCWideOverlayTestNetwork(f, defaultPod.Spec.NodeName)
	expect := func(n *dcWideOverlayTestNetwork, exempt bool) {
		expectDCWideOverlaySourceIPs(f, n.pod, namespacedName(n.namespace, n.name), serverIPs, exempt, false)
	}
	expect(a, true)
	expect(b, true)
	ginkgo.By("restarting existing CUDNs and creating a fresh CUDN from the unchanged ConfigMap")
	framework.ExpectNoError(restartOVNKubeNodePod(f.ClientSet, deploymentconfig.Get().OVNKubernetesNamespace(), defaultPod.Spec.NodeName), "restarting ovnkube-node on node %s with existing CUDNs %s and %s", defaultPod.Spec.NodeName, a.name, b.name)
	expect(a, true)
	expect(b, true)
	fresh := newDCWideOverlayTestNetwork(f, defaultPod.Spec.NodeName)
	expect(fresh, true)
	fresh.delete()
	expectDCWideOverlaySourceIPs(f, defaultPod, "default", serverIPs, true, false)
	oldUID := a.network.UID
	oldAddressSets, err := dcWideOverlayAddressSets(f, a.node, ovnutil.GenerateCUDNNetworkName(a.name))
	framework.ExpectNoError(err, "reading original exemption address sets for CUDN %s on node %s", a.name, a.node)
	ginkgo.By("updating exemptions while a CUDN is being removed")
	setValue("192.0.2.255/32,2001:db8:ffff::ffff/128")
	a.delete()
	expect(b, false)
	expectDCWideOverlaySourceIPs(f, defaultPod, "default", serverIPs, false, false)
	expectDeletedDCWideOverlayNetwork(a)
	ginkgo.By("updating surviving networks without recreating the deleted network's address sets")
	setValue(matchingSubnets)
	expect(b, true)
	expectDCWideOverlaySourceIPs(f, defaultPod, "default", serverIPs, true, false)
	expectDeletedDCWideOverlayNetwork(a)
	ginkgo.By("recreating the same CUDN name with the current exemptions and fresh OVN objects")
	a.create()
	gomega.Expect(a.network.UID).NotTo(gomega.Equal(oldUID), "recreated CUDN %s must have a new Kubernetes UID", a.name)
	expect(a, true)
	newAddressSets, err := dcWideOverlayAddressSets(f, a.node, ovnutil.GenerateCUDNNetworkName(a.name))
	framework.ExpectNoError(err, "reading recreated exemption address sets for CUDN %s on node %s", a.name, a.node)
	for _, old := range oldAddressSets {
		gomega.Expect(newAddressSets).NotTo(gomega.ContainElement(old), "recreated CUDN %s on node %s must not reuse deleted address-set UUID %s", a.name, a.node, old)
	}
	ginkgo.By("clearing exemptions on the default network and both CUDNs")
	setValue("")
	expect(a, false)
	expect(b, false)
	expectDCWideOverlaySourceIPs(f, defaultPod, "default", serverIPs, false, false)
	ginkgo.By("deleting both CUDNs and applying another update")
	a.delete()
	b.delete()
	setValue(matchingSubnets)
	expectDCWideOverlaySourceIPs(f, defaultPod, "default", serverIPs, true, false)
	expectDeletedDCWideOverlayNetwork(a)
	expectDeletedDCWideOverlayNetwork(b)
}
