# Building and installing ovn-kubernetes

How to build the chart and the Ubuntu container image, publish them to a registry,
and deploy them to a cluster.

Run the commands from the repository root. Commands that need a different
working directory use a subshell so subsequent commands still run from the root.

| What                        | Where it lives                  | Build command                       |
| --------------------------- | ------------------------------- | ----------------------------------- |
| `ovn-kubernetes` Helm chart | `charts/ovn-kubernetes`          | `helm package charts/ovn-kubernetes` |
| `ovnkube` Ubuntu image      | `dist/images/Dockerfile.ubuntu` | `make -C dist/images ubuntu-image`   |

## Prerequisites

- Helm 3.13 or newer, required for `helm push --plain-http` (verified with 4.2.3).
- Docker, to build the Ubuntu image.
- A registry the cluster nodes can pull from — a GitHub organization on
ghcr.io, or a local registry.
- `kubectl` with a `KUBECONFIG` pointing at the target cluster, to install.
- A cluster with **no other CNI installed** and **kube-proxy disabled**.
- Open vSwitch running on every node, either on the host or through the chart's
  `ovs-node` DaemonSet. See [Choose a deployment mode](#choose-a-deployment-mode).
- Optional: Python 3 with PyYAML, to regenerate `values.schema.json`.

---

## Registry

### ghcr.io

Create a classic [personal access token](https://github.com/settings/tokens/new?scopes=write:packages) carrying `write:packages`.

```bash
export CR_PAT=<token>
echo "$CR_PAT" | docker login ghcr.io -u <github-user> --password-stdin
echo "$CR_PAT" | helm registry login ghcr.io -u <github-user> --password-stdin
```

### Local registry

`registry:2` is an OCI registry, so a single container holds both the images
and the charts, and there is nothing to log in to.

```bash
./contrib/kind-helm.sh --local-kind-registry
```

That starts a `kind-registry` container on `localhost:5000`, joins it to the
`kind` docker network, and writes a containerd `hosts.toml` on every node so
the nodes resolve `localhost:5000` as well. Override the name and port with
`KIND_LOCAL_REGISTRY_NAME` and `KIND_LOCAL_REGISTRY_PORT`.

Standalone:

```bash
docker run -d --restart=always --name registry -p 5000:5000 \
  -v /opt/registry/data:/var/lib/registry registry:2
```

Either way, substitute `localhost:5000` for `ghcr.io/<org>` in the push
commands below. The registry speaks plain HTTP, so Helm needs `--plain-http`
on both the push and the install:

```bash
helm push /tmp/chartout/ovn-kubernetes-<version>.tgz \
  oci://localhost:5000/charts --plain-http
```

For a registry that lives off your laptop, put TLS in front of it and add the
CA to every node's trust store. Otherwise each node needs its own
`/etc/containerd/certs.d/<host>:<port>/hosts.toml` to allow the insecure
endpoint, which is exactly what the kind script does.

---

## Build

### Local E2E

This setup creates a Kind cluster with the Helm chart, DOCA OVS, and UCA OVN.

Run this setup on a Linux host; the script configures Linux sysctls. In addition
to the prerequisites above, install Kind 0.27 or newer, Go 1.26 or newer, make,
jq, and the `jinjanate` command provided by `jinjanator[yaml]`.

If the default public DNS servers are unreachable on your network, set
`KIND_DNS_SERVERS` to a space-separated list of DNS server IP addresses reachable
from the Kind nodes and pods before running the script. Use your site's DNS
servers or a resolver on your local network.

```bash
export KUBECONFIG="$HOME/ovn.conf"
export K8S_VERSION=v1.36.4
bash contrib/kind-nv.sh --multicast-enabled

kubectl get nodes
kubectl -n ovn-kubernetes get pods

(
  cd test/e2e
  CGO_ENABLED=0 go test -c -o /tmp/kind-nv-e2e.test .
)

export KUBERNETES_CONFORMANCE_TEST=y
export PLATFORM_IPV4_SUPPORT=true PLATFORM_IPV6_SUPPORT=false
export OVN_GATEWAY_MODE=shared

focus='Should validate connectivity within a namespace of pods on separate nodes'
focus+='|Should validate the hairpinned traffic is always allowed'
focus+='|queries to the nodePort service shall work'
focus+='|Creates a service with session-affinity'
focus+='|should be able to send multicast UDP traffic between nodes'
focus+='|Should validate the egress firewall policy functionality for allowed IP'
focus+='|Should validate the egress firewall policy functionality for allowed CIDR and port'

mkdir -p /tmp/kind-nv-e2e-report

/tmp/kind-nv-e2e.test \
  -test.timeout=45m \
  -ginkgo.v -ginkgo.no-color -ginkgo.fail-fast \
  -ginkgo.fail-on-empty -ginkgo.flake-attempts=1 \
  -ginkgo.label-filter='!Feature:NetworkSegmentation' \
  -ginkgo.focus="$focus" \
  -ginkgo.junit-report=/tmp/kind-nv-e2e-report/junit.xml \
  -provider=skeleton -kubeconfig="$KUBECONFIG" -num-nodes=3 \
  -report-dir=/tmp/kind-nv-e2e-report \
  -report-path=/tmp/kind-nv-e2e-report
```

### Helm chart

#### Pre-flight checks

Run these before submitting a change:

```bash
(
  cd charts/ovn-kubernetes

  # 1. Lint.
  helm lint . --set global.skipCallToK8s=true

  # 2. Render for real. This is what actually catches template breakage.
  helm template ovn-kubernetes . -n ovn-kubernetes \
    --set k8sAPIServer=https://10.0.0.1:6443 \
    --set podNetwork=10.192.0.0/16/26 \
    --set global.skipCallToK8s=true > /dev/null

  # 3. If values.yaml or any subchart values.yaml changed, regenerate the schema.
  python3 scripts/gen-values-schema.py > values.schema.json
)
```

#### Package and push the chart

```bash
# Uses the version from Chart.yaml
helm package charts/ovn-kubernetes -d /tmp/chartout
helm push /tmp/chartout/ovn-kubernetes-<version>.tgz oci://ghcr.io/<org>/charts
```

`helm push` appends the chart name to the target, so this lands at
`ghcr.io/<org>/charts/ovn-kubernetes`. Do not include the chart name in the
`oci://` URL.

### Ubuntu image

```bash
make -C dist/images ubuntu-image
```

Produces `ovn-kube-ubuntu-<arch>`, and additionally tags it `ovn-kube-ubuntu`
on amd64. OVS and OVN come from the distro packages in the base image
(`ubuntu:26.04` by default, override with `UBUNTU_BASE_IMAGE`).

The `kubectl` minor version is derived from the `k8s.io/kubernetes` entry in
`go-controller/go.mod` rather than hardcoded. Check what it resolves to with:

```bash
make -C dist/images print-kubectl-minor    # e.g. 1.36
```

#### Push the image

Name the repository `ovnkube`, and specify a tag:

```bash
TAG=cbcda82f-71385617-7852734
IMG=ghcr.io/<org>/ovnkube:$TAG

make -C dist/images ubuntu-image IMAGE=$IMG
docker login ghcr.io
docker push $IMG
```

---

## Install

### Required values

`k8sAPIServer` and `podNetwork` have no defaults and the templates call `fail`
without them, so both need to be supplied:

```bash
helm upgrade --install ovn-kubernetes charts/ovn-kubernetes \
  -n ovn-kubernetes --create-namespace \
  --set k8sAPIServer=https://10.0.0.1:6443 \
  --set podNetwork=10.192.0.0/16/26
```

`podNetwork` carries two masks: the cluster-wide pod CIDR followed by the
per-node subnet carved out of it.

To install a chart you pushed rather than the working tree, point Helm at the
registry instead of `charts/ovn-kubernetes`:

```bash
helm upgrade --install ovn-kubernetes \
  oci://ghcr.io/<org>/charts/ovn-kubernetes --version <version> \
  -n ovn-kubernetes --create-namespace \
  --set k8sAPIServer=https://10.0.0.1:6443 \
  --set podNetwork=10.192.0.0/16/26
```

See [README-deployment.md](charts/ovn-kubernetes/README-deployment.md) for all
the configurable values.

### Choose a deployment mode

The `tags` block in `values.yaml` selects which subcharts render. The defaults
install a single-node-zone (interconnect) topology:

| Tag                            | Default | Purpose                                                                   |
| ------------------------------ | ------- | ------------------------------------------------------------------------- |
| `ovnkube-crds`                 | `true`  | CRDs for ANP/BANP, NAD, MultiNetworkPolicy, and the OVN-K APIs            |
| `ovnkube-control-plane`        | `true`  | Cluster manager Deployment                                                |
| `ovnkube-single-node-zone`     | `true`  | Per-node DaemonSet running the OVN databases, northd, and the node agents |
| `ovs-node`                     | `false` | OVS as a DaemonSet, instead of OVS on the host                            |
| `ovnkube-identity`             | `false` | ovnkube identity admission webhook                                        |
| `ovnkube-node-dpu-host`        | `false` | Host-side agent when the DPU runs the datapath                            |
| `ovnkube-single-node-zone-dpu` | `false` | Daemonset runs on the DPUs                                                |

Flip one with `--set tags.ovs-node=true` or a values override file.

With the default `tags.ovs-node=false`, Open vSwitch must already be installed
and running on each node. If the hosts do not manage OVS, add
`--set tags.ovs-node=true` to the Helm installation command to deploy it as a
DaemonSet. Use one OVS provider per node. The `contrib/kind-nv.sh` wrapper already
enables the DaemonSet for its Kind cluster.

### Pointing at your registry

Per-component image pins live in `values.yaml` under each subchart key. Two
global parameters control where they resolve to:

- `global.imageRepositoryPrefix` replaces the registry prefix, for example
`ghcr.io/<org>`, while retaining the per-component image names and tags.
- `global.useImageDigest=true` appends `@sha256:<sha256>` from the values, and
fails the render if any pinned image is missing its `sha256` field.

To deploy the single image built above, override the repository and tag for
all components with `global.image`:

```bash
helm upgrade --install ovn-kubernetes charts/ovn-kubernetes \
  -n ovn-kubernetes --create-namespace \
  --set k8sAPIServer=... --set podNetwork=... \
  --set global.image.repository=ghcr.io/<org>/ovnkube \
  --set-string global.image.tag="$TAG"
```

When using `global.image` together with `global.useImageDigest=true`, also set
`global.image.sha256` to the published image's digest without the `sha256:`
prefix. The shared image replaces the per-component image settings, so their
digests do not apply:

```bash
--set global.useImageDigest=true --set-string global.image.sha256=<digest>
```

For a private registry, either reference an existing pull secret with
`global.imagePullSecretName`, or have the chart create one by setting
`global.dockerConfigSecret.create=true` along with `.registry` and a base64
`username:password` in `.auth`.

### Rendering without cluster access

The chart calls `lookup` to find the CoreDNS service ClusterIP and to decide
whether it owns the namespace. Outside a cluster (CI, ArgoCD diffing, plain
`helm template`) those lookups return nothing, so set:

```bash
--set global.skipCallToK8s=true
```

Against a live cluster, leave it off; if the CoreDNS service still cannot be
resolved, the chart requires `global.corednsClusterIP` instead.

### Verify

Start by checking the pods in the chart's namespace, which is
`global.namespace` and defaults to `ovn-kubernetes`. With the default tags you
should see one `ovnkube-node` pod per node and a single `ovnkube-control-plane`
pod, all `Running` with every container ready:

```bash
kubectl -n ovn-kubernetes get pods -o wide
kubectl -n ovn-kubernetes rollout status ds/ovnkube-node
kubectl -n ovn-kubernetes rollout status deploy/ovnkube-control-plane
```

`ovnkube-node` runs five containers per pod — `nb-ovsdb`, `sb-ovsdb`,
`ovn-northd`, `ovnkube-controller` and `ovn-controller` — so a pod that is
`Running` but shows `4/5` has one container crash-looping. Use
`kubectl -n ovn-kubernetes logs <pod> -c <container>` to find out which.

Then confirm the CNI actually works by creating a couple of pods:

```bash
kubectl run nettest-a --image=busybox:1.36 --restart=Never -- sleep 3600
kubectl run nettest-b --image=busybox:1.36 --restart=Never -- sleep 3600
kubectl wait --for=condition=Ready pod/nettest-a pod/nettest-b --timeout=120s

# IPs should fall inside podNetwork
kubectl get pods -o wide

# pod-to-pod across the overlay
kubectl exec nettest-a -- ping -c3 "$(kubectl get pod nettest-b -o jsonpath='{.status.podIP}')"

# cluster DNS, which also exercises a service VIP from serviceNetwork
kubectl exec nettest-a -- nslookup kubernetes.default.svc.cluster.local

kubectl delete pod nettest-a nettest-b
```
