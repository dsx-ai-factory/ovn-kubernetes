#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# This script is run by the init container of the ovnkube-node Pod on a VMAAS DPU,
# it is used to populate the external_ids in OVS DB which is required by ovnkube-node.
#
# ovnkube.sh (ovnkube-controller-with-node, DPU mode) runs it again, with
# OVNKUBE_INIT_RERUN=true, on every start of the ovnkube-controller container: the host
# node this DPU serves may have been deleted and re-added, possibly reprovisioned under a
# new name and address, while that container was down, and init containers do not re-run
# on a container restart. The script must therefore stay idempotent; on a re-run the
# settings that only the init container receives through the environment (VTEP and
# gateway interface) fall back to the values already present in OVS.


OVS_RUNDIR=/var/run/openvswitch

# OVNK8S_INSECURE_SKIP_TLS_VERIFY - do not verify the Kubernetes API server certificate, so that no CA
# certificate is needed to reach it. The DPU then authenticates with the SPIFFE/SPIRE issued
# K8S_TOKEN alone. INSECURE: enable only when the API server endpoint and the network path to it
# are trusted.
ovnk8s_insecure_skip_tls_verify=${OVNK8S_INSECURE_SKIP_TLS_VERIFY:-"false"}

# OVNKUBE_INIT_RERUN - set by ovnkube.sh when it re-runs this script from the
# ovnkube-controller container (see the header).
ovnkube_init_rerun=${OVNKUBE_INIT_RERUN:-"false"}

# kubectl_opts is applied to every kubectl invocation through the wrapper below, so that the TLS
# verification setting cannot be missed by a call site.
kubectl_opts=()

kubectl() {
  command kubectl "${kubectl_opts[@]}" "$@"
}

# wait_for_event [attempts=<num>] function_to_call [arguments_to_function]
#
# Processes running inside the container should immediately start, so we
# shouldn't be making 80 attempts (default value). The "attempts=<num>"
# argument will help us in configuring that value.
wait_for_event() {
  retries=0
  sleeper=1
  attempts=80
  if [[ $1 =~ ^attempts= ]]; then
    eval $1
    shift
  fi
  while true; do
    $@
    if [[ $? != 0 ]]; then
      ((retries += 1))
      if [[ "${retries}" -gt ${attempts} ]]; then
        echo "error: $@ did not come up, exiting"
        exit 1
      fi
      echo "info: Waiting for $@ to come up, waiting ${sleeper}s ..."
      sleep ${sleeper}
      sleeper=5
    else
      if [[ "${retries}" != 0 ]]; then
        echo "$@ came up in ${retries} tries ${sleeper} sec"
      fi
      break
    fi
  done
}


# OVS must be up before OVN comes up.
# This checks if OVS is up and running
ovs_ready() {
  for daemon in $(echo ovsdb-server ovs-vswitchd); do
    pidfile=${OVS_RUNDIR}/${daemon}.pid
    if [[ -f ${pidfile} ]]; then
      ctl_file=${OVS_RUNDIR}/$daemon.$(cat $pidfile).ctl
      ovs-appctl -t ${ctl_file} version >/dev/null
      if [[ $? == 0 ]]; then
        continue
      fi
    fi
    return 1
  done
  return 0
}

echo "generate kubeconfig ..."
if [[ ${ovnk8s_insecure_skip_tls_verify} == "true" ]]; then
  echo "WARNING: Kubernetes API server certificate verification is disabled"
  kubectl_opts+=("--insecure-skip-tls-verify=true")
  cluster_tls="insecure-skip-tls-verify: true"
elif [[ -n ${K8S_CACERT:-} && -s ${K8S_CACERT} ]]; then
  # CA file, as ovnkube's --k8s-cacert accepts it.
  cluster_tls="certificate-authority: $K8S_CACERT"
else
  echo $K8S_CACERT_DATA | base64 -d > /tmp/k8s-cacert.crt
  cluster_tls="certificate-authority: /tmp/k8s-cacert.crt"
fi

k8s_auth="token: $K8S_TOKEN"
if [[ -n ${K8S_TOKEN_FILE:-} ]]; then
  k8s_auth="tokenFile: $K8S_TOKEN_FILE"
fi

cat > /tmp/kubeconfig.yaml <<EOF
apiVersion: v1
kind: Config
clusters:
- name: cluster
  cluster:
    server: $K8S_APISERVER
    $cluster_tls
users:
- name: ovn-kube
  user:
    $k8s_auth
contexts:
- name: default
  context:
    cluster: cluster
    user: ovn-kube
current-context: default
EOF

export KUBECONFIG=/tmp/kubeconfig.yaml

echo "Wait OVS ready..."
wait_for_event ovs_ready

echo "current OVS external_ids:"
ovs-vsctl get Open_vSwitch . external_ids

declare -A external_ids

# host-k8s-nodename
# Get MAC address from P0
mac=$(cat /sys/class/net/p0/smart_nic/pf/config | grep MAC | awk '{print $NF}')
if [ -z "$mac" ]; then
  echo "failed to get MAC address from /sys/class/net/p0/smart_nic/pf/config, exiting..."
  exit 1
fi
echo "find P0 MAC address: $mac"
# Get k8s node by k8s.ovn.org/node-primary-mac-addr label
mac="${mac//:/-}"

# find_k8s_nodename sets k8s_nodename to the host cluster node this DPU serves:
# the node labeled with the DPU's PF MAC address, preferring the most recently
# registered one when several match. Returns 1 when no node matches yet.
#
# The label is applied by ovnkube-node running in dpu-host mode, which may not
# have come up yet when the DPU boots, so this is retried by wait_for_event.
find_k8s_nodename() {
  local nodes candidates
  # One line per matching node: "<creationTimestamp> <name> <deletionTimestamp>",
  # the last field being empty for a node that is not being deleted. Use
  # {.items[*]...} rather than {.items[0]...}: the latter makes kubectl fail with
  # an "array index out of bounds" jsonpath error instead of returning nothing
  # when no node carries the label yet.
  nodes=$(kubectl get node -l "k8s.ovn.org/node-primary-mac-addr=$mac" \
    -o jsonpath='{range .items[*]}{.metadata.creationTimestamp} {.metadata.name} {.metadata.deletionTimestamp}{"\n"}{end}') || return 1
  # A host reprovisioned under a new name can leave its old Node object behind
  # for a while, still carrying the label: skip nodes being deleted (3 fields)
  # and sort the rest newest first (RFC 3339 timestamps sort lexically).
  candidates=$(echo "$nodes" | awk 'NF == 2 {print $1, $2}' | sort -r)
  # First line = most recently registered node; empty when none matched yet.
  k8s_nodename=$(echo "$candidates" | awk 'NR == 1 {print $2}')
  [ -n "$k8s_nodename" ] || return 1
  # More than one live candidate: report the ambiguity, the choice is made above.
  if [ "$(echo "$candidates" | grep -c .)" -gt 1 ]; then
    echo "WARNING: several nodes carry k8s.ovn.org/node-primary-mac-addr=$mac, using the newest: $k8s_nodename"
    echo "$candidates"
  fi
  return 0
}

echo "Wait for k8s node with k8s.ovn.org/node-primary-mac-addr=$mac ..."
wait_for_event attempts=60 find_k8s_nodename
external_ids["host-k8s-nodename"]="$k8s_nodename"
echo "set external_ids:host-k8s-nodename to ${external_ids["host-k8s-nodename"]}"
# Marks the name as resolved by this script, so that ovnkube.sh knows it can
# (and must) re-resolve it on a controller restart.
external_ids["host-k8s-nodename-source"]="ovnkube-init"

# current_external_id KEY prints the value of external_ids:KEY currently in OVS, if any.
current_external_id() {
  ovs-vsctl --if-exists get Open_vSwitch . "external_ids:$1" | tr -d '"'
}

# ovn-encap-ip
# The VTEP interface is only known to the init container (environment); on a
# re-run without it, keep the encap IP the init container derived at pod start.
encap_ip=
if [ "$ovnkube_init_rerun" = "true" ] && [ -z "${OVN_VTEP_IFACE:-}" ]; then
  encap_ip=$(current_external_id ovn-encap-ip)
fi
OVN_VTEP_IFACE=${OVN_VTEP_IFACE:-br-dpu}
if [ -z "$encap_ip" ]; then
  encap_ip=$(ip -4 -o addr show dev $OVN_VTEP_IFACE scope global \
    | awk '{print $4}' | grep -v '^169\.254\.' | head -n1)
fi
if [ -z "$encap_ip" ]; then
  echo "failed to get ovn-encap-ip from interface $OVN_VTEP_IFACE"
  exit 1
fi
external_ids["ovn-encap-ip"]=${encap_ip%/*}
echo "set external_ids:ovn-encap-ip to ${external_ids["ovn-encap-ip"]}"

# ovn-gw-interface
if [ "$ovnkube_init_rerun" = "true" ] && [ -z "${OVN_GW_INTERFACE:-}" ]; then
  OVN_GW_INTERFACE=$(current_external_id ovn-gw-interface)
fi
OVN_GW_INTERFACE=${OVN_GW_INTERFACE:-br-host}
external_ids["ovn-gw-interface"]="$OVN_GW_INTERFACE"
echo "set external_ids:ovn-gw-interface to ${external_ids["ovn-gw-interface"]}"

# ovn-gw-router-subnet and ovn-gw-nexthop
# Both lookups are retried: the dpu-host ovnkube-node labels the node before it
# writes host-cidrs, and a transient API error must not fail the container.
get_host_cidrs() {
  host_cidrs=$(kubectl get node $k8s_nodename -o jsonpath='{.metadata.annotations.k8s\.ovn\.org/host-cidrs}') || return 1
  [ -n "$host_cidrs" ]
}
get_node_ip() {
  node_ip=$(kubectl get node $k8s_nodename -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}') || return 1
  node_ip=$(echo $node_ip | awk '{print $1}')
  [ -n "$node_ip" ]
}
wait_for_event attempts=24 get_host_cidrs
host_cidrs=${host_cidrs//[\[\]\"]/}
# The host may expose several CIDRs (e.g. 10.0.1.2/24, 172.17.0.1/16,
# 7.243.100.81/31), so we cannot just pick the first entry. Use the node's K8s
# InternalIP to select the matching host-cidr; that entry also carries the
# correct prefix length used by the /31-vs-larger logic below.
wait_for_event attempts=24 get_node_ip
# Find the host-cidr whose address equals the node's InternalIP to recover its prefix.
cidr=$(echo "$host_cidrs" | tr ',' '\n' | awk -F'/' -v ip="$node_ip" '$1==ip {print; exit}')
if [ -z "$cidr" ]; then
  echo "failed to find host-cidr matching node InternalIP $node_ip in [$host_cidrs], exiting..."
  exit 1
fi
suffix=${cidr##*/}
if [ "$suffix" == "31" ]; then
  # A /31 holds exactly two addresses: the host holds $node_ip, so our next hop
  # is the other one. Which of the two is even varies by deployment.
  # output is like: network=10.192.0.0/31 gateway=10.192.0.1
  output=`python3 -c "import ipaddress; net=ipaddress.ip_network(\"$cidr\", strict=False); ip=ipaddress.ip_address(\"$node_ip\"); print('network=%s\ngateway=%s'%(net, next(a for a in net if a != ip)))"`
  network=$(echo "$output" | awk -F= '/^network=/ {print $2}')
  gateway=$(echo "$output" | awk -F= '/^gateway=/ {print $2}')
else
  # gateway is on the switch, output is like: network=10.192.0.0/16 gateway=10.192.0.1
  output=`python3 -c "import ipaddress; net=ipaddress.ip_network(\"$cidr\", strict=False); print('network=%s\ngateway=%s'%(net, net.network_address + 1))"`
  network=$(echo "$output" | awk -F= '/^network=/ {print $2}')
  gateway=$(echo "$output" | awk -F= '/^gateway=/ {print $2}')
fi
if [ -z "$network" ] || [ -z "$gateway" ]; then
  echo "failed to get network and gateway from $cidr, exiting..."
  exit 1
fi
external_ids["ovn-gw-router-subnet"]="$network"
external_ids["ovn-gw-nexthop"]="$gateway"
echo "set external_ids:ovn-gw-router-subnet to ${external_ids["ovn-gw-router-subnet"]}"
echo "set external_ids:ovn-gw-nexthop to ${external_ids["ovn-gw-nexthop"]}"

# ovn-gw-vlanid
OVN_GW_VLANID=${OVN_GW_VLANID:-""}
if [ -n "$OVN_GW_VLANID" ]; then
  external_ids["ovn-gw-vlanid"]="$OVN_GW_VLANID"
  echo "set external_ids:ovn-gw-vlanid to ${external_ids["ovn-gw-vlanid"]}"
fi

ovn_datapath_type=${OVN_DATAPATH_TYPE:-""}
if [ -n "$ovn_datapath_type" ]; then
  external_ids["ovn-bridge-datapath-type"]="$ovn_datapath_type"
  echo "set external_ids:ovn-bridge-datapath-type to ${external_ids["ovn-bridge-datapath-type"]}"
fi

echo "write external_ids to OVSDB..."
args=()
for k in "${!external_ids[@]}"; do
  args+=("external_ids:${k}=${external_ids[$k]}")
done
ovs-vsctl set Open_vSwitch . "${args[@]}"
if [[ $? != 0 ]]; then
  echo "failed to update Open_vSwitch, exiting..."
  exit 1
fi

echo "OVS external_ids after update:"
ovs-vsctl get Open_vSwitch . external_ids

echo "done"
