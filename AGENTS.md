# AGENTS.md — OVN-Kubernetes

Project context for AI coding agents. See [agents.md](https://agents.md/) for the open standard.

## Rules for Agents

This repository is a **read-only, point-in-time snapshot** that accepts no pull
requests — see [README.md](README.md) and [CONTRIBUTING.md](CONTRIBUTING.md).
A change meant for the project itself belongs
[upstream](https://github.com/ovn-kubernetes/ovn-kubernetes). Do not prepare a
pull request against this repository; it cannot be merged.

### In scope

- Reading and explaining the code, and tracing behaviour down through the
  OVN-Kubernetes, OVN, and OVS layers.
- Local changes for debugging, building, or vendoring this tree into a
  downstream product.
- Fixing a defect in this snapshot once the equivalent fix has gone upstream,
  so the two trees do not diverge further.
- Updating documentation under `docs/` that describes this snapshot, and the
  `AGENTS.md` files themselves.

### Never do these

- **Never commit secrets.** No kubeconfigs, bearer tokens, API keys,
  certificates, private keys, `.env` files, or registry credentials. Read
  configuration from environment variables instead. `contrib/kind.sh` writes a
  kubeconfig to `$KUBECONFIG` (usually under `$HOME`) — it must never enter the
  tree. When copying output from a live cluster into a commit, an issue, or a
  test fixture, scrub real node names, hostnames, and IP addresses first.
- **Never hand-edit a generated file.** Regenerate it:

  | Generated path | Regenerate with |
  |---|---|
  | `go-controller/pkg/{nbdb,sbdb,vswitchd}/` | `make modelgen` |
  | `go-controller/pkg/crd/*/*/apis/`, `zz_generated.deepcopy.go` | `make codegen` |
  | `charts/ovn-kubernetes/charts/ovnkube-crds/crds/*.yaml` | `make codegen` (commit these too) |
  | `go-controller/**/mocks/` | `make mocksgen` |
  | `go-controller/vendor/` | `go mod vendor` |
  | `LICENSES/` | `make third-party-licenses` |
  | `charts/ovn-kubernetes/values.schema.json` | `python3 scripts/gen-values-schema.py > values.schema.json` from the chart root |
  | `charts/ovn-k8s-observability/README.md` | `make helm-docs` in that chart |

- **Never rename the Go module path.** `go-controller/go.mod` deliberately
  keeps upstream's path so this tree stays a drop-in replacement. Consumers
  vendor it or use a `replace` directive.
- **Never silence a linter to make a change pass** — no new `//nolint`, and no
  edits to `.golangci.yml` exclusions. Fix the finding instead.
- **Never drop the DCO sign-off** (`git commit -s`) or the SPDX header on a new
  `.go`, `.sh`, `.py`, `Makefile`, or `Dockerfile*` file.
- **Never make an incompatible CRD API change.** See
  [`go-controller/pkg/crd/AGENTS.md`](go-controller/pkg/crd/AGENTS.md).

### Verify before proposing a change

Run these from `go-controller/`. Only `build`, `test` and `lint` also exist at
the repository root; `gofmt` and the `verify-*` targets do not.

```bash
make lint                       # golangci-lint, pinned version
make gofmt                      # gofmt + gci import ordering
make build                      # all seven binaries
make test                       # unit tests, with -race (`make check` is the same target)
make verify-codegen             # fails if generated CRD code is stale
make verify-mocksgen            # fails if mocks are stale
make verify-go-mod-vendor       # fails if vendor/ is out of sync
make verify-third-party-licenses
```

`make lint` and `make test` must both pass. Run the `verify-*` target that
matches whatever you regenerated.

### Tools and environment

- **Lint and unit tests run inside a container by default.** The Makefile
  probes for `docker`; override with `CONTAINER_RUNTIME=podman`. The flag is
  inverted — `CONTAINER_RUNNABLE=0` means a runtime *was* found, and
  `CONTAINER_RUNNABLE=1` forces the native path. CI sets `1` because
  `docker run -it` fails without a TTY.
- **golangci-lint is version-pinned** in `go-controller/hack/lint.sh`, with the
  v2-schema config in `go-controller/.golangci.yml`. Do not run a different
  version — results will not match CI. `make lint-fix` applies fixable findings.
- **mockery is version-pinned** in `go-controller/Makefile` and installed under
  `go-controller/_output/`; configuration is in `go-controller/.mockery.yaml`.
  Do not install mockery globally.
- **The build is Linux-only in practice.** `pkg/node/nftables` and
  `containernetworking/plugins/pkg/ns` do not compile on macOS. Cross-build
  with `GOOS=linux make build`.
- **Unit tests need the `vrf` kernel module** (`sudo modprobe vrf`). Narrow a
  Ginkgo run with `GINKGO_FOCUS="..." make check`.
- **E2E tests need a cluster.** Bring one up with `contrib/kind.sh`; see
  [`docs/developer-guide/local_testing_guide.md`](docs/developer-guide/local_testing_guide.md).

### Code conventions

Component-specific rules live in nested files — read the one closest to the
code you are changing:

| File | Covers |
|---|---|
| [`go-controller/AGENTS.md`](go-controller/AGENTS.md) | binaries, component boundaries, Go conventions (logging, error wrapping, locking, dual-stack, comments) |
| [`go-controller/pkg/AGENTS.md`](go-controller/pkg/AGENTS.md) | package map, network topologies, OVN/OVS database rules |
| [`go-controller/pkg/crd/AGENTS.md`](go-controller/pkg/crd/AGENTS.md) | CRD API conventions, kubebuilder markers, validation tests |

Three linter-enforced conventions catch agents out most often.

**Import aliases are mandatory.** `importas` runs with `no-unaliased: true`, so
write the alias even where Go would infer a usable name — and leave the
libovsdb `ovsdb` package unaliased:

```go
// Preferred
import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/ovn-kubernetes/libovsdb/ovsdb"
)

// Rejected by `make lint`
import (
	v1 "k8s.io/api/core/v1"                          // wrong alias
	"k8s.io/apimachinery/pkg/apis/meta/v1"           // alias missing
	ovsdb "github.com/ovn-kubernetes/libovsdb/ovsdb" // must not be aliased
)
```

The full alias list is in `go-controller/.golangci.yml`.

**Import groups have a fixed order**, applied by `gci`: standard library,
other third party, `k8s.io`/`sigs.k8s.io`, `github.com/ovn-org`,
`github.com/ovn-kubernetes`, this module, then dot-imports. `make gofmt`
rewrites them for you.

**Join host and port with `net.JoinHostPort`.** The `nosprintfhostport` linter
rejects the `Sprintf` form because it produces an unparseable address for IPv6
literals, and this codebase is dual-stack throughout:

```go
// Preferred — yields "[::1]:6443"
addr := net.JoinHostPort(host, strconv.Itoa(port))

// Rejected — yields "::1:6443"
addr := fmt.Sprintf("%s:%d", host, port)
```

## Project Overview

OVN-Kubernetes is a network plugin written according to CNI Spec that provides
networking for Kubernetes clusters with Open Virtual Network (OVN) and
Open vSwitch (OVS) at its core.

| Feature | Description |
|---------|-------------|
| Pod Networking | Pod-to-pod connectivity via OVN logical switches and routers |
| IPAM | IP address management for pods and networks |
| Services | Kubernetes Services implemented as OVN load balancers |
| Endpoint Slices | Scalable endpoint tracking for services |
| NetworkPolicy | Kubernetes NetworkPolicy enforcement via OVN ACLs |
| AdminNetworkPolicy | Cluster-scoped network policy (ANP/BANP) |
| EgressIP | Source IP control for egress traffic |
| EgressFirewall | Egress traffic filtering rules |
| EgressService | Egress traffic routing through services |
| EgressQoS | QoS marking on egress traffic |
| Multi-Egress Gateway | Egress traffic via multiple gateway nodes |
| User Defined Networks | Multi-network support, network segmentation (UDN) |
| Cluster Network Connect | Connecting isolated User Defined Networks together for controlled inter-UDN connectivity |
| Multi-Homing | Pods attached to multiple networks |
| DPU/SmartNIC Offload | Hardware acceleration via OVS offload |
| Multicast | IGMP snooping and relay via OVN |
| NetworkQoS | DSCP marking and traffic shaping |
| BGP | Route advertisements and peering |
| EVPN | Ethernet VPN integration |
| No-Overlay | Direct pod routing using BGP-learned routes, without encapsulation |
| KubeVirt | VM live migration and persistent IPs support |
| Hybrid Overlay | Mixed Windows/Linux clusters via VXLAN |

## Repository Layout

```text
Makefile       # Top-level entry point — forwards to go-controller/ and dist/images (`make help`)
go-controller/ # Main Go codebase — feature implementations, component code, ovnkube binaries, CRDs, libovsdb models, observability library, hybrid-overlay
test/e2e/      # End-to-end Ginkgo tests covering all features (network policy, egress, UDN, KubeVirt, BGP, etc.)
test/conformance/ # Kubernetes network conformance suite
test/crd-integration/ # CRD validation tests against a real API server
dist/          # Container images (Dockerfiles) and deployment YAML manifests
charts/        # Helm charts — ovn-kubernetes (with its ovnkube-crds sub-chart) and ovn-k8s-observability
docs/          # mkdocs source for ovn-kubernetes.io — OKEPs, feature docs, design docs, developer, installation guides
contrib/       # Kind cluster scripts, local deployment helpers (kind.sh), and development tooling
hack/          # Repository-level scripts — docs site build and publishing
debian/        # Debian packaging metadata
LICENSES/      # License and third-party package licensing information
```

`go-controller/`, `test/e2e/` and `test/conformance/` are **three separate Go
modules**, each with its own `go.mod`. Run `go` commands from the module you
are actually changing.

## Build and Test

`make help` at the repository root lists the top-level targets, each of which
forwards to the component Makefile that owns it:

```bash
make build          # Build ovnkube binaries
make test           # Run unit tests
make lint           # Format and lint Go code
make image          # Build the ovn-kubernetes node image
```

`build`, `test` and `lint` also work from `go-controller/`, which additionally
has `gofmt`, `lint-fix`, the codegen targets, and the `verify-*` targets.
`image` is the exception — it forwards to `dist/images/`. See
[Verify before proposing a change](#verify-before-proposing-a-change) for what
must pass, and [Tools and environment](#tools-and-environment) for the
container, kernel module, and platform caveats.

E2E tests run via CI on Kind clusters. See `test/e2e/` for Ginkgo test suites.
To run E2E locally, first set up a Kind cluster using `contrib/kind.sh`, then run the tests.
See `docs/developer-guide/local_testing_guide.md` for more details.

## Key Conventions

See `CONTRIBUTING.md` for full details:
- [Commit message guidelines](CONTRIBUTING.md#commit-message-guidelines)
- [Pull request checklist](CONTRIBUTING.md#pull-request-checklist)
- [Sign your commits (DCO)](CONTRIBUTING.md#sign-your-commits)
- [AI guidelines](CONTRIBUTING.md#ai-guidelines)

## OKEPs (Enhancement Proposals)

New features require an OKEP in `docs/okeps/`. See `docs/okeps/okep-4368-template.md` for the
template. OKEPs must have an associated GitHub issue, cover all template sections and update
`mkdocs.yml`.

## Architecture Notes

The OVN-Kubernetes plugin watches the Kubernetes API. It acts on the generated Kubernetes
cluster events by creating and configuring the corresponding OVN logical constructs in the
OVN database for those events. OVN (which is an abstraction on top of Open vSwitch) converts
these logical constructs into logical flows in its database and programs the OpenFlow flows
on the node, which enables networking on a Kubernetes cluster.

See [Architecture](docs/design/architecture.md) for component details (ovnkube-control-plane,
ovnkube-node, ovs-node pods and their containers).

### Gateway Modes

OVN-Kubernetes supports two gateway modes that affect how traffic enters and
leaves the cluster:
- **Local gateway (lgw)** — Traffic leaving the pod to go outside the cluster leaves the OVN stack
  and enters the host networking stack via the management port (mp-X) and then based on routes on
  the host, it either leaves via breth0/primary node NIC or via other interfaces on the host. This
  mode uses nftables to implement a lot of the service traffic and since traffic leaves the OVN/OVS
  datapath, it is not hardware offloadable.
- **Shared gateway (sgw)** — Traffic leaving the pod to go outside the cluster stays within the
  OVN/OVS datapath. It goes from the pod through the OVN logical switch, to the Gateway Router
  (GR), and out via the OVS bridge (breth0) directly. This mode is hardware offloadable to DPUs/SmartNICs.

To determine which mode a cluster is using, check the `k8s.ovn.org/l3-gateway-config`
annotation on any node — the `mode` field will be `"local"` or `"shared"`.
Shared gateway is the default.
