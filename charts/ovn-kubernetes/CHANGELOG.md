# 2.4.4
- ovn: stop the pod handlers of every network policy when a UDN network
  controller stops. They kept editing the default deny port groups that the
  next controller for the network recreates by name, so a pod could leave
  ingress deny while a policy still selected it.
- node: read the current Node when programming a UDN gateway, so a gateway set
  up before its subnet was allocated picks the allocation up on retry.
- clustermanager: reconcile the Uplink `Ready` and CUDN `UplinksReady`
  conditions on status events, so an interface change whose status write lands
  after the resolved `UplinkState` no longer leaves either stuck not ready.
- helm: leave every `image.tag` and `image.sha256` empty, and set `appVersion`
  to `2026-10.commitID` on this chart and its subcharts. The release pipeline
  builds the image from the release tag and substitutes all three, so the chart
  no longer carries an image reference resolved by hand. Installing straight
  from the tree needs `global.image` or a per-component `image.tag`, and
  rendering without one now fails saying so rather than on the invalid image
  reference an empty tag produced.

# 2.4.3
- helm: scope subchart selection with `condition: tags.<name>` instead of the
  `dependencies[].tags` field, which Helm resolves only against the
  release-root `tags` map. Value names and defaults are unchanged, so
  standalone installs are unaffected, but an umbrella chart passing root-level
  tags has to move them under the `ovn-kubernetes` key. Every tag is now
  required by `values.schema.json`, and a `NOTES.txt` lists the components a
  release rendered.
- helm: drop `NET_ADMIN` from the `nb-ovsdb` and `sb-ovsdb` containers of the
  `ovnkube-single-node-zone` and `ovnkube-single-node-zone-dpu` charts. They
  serve only unix sockets; the containers still run as root.
- helm: remove the `update` verb on pods from the `ovnkube` RBAC, now that the
  `k8s.ovn.org/nodeName` label is written through `pods/status`, and allow a
  node to update that label on its own pods in the admission policy.
- helm: set `OVN_KUBERNETES_NAMESPACE` on the `ovnkube-node-dpu-host` and
  `ovnkube-node-dpu` charts, so installing into a namespace other than
  `ovn-kubernetes` no longer leaves `ovnkube` writing its lease where its
  service account has no permissions. Both default to `ovn-kubernetes`.
- helm: persist the OVN databases by mounting the shared volume at
  `/var/lib/ovn` for the NB and SB containers.
- helm: add `global.secureMgmtPort`, passed to `ovnkube-node` as
  `OVNKUBE_SECURE_MGMT_PORT` and enabled by default.
- helm: raise the `ovnkube-node` readiness probe `timeoutSeconds` to 15 in the
  two charts that run the script, which now bounds every check it makes.
- helm: remove the dangling OVS D-Bus mount that no container used.
- helm: drop `SECURITY.md`, the internal security triage metadata, and
  maintainer scripts from the packaged chart.
- helm: pin every `image.sha256` to the digest of the tag this chart ships.
  The 2.4.0, 2.4.1, and 2.4.2 cuts each repointed `image.tag` and left the
  digest behind.
- helm: regenerate `values.schema.json` and `README-deployment.md`, and teach
  the schema generator to carry element types for lists whose default is empty.

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
