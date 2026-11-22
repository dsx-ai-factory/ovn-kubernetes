# Changelog

This changelog summarizes major changes in this repository compared with
[upstream OVN-Kubernetes](https://github.com/ovn-kubernetes/ovn-kubernetes).
Each release identifies the upstream branch, release tag, and commit used as
its comparison baseline. Small fixes and internal refactoring are omitted.
Downstream releases are numbered `2.MINOR.PATCH`, where `MINOR` tracks the
upstream minor release the drop is based on.

## [2.4.3]

Changes since 2.4.2 on `release-2.4`. The upstream baseline moves for the first
time in this series, from `release-1.4` at tag `v1.4.0` to `release-1.4` at
commit
[`d6fb77aaf7f643e5c7b8ae6b1ba595645def1b08`](https://github.com/ovn-kubernetes/ovn-kubernetes/commit/d6fb77aaf7f643e5c7b8ae6b1ba595645def1b08),
which carries no upstream tag. That merge brings the 69 commits made on
`release-1.4` after `v1.4.0`; the entries below describe the NVIDIA changes
only.

### Removals

- **Pod-controlled skip-IPAM:** Remove the remaining pod-driven skip-IPAM
  support, completing the removal of the pod-writable port security surface
  that took `k8s.ovn.org/port-security-info` and
  `k8s.ovn.org/skip-spoofchk-on-networks` in 2.4.1.
- **`ovnkube` pod `update` permission:** Drop the `update` verb on pods from
  the `ovnkube` RBAC. The NVIDIA-specific `k8s.ovn.org/nodeName` label is
  written through the `pods/status` subresource instead, and the admission
  policy is relaxed so a node may still set that label on its own pods.
- **`NET_ADMIN` on the OVN database containers:** `nb-ovsdb` and `sb-ovsdb`
  serve only unix sockets, so the `securityContext` granting them `NET_ADMIN`
  is dropped from the `ovnkube-single-node-zone` and
  `ovnkube-single-node-zone-dpu` charts. The containers still run as root,
  which `ovnkube.sh` needs for the `setpriv` call that hands `ovsdb-server` to
  the `ovs` user.

### Networking and IP management

- **Uplink bridge deleted under an active CUDN:** An admin deleting an Uplink
  OVS bridge no longer restarts `ovnkube-node` on every node running a CUDN on
  it. The periodic port check skips a bridge gone from OVSDB, network cleanup
  treats a vanished bridge as already cleaned up and deregisters it, masquerade
  neighbour cleanup tolerates the missing link, and discovery now re-runs on
  OVSDB bridge additions and deletions so `UplinkState` stops reporting a
  bridge that no longer exists.
- **Uplink default gateway changes:** Update the next hops and managed default
  routes of the families that changed instead of tearing down and rebuilding
  the whole gateway. The rebuild released the gateway interface from the
  network VRF and enslaved it again, which under VRF-Lite dropped the BGP
  session running over it and looped at the pace of the BGP reconnect.
- **Uplink VF recovery:** Recover the downstream uplink VF before resolving
  `UplinkState`, so a CUDN teardown that took the VF down no longer leaves
  gateway resolution failing ahead of the helper that raises it.
- **Stale `UplinkState`s of deleted nodes:** Cluster-manager deletes them on
  the Node delete event rather than only during its initial sync. Their owner
  is the Uplink rather than the Node, so garbage collection does not remove
  them and they used to accumulate until the next restart.
- **Stale localnet port:** Remove any other localnet port on the external
  switch in the same transaction that creates the current one, so a changed
  interface ID no longer leaves two.
- **Localnet bridge mappings:** Clear `ovn-bridge-mappings` when the last
  mapping is removed, by deleting the `external_ids` key rather than setting an
  empty map, which `ovs-vsctl` rejects. Cleanup previously reported success
  while `ovn-controller` kept a patch port for a network that was gone.
  Mappings are also preserved across node shutdown.
- **Localnet pod ports across restart:** The startup pod sync no longer deletes
  the port of every local pod on a localnet network with subnets or DHCP, where
  the annotation is written by the cluster manager or the CNI rather than by
  the sync itself. Those pods lost their datapath until the recreated port was
  bound.
- **VRF route tracking:** Replace a tracked route carrying the same
  destination, table, and metric instead of appending to the VRF cache. A
  default route via a new gateway used to leave the old one tracked, so each
  sync re-applied both and flipped the kernel default.
- **EVPN VTEP reconcile:** A fast CUDN create-then-delete no longer loses the
  VTEP reconcile to workqueue coalescing, which could leave a VTEP stuck in its
  last condition or blocked on its finalizer. The racy side cache is replaced
  by an optional `OnDelete` hook on the shared controller framework, which
  delivers the deleted object itself.
- **RouteAdvertisements partial failures:** Isolate per-node and per-template
  failures so one failing node no longer withdraws the advertisements of
  healthy ones, and publish `Accepted` and `Degraded` through the existing
  condition gauge.
- **Managed BGP self-peer filtering:** Filter self-peers out of the managed
  full-mesh template using node `InternalIP`s, the same addresses used to
  populate its neighbors, and reconcile node address changes. Missing
  `InternalIP`s now return a retryable error that preserves the existing
  configuration instead of entering pending cleanup.
- **RouteAdvertisements reconcile scope:** Index NADs by the advertisements
  they reference so an event reconciles only the networks that can be affected
  by it, rather than re-evaluating every running network. The node NAD informer
  is also initialised whenever route advertisements are enabled, which the node
  previously dereferenced as nil when segmentation and DPU mode were both off.
- **CUDN NAD tracking and deletion:** Track a NAD as soon as it is created, and
  record its namespace before the create request is sent, so a CUDN deleted
  after a partially failed or timed-out sync still releases its NADs and drops
  its finalizer instead of leaving the network controller running. Deletions
  blocked on a network still in use are paced by the controller's exponential
  limiter rather than retried about ten times a second.
- **NetworkQoS event handling:** Skip pod and namespace events while no policy
  exists and skip host-network pods, seed the label and IP caches when the
  first policy appears, handle deletion tombstones and namespaces that vanish
  before a queued pod event is processed, version the cached history across
  policy lifetimes so a stale snapshot cannot be trusted, and release informer
  handlers on controller shutdown.
- **Gateway MTU support change:** Resync the cluster router port as well as the
  gateway router when `k8s.ovn.org/gateway-mtu-support` changes. The stale
  `options:gateway_mtu` left behind broke hardware offload.
- **VFIO DHCP:** Address the temporary netdev by ifindex rather than by name
  throughout the localnet VFIO DHCP exchange, since udev can rename it mid-way
  and fail the CNI ADD. A failed rebind to `vfio-pci` no longer leaves a host
  interface up on the VM's network.
- **DPU host node lifecycle:** `ovnkube-node` in DPU mode exits when the Node
  object it manages is deleted, rather than continuing to serve a node that no
  longer exists, and `ovnkube.sh` re-resolves the DPU's host node on container
  restart so a host reprovisioned under a new name is picked up instead of the
  stale one cached by the init container.
- **No-overlay DC-wide SNAT exemptions:** Preserve source addresses for
  configured DC-wide overlay subnets in DPU no-overlay deployments, kept in
  step with the LaunchLayer subnet configuration.

### Security and hardening

- **VTEP advertisement validation:** Validate VTEP addresses and detect
  conflicting claims across RouteAdvertisements sharing an underlay, so a
  malformed or conflicting node annotation can no longer advertise incorrect
  reachability and blackhole traffic meant for another node. A unique
  established advertiser is preserved, rejected node configurations are
  withdrawn, and unmanaged annotations are repaired regardless of field
  manager.
- **Management port security:** Pass `OVNKUBE_SECURE_MGMT_PORT` to
  `ovnkube-node`, enabled by default.
- **Leader election identity:** Key the lease on the pod UID rather than the
  node name. client-go treats a lease whose holder matches its own identity as
  already won, so a replacement pod scheduled onto the same node inherited a
  lease the outgoing process still held and both ran as leader until the old
  renew deadline expired.
- **Dependency and toolchain CVEs:** Build with Go 1.26.10 and then 1.26.11,
  for the `crypto/x509` fix for CVE-2026-111943 and the standard library fix
  for CVE-2026-97043. Raise `go-jose/v4` to v4.1.6 (CVE-2026-116585,
  CVE-2026-116586, CVE-2026-116587), `golang.org/x/net` to v0.62.0
  (CVE-2026-78661, CVE-2026-97045), and `klauspost/compress` to v1.20.4.

### Observability

- **Per-cluster alert isolation:** Add `global.clusterLabel`, defaulting to
  `cluster`, and include it in the aggregation and join clauses of the 16 alert
  rules that previously merged series from different clusters when several
  clusters forward metrics to one store. `OvnKubeClusterManagerNotRunning` no
  longer uses `absent()`, which only fired when no cluster had a cluster
  manager, and `OvnControllerGeneveTunnelCountIsLow` compares each node against
  its own cluster's node count. The dashboards take the same label, and
  `dashboard.clusterLabel.discoveryMetric` is flattened to
  `dashboard.clusterDiscoveryMetric`.
- **Pod creation latency alert:** Correct the comparison order of
  `OvnKubeIncreaseInPodCreationLatencyP100` so a rising latency fires it, and
  reduce the pending duration below the comparison window.
- **Dashboards and rule configuration:** Fix the `ovsdb-server` CPU panel unit
  and the NOT READY and UNAVAILABLE colours, and declare
  `rules.ovsNoOffloadIntraNodeBytes.filter` in `values.yaml`, which the
  template requires but neither the values nor the README documented.
- **Interconnect log volume:** Move the six loudest interconnect log sites to
  `V(6)`.

### Deployment and images

- **Readiness and liveness probes:** Bound every check in the `ovnkube-node`
  readiness script so a blackholed API VIP or resolver is reported by name
  instead of as a generic `command timed out`, honour `METRICS_IP` the way
  `ovnkube.sh` does when choosing the metrics address, and fix the IPv6
  bracketing that `ncat` and `curl` want in opposite forms, which made the
  probe unpassable on an IPv6 cluster. `timeoutSeconds` rises to 15 in the two
  charts that run the script, and the liveness script's CNI socket check gains
  a 3s limit.
- **OVN database persistence:** Mount the shared OVN DB volume at
  `/var/lib/ovn` for the NB and SB containers, so the database files survive
  container restarts and stay visible to `ovnkube-controller`.
- **DPU chart namespace:** Set `OVN_KUBERNETES_NAMESPACE` on the
  `ovnkube-node-dpu-host` and `ovnkube-node-dpu` charts. Neither did, so
  installing the host cluster into any namespace other than `ovn-kubernetes`
  left `ovnkube` writing its lease where its service account has no
  permissions — fatal on the host side, and a silent renewal failure on the
  DPU side that drove the node `NotReady`. Both default to `ovn-kubernetes`, so
  existing deployments are unaffected.
- **Subchart selection:** Select each component with `condition: tags.<name>`
  rather than the `dependencies[].tags` field, which Helm resolves only against
  the release-root `tags` map. The value names and defaults are unchanged and
  standalone installs are unaffected, but an umbrella chart passing root-level
  tags has to move them under the `ovn-kubernetes` key. Every tag is now
  required by `values.schema.json`, since an absent condition path leaves a
  dependency enabled.
- **Image digests:** Set all fourteen `image.sha256` entries to the digest the
  registry reports for the tag the chart ships. The 2.4.0, 2.4.1, and 2.4.2
  cuts each repointed `image.tag` and left the digest untouched, so installs
  setting `global.useImageDigest` pulled a digest belonging to another tag.
- **Chart package contents:** Drop `SECURITY.md`, the internal security triage
  metadata, and maintainer scripts from the packaged chart, and remove the
  dangling OVS D-Bus mount no container used.
- **Node initialisation:** Install `python3`, which `ovnkube-init.sh` needs to
  compute DPU network and gateway addresses, and derive a `/31` gateway from
  the node IP rather than from the network address, which is the gateway only
  when the node holds the odd address of the pair.
- **Generated chart files:** Bring `values.schema.json` back in step with its
  generator, teaching it to carry element types for lists whose default is
  empty, and regenerate `README-deployment.md`.

## [2.4.2]

Changes since 2.4.1 on `release-2.4`. The upstream baseline is unchanged —
`release-1.4` at tag `v1.4.0`, commit
[`d6a3812d1bc1c36bf24c7eda0e86e9b13d84907d`](https://github.com/ovn-kubernetes/ovn-kubernetes/commit/d6a3812d1bc1c36bf24c7eda0e86e9b13d84907d).

### Networking and IP management

- **Opt-in pod `hostPort` support:** Watch local pods in `ovnkube-node` and
  reconcile native nftables `hostPort` rules for the default primary network,
  on full nodes running a shared gateway. The feature is disabled by default so
  that ownership of the rules, between the container runtime and the CNI plugin,
  stays an explicit operator choice. Includes lifecycle and UDP conntrack
  cleanup, and is wired through configuration, Helm, and node startup.

### Security and hardening

- **Node subnet annotation ownership:** Ship a `ValidatingAdmissionPolicy` that
  restricts `k8s.ovn.org/node-subnets` to the `ovnkube-cluster-manager` service
  account, so a node credential able to write its own annotations can no longer
  reassign the pod subnet the cluster manager allocated to it.
- **Hybrid overlay subnet annotation ownership:** Extend that policy to
  `k8s.ovn.org/hybrid-overlay-node-subnet` when hybrid overlay is enabled.
  `global.hybridOverlaySubnetWriters` grants additional writers for deployments
  where another controller owns the annotation.
- **Restored subnet size validation:** Accept a restored node subnet only when
  its prefix length matches the fixed size the allocator manages, rather than
  only checking that the address falls inside the cluster subnet. A subnet
  annotation carrying a wider or narrower prefix is now ignored instead of
  being marked allocated.
- **Cross-network IPAMClaim reuse:** Prevent a pod from using an `IPAMClaim`
  that belongs to a different network, which could otherwise produce duplicate
  IP assignments and the connectivity failures that follow.
- **Cross-namespace IPAMClaim reuse:** Prevent a pod from using another
  namespace's `IPAMClaim`, even when both namespaces attach to the same
  network, keeping persistent IP allocations inside a namespace boundary.
- **Observability dependency CVEs:** Bump the `obs-as-code` Go modules.

### Deployment and images

- **Configurable ovnkube-control-plane resources:** Replace the hardcoded 100m
  CPU and 300Mi memory requests on the `ovnkube-cluster-manager` container with
  an `ovnkubeControlPlane.resources` value, defaulted to those same requests.
  Note that the container now also carries 2 CPU and 3Gi limits it previously
  ran without.

## [2.4.1]

Changes since 2.4.0 on `release-2.4`. The upstream baseline is unchanged —
`release-1.4` at tag `v1.4.0`, commit
[`d6a3812d1bc1c36bf24c7eda0e86e9b13d84907d`](https://github.com/ovn-kubernetes/ovn-kubernetes/commit/d6a3812d1bc1c36bf24c7eda0e86e9b13d84907d).

### Removals

- **`k8s.ovn.org/port-security-info` annotation:** Remove the annotation that
  let a pod write arbitrary extra IPs into the `port_security` column of its
  own logical switch port, which allowed any workload able to edit its own
  annotations to widen the source addresses OVN accepted from it. Skip-IPAM
  ports keep MAC-only port security.
- **`k8s.ovn.org/skip-spoofchk-on-networks` annotation:** Remove the annotation
  that let a pod disable port security outright on its own logical switch
  ports. Port security is now set unconditionally on pod LSPs.
- **firewalld support:** Stop reconciling endpoint slices to configure host
  firewall rules through `firewall-cmd`.

### Security and hardening

- **RouteAdvertisements network values:** Validate the route targets, VNIs, and
  VRF names of selected networks before writing them into generated
  `FRRConfiguration`s, so a value carrying a newline or extra tokens can no
  longer inject FRR configuration lines onto nodes.
- **RouteAdvertisements neighbor keys:** Validate each neighbor address or
  interface name read from the source `FRRConfiguration`, rejecting the whole
  RouteAdvertisements when one fails.
- **RouteAdvertisements target VRF:** Validate the target VRF once before any
  router is matched, requiring a Linux interface name when set and not `auto`.
- **Stale FRR config on rejected input:** Delete `FRRConfiguration`s generated
  before input validation existed when validation now fails, so a rejected
  value cannot keep advertising networks after an upgrade.
- **CNI device info file:** Accept `runtimeConfig.CNIDeviceInfoFile` only as a
  regular file under `/var/run/k8s.cni.cncf.io/devinfo`, and stop returning the
  JSON parse error, which could surface file content in pod events.
- **Bearer token redaction:** Redact the Kubernetes token from the parsed
  config logged at default verbosity, which previously printed a token set
  inline in the config file verbatim.
- **Seccomp profile:** Set `RuntimeDefault` at the pod `securityContext` level
  for `ovnkube-control-plane` and `ovnkube-single-node-zone`.
- **Kubernetes APT key verification:** Check the downloaded key against the
  expected fingerprint before creating the trusted keyring in NVIDIA image
  builds, rejecting mismatched keys before adding the repository.
- **Dependency CVEs:** Raise `golang.org/x/net` to v0.60.0 and build with Go
  1.26.9, addressing the HTTP/2 flow-control double refund (CVE-2026-78663)
  and the `net/http` CONNECT fix (CVE-2026-94439). Bump the observability
  chart's crypto modules for the same class of finding.

### Networking and IP management

- **NetworkQoS ipBlock:** Build the OVN QoS match from parsed `cidr` and
  `except` values rather than splicing them in verbatim, and mark the
  NetworkQoS `NotReady` when a value is not an IP or CIDR.

### Deployment and images

- **Configurable ovnkube-node resources:** Replace the hardcoded 100m CPU and
  300Mi memory requests on each container of the `ovnkube-single-node-zone`
  daemonset with per-container `resources` values, defaulted to each
  container's own footprint. Note that the containers now also carry CPU and
  memory limits they previously ran without.
- **DOCA release pinning:** Pin the DOCA repository to 3.5.0 rather than
  following `latest`, and default the OVS packages to revision 3.5.0052-1
  while still honouring `OVS_VER`.
- **Third-party licenses in the NVIDIA image:** Copy the checked-in notices
  into the final runtime stage, matching the standard Ubuntu image.

## [2.4.0]

Changes in downstream `release-2.4` compared with upstream `release-1.4` at
tag `v1.4.0`, commit
[`d6a3812d1bc1c36bf24c7eda0e86e9b13d84907d`](https://github.com/ovn-kubernetes/ovn-kubernetes/commit/d6a3812d1bc1c36bf24c7eda0e86e9b13d84907d).

### Networking and IP management

- **AdminPBR:** Add the `AdminPolicyBasedRoute` API to control routing on
  Layer3 networks using node, namespace, and pod selectors, including support
  for interconnect deployments.
- **IP reservation:** Add the `IPReservation` API to reserve a requested
  number of IPv4 or IPv6 addresses from Layer2 or localnet networks and report
  the reserved addresses in resource status.
- **Variable-size node subnets:** Select IPv4 node subnets from configured
  ranges according to the node's requested pod capacity.
- **Uplink and VRF-Lite extensions:** Discover default gateways from VRF
  routing tables and multipath routes, retry discovery while gateways are
  missing, and preserve eligible interface routes across VRF attachment and
  release. Reconcile active network gateways when Uplink configuration
  changes and report programming results through `UplinkState` conditions.
- **Custom SNAT:** Allow destination-based selection of IPv4 source NAT
  addresses through custom gateway SNAT rules.
- **Local-first service routing:** Add the
  `k8s.ovn.org/prefer-local-with-fallback` service annotation to prefer selected
  local endpoints, such as node-local DNS, and use fallback endpoints when
  matching local endpoints are unavailable.
- **Istio Ambient integration:** Add routing policies for kubelet health
  checks using configurable Istio Ambient SNAT addresses.
- **Localnet bridge mappings:** Manage per-network OVS bridge mappings from
  node-local configuration, including network-name prefix matching and
  compatibility with downstream bridge naming.
- **VM network metadata:** Include network MTU in pod annotations and retain
  directly connected route information for localnet networks, allowing VM
  sidecars to configure guest networking.

### Security, DPU, and runtime integration

- **Management-port protection:** Add optional ingress ACLs that restrict
  direct access to management ports while allowing configured EgressIP
  health checks.
- **DPU device lifecycle:** Reconfigure management ports when host device
  assignments change and resolve VF representors using the host PF's MAC
  address.
- **DPU authentication:** Extend deployment support for token-file
  authentication, including externally issued SPIFFE/SPIRE tokens. Add an
  explicit option to disable Kubernetes API certificate verification for
  deployments without the API server's CA certificate.
- **Kata Containers:** Generate Directly Attachable Network (DAN)
  configuration for VFIO devices, including MAC and IP addresses, MTU, and
  routes.

### Observability

- **OpenTelemetry tracing:** Add optional tracing for pod networking, CNI,
  controller operations, and DPU connection annotations, with OTLP export
  and configurable sampling.
- **Native OVS metrics:** Support collecting native OVS metrics through
  `metrics/show` in interconnect deployments.
- **Observability chart:** Package ServiceMonitors, Prometheus alert rules,
  and Grafana dashboards in a separate `ovn-k8s-observability` Helm chart.

### Deployment

- **Ubuntu image:** Add an Ubuntu-based container image with NVIDIA OVN and
  OVS packages.
- **Helm packaging:** Replace the legacy Helm tree with downstream charts
  under `charts/`, including deployment support for DPU and DPU-host modes.
