#!/usr/bin/env bash

# This script is run by init container of static ovnkube-node Pod on VMAAS DPU,
# it is used to populate the external_ids in OVS DB which is required by ovnkube-node.


OVS_RUNDIR=/var/run/openvswitch

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
echo $K8S_CACERT_DATA | base64 -d > /tmp/k8s-cacert.crt

cat > /tmp/kubeconfig.yaml <<EOF
apiVersion: v1
kind: Config
clusters:
- name: cluster
  cluster:
    server: $K8S_APISERVER
    certificate-authority: /tmp/k8s-cacert.crt
users:
- name: ovn-kube
  user:
    token: $K8S_TOKEN
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
k8s_nodename=$(kubectl get node -l k8s.ovn.org/node-primary-mac-addr=$mac -o jsonpath='{.items[0].metadata.name}')
if [ -z "$k8s_nodename" ]; then
  echo "failed to get k8s node with k8s.ovn.org/node-primary-mac-addr=$mac, exiting..."
  exit 1
fi
external_ids["host-k8s-nodename"]="$k8s_nodename"
echo "set external_ids:host-k8s-nodename to ${external_ids["host-k8s-nodename"]}"

# ovn-encap-ip
OVN_VTEP_IFACE=${OVN_VTEP_IFACE:-br-dpu}
encap_ip=$(ip -4 addr show dev $OVN_VTEP_IFACE | awk '/inet / {print $2}')
if [ -z "$encap_ip" ]; then
  echo "failed to get ovn-encap-ip from interface $OVN_VTEP_IFACE"
  exit 1
fi
external_ids["ovn-encap-ip"]=`echo $encap_ip | cut -d '/' -f 1`
echo "set external_ids:ovn-encap-ip to ${external_ids["ovn-encap-ip"]}"

# ovn-gw-interface
OVN_GW_INTERFACE=${OVN_GW_INTERFACE:-br-host}
external_ids["ovn-gw-interface"]="$OVN_GW_INTERFACE"
echo "set external_ids:ovn-gw-interface to ${external_ids["ovn-gw-interface"]}"

# ovn-gw-router-subnet and ovn-gw-nexthop
host_cidrs=$(kubectl get node $k8s_nodename -o jsonpath='{.metadata.annotations.k8s\.ovn\.org/host-cidrs}')
if [ -z "$host_cidrs" ]; then
  echo "failed to get 'k8s.ovn.org/host-cidrs' annotation from node $k8s_nodename, exiting..."
  exit 1
fi
host_cidrs=${host_cidrs//[\[\]\"]/}
cidr=$(echo $host_cidrs | awk -F',' '{print $1}')
# output is like: network=10.192.0.0/16 gateway=10.192.0.1
output=`python3 -c "import ipaddress; net=ipaddress.ip_network(\"$cidr\", strict=False); print('network=%s\ngateway=%s'%(net, net.network_address + 1))"`
network=$(echo "$output" | awk -F= '/^network=/ {print $2}')
gateway=$(echo "$output" | awk -F= '/^gateway=/ {print $2}')
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

echo "write external_ids to OVSDB..."
args=$(for k in "${!external_ids[@]}"; do echo -n "$k=${external_ids[$k]} "; done)
ovs-vsctl set Open_vSwitch . $args

echo "OVS external_ids after update:"
ovs-vsctl get Open_vSwitch . external_ids

echo "done"
