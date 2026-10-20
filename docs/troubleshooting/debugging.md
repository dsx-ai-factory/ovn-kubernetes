## Initial setup issues.

### Each host should have unique system-ids.

In a OVN cluster, each host should have a unique system-id.  This value
is set in external-ids:system-id of the Open_vSwitch table of each host's
Open_vSwitch database.  You can fetch its value with:

```
ovs-vsctl get Open_vSwitch . external-ids:system-id
```

This is automatically set by OVS startup scripts (that come with packages).
But if you installed OVS from source, and don't use a startup script,
this will be empty.  If you clone your VM, then you may endup with
same system-id for all your VMs - which is a problem.

### All nodes should register with OVN SB database.

On the master, run:

```
ovn-sbctl list chassis
```

You should see as many records as the number of nodes in your cluster.  The
"name" column in each record represents the system-id of each host and they
should all be unique.

## Hosts should have unique node names.

When you run 'ovnkube -init-node', 'ovnkube -init-ovnkube-controller'
or 'ovnkube -init-cluster-manager' commands, it will ask for node names.
This should all be unique.
This should also be the same as used by "kubelet" on each node.
So the names you see when you run the below command should be the same
ones that you supply to ovnkube:

```
kubectl get nodes
```

### Check the presence of "geneve" or "stt" kernel module.

If you cannot ping between pods in 2 nodes, make sure to check whether
you have the OVS kernel modules correctly installed by running:

```
lsmod | grep openvswitch
lsmod | grep geneve  # Or "stt" if your encap type was chosen to be "stt".
```

If you do not see the kernel module, modprobe them

```
modprobe openvswitch
modprobe vport-geneve  # or vport-stt for "stt".
```

### Sanity check cross host ping.

Since on each host, an OVS internal device named "k8s-$NODENAME" is created
with the second IP address in the subnet assigned for that node, you should be
able to check cross host connectivity by pinging these internal devices across
hosts.

### Firewall blocking overlay networks

If your host has a firewall blocking incoming connections as a default policy,
you should open up ports to allow overlay networks.  If you use "geneve" as the
encapsulation type, you should open up UDP port 6081.  If you use "stt" as the
encapsulation type, you should open up TCP port 7471.

You can use the following command to achieve it via iptables.

```
# To open up Geneve port.
/usr/share/openvswitch/scripts/ovs-ctl --protocol=udp \
        --dport=6081 enable-protocol

# To open up STT port.
/usr/share/openvswitch/scripts/ovs-ctl --protocol=tcp \
        --dport=7471 enable-protocol
```

### Check ovn-northd's log file.

On the node running ovn-northd, look at /var/log/openvswitch/ovn-northd.log
for any errors with the OVN database and northd setup.

## Runtime issues

### Check the watcher's log file.

On the master, check whether ovnkube is running by:

```
ps -ef | grep ovnkube
```

Check the watcher's log file at "/var/log/ovn-kubernetes/ovnkube.log"
to see whether it is creating logical ports whenever a pod is created and
for any obvious errors.

### Check the OVN CNI log file.

When you create a pod and it gets scheduled on a particular host, the
OVN CNI plugin on that host, tries to access the pod's information from
the K8s api server.  Specifically, it tries to get the IP address and
mac address for that pod.  This information is logged in the OVN CNI log
file on each node if you have specified a log file via
"/etc/openvswitch/ovn_k8s.conf". You can read how to provide a logfile
by reading 'man ovn_k8s.conf.5'.

### Check the kubelet's log file.

If there were any issues with downloading upstream CNI plugins, then
kubelet will complain about them in its log file.

### Check ovn-controller's log file.

If you suspect issues on only one of the host, look at the log file of
ovn-controller at /var/log/openvswitch/ovn-controller.log to see any
obvious error messages.

### Read the ovnkube-node readiness probe event.

When an ovnkube-node pod is not Ready, the `Unhealthy` event on the pod names
the check that failed:

```
kubectl -n ovn-kubernetes get events \
  --field-selector involvedObject.kind=Pod,involvedObject.name=<ovnkube-node-pod>,reason=Unhealthy
```

The readiness probe runs `/root/ovnkube-node-readiness-check.sh` in the
`ovnkube-controller` container on full-mode nodes and in the `ovnkube-node`
container on DPU hosts. It performs three checks in order and stops at the
first failure:

1. a TLS connection to the ovnkube-node metrics endpoint, which is one of the
   last things ovnkube-node starts;
2. an HTTPS request to `/readyz` on the Kubernetes API service VIP
   (`KUBERNETES_SERVICE_HOST`), to prove the node can reach the cluster
   network;
3. an A-record lookup of each name in `EXTERNAL_DNS_NAMES` (default
   `google.com cloudflare.com`) against the CoreDNS cluster IP
   (`COREDNS_CLUSTER_IP`), passing as soon as one name resolves.

Each check is time-bounded, so the script finishes within the probe's
`timeoutSeconds` (15s in the Helm charts) and the event reports the failing
check together with the tool's own error, for example:

```
connectivity check on k8s svc endpoint 10.112.0.1:443 failed (rc=28)
curl: (28) Failed to connect to 10.112.0.1 port 443 after 2001 ms: Timeout was reached
```

An event that only says `command timed out` with no such line means the script
itself was killed by the kubelet, which should not happen with the bounded
checks: exec into that container and run the script by hand to see which step
takes too long.
