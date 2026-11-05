// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/ovn-kubernetes/ovn-kubernetes/test/e2e/deploymentconfig"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"
)

// testDCWideOverlayAdmissionCleanup copies the deployed policy into an isolated namespace to test real cleanup controllers.
func testDCWideOverlayAdmissionCleanup(f *framework.Framework) {
	ginkgo.GinkgoHelper()
	ctx := context.Background()
	ovnNamespace := deploymentconfig.Get().OVNKubernetesNamespace()
	policies := f.ClientSet.AdmissionregistrationV1().ValidatingAdmissionPolicies()
	source, err := policies.Get(ctx, dcWideSubnetsPolicyName, metav1.GetOptions{})
	framework.ExpectNoError(err, "reading deployed admission policy %s", dcWideSubnetsPolicyName)
	gomega.Expect(source.Spec.MatchConstraints).NotTo(gomega.BeNil(), "policy %s must define match constraints", source.Name)
	gomega.Expect(source.Spec.MatchConstraints.NamespaceSelector).NotTo(gomega.BeNil(), "policy %s must restrict its namespace scope", source.Name)
	gomega.Expect(source.Spec.MatchConstraints.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]).To(gomega.Equal(ovnNamespace), "policy %s must select OVN namespace %s", source.Name, ovnNamespace)
	namespace, err := f.CreateNamespace(ctx, "dc-wide-cleanup", nil)
	framework.ExpectNoError(err, "creating isolated namespace for admission cleanup tests")
	name := "dc-wide-cleanup-" + framework.RandomSuffix()
	policy := source.DeepCopy()
	policy.ObjectMeta = metav1.ObjectMeta{Name: name}
	policy.Status = admissionv1.ValidatingAdmissionPolicyStatus{}
	policy.Spec.MatchConstraints.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] = namespace.Name
	_, err = policies.Create(ctx, policy, metav1.CreateOptions{})
	framework.ExpectNoError(err, "creating cloned admission policy %s for namespace %s", name, namespace.Name)
	ginkgo.DeferCleanup(func() {
		framework.ExpectNoError(policies.Delete(ctx, name, metav1.DeleteOptions{}), "deleting cloned admission policy %s", name)
	})
	bindings := f.ClientSet.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings()
	_, err = bindings.Create(ctx, &admissionv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       admissionv1.ValidatingAdmissionPolicyBindingSpec{PolicyName: name, ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny}},
	}, metav1.CreateOptions{})
	framework.ExpectNoError(err, "creating admission binding %s for cloned policy %s", name, name)
	ginkgo.DeferCleanup(func() {
		framework.ExpectNoError(bindings.Delete(ctx, name, metav1.DeleteOptions{}), "deleting cloned admission binding %s", name)
	})
	// Clean the namespace while the policy is still present, including on an earlier assertion failure.
	deleteNamespace := func() {
		err := f.ClientSet.CoreV1().Namespaces().Delete(ctx, namespace.Name, metav1.DeleteOptions{})
		if !apierrors.IsNotFound(err) {
			framework.ExpectNoError(err, "deleting cleanup-test namespace %s", namespace.Name)
		}
		gomega.Eventually(func() error {
			remaining, err := f.ClientSet.CoreV1().Namespaces().Get(ctx, namespace.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("checking deletion of namespace %s: %w", namespace.Name, err)
			}
			return fmt.Errorf("namespace %s still exists with phase %s and finalizers %v", namespace.Name, remaining.Status.Phase, remaining.Spec.Finalizers)
		}, 90*time.Second, time.Second).Should(gomega.Succeed(), "namespace %s must be deleted while admission policy %s is installed", namespace.Name, name)
	}
	ginkgo.DeferCleanup(deleteNamespace)
	untrusted := dcWideOverlayUntrustedClient(f, namespace.Name)
	configMaps := f.ClientSet.CoreV1().ConfigMaps(namespace.Name)
	candidate := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: dcWideSubnetsConfigMapName, Namespace: namespace.Name}}
	// Wait for admission and the temporary RBAC binding before relying on the cloned policy.
	gomega.Eventually(func() error {
		_, err := untrusted.CoreV1().ConfigMaps(namespace.Name).Create(ctx, candidate, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if apierrors.IsForbidden(err) && strings.Contains(err.Error(), "not allowed to modify the DC-wide overlay subnets ConfigMap") {
			return nil
		}
		return fmt.Errorf("expected cloned policy %s to deny creation of ConfigMap %s/%s, got %v", name, namespace.Name, candidate.Name, err)
	}, 30*time.Second, time.Second).Should(gomega.Succeed(), "waiting for cloned policy %s to enforce ConfigMap protection in namespace %s", name, namespace.Name)

	ginkgo.By("allowing garbage collection of an owner-referenced exemptions ConfigMap")
	owner, err := configMaps.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "dc-wide-owner"}}, metav1.CreateOptions{})
	framework.ExpectNoError(err, "creating garbage-collection owner ConfigMap %s/dc-wide-owner", namespace.Name)
	candidate.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: owner.Name, UID: owner.UID}}
	_, err = configMaps.Create(ctx, candidate, metav1.CreateOptions{})
	framework.ExpectNoError(err, "creating owner-referenced ConfigMap %s/%s for garbage collection", namespace.Name, candidate.Name)
	background := metav1.DeletePropagationBackground
	framework.ExpectNoError(configMaps.Delete(ctx, owner.Name, metav1.DeleteOptions{PropagationPolicy: &background}), "deleting owner ConfigMap %s/%s to trigger garbage collection", namespace.Name, owner.Name)
	// Allow time for GC discovery and work queues on loaded nodes, preserving API errors on failure.
	gomega.Eventually(func() error {
		_, err := configMaps.Get(ctx, candidate.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("checking garbage collection of %s/%s: %w", namespace.Name, candidate.Name, err)
		}
		return fmt.Errorf("ConfigMap %s/%s still exists", namespace.Name, candidate.Name)
	}, 3*time.Minute, time.Second).Should(gomega.Succeed(), "garbage collection must delete ConfigMap %s/%s under policy %s", namespace.Name, candidate.Name, name)

	ginkgo.By("allowing namespace termination with an exemptions ConfigMap still present")
	candidate.OwnerReferences = nil
	_, err = configMaps.Create(ctx, candidate, metav1.CreateOptions{})
	framework.ExpectNoError(err, "recreating ConfigMap %s/%s before namespace deletion", namespace.Name, candidate.Name)
	deleteNamespace()
}
