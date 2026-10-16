# OKEP draft: Pod hostPort support

Status: local implementation draft. An upstream enhancement tracking issue and
number must be assigned before submission; no issue has been created for this
draft. Rename this file to match that number before upstream submission.

## Problem Statement

The default OVN CNI configuration does not publish Kubernetes hostPorts. Some
runtimes, including CRI-O by default, implement them independently, while other
deployments require a chained portmap plugin. Operators need an explicit way to
select OVN-Kubernetes as the owner without duplicating runtime-owned rules.

## Goals

- Opt-in hostPort management in ovnkube-node, disabled by default.
- Observe local pods and reconcile nftables rules through pod lifecycle events.
- Distinguish host IP, protocol, and port; support default-network dual stack.
- Recover from restarts and missed events without changing unrelated rules.

## Non-Goals

- Automatic runtime detection or migration of runtime/CNI rules.
- Initial DPU, DPU-host, local-gateway, primary UDN, or secondary-network support.
- IPv6 localhost forwarding, socket reservation, or TCP/SCTP conntrack migration.

## Introduction

The shared gateway already routes host traffic to local pods through the
management port and supplies SNAT. Pod hostPort needs destination translation
in the host network namespace, driven by the existing pod informer.

## User-Stories/Use-Cases

An operator using containerd enables hostPorts without changing the CNI chain.
An operator using CRI-O retains current behavior by leaving the feature disabled.
A workload publishes TCP and UDP on the same node port without collisions between
protocols, or publishes only on a specific node IP or IPv4 loopback.

## Proposed Solution

### API Details

No new Kubernetes API. Consume `spec.nodeName`, `spec.hostNetwork`, regular
container ports, pod phase, and the default-network OVN annotation. Add the
boolean `--enable-pod-host-port` / `enable-pod-host-port` configuration
option, default false, with Helm `global.enablePodHostPort` and
`OVNKUBE_POD_HOST_PORT_ENABLE` wiring.

### Implementation Details

Require full-mode nodes and shared gateways. Run after management-port setup.
Use a single rate-limited reconciliation key, wait for informer sync, and rebuild
all local mappings atomically. Match only locally assigned destination addresses;
prioritize explicit host IPs over wildcards and separate L4 protocols. Prevent
PREROUTING access to loopback mappings. Publish only primary default-network
addresses. Ignore terminal and host-network pods. Retain terminating pod mappings
until deletion or completion. Preserve rules across daemon shutdown, clean stale
rules on startup, and remove owned chains when disabled. Retry failed operations
and resync every five minutes. No OVN database access is needed.

### Testing Details

Unit tests cover eligibility, address families, same-port TCP/UDP, precedence,
replacement, deletion, restart cleanup, failure retries, and unrelated-rule
preservation. An opt-in real-kernel test validates nft syntax. E2E coverage tests
host, remote-host, peer-pod and hairpin clients, localhost, wildcard addresses,
and pod replacement. UDNs are explicitly excluded rather than exposing their
infrastructure interfaces. Node-scale cost is proportional to local hostPort
mappings; benchmarking at maximum supported pod density is still needed.

### Documentation Details

See [Pod hostPort](../features/hostport.md) for configuration, ownership,
packet flow, tests, and limitations.

## Risks, Known Limitations and Mitigations

Two rule owners produce ambiguous behavior: explicit opt-in plus a documented
migration is required. API watching is eventually consistent with sandbox setup
and deletion. Management-port SNAT changes source identity; this feature does not
introduce new network-policy semantics for host traffic. Existing TCP/SCTP conntrack state
outlives rule changes. UDP state for affected local destination addresses and
host ports is cleared after committing rule changes. Owned nftables sets preserve
active and pending UDP tuples across daemon restarts, including when the pod is
gone; removed tuples are forgotten only after successful cleanup. Startup also
recovers older rules that lack these sets. Manually node-bound pods
can bypass scheduler port-conflict checks; deterministic ordering selects the
lexically first namespace/name for identical keys and logs the conflict.

## OVN-Kubernetes Version Skew

Only the node agent changes. Enable the option only on upgraded agents. Disable
and restart upgraded agents to remove native chains before downgrading to a
version unaware of them.

## Backwards Compatibility

The default is disabled. Existing CRI-O and chained portmap rules are not adopted
or removed. An enabled node uses only its own chains in the existing OVN table.

## Alternatives

Chain the standard CNI portmap plugin, or keep runtime-provided mapping. Both
remain valid when this feature is disabled. Runtime-name-based auto-selection is
insufficient because hostPort ownership is configurable independently.
