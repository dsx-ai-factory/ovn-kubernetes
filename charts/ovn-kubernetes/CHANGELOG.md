# 1.5.18
OVNK8s-Interconnect
- declare the DPU token volume before DaemonSet validation when SPIFFE-derived tokens are enabled.
- re-plumb DPU management ports when the host-published device changes, with retry and restart handling.

# 1.5.17
OVNK8s-Interconnect
- improve Uplink default-gateway discovery for VRF and multipath routes and discover gateways added after startup.
- fix DPU pod representor selection on hosts with multiple cards by resolving VFs through the host PF MAC.
- reconcile DPU management ports when the host-published device changes.

# 1.5.16
OVNK8s-Interconnect
- [VMAAS-2126] support DPF serviceDaemonSet metadata, scheduling, update strategy, and resources.
- [VMAAS-2192] support injected token files for DPU API authentication.
- [VMAAS-2136] authorize additional groups with OVN node RBAC.
- allow DPU deployments to opt out of API-server TLS certificate verification.
- reconcile CUDN gateway programming when UplinkState configuration changes.

# 1.5.15
OVNK8s-Interconnect
- preserve kernel-protocol routes when moving interfaces into VRFs
- resolve DPU gateway representors by host function
- reduce OVSDB cache scans during route, policy, NAT, and bridge reconciliation

OVN-Northd
- fix connection tracking and offload for force-SNAT load-balancer VIPs
- allow remote logical switch ports representing routers to be excluded from L2 multicast flooding

# 1.5.14
OVNK8s-Interconnect
- move CRDs into a dedicated ovnkube-crds subchart so deployments can disable CRD installation
- [SDN-4607] fix a nil-pointer crash in the AdminPolicyBasedRoute controller
- identify DPU hosts by their primary-address annotation when selecting BGP next hops
- support configurable kubelet cgroup paths and restart tracking, and tolerate unavailable host-isolation prerequisites during node startup
- preserve conntrack entries for IPs owned by another pod during CNI cleanup
- fix EgressIP cleanup after namespace deletion and clean up UDN pods after NAD deletion
- handle missing namespaces when resolving primary networks and assigning infrastructure-locked roles

# 1.5.13
OVNK8s-Interconnect
- [SDN-2498] export OVS DOCA metrics from coverage/show
- bound OVS/OVN metrics collection with command timeouts and a total scrape budget
- [SDN-4612] omit per-VF data from RTM_GETLINK to prevent PF netlink stalls
- publish derived VRF device names in UDN and CUDN status
- serve pprof/debug endpoints on loopback with retry and graceful shutdown

# 1.5.12
OVNK8s-Interconnect
- fix single-node-zone rendering when mgmtPortVF resources are configured
- skip EgressIP nodes with missing, malformed, or empty host-cidrs annotations
- [OKEP-6224] add DHCP IPAM support for localnet UDNs

# 1.5.11
OVNK8s-Interconnect
- uplink: resolve the DPU gateway representor by host function
- uplink: read default gateways from the interface's routing table
- Uplink: report host-side gateway programming on HostGatewayReady
- vrfmanager: preserve slave interface routes across enslavement
- Uplink API: add the HostGatewayReady UplinkState condition

# 1.5.10
OVNK8s-Interconnect
- resolve DPU Uplink bridges from host-function information published in UplinkState status
- [VMAAS-1746] recover UplinkStates deleted out of band and restore gateway readiness
- fix CUDN gateway-router ARP/NDP storms and FDB learning on external bridges
- port the remaining node-side Service rules from iptables to nftables
- [CORENET-7288] reduce controller memory usage by trimming cached Node objects

# 1.5.9
OVNK8s-Interconnect
- resolve DPU Uplink bridges for VF- and SF-backed host interfaces
- fix split-DPU UplinkState condition ownership, retry and recovery, and MAC validation
- fix no-overlay SNAT exemptions and remove stale duplicate NATs
- advertise dynamic Layer2 networks with transit-router topology
- prevent platform tunnel-ID collisions and unnecessary northd full recomputes
- [VMAAS-1637] refresh RouteAdvertisement failure status and make errors deterministic

# 1.5.8
- add enableUplink flag for the ovnkube-ndoe-dpu static pod spec

# 1.5.7
OVNK8s-Interconnect
- [VMAAS-1706] add the DPU-host Prometheus discovery Service
- [SDN-4340] add the IC control-plane readiness probe and PodDisruptionBudget
- add and enable Uplink support
- [SDN-4584] apply host OVS tuning and ovn-encap-tos on IC nodes
- fix UDN masquerade ARP storms and Layer2 UDN same-node NodePort routing
- [SDN-4557] rate-limit API-server reads while waiting for pod annotations
- revert live pod GETs after annotation conflicts to restore scale latency
- re-evaluate Egress IPs when cloud egress-IP configuration changes
- improve DPU-host setup and OVS PMD affinity handling

# 1.5.6
OVNK8s-Interconnect
- [VMAAS-1313] ovnkube-node-dpu: bind the metrics endpoint to 127.0.0.1
- [VMAAS-1609] node: set ovsClient in the localnet node network controller
- ovn: complete egress node setup during reconciliation
- restore DPU route import VRFs when no Uplinks are configured
- routemanager: normalize IPv6 route metric to the kernel default
- scale: reduce UDN isolation, stale pod SNAT, namespace filtering and address lookup overhead

# 1.5.5
OVNK8s-Interconnect
- add global.routingTableIDStart to configure ovnkube-node's routing table ID start (VMaaS defaults to 1002)
- add CRDs and RBAC for the new Uplink / UplinkState API (VRF-Lite shared gateway, OKEP-6019)
- [SDN-4412] IPReservation: skip re-allocation when IPs already reserved
- [SDN-4562] ovnkube-node does not generate DAN config in IC mode
- [VMAAS-1469] ovnkube.sh: unify gateway option loading from OVS external_ids
- no-overlay: add CUDN support for managed routing
- udn: add VRF routes for all UDN cluster subnets
- node: don't short-circuit management port IP ingress to the physical bridge
- fix traffic leak in egress IP on secondary interface
- node: don't crash-loop on UDN management-port sysctl failure
- node: fix DPUHost controller crash when node-primary-ifaddr is not yet set
- node: reconcile DPU/DPUHost gateway annotations on IP change
- node: set gateway FDB on DPU host representor
- enable nodeport based on per-node configuration
- no-overlay: route host -> pods on other nodes via mp0
- routeadvertisements: advertise dynamic UDNs only from active nodes
- allow networks to start while route advertisements settle
- support updating the chassis-id without re-creating the node
- don't change the IPv4 FORWARD chain default policy, and don't override it when disable-forwarding is unset
- use the new IPv6 force_forwarding sysctl when available
- drop legacy external-gateway annotation handling from apbroute, node, util and config

OVN-Northd
- rebase to OVN 26.03.2 (from 25.09.1)
- ovn-ic: add Transit Router support, including transit router ports
- EVPN: learn IP neighbors through EVPN, advertise local type-2 routes, support multiple ovn-evpn-local-ip values and EVPN device names, and learn workload FDB from ingress traffic
- controller, northd: let the CMS specify the VRF table_id for advertised routes, and add an option to disable route learning
- northd: add requested-encap-ip option for logical ports
- northd: set default value of ignore_chassis_features to true
- encaps: use chassis row UUID for tunnel name generation
- chassis: clear stale chassis according to hostname
- controller: disable ofctrl probe from OVS side

OVS
- rebase to OVS 3.7.2 (from 3.6.1)
- ovs-router: add multi-table routing and route rules (ovs/route/rule/{add,del,show}, table=id)

# 1.5.4
OVNK8s-Interconnect
- [VMAAS-1470] dist/ovnkube-init: select host-cidr by node InternalIP on DPU
- get coredns cluster IP from values.yaml and restore coredns-based readiness probe
- [SDN-4549] add COREDNS_CLUSTER_IP to ovnkube-single-node-zone
- add missing config in the ovnkube-node-dpu chart
- [SDN-4563] clean up deployment modes

OVN-Northd
- northd: add back check for datapath hmap (fixes northd crash)

# 1.5.3
- specify ephemeral-port-range for dpu static pod
- vmaas: advertise CDN network and override its frrconfig selector

# 1.5.2
- set the VRF to exec in DPU to mgmt by default
- disable dynamic UDN since it not playing along with RouteAdvertisement (see: VMAAS-1456)

# 1.5.1
- introduce ovnkube-single-node-zone-dpu (new subchart)
- add support for running ovnkube-controller and ovnkube-init in VRF
- add new values files for running OVNK8s in NoOverlay mode in DSX
- add k8s resources for the ovnk8s-dpu-deployer to consume
- add K8S_TOKEN and K8S_CACERT_DATA env vars to single-node-zone-dpu daemonset
- add restart annotation to trigger ovnk8s components rollout during ski-lift
- enable ovnkube-node-dpu-host to run on both amd64/arm64 nodes
- fix RBAC for ovnkube-node-dpu-host serviceaccount
- VMAAS-1348 Revert "Create UDN VRFs in DPU mode"
- VMAAS-1350 ovn: scope GR-to-cluster-router routes to node subnet in no-overlay mode
- VMAAS-1285 incorrect assumption about the VLAN in SetStaticFDBEntry
- VMAAS-1284 incorrect assumption about the port in SetStaticFDBEntry
- VMAAS-1283 guard NadConfigController.Stop against nil receiver
- VMAAS-1452 duplicate cluster SNAT entries in Node GR
- VMAAS-1453 don't short-circuit management port IP ingress to the physical bridge
- VMAAS-1454 fix OVS external_ids setup in ovnkube-init container
  
# 1.5.0
- Initial release of Helm Chart tracking ovn-kubernetes@nv-1.5 branch
- Has support for
  - NoOverlay support for cluster-wide-default network
  - OVN K8s Interonnect is the only supported mode
