# Security Policy: ovn-kubernetes

NVIDIA is dedicated to the security and trust of our software products and services,
including all source code repositories managed through our organization.

If you need to report a security issue, please use the appropriate contact points
outlined below. **Please do not report security vulnerabilities through public
issues or pull requests.**

## Reporting a Vulnerability

If you discover a potential security vulnerability, please **do not open a public issue.**

* **Web (preferred):** [NVIDIA Vulnerability Disclosure Program](https://www.nvidia.com/en-us/security/)
  The preferred method for reporting security concerns across all NVIDIA products.
* **E-Mail:** [psirt@nvidia.com](mailto:psirt@nvidia.com)
  - We encourage you to use the following PGP key for secure email communication:
    [NVIDIA public PGP Key](https://www.nvidia.com/en-us/security/pgp-key)
* **GitHub:** Use this repository's **Security** tab > **Report a vulnerability**
  to submit a private report directly.

Please include the following information:
- Project name and the branch, tag, or commit the issue was found on
- Type of vulnerability
- Step-by-step reproduction instructions
- Proof-of-concept code (if available)
- Impact assessment

**Detailed reports help NVIDIA evaluate and address issues faster.**

NVIDIA's Product Security Incident Response Team (PSIRT) will:
1. Acknowledge receipt of your report
2. Validate the vulnerability and assess severity
3. Develop and test a fix
4. Publish a security bulletin as appropriate

While NVIDIA does not currently operate a public bug bounty program, externally
reported issues will receive acknowledgement under our coordinated vulnerability
disclosure policy.

For ongoing security updates, subscribe to notifications at the
[NVIDIA Product Security](https://www.nvidia.com/en-us/security/) portal.

Dependency-update automation may also open pull requests for vulnerable
libraries; that process complements—but does not replace—PSIRT coordinated
disclosure.

## Security Architecture & Context

OVN-Kubernetes is a Kubernetes CNI networking platform that programs Open Virtual
Networking (OVN) and Open vSwitch (OVS) to provide pod networking, Services,
NetworkPolicy / AdminNetworkPolicy enforcement, EgressIP / EgressFirewall /
EgressService / NetworkQoS, User Defined Networks (UDN) and Cluster Network
Connect, BGP route advertisements / EVPN / no-overlay routing, hybrid overlay
(VXLAN), KubeVirt support, distributed tracing, and DPU/SmartNIC offload modes.

This software operates primarily at the **Service / infrastructure platform** level
(cluster networking control plane and node dataplane agents), with supporting
**CLI** tooling (`ovnkube-trace`, `ovn-kube-util`, `ovnkube-observ`) and
**Library** packages under `go-controller/pkg/`. Its primary security
responsibility is to protect the integrity and isolation of cluster network
policy and dataplane state: who can attach workloads to networks, how traffic is
filtered and routed, and how privileged OVN/OVS configuration is applied on nodes.

**Repository Exposure Classification:** Public.
Basis: the project is distributed as open source on GitHub under Apache-2.0;
this document is written to public-safe detail.

**Service Exposure Classification:** External / Regulated (high confidence).
Basis: user-confirmed; externally distributed open-source infrastructure
software that forms a production security boundary — it enforces network
isolation and policy, holds cluster-scoped Kubernetes RBAC, runs privileged
hostNetwork node agents, and handles network identity, certificates, and
policy state in downstream clusters.

### Key components and interfaces

| Component | Path / binary | Role |
|-----------|---------------|------|
| `ovnkube` (cluster-manager / controller / node modes) | `go-controller/cmd/ovnkube` | Watches Kubernetes objects; programs OVN NB/SB via libovsdb; configures node gateway, CNI server, metrics |
| `ovn-k8s-cni-overlay` | `go-controller/cmd/ovn-k8s-cni-overlay` | CNI plugin invoked by the container runtime; forwards ADD/DEL/CHECK to the node CNI HTTP-over-Unix-socket server |
| `ovnkube-identity` | `go-controller/cmd/ovnkube-identity` | Validating admission webhooks (node/pod status) and CSR approver for node client certificates |
| Admission logic | `go-controller/pkg/ovnwebhook` | Restricts which annotations node identities may set on pods and nodes |
| CSR approver | `go-controller/pkg/csrapprover` | Approves kube-apiserver client CSRs matching configured acceptance conditions |
| Metrics server | `go-controller/pkg/metrics` | Prometheus `/metrics` (optional TLS via `go-controller/pkg/tls`, optional loopback pprof) |
| Distributed tracing | `go-controller/pkg/tracing` | OpenTelemetry OTLP/gRPC span export; consumes propagated trace context from a pod annotation |
| Route advertisements / BGP / EVPN | `go-controller/pkg/clustermanager/{routeadvertisements,managedbgp,nooverlay}` | Renders `FRRConfiguration` for frr-k8s; advertises pod, UDN, and EgressIP prefixes; optional no-overlay mode |
| Cluster Network Connect | `go-controller/pkg/clustermanager/networkconnect`, `go-controller/pkg/ovn/controller/networkconnect` | Controlled connectivity between otherwise isolated User Defined Networks |
| Hybrid overlay | `go-controller/hybrid-overlay` | VXLAN bridging for mixed Windows/Linux clusters |
| Observability | `go-controller/pkg/observability`, `go-controller/cmd/ovnkube-observ`, `charts/ovn-k8s-observability` | OVN sampling / ACL observability configuration and dashboards |
| Helm charts / images | `charts/ovn-kubernetes`, `dist/images` | Deployment surfaces (hostNetwork, privileged, NET_ADMIN, RBAC) |

### Trust boundaries

1. **Kubernetes API server** — primary control-plane trust boundary. Controllers
   authenticate with ServiceAccount tokens or per-node client certificates
   (`StartNodeCertificateManager` in `go-controller/pkg/util/kube.go`).
2. **Admission webhooks** (`ovnkube-identity` over TLS, port 9443 in the shipped
   chart) — enforce that only authorized node identities can mutate OVN-related
   annotations on `nodes/status` and `pods/status`.
3. **OVN northbound / southbound databases** — libovsdb clients in
   ovnkube-controller write logical network state; compromise of NB/SB or the
   connecting channel yields cluster-wide dataplane control.
4. **Node host / OVS** — `ovnkube-node` and `ovs-node` run with `hostNetwork`
   and elevated capabilities (`privileged` / `NET_ADMIN`) to manage bridges,
   nftables/iptables, VRFs, routes, and encapsulation.
5. **CNI Unix domain socket** — private root-owned HTTP API between the CNI shim
   and `ovnkube-node` (`go-controller/pkg/cni/cniserver*.go`); not a public API.
6. **Metrics / healthz / loopback pprof** — HTTP listeners configurable via
   `--metrics-bind-address`, `--ovn-metrics-bind-address`,
   `--healthz-bind-address`, `--metrics-enable-pprof`, and `--metrics-pprof-port`.
7. **Underlay routing peers** — when route advertisements, EVPN, or no-overlay
   mode are enabled, `FRRConfiguration` objects drive an frr-k8s BGP speaker that
   exchanges routes with the physical fabric.
8. **OTLP trace collector** (optional) — outbound gRPC channel from ovnkube to a
   configured collector endpoint, carrying span metadata about cluster objects.
9. **EgressIP gRPC health check** (optional) — node-to-node channel configured
   via `egressip-node-healthcheck-port` and the EgressIP health-check TLS settings.

## Threat Model

The following scenarios represent the primary security concerns for this project
(including auxiliary and support code), ordered by severity and likelihood:

1. **Compromise of privileged node agents:** An attacker who can run code in
   `ovnkube-node` or `ovs-node` pods, or who exploits a vulnerability in those
   processes, inherits `hostNetwork`, `privileged` / `NET_ADMIN`, and broad
   hostPath mounts declared in
   `charts/ovn-kubernetes/charts/{ovs-node,ovnkube-single-node-zone,ovnkube-single-node-zone-dpu,ovnkube-node-dpu-host}/templates/daemonset.yaml`.
   This enables host takeover, traffic interception, or persistent OVS/OVN
   reconfiguration.

2. **Admission webhook or CSR approver abuse (`ovnkube-identity`):** Bypass or
   misconfiguration of `pkg/ovnwebhook` node/pod admission, or overly permissive
   CSR acceptance conditions in `pkg/csrapprover`, could let a non-node principal
   set OVN IP/chassis/zone annotations or obtain node-equivalent client
   certificates, undermining per-node network identity. The chart template
   `charts/ovn-kubernetes/charts/ovnkube-identity/templates/webhook.yaml`
   generates a self-signed CA and certificate that are **not renewed** and are
   documented as development-only, and it notes that validating the `/status`
   subresource does not stop other principals from changing the same annotations
   through the parent resource.

3. **OVN NB/SB or OVS control-channel compromise:** Unauthorized write access to
   the OVN databases, or to the libovsdb sessions used by ovnkube-controller and
   ovnkube-node, can rewrite ACLs, logical routers and switches, load balancers,
   and port bindings — defeating NetworkPolicy, AdminNetworkPolicy, and
   EgressFirewall isolation cluster-wide.

4. **Credential exposure and over-privileged Kubernetes RBAC escalation:**
   ClusterRoles in `charts/ovn-kubernetes/templates/rbac-ovnkube-node.yaml`,
   `charts/ovn-kubernetes/charts/ovnkube-control-plane/templates/rbac-ovnkube-cluster-manager.yaml`,
   and `charts/ovn-kubernetes/charts/ovnkube-identity/templates/rbac-ovnkube-identity.yaml`
   grant broad watch/patch/update on core and `k8s.ovn.org` resources — including
   `secrets` — plus CSR create and approve paths, bound to the `system:ovn-nodes`
   group. Theft of these ServiceAccount tokens or a node client certificate — via
   a mounted volume, a log leak, or a compromised node agent — enables
   cluster-wide network reconfiguration and further privilege escalation.
   Credentials supplied to the chart compound this:
   `charts/ovn-kubernetes/templates/ovnkube-setup.yaml`
   writes the optional `k8sToken` and `k8sCACertData` values into the `ovn-config`
   **ConfigMap** rather than a Secret, so they are unencrypted at rest and
   readable by anyone holding `get configmaps` in that namespace, and the same
   template renders a `kubernetes.io/dockerconfigjson` Secret from
   `global.dockerConfigSecret`.

5. **Route advertisement and no-overlay dataplane exposure:** The controllers in
   `pkg/clustermanager/routeadvertisements`, `pkg/clustermanager/managedbgp`, and
   `pkg/clustermanager/nooverlay` translate `RouteAdvertisements` and related CRDs
   into `FRRConfiguration` for an frr-k8s speaker. An over-scoped or malicious
   advertisement selector can leak pod, UDN, or EgressIP prefixes to unintended
   peers, and a hostile or misconfigured BGP peer can inject routes that
   blackhole or redirect cluster traffic. The chart can widen the blast radius:
   `global.advertiseDefaultNetwork` and `global.advertiseAllTenantCUDNs` cause
   `charts/ovn-kubernetes/templates/ovnkube-setup.yaml` to create
   `RouteAdvertisements` (and an `Uplink`) with empty node selectors, so a
   mismatched network or `frrConfigurationSelector` label advertises to every
   peer that matches. In
   no-overlay mode there is no encapsulation layer, so the underlay sees and
   routes pod addresses directly.

6. **Network segmentation bypass across User Defined Networks:** Isolation
   between UDNs depends on correct OVN topology programming in
   `pkg/ovn/controller/networkconnect`, `pkg/clustermanager/networkconnect`, the
   UDN controllers, and the NetworkAttachmentDefinitions rendered by the charts.
   An over-permissive `ClusterNetworkConnect`, an unintended
   NetworkAttachmentDefinition, a gateway or uplink misprogramming
   (`pkg/node/gateway_*.go`, `pkg/uplink`), or a hybrid-overlay VXLAN endpoint
   mismatch can create cross-tenant east-west or north-south paths that policy
   does not cover.

7. **CNI Unix socket exposure or path confusion:** The CNI server uses a
   root-owned Unix socket whose parent directory is re-created with `0700` on
   every node restart (`pkg/cni/cniserver.go`). Incorrect socket ownership or
   permissions, a symlink or path race on the socket directory, or a flaw in
   ADD/DEL handling could let an unprivileged local process attach interfaces,
   divert pod traffic, or disrupt networking for arbitrary pods.

8. **Telemetry and debug surface abuse:** `pkg/metrics` serves Prometheus metrics
   with no application-layer authentication, and Go pprof plus
   `/debug/flags/v` on a loopback listener when pprof is enabled (see
   [Metrics and debug endpoints](#metrics-and-debug-endpoints)). Separately,
   `pkg/tracing` exports spans over OTLP/gRPC to a configured collector; TLS is
   opt-in (`use-tls`) and certificate verification can be disabled
   (`tls-insecure-skip-verify`), so an unencrypted or unverified channel can leak
   cluster topology metadata or be impersonated. Trace context is read from an
   attacker-influenceable pod annotation (`tracing.k8s.io/traceparent`), which a
   workload can use to forge or inflate trace data.

9. **Deployment supply chain and admission bypass:** Image references built by
   `ovn-kubernetes.generateContainerImageString` in
   `charts/ovn-kubernetes/templates/_helpers.tpl` are tag-based unless
   `global.useImageDigest` is set, so a mutated tag in the configured registry
   silently changes what runs with host privileges. The maintainer script
   `charts/ovn-kubernetes/scripts/sync-crds.sh` clones upstream CRD repositories
   over the network, so a poisoned upstream tag can introduce malicious CRD
   definitions. Finally,
   `charts/ovn-kubernetes/templates/ovnkube-setup.yaml` labels the chart's two
   namespaces so that cluster admission-policy and image-trust webhooks skip
   them — necessary for a CNI that must start before those controllers exist,
   but it removes that defense from every workload scheduled into those
   namespaces.

## Critical Security Assumptions

- **Kubernetes API authentication and authorization are correctly configured.**
  Controllers trust objects and SubjectAccessReview outcomes from the API
  server; they do not independently re-authenticate end users.
- **Admission webhooks remain registered and reachable.** NetworkPolicy,
  webhook configuration, or `failurePolicy` changes that disable
  `ovnkube-identity` weaken annotation and identity guarantees.
- **Chart-generated webhook certificates are not used as-is in production.**
  The shipped Helm template issues a self-signed, non-renewing CA and
  certificate for development; production deployments must supply managed,
  renewable TLS material.
- **CNI socket filesystem permissions hold.** The design assumes the Unix
  domain socket and its directory remain root-owned with mode `0700` and are
  not shared with untrusted workloads.
- **OVN/OVS management planes are on a trusted or separately authenticated
  network.** Operators are expected to deploy TLS and ACL controls for NB/SB and
  restrict who can reach database ports; the Go controllers assume database
  peers are legitimate once connected with the configured credentials.
- **Host kernel, container runtime, and device plugins enforce isolation.**
  `NET_ADMIN` and privileged capabilities are required for dataplane setup; the
  project assumes the node OS, seccomp/AppArmor/SELinux policy where present,
  and the container runtime correctly bound those privileges to the intended pods.
- **The underlay and physical fabric are operated as a trusted network.**
  Geneve, VXLAN, and GRE encapsulation provide no confidentiality or
  authenticity on their own. OVN IPsec can be turned on through the
  `ENABLE_IPSEC` environment variable, which sets the corresponding OVN
  northbound global flag, but these charts do not provision IPsec keys or
  certificates — operators must supply and rotate that material themselves.
- **BGP peers and the frr-k8s speaker are trusted and independently filtered.**
  The route-advertisement controllers generate `FRRConfiguration`; they do not
  authenticate peers or validate learned routes. Inbound and outbound route
  policy, peer authentication, and prefix limits are expected to be enforced by
  frr-k8s configuration and the fabric.
- **Metrics, healthz, and pprof listeners are network-restricted by the
  operator** (NetworkPolicy, host firewall, or non-routable bind addresses) when
  authentication is not provided by the metrics server itself.
- **The tracing collector endpoint is trusted and reachable over a protected
  path.** Span export carries cluster object metadata; propagated trace context
  from pod annotations is untrusted input and is used only for correlation, never
  for authorization.
- **Operators supply chart credentials through protected channels.** Registry
  authentication and any `k8sToken` or CA data are expected to arrive via CI
  secrets, sealed secrets, or an external secret store rather than plaintext in
  version control, and to be rotated. Because `k8sToken` is rendered into a
  ConfigMap, projected ServiceAccount tokens are preferable wherever the
  deployment mode allows them.
- **Render-time cluster lookups are integrity-protected.**
  `charts/ovn-kubernetes/templates/_helpers.tpl` reads the CoreDNS Service and a
  GitOps-managed zone ConfigMap
  to derive the cluster DNS address and gateway next-hop. Anyone who can write
  those objects can influence the rendered gateway configuration of every node;
  the chart assumes cluster RBAC prevents that.
- **The dependency and image supply chain is verified by the consumer.** Go
  module pins live in `go-controller/go.mod` with a checked-in `vendor/` tree,
  and container images are built from `dist/images/Dockerfile*`. Digest pinning
  through `global.useImageDigest` is an operator choice, not a chart-enforced
  default. Downstream consumers are expected to verify provenance of the
  artifacts they deploy.

## Trust Model

- **Cluster operators** are fully trusted to deploy Helm values, RBAC, and
  network underlay controls.
- **`ovnkube-identity` and the CSR approver** are trusted to gate node client
  certificate issuance and annotation mutations for OVN node identities.
- **Per-node `ovnkube-node` identity** is trusted only for the annotations and
  resources scoped to that node, and only while the webhooks are active.
- **Workload pods are untrusted from a networking perspective.** Isolation
  depends on OVN ACL translation of NetworkPolicy, ANP/BANP, and the Egress*
  CRDs, plus correct CNI plumbing.
- **Multi-tenancy is namespace- and policy-based** within a shared cluster
  network fabric. UDN and network segmentation features add logical isolation
  but still rely on the same control-plane trust assumptions.
- **The underlay fabric and its BGP speakers** are treated as a peer trust
  domain, not as an attacker-controlled network.

## Deployment Assumptions

This policy covers the Helm charts under `charts/` as well as the Go code; the
charts have no separate security policy.

- Deployed inside Kubernetes via the Helm charts under `charts/ovn-kubernetes`,
  with `hostNetwork: true` for node and control-plane networking components.
  Which components are installed is selected by the chart's `tags` values
  (`ovs-node`, `ovnkube-crds`, `ovnkube-identity`, `ovnkube-control-plane`,
  `ovnkube-node-dpu-host`, and the single-node-zone variants).
- Helm install and upgrade are performed by privileged automation or cluster
  operators with authority to create ClusterRoles, CRDs, and privileged
  DaemonSets.
- Default charts grant elevated Linux capabilities on the nodes that run the
  CNI; those nodes should be treated as part of the cluster's trusted computing
  base.
- Monitoring may scrape the Prometheus endpoints; scrapers must be treated as a
  sensitive audience.
- Optional features — BGP route advertisements, EVPN, no-overlay, Cluster
  Network Connect, hybrid overlay, DPU modes, OTLP tracing, and the EgressIP
  gRPC health check — expand the attack surface and should be enabled only with
  matching operational controls.

## Out of Scope

- Vulnerabilities solely in upstream Kubernetes, container runtimes, OVN/OVS
  binaries, frr-k8s/FRR, or the physical underlay that are not reachable through
  ovn-kubernetes code paths.
- Misconfiguration that intentionally disables webhooks, runs the chart's
  development self-signed webhook certificates in production, or exposes
  metrics and pprof cluster-wide without mitigation — these are operator risks
  called out above.
- Denial-of-service against the Kubernetes API server or etcd itself.
- Social engineering or physical access to nodes.

## Dependency Security

The primary runtime language is Go (`go-controller/go.mod`, Go 1.26 toolchain)
with vendored dependencies. Security-relevant libraries include Kubernetes
client-go / apiserver / controller-runtime (RBAC, webhooks, certificates),
gRPC and `advancedtls`, the OpenTelemetry SDK and OTLP exporter, the Prometheus
client, libovsdb, CNI plugins, netlink, iptables and knftables, and gorilla/mux
for the CNI HTTP server. Container images are built from
`dist/images/Dockerfile*`. The Helm chart additionally vendors third-party CRDs
under `charts/ovn-kubernetes/charts/ovnkube-crds/crds/`, refreshed from upstream
repositories by `charts/ovn-kubernetes/scripts/sync-crds.sh`. Operators should
track CVEs in these dependencies and in the base images through their own
scanning and update processes.

## Metrics and debug endpoints

The metrics server can optionally expose Go debug endpoints when started with
`--metrics-enable-pprof`. The Go flag defaults to off, but the shipped container
image turns pprof on by default (`ovnkube.sh` defaults `OVN_METRICS_ENABLE_PPROF`
to `true`, and some components enable it unconditionally). These endpoints are
unauthenticated and powerful:

- `/debug/pprof/*` exposes heap, goroutine, and CPU profiles (potential information
  disclosure and, via `/debug/pprof/profile`, a CPU-based denial-of-service vector).
- `/debug/flags/v` accepts `PUT` requests that change the klog verbosity level at
  runtime, which could be abused to flood logs (disk exhaustion) or suppress them.

To keep these off the network even when the metrics server binds to a routable
address, the `/debug/*` handlers are not served on the metrics port. Instead they
are served on a dedicated listener bound only to loopback (`127.0.0.1` and `::1`).
Each component listens on a fixed loopback port (`ovnkube.sh` assigns a distinct
port per component); it can be overridden with `--metrics-pprof-port` (or the
`pprof-port` config option).

Because the ovnkube components run with `hostNetwork: true`, this loopback is the
node's host loopback. The endpoints are therefore reachable, without
authentication, by any process on the node — including other host-network pods —
as well as via `kubectl exec` and `kubectl port-forward`. They are not reachable
from the pod network or on the routable metrics address. This narrows the
exposure; it is not an authenticated or RBAC-gated boundary. Leave pprof disabled
in production unless it is actively needed for debugging.
