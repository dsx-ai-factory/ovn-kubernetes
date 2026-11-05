// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/ovn-kubernetes/ovn-kubernetes/test/e2e/deploymentconfig"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/kubernetes/test/e2e/framework"
)

const (
	dcWideSubnetsConfigMapName = "dc-wide-overlay-subnets-config"
	dcWideSubnetsConfigMapKey  = "dc-wide-overlay-subnets"
	dcWideSubnetsPolicyName    = "dc-wide-overlay-subnets-writers"
)

// testDCWideOverlaySNATExemptions shares ConfigMap ownership and feature guards across the scenarios.
func testDCWideOverlaySNATExemptions(f *framework.Framework, pod *corev1.Pod, serverIPs []string, scenario string) {
	ginkgo.GinkgoHelper()
	if os.Getenv("ENABLE_NO_OVERLAY") != "true" {
		ginkgo.Skip("DC-wide SNAT exemption coverage runs only in the no-overlay lane")
	}
	expected := os.Getenv("ENABLE_DC_WIDE_OVERLAY_SNAT_EXEMPTIONS") == "true"
	if os.Getenv("ENABLE_DC_WIDE_OVERLAY_SNAT_EXEMPTIONS") == "false" {
		ginkgo.Skip("DC-wide SNAT exemption coverage is explicitly disabled")
	}
	if IsGatewayModeLocal(f.ClientSet) || !isNoOverlayOutboundSNATEnabled(f) {
		ginkgo.Skip("default-network exemptions require shared gateway mode and outbound SNAT")
	}
	ctx := context.Background()
	namespace := deploymentconfig.Get().OVNKubernetesNamespace()
	configMaps := f.ClientSet.CoreV1().ConfigMaps(namespace)
	config, err := configMaps.Get(ctx, "ovnkube-config", metav1.GetOptions{})
	framework.ExpectNoError(err, "reading controller configuration ConfigMap %s/ovnkube-config", namespace)
	if !regexp.MustCompile(`(?m)^\s*enable-dc-wide-overlay-snat-exemptions\s*=\s*true\s*$`).MatchString(config.Data["ovnkube.conf"]) {
		if expected {
			framework.Failf("the no-overlay lane expects DC-wide SNAT exemptions, but ovnkube.conf does not enable them")
		}
		ginkgo.Skip("DC-wide overlay SNAT exemptions are disabled")
	}
	_, err = configMaps.Get(ctx, dcWideSubnetsConfigMapName, metav1.GetOptions{})
	if err == nil {
		if expected {
			framework.Failf("the feature-test lane requires an absent DC-wide exemptions ConfigMap; refusing to modify an existing object")
		}
		ginkgo.Skip("an externally managed DC-wide exemptions ConfigMap already exists")
	}
	gomega.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue(), "checking that ConfigMap %s/%s is absent: %v", namespace, dcWideSubnetsConfigMapName, err)

	// A dry-run detects RBAC and admission denial before the test changes cluster state.
	_, err = configMaps.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: dcWideSubnetsConfigMapName, Namespace: namespace},
		Data:       map[string]string{dcWideSubnetsConfigMapKey: ""},
	}, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	if apierrors.IsForbidden(err) && !expected {
		ginkgo.Skip("the e2e identity is not authorized to manage the exemptions ConfigMap: " + err.Error())
	}
	framework.ExpectNoError(err, "the feature-test identity must have ConfigMap RBAC and admission writer access")

	var subnets []string
	for _, serverIP := range serverIPs {
		address, err := netip.ParseAddr(serverIP)
		framework.ExpectNoError(err, "parsing external server IP %q for scenario %s", serverIP, scenario)
		subnets = append(subnets, netip.PrefixFrom(address, address.BitLen()).String())
	}
	gomega.Expect(subnets).NotTo(gomega.BeEmpty(), "scenario %s requires at least one external server IP", scenario)
	matchingSubnets := strings.Join(subnets, ",")

	expectSourceIPs := func(exempt, consistent bool) {
		expectDCWideOverlaySourceIPs(f, pod, "default", serverIPs, exempt, consistent)
	}

	var owned *corev1.ConfigMap
	// Exemptions affect every network; only remove the ConfigMap instance created by this test.
	deleteOwned := func() error {
		if owned == nil {
			return nil
		}
		err := configMaps.Delete(ctx, owned.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &owned.UID}})
		if err == nil || apierrors.IsNotFound(err) {
			owned = nil
			return nil
		}
		return fmt.Errorf("deleting owned ConfigMap %s/%s (UID=%s): %w", namespace, owned.Name, owned.UID, err)
	}
	ginkgo.DeferCleanup(func() {
		framework.ExpectNoError(deleteOwned(), "cleaning up exemptions ConfigMap for scenario %s", scenario)
	})
	setValue := func(value string) {
		ginkgo.GinkgoHelper()
		operation := "create"
		var updated *corev1.ConfigMap
		var updateErr error
		if owned == nil {
			updated, updateErr = configMaps.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: dcWideSubnetsConfigMapName, Namespace: namespace},
				Data:       map[string]string{dcWideSubnetsConfigMapKey: value},
			}, metav1.CreateOptions{})
		} else {
			operation = "update"
			current, getErr := configMaps.Get(ctx, owned.Name, metav1.GetOptions{})
			framework.ExpectNoError(getErr, "reading owned ConfigMap %s/%s before update", namespace, owned.Name)
			gomega.Expect(current.UID).To(gomega.Equal(owned.UID), "refusing to update replacement ConfigMap %s/%s", namespace, owned.Name)
			current.Data = map[string]string{dcWideSubnetsConfigMapKey: value}
			updated, updateErr = configMaps.Update(ctx, current, metav1.UpdateOptions{})
		}
		framework.ExpectNoError(updateErr, "%s ConfigMap %s/%s with subnet value %q for scenario %s", operation, namespace, dcWideSubnetsConfigMapName, value, scenario)
		owned = updated
	}

	// ConfigMap deletion is asynchronous; wait for SNAT restoration before later specs run.
	clearExemptions := func() {
		ginkgo.GinkgoHelper()
		framework.ExpectNoError(deleteOwned(), "clearing exemptions ConfigMap after scenario %s", scenario)
		expectSourceIPs(false, false)
	}
	if strings.HasPrefix(scenario, "startup-") {
		testDCWideOverlayStartup(f, pod, serverIPs, scenario, matchingSubnets, setValue, deleteOwned)
		clearExemptions()
		return
	}
	if scenario == "networks" {
		testDCWideOverlayNetworkLifecycle(f, pod, serverIPs, matchingSubnets, setValue)
		clearExemptions()
		return
	}

	// Dry-run requests test admission without changing dataplane state if protection regresses.
	untrusted := dcWideOverlayUntrustedClient(f, namespace)
	expectDenied := func(operation string, err error) {
		ginkgo.GinkgoHelper()
		gomega.Expect(apierrors.IsForbidden(err)).To(gomega.BeTrue(), "expected %s of ConfigMap %s/%s to be forbidden: %v", operation, namespace, dcWideSubnetsConfigMapName, err)
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("not allowed to modify the DC-wide overlay subnets ConfigMap")), "%s of ConfigMap %s/%s must be denied by VAP rather than RBAC", operation, namespace, dcWideSubnetsConfigMapName)
	}
	ginkgo.By("rejecting an unauthorized create while allowing unrelated ConfigMaps")
	candidate := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: dcWideSubnetsConfigMapName, Namespace: namespace},
		Data:       map[string]string{dcWideSubnetsConfigMapKey: matchingSubnets},
	}
	// Wait for the temporary RoleBinding to reach the API server's authorization cache.
	candidate.Name = "dc-wide-overlay-unprotected-" + framework.RandomSuffix()
	gomega.Eventually(func() error {
		_, err := untrusted.CoreV1().ConfigMaps(namespace).Create(ctx, candidate, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		return err
	}, 30*time.Second, time.Second).Should(gomega.Succeed(), "waiting for untrusted writer access to unrelated ConfigMap %s/%s", namespace, candidate.Name)
	candidate.Name = dcWideSubnetsConfigMapName
	_, err = untrusted.CoreV1().ConfigMaps(namespace).Create(ctx, candidate, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	expectDenied("create", err)

	ginkgo.By("observing outbound SNAT while the ConfigMap is absent")
	expectSourceIPs(false, false)
	ginkgo.By("creating exemptions for the external server addresses")
	setValue(matchingSubnets)
	expectSourceIPs(true, false)
	ginkgo.By("allowing reads but rejecting unauthorized updates, patches and deletion")
	_, err = untrusted.CoreV1().ConfigMaps(namespace).Get(ctx, owned.Name, metav1.GetOptions{})
	framework.ExpectNoError(err, "reading ConfigMap %s/%s as the untrusted writer", namespace, owned.Name)
	unauthorizedUpdate := owned.DeepCopy()
	unauthorizedUpdate.Data[dcWideSubnetsConfigMapKey] = ""
	_, err = untrusted.CoreV1().ConfigMaps(namespace).Update(ctx, unauthorizedUpdate, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
	expectDenied("update", err)
	_, err = untrusted.CoreV1().ConfigMaps(namespace).Patch(ctx, owned.Name, types.MergePatchType,
		[]byte(`{"data":{"dc-wide-overlay-subnets":""}}`), metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}})
	expectDenied("patch", err)
	expectDenied("delete", untrusted.CoreV1().ConfigMaps(namespace).Delete(ctx, owned.Name, metav1.DeleteOptions{DryRun: []string{metav1.DryRunAll}}))
	expectDenied("collection delete", untrusted.CoreV1().ConfigMaps(namespace).DeleteCollection(ctx,
		metav1.DeleteOptions{DryRun: []string{metav1.DryRunAll}}, metav1.ListOptions{FieldSelector: "metadata.name=" + owned.Name}))
	ginkgo.By("retaining the last valid exemptions after malformed input")
	setValue(matchingSubnets + ",invalid")
	expectSourceIPs(true, true)
	ginkgo.By("restoring SNAT when the updated list excludes the external server")
	setValue("192.0.2.255/32,2001:db8:ffff::ffff/128")
	expectSourceIPs(false, false)
	ginkgo.By("restoring exemptions through another valid update")
	setValue(matchingSubnets)
	expectSourceIPs(true, false)
	ginkgo.By("removing exemptions when the ConfigMap is deleted")
	framework.ExpectNoError(deleteOwned(), "cleaning up exemptions ConfigMap for scenario %s", scenario)
	expectSourceIPs(false, false)
	ginkgo.By("restoring exemptions when the ConfigMap is recreated")
	setValue(matchingSubnets)
	expectSourceIPs(true, false)
	ginkgo.By("clearing exemptions with an explicitly empty value")
	setValue("")
	expectSourceIPs(false, false)
	// The empty value already restored SNAT, so deletion needs no second dataplane probe.
	framework.ExpectNoError(deleteOwned(), "cleaning up exemptions ConfigMap for scenario %s", scenario)
	testDCWideOverlayAdmissionCleanup(f)
}

// dcWideOverlayUntrustedClient grants namespace writes so denials must come from admission.
func dcWideOverlayUntrustedClient(f *framework.Framework, namespace string) kubernetes.Interface {
	ginkgo.GinkgoHelper()
	ctx := context.Background()
	name := "dc-wide-overlay-admission-" + framework.RandomSuffix()
	roles := f.ClientSet.RbacV1().Roles(namespace)
	_, err := roles.Create(ctx, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"configmaps"},
			Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection"}}},
	}, metav1.CreateOptions{})
	framework.ExpectNoError(err, "creating temporary writer Role %s/%s", namespace, name)
	ginkgo.DeferCleanup(func() {
		if err := roles.Delete(ctx, name, metav1.DeleteOptions{}); !apierrors.IsNotFound(err) {
			framework.ExpectNoError(err, "deleting temporary writer Role %s/%s", namespace, name)
		}
	})
	bindings := f.ClientSet.RbacV1().RoleBindings(namespace)
	_, err = bindings.Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Subjects:   []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: name}},
		RoleRef:    rbacv1.RoleRef{Kind: "Role", APIGroup: rbacv1.GroupName, Name: name},
	}, metav1.CreateOptions{})
	framework.ExpectNoError(err, "creating temporary writer RoleBinding %s/%s", namespace, name)
	ginkgo.DeferCleanup(func() {
		if err := bindings.Delete(ctx, name, metav1.DeleteOptions{}); !apierrors.IsNotFound(err) {
			framework.ExpectNoError(err, "deleting temporary writer RoleBinding %s/%s", namespace, name)
		}
	})
	config := rest.CopyConfig(f.ClientConfig())
	config.Impersonate = rest.ImpersonationConfig{UserName: name, Groups: []string{"system:authenticated"}}
	client, err := kubernetes.NewForConfig(config)
	framework.ExpectNoError(err, "creating Kubernetes client impersonating user %s", name)
	return client
}
