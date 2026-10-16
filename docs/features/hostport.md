# Pod hostPort support

OVN-Kubernetes can publish a pod's container ports on its node using nftables.
The opt-in node controller watches pod objects; it does not require a chained
CNI `portmap` plugin.

## Enable

Pod hostPort support is **disabled by default**. Enable it with
`--enable-pod-host-port`, or in the configuration file:

```ini
[ovnkubernetesfeature]
enable-pod-host-port=true
```

For the Helm chart, set `global.enablePodHostPort=true`.

Choose exactly one owner of hostPort rules on each node. CRI-O enables its own
hostPort management by default; set `disable_hostport_mapping = true` in its
`[crio.network]` configuration before enabling this feature. With containerd,
remove any chained CNI `portmap` plugin before enabling this feature. Runtime
name alone does not identify whether hostPort management is enabled, so
OVN-Kubernetes does not try to select an owner automatically. Drain and recreate
hostPort pods when changing owners, allowing the previous owner to clean up its
rules before enabling the new owner.

## Scope

The initial implementation requires Linux, full-mode nodes, and a shared gateway.
It supports the default network when that network is the pod's primary network.
It does not publish UDN infrastructure addresses. DPU, DPU-host, local/disabled
gateway modes, secondary networks, and primary UDNs are outside this initial scope.

TCP, UDP, and SCTP are matched separately, so TCP and UDP can use the same numeric
host port. An explicit `hostIP` restricts the mapping to that local address.
Omitting `hostIP` publishes on local addresses of each allocated pod IP family;
`0.0.0.0` and `::` restrict the wildcard to IPv4 and IPv6 respectively. Explicit
addresses take precedence over wildcards. IPv4 loopback mappings are reachable
only from the node. IPv6 localhost (`::1`) mapping is unsupported.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: hostport-example
spec:
  containers:
  - name: server
    image: registry.k8s.io/e2e-test-images/agnhost:2.56
    args: [netexec, --http-port=8080, --udp-port=8080]
    ports:
    - containerPort: 8080
      hostPort: 54323
      protocol: TCP
    - containerPort: 8080
      hostPort: 54323
      protocol: UDP
```

## Behavior and troubleshooting

The controller reconciles after the default-network pod annotation provides its
addresses; it does not wait for readiness. As with other API-driven networking,
there can be a short delay before a new mapping is reachable. Host-network pods
are ignored. Completed, failed, and deleted pods lose their rules; terminating
pods retain their mappings until deletion or completion. Regular containers are
used, matching kubelet's sandbox port mappings.

Rules live in `hostports`, `hostports-prerouting`, and `hostports-output` in the
existing `inet ovn-kubernetes` table. Updates replace only these chains in one
atomic transaction. Startup rebuilds them from the informer cache and a periodic
resync repairs removed chains. Failures are retried with backoff. The existing
management-port routing and SNAT provide the return path and hairpin handling;
client source addresses may therefore be translated to the management-port IP.
HostPort traffic is subject to the network's existing treatment of host traffic.

Inspect the rules with `nft list table inet ovn-kubernetes` in the node network
namespace. Disabling the flag and restarting ovnkube-node removes only the
controller's chains and sets. Daemon restarts retain rules until reconciliation;
existing TCP/SCTP connections retain their conntrack state. Adding, changing,
or removing a UDP mapping clears conntrack entries for the affected local
destination addresses
and host port, so a reused UDP tuple can reach the replacement backend. Startup
also clears UDP state for mappings whose pods disappeared while the daemon was
down. The `hostports-udp4` and `hostports-udp6` nftables sets record active and
pending UDP tuples atomically with rule changes. Removed tuples remain recorded
until conntrack cleanup succeeds, so failures can be retried across restarts.
Existing installations without these sets are recovered from their installed
hostPort rules. Disabling the feature removes rules and sets but does not
terminate existing connections.

## Tests

Run the controller unit tests from `go-controller`:

```sh
go test ./pkg/node/controllers/hostport
```

On a disposable Linux host with nftables and `CAP_NET_ADMIN`, also run
`OVN_HOSTPORT_KERNEL_TEST=1 go test ./pkg/node/controllers/hostport`. The kernel
tests use temporary tables and remove them afterward. Run the kernel tests in
a disposable network namespace with loopback up; they also exercise real UDP
conntrack cleanup while keeping the old backend alive.

For a two-node cluster configured with this feature and no other hostPort owner,
run the e2e suite with `OVNKUBE_POD_HOST_PORT_ENABLE=true` and focus `Pod hostPort`.
It covers TCP/UDP sharing a host port, explicit and wildcard addresses, IPv4
localhost, host and pod clients, self-hairpin, deletion, and replacement.
