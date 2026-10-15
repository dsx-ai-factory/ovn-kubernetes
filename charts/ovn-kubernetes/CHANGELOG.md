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
