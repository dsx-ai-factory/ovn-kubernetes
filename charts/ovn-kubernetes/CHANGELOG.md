# 2.4.2
- helm: protect node subnet ownership — add a `ValidatingAdmissionPolicy` and
  binding to `ovnkube-control-plane` that restrict `k8s.ovn.org/node-subnets`
  to the `ovnkube-cluster-manager` service account.
- helm: protect hybrid overlay subnet ownership — extend that policy to
  `k8s.ovn.org/hybrid-overlay-node-subnet` when `global.enableHybridOverlay` is
  set, and add `global.hybridOverlaySubnetWriters` to grant additional writers.
- helm: make ovnkube-control-plane resources configurable — replace the
  hardcoded 100m CPU and 300Mi memory requests on the `ovnkube-cluster-manager`
  container with an `ovnkubeControlPlane.resources` value. The container now
  also carries 2 CPU and 3Gi limits it previously ran without.
- node: add opt-in pod hostPort support — wire the feature flag through
  `global.enablePodHostPort`, the `ovnkube-single-node-zone` daemonset, and
  `values.schema.json`. Disabled by default; CRI-O/CNI port mapping must be
  turned off before enabling it.

# 2.4.1
- node: revert firewalld support — drop the firewalld containers, values, and
  `values.schema.json` entries from the control-plane, node-dpu-host,
  single-node-zone, and ovs-node templates.
- helm: make ovnkube-node container resources configurable — replace the
  hardcoded 100m CPU and 300Mi memory requests on each `ovnkube-single-node-zone`
  container with per-container `resources` values. The containers now also carry
  limits they previously ran without.
- helm: set seccomp profile in template spec — set `RuntimeDefault` at the pod
  `securityContext` level for `ovnkube-control-plane` and
  `ovnkube-single-node-zone`.

# 2.4.0
- Initial release of Helm Chart tracking ovn-kubernetes@release-2.4 branch
