# Changelog

This changelog summarizes major changes in this repository compared with
[upstream OVN-Kubernetes](https://github.com/ovn-kubernetes/ovn-kubernetes).
Each release identifies the upstream branch, release tag, and commit used as
its comparison baseline. Small fixes and internal refactoring are omitted.
Downstream releases are numbered `2.MINOR.PATCH`, where `MINOR` tracks the
upstream minor release the drop is based on.

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
