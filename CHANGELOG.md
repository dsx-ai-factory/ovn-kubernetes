# Changelog

This changelog summarizes major changes in this repository compared with
[upstream OVN-Kubernetes](https://github.com/ovn-kubernetes/ovn-kubernetes).
Each release identifies the upstream branch, release tag, and commit used as
its comparison baseline. Small fixes and internal refactoring are omitted.
Downstream releases are numbered `2.MINOR.PATCH`, where `MINOR` tracks the
upstream minor release the drop is based on.

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
