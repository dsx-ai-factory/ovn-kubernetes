#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
# SPDX-License-Identifier: Apache-2.0

# NVIDIA downstream CI policy for control-plane e2e runs. This file is sourced
# by e2e-cp.sh after upstream skip filters are built and before tests run.

MULTIHOMING_DUAL_STACK_TESTS="Multi Homing A single pod with an OVN-K secondary network is able to get to the Running phase when attaching to an L2 - switched - network with a dual stack configuration|\
secondary network with IPv6 subnet|\
secondary network with an IPv6 subnet|\
secondary network with a dual stack configuration|\
with a trunked configuration|\
Multi Homing A single pod with an OVN-K secondary network is able to get to the Running phase when attaching to an L2 - switched - network with an IPv6 subnet|\
Multi Homing A single pod with an OVN-K secondary network is able to get to the Running phase when attaching to an localnet - switched - network with an IPv6 subnet|\
Multi Homing multiple pods connected to the same OVN-K secondary network can communicate over the secondary network can communicate over an localnet secondary network when the pods are scheduled on different nodes|\
Multi Homing multiple pods connected to the same OVN-K secondary network can communicate over the secondary network can communicate over an localnet secondary network without IPAM when the pods are scheduled on different nodes|\
Multi Homing multiple pods connected to the same OVN-K secondary network can communicate over the secondary network can communicate over an localnet secondary network without IPAM when the pods are scheduled on different nodes, with static IPs configured via network selection elements|\
Multi Homing multiple pods connected to the same OVN-K secondary network localnet OVN-K secondary network with a service running on the underlay can communicate over a localnet secondary network from pod to the underlay service|\
Multi Homing multiple pods connected to the same OVN-K secondary network localnet OVN-K secondary network with a service running on the underlay with multi network policy blocking the traffic can not communicate over a localnet secondary network from pod to the underlay service|\
Multi Homing multiple pods connected to the same OVN-K secondary network multi-network policies multi-network policies configure traffic allow lists for a localnet topology when the multi-net policy describes the allow-list using pod selectors|\
Multi Homing multiple pods connected to the same OVN-K secondary network multi-network policies multi-network policies configure traffic allow lists for a localnet topology when the multi-net policy describes the allow-list using IPBlock|\
Multi Homing multiple pods connected to the same OVN-K secondary network multi-network policies multi-network policies configure traffic allow lists for a localnet topology when the multi-net policy describes the allow-list via namespace selectors|\
Multi Homing multiple pods connected to the same OVN-K secondary network localnet OVN-K secondary network with a service running on the underlay when a policy is provisioned can communicate over a localnet secondary network from pod to gw ingress denyall, egress allow all, ingress policy should have no impact on egress|\
Multi Homing multiple pods connected to the same OVN-K secondary network localnet OVN-K secondary network with a service running on the underlay when a policy is provisioned can communicate over a localnet secondary network from pod to gw egress allow all|\
Multi Homing multiple pods connected to the same OVN-K secondary network localnet OVN-K secondary network with a service running on the underlay when a policy is provisioned can communicate over a localnet secondary network from pod to gw egress deny all|\
Multi Homing multiple pods connected to the same OVN-K secondary network multi-network policies multi-network ingress allow all for a localnet topology when the multi-net policy is ingress allow-all|\
Multi Homing multiple pods connected to the same OVN-K secondary network multi-network policies multi-network ingress allow all for a localnet topology when the multi-net policy is egress deny-all, ingress allow-all|\
Multi Homing multiple pods connected to the same OVN-K secondary network multi-network policies multi-network ingress deny all policies for a localnet topology when the multi-net policy is ingress deny-all"

if [ "${ENABLE_MULTI_NET:-}" = "true" ]; then
  # The downstream environment does not support these IPv6/dual-stack
  # multihoming scenarios yet.
  skip "$MULTIHOMING_DUAL_STACK_TESTS"
fi

if [ "${PLATFORM_IPV4_SUPPORT:-false}" = "true" ] && [ "${PLATFORM_IPV6_SUPPORT:-false}" = "true" ]; then
  skip "external.gateway"
fi

if [ "${PLATFORM_IPV6_SUPPORT:-false}" = "true" ]; then
  # Extra downstream IPv6 skips carried from the old NVIDIA runner.
  skip "Should validate the egress firewall policy functionality against remote hosts"
  skip "Should validate the egress firewall policy functionality against cluster nodes by using node selector"
  skip "Should validate ICMP connectivity to multiple external gateways for an ECMP scenario"
  skip "Should validate ICMP connectivity to an external gateway's loopback address via a pod with external gateway annotations enabled"
  skip "Should validate TCP/UDP connectivity to multiple external gateways for a UDP / TCP scenario"
  skip "Should validate TCP/UDP connectivity to an external gateway's loopback address via a pod with external gateway annotations enabled"
  skip "Should validate conntrack entry deletion for TCP/UDP traffic via multiple external gateways a.k.a ECMP routes"
  skip "can retrieve multicast IGMP query"
  skip "egress IP validation"
  skip "e2e egress firewall policy validation"
  skip "OVS CPU affinity pinning"
  skip "should listen on each host addresses"

  if [ "${OVN_GATEWAY_MODE:-}" = "local" ]; then
    skip "Should be allowed to node local host-networked endpoints by nodeport services"
    skip "Should be allowed to node local cluster-networked endpoints by nodeport services with externalTrafficPolicy=local"
  fi
fi

HOST_CGROUP_VERSION=$(docker info --format '{{.CgroupVersion}}' 2>/dev/null || true)
if [ -z "$HOST_CGROUP_VERSION" ]; then
  HOST_CGROUP_VERSION=$(docker info 2>/dev/null | sed -n 's/^ Cgroup Version: //p' | head -n 1)
fi
if [ "$HOST_CGROUP_VERSION" = "1" ]; then
  # TODO(SDN-4125): remove once downstream CI switches to cgroup v2.
  skip "is isolated from the default network"
fi

# Revisit if and when routeAdvertisementsEnable, BGP, EVPN, and NoOverlay are
# enabled in the downstream ovn-k helm chart.
skip "BGP"
skip_label "Feature:RouteAdvertisements"
skip_label "Feature:EVPN"
skip_label "Feature:NoOverlay"

if [ "${KIND_INSTALL_METALLB:-false}" = "false" ]; then
  skip "EgressService|Egress Services|Load Balancer Service Tests with MetalLB"
fi

# NVIDIA known unsupported tests.
skip "e2e delete databases"
skip "Pod to external server PMTUD"
skip "Should validate correct DSCP value on pod labels changes"
skip "Node IP address migration"
skip "Should validate egress IP logic when one pod is managed by more than one egressIP object"
skip "queries to the nodePort service shall work for UDP"
skip "of type NodePort should listen on each host addresses"
skip "hybrid.overlay"
skip "Check whether gateway-mtu-support annotation"
skip "Services of type NodePort should work on secondary node interfaces for ETP=local and ETP=cluster when backend pods are"
skip "e2e control plane test node readiness according to its defaults interface MTU size should get node not ready with a too small MTU"
skip "Status manager validation"
skip "should provide Internet connection continuously when ovnkube-node pod is killed"

# Known flakes.
skip "Should validate the egress firewall policy functionality against cluster nodes by using node selector"
skip "should provide Internet connection continuously when all pods are killed on node running master instance of ovnkube-control-plane"

# TODO: remove once downstream uses OVN/OVS 23.09 or newer.
skip "large queries to the server pod on another node shall work for TCP"
skip "Allow connection to an external IP using a source port that is equal to a node port"
skip "Creates a service with session-affinity, and ensures it works after backend deletion"

# TODO: consider a separate downstream serial lane.
if [ "${WHAT:-}" != "Serial" ]; then
  skip_label "Serial"
fi

# Skip ClusterNetworkConnect unconditionally for downstream:
# central mode does not support it, and IC mode needs helm feature enablement.
skip "ClusterNetworkConnect"

# Skip failing multihoming localnet IPv6 test.
# TODO: revisit after NID-8182 is fixed.
skip "Multi Homing A single pod with an OVN-K secondary network is able to get to the Running phase when attaching to a localnet - switched - network with an IPv6 subnet"

# SDN-4218 - Skip failing multihoming localnet redundancy test
skip "Multi Homing A pod with multiple attachments to the same secondary NAD features multiple different IPs and connectivity redundancy.*Localnet secondary NAD"

# Skip KubeVirt tests that require enablePersistentIPs support.
skip "Kubevirt Virtual Machines with user defined networks and persistent ips configured"
skip "Kubevirt Virtual Machines with kubevirt VM using layer2 UDPN"
skip "Kubevirt Virtual Machines with user defined networks with ipamless localnet topology"
skip "Kubevirt Virtual Machines ipv4 subnet exhaustion"

if [[ "${WHAT:-}" != "Multi-VTEP"* ]]; then
  skip "Multi-VTEP"
fi

if [[ "${WHAT:-}" = "Network Segmentation"* ]]; then
  # NVIDIA: known failure due to load balancer dependency, SDN-2795.
  skip "should be reachable through their cluster IP, node port and load balancer"
fi

export KUBEVIRT_SKIP_MIGRATE_POST_COPY="${KUBEVIRT_SKIP_MIGRATE_POST_COPY:-true}"

# Silence Ginkgo deprecation warnings from the currently vendored Kubernetes
# e2e framework.
export ACK_GINKGO_DEPRECATIONS="${ACK_GINKGO_DEPRECATIONS:-2.4.0}"
