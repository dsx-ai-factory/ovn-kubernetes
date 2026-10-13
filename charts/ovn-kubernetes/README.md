# ovn-kubernetes

-----------------------

![Version: 2.4.0](https://img.shields.io/badge/Version-2.4.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 2026-10.0](https://img.shields.io/badge/AppVersion-2026--10.0-informational?style=flat-square)

**Homepage:** <https://github.com/dsx-ai-factory/ovn-kubernetes>

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| SDN Developers |  |  |

## Source Code

* <https://github.com/dsx-ai-factory/ovn-kubernetes>
* <https://github.com/ovn-kubernetes/ovn-kubernetes>
* <https://github.com/ovn-org/ovn>
* <https://github.com/openvswitch/ovs>
* <https://github.com/ovn-org/libovsdb>

Open Virtual Networking (OVN) Kubernetes CNI is an open source networking and
network security solution for Kubernetes workloads. It leverages distributed
OVN SDN control plane and per-node Open vSwitch (OVS) to provide network
virtualization and network connectivity to K8s Pods. It does so by creating a logical
network topology using logical constructs such as logical switches (Layer 2) and
logical routers (Layer 3). The Pod interfaces are represented by logical ports on
the logical switches. On these logical switch ports, one can specify IP network
information (IP address and MAC address), anti-spoofing rules (MAC and IP),
Security Groups, QoS configuration, and so on.

A physical port (SR-IOV VF or VETH) assigned to a Pod will be associated with
a corresponding logical port, this will result in applying all the logical port
configuration on to the physical port. The logical port becomes the API for
configuring the physical port.

In addition to providing overlay network connectivity for Pods in the K8s cluster,
OVN K8s CNI supports plethora of advanced networking features, such as

```
- Optimized and Accelerated K8s Network Policy on Pod's traffic
- Optimized and Accelerated K8s Service Implementation (aka LoadBalancers and NAT)
- Optimized and Accelerated Policy Based Routing
- Multi-Home Pods with an option for Secondary networks to be on a Layer-2
  Overlay (flat network), Layer-2 Underlay (VLAN-based) on private or public
  subnets.
- Optimized and Accelerated K8s Network Policy on Pod's secondary networks
```

Most of these services are distributed and implemented via a pipeline (series
of OpenFlow tables with OpenFlow flows) on local OVS switches. These OVS
pipelines are very amenable to offloading to NIC hardware which should result
in the best possible networking performance and CPU savings on the host.

The NVIDIA CX NICs and DPUs offload OVS pipelines and provides accelerated
networking services to tenant applications at wire speed.

This helm chart supports deploying OVN K8s CNI in myriad of K8s cluster
deployment modes. Some of which are enumerated below:

```
- single node K8s cluster either on a VM or BM or DPU
- multiple nodes K8s cluster (on KIND or BM or VM)
- single K8s Cluster with both hosts and their DPUs as nodes
- multiple K8s Clusters - DPU cluster and per-tenant K8s clusters - with shared OVN
  (managed K8s case)
```

OVN K8s CNI architecture is a layered architecture with OVS at the bottom,
followed by OVN, and finally OVN K8s CNI at the top. Each layer has several
K8s components - deployments, daemonsets, and statefulsets. Each component at
every layer is a subchart by itself.

## Sub-charts

| Chart | Version | Path |
| --- | --- | --- |
| `ovnkube-control-plane` | `2.4.0` | `charts/ovnkube-control-plane` |
| `ovnkube-crds` | `2.4.0` | `charts/ovnkube-crds` |
| `ovnkube-identity` | `2.4.0` | `charts/ovnkube-identity` |
| `ovnkube-node-dpu-host` | `2.4.0` | `charts/ovnkube-node-dpu-host` |
| `ovnkube-single-node-zone` | `2.4.0` | `charts/ovnkube-single-node-zone` |
| `ovnkube-single-node-zone-dpu` | `2.4.0` | `charts/ovnkube-single-node-zone-dpu` |
| `ovs-node` | `2.4.0` | `charts/ovs-node` |

Install/upgrade the helm chart. `k8sAPIServer`, `podNetwork`, and
`global.imageRepositoryPrefix` have no defaults, so replace the placeholders
below with your API server endpoint, pod CIDR, and registry path. The registry
path must contain the component images and tags pinned in `values.yaml`:

```bash
helm upgrade --install ovn-kubernetes . \
  -n ovn-kubernetes --create-namespace \
  --set k8sAPIServer=https://10.0.0.1:6443 \
  --set podNetwork=10.192.0.0/16/26 \
  --set global.imageRepositoryPrefix=registry.example.com/sdn
```

`podNetwork` carries two masks: the first is the cluster-wide pod CIDR, the
second is the per-node subnet carved out of it. If the CoreDNS Service cannot
be looked up, also set `global.corednsClusterIP` to its cluster IP.

See [README-deployment.md](README-deployment.md) for the full list of
configurable values, including subcharts.

# Maintenance Scripts

## CRD Synchronization

The `scripts/sync-crds.sh` script synchronizes external CRDs from upstream
sources to the `charts/ovnkube-crds/crds/` directory.

**CRD Sources:**

| Source | CRDs | Upstream Repository |
|--------|------|---------------------|
| `multi-net-policy` | `k8s.cni.cncf.io_multinetworkpolicy.yaml` | `k8snetworkplumbingwg/multi-networkpolicy` |
| `net-attach-def` | `k8s.cni.cncf.io_netattachdef.yaml` | `k8snetworkplumbingwg/network-attachment-definition-client` |
| `policy` | `policy.networking.k8s.io_adminnetworkpolicies.yaml`, `policy.networking.k8s.io_baselineadminnetworkpolicies.yaml` | `kubernetes-sigs/network-policy-api` |

`OVN_K_PATH` selects the repository whose `go-controller/go.mod` supplies
the external CRD versions. It defaults to this repository's root.

To update OVN-Kubernetes CRDs, run `make codegen` from `go-controller/`.

**Usage:**
```bash
# Run from charts/ovn-kubernetes
./scripts/sync-crds.sh

# Or sync a specific external source
./scripts/sync-crds.sh policy
```

The default sync includes Network Attachment Definition and Admin Network
Policy CRDs. Use `multi-net-policy` explicitly to sync Multi-NetworkPolicy.

The external sources are pinned to the versions in `go-controller/go.mod`.
The script cleans up temporary files after completion. Review changes with
`git diff` before committing.
