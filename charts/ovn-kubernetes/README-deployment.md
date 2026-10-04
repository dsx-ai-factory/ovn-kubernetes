

## Values

<table>
	<thead>
		<th>Key</th>
		<th>Type</th>
		<th>Default</th>
		<th>Description</th>
	</thead>
	<tbody>
		<tr>
			<td>global.advertiseAllTenantCUDNs</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Advertise every tenant ClusterUserDefinedNetwork, and render the vm-uplink Uplink alongside it</td>
		</tr>
		<tr>
			<td>global.advertiseDefaultNetwork</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Whether to deploy a RouteAdvertisements that advertises the cluster-wide default network</td>
		</tr>
		<tr>
			<td>global.advertisedUDNIsolationMode</td>
			<td>string</td>
			<td><pre lang="json">
"strict"
</pre>
</td>
			<td>Pod network isolation between advertised UDN networks. (strict or loose)</td>
		</tr>
		<tr>
			<td>global.allowICMPNetworkPolicy</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Configure to allow ICMP and ICMPv6 traffic to bypass NetworkPolicy deny rules</td>
		</tr>
		<tr>
			<td>global.clusterDefaultNad</td>
			<td>string</td>
			<td><pre lang="json">
"default/cluster-network"
</pre>
</td>
			<td>name of the cluster wide default net-attach-def, as namespace/name.</td>
		</tr>
		<tr>
			<td>global.coreDNSServiceName</td>
			<td>string</td>
			<td><pre lang="json">
"kube-dns"
</pre>
</td>
			<td>name of coredns service, used by ovnkube-node for readiness check</td>
		</tr>
		<tr>
			<td>global.corednsClusterIP</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td>fallback coredns cluster IP, required when the coredns service can't be looked up (e.g. lookup is disabled in argocd)</td>
		</tr>
		<tr>
			<td>global.dockerConfigSecret.auth</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td>base64 encoded username:password credentials to pull all the OVN K8s component's container images</td>
		</tr>
		<tr>
			<td>global.dockerConfigSecret.create</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>creates a k8s dockerconfigjson secret using the registry and auth information</td>
		</tr>
		<tr>
			<td>global.dockerConfigSecret.registry</td>
			<td>string</td>
			<td><pre lang="json">
"nvcr.io"
</pre>
</td>
			<td>name of the container registry hosting the images</td>
		</tr>
		<tr>
			<td>global.dynamicUDNGracePeriod</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>global.egressIpHealthCheckPort</td>
			<td>int</td>
			<td><pre lang="json">
9107
</pre>
</td>
			<td>Configure EgressIP node reachability using gRPC on this TCP port</td>
		</tr>
		<tr>
			<td>global.emptyLbEvents</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>If set, then load balancers do not get deleted when all backends are removed</td>
		</tr>
		<tr>
			<td>global.enableAdminNetworkPolicy</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Whether or not to use Admin Network Policy CRD feature with ovn-kubernetes</td>
		</tr>
		<tr>
			<td>global.enableAdminPbr</td>
			<td>bool</td>
			<td><pre lang="json">
true
</pre>
</td>
			<td>Configure to use AdminPolicyBasedRoute CRD feature with ovn-kubernetes</td>
		</tr>
		<tr>
			<td>global.enableDNSNameResolver</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Configure to use OpenShift DNSNameResolver CRD to resolve DNS records for EgressFirewall.</td>
		</tr>
		<tr>
			<td>global.enableDynamicUDNAllocation</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Enables dynamic UDN allocation</td>
		</tr>
		<tr>
			<td>global.enableEVPN</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>global.enableEgressFirewall</td>
			<td>bool</td>
			<td><pre lang="json">
true
</pre>
</td>
			<td>Configure to use EgressFirewall CRD feature with ovn-kubernetes</td>
		</tr>
		<tr>
			<td>global.enableEgressIp</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Configure to use EgressIP CRD feature with ovn-kubernetes</td>
		</tr>
		<tr>
			<td>global.enableEgressQos</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Configure to use EgressQoS CRD feature with ovn-kubernetes</td>
		</tr>
		<tr>
			<td>global.enableEgressService</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Configure to use EgressService CRD feature with ovn-kubernetes</td>
		</tr>
		<tr>
			<td>global.enableLFlowCache</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Enables the OVN logical flow cache</td>
		</tr>
		<tr>
			<td>global.enableMulticast</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Enables multicast support between the pods within the same namespace</td>
		</tr>
		<tr>
			<td>global.enableNetworkConnect</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Enables UDN interconnect</td>
		</tr>
		<tr>
			<td>global.enableNetworkQos</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Enables network QoS support from/to pods</td>
		</tr>
		<tr>
			<td>global.enableNetworkSegmentation</td>
			<td>bool</td>
			<td><pre lang="json">
true
</pre>
</td>
			<td>Whether or not enable network segmentation, aka UDN</td>
		</tr>
		<tr>
			<td>global.enableNoOverlay</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Configure to enable no-overlay mode for the default network</td>
		</tr>
		<tr>
			<td>global.enableNoOverlayManagedRouting</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Configure to enable managed routing mode for no-overlay</td>
		</tr>
		<tr>
			<td>global.enableNoOverlaySnat</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Configure to enable SNAT for outbound traffic in no-overlay mode</td>
		</tr>
		<tr>
			<td>global.enableOvnKubeIdentity</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Whether or not enable ovnkube identity webhook</td>
		</tr>
		<tr>
			<td>global.enablePersistentIPs</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>If set, enable persistent IPs</td>
		</tr>
		<tr>
			<td>global.enablePreconfiguredUDNAddresses</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Configure to enable workloads with preconfigured network connect to UDNs</td>
		</tr>
		<tr>
			<td>global.enableRouteAdvertisements</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Configure to use route advertisements feature with ovn-kubernetes</td>
		</tr>
		<tr>
			<td>global.enableTracing</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Enables OpenTelemetry tracing for OVN-Kubernetes</td>
		</tr>
		<tr>
			<td>global.enableUplink</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Configure to enable the uplink feature with ovn-kubernetes</td>
		</tr>
		<tr>
			<td>global.frrConfigDefaultNetworkLabels</td>
			<td>object</td>
			<td><pre lang="json">
{}
</pre>
</td>
			<td>labels selecting the FRRConfiguration for the default network. Empty selects {name: receive-all}.</td>
		</tr>
		<tr>
			<td>global.frrConfigTenantCUDNLabels</td>
			<td>object</td>
			<td><pre lang="json">
{}
</pre>
</td>
			<td>labels selecting the FRRConfiguration for tenant CUDNs. Empty selects {name: receive-all}.</td>
		</tr>
		<tr>
			<td>global.gatewayOptions</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>global.imagePullSecretName</td>
			<td>string</td>
			<td><pre lang="json">
"ovncni-nvcr-ips"
</pre>
</td>
			<td>name of the secret used to pull container images used by various OVN K8s components</td>
		</tr>
		<tr>
			<td>global.imageRepositoryPrefix</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td>registry and path prepended to every image.repository that is not already fully qualified, e.g. myregistry.example.com/sdn. Required unless every image.repository is overridden with a value that includes a registry host.</td>
		</tr>
		<tr>
			<td>global.kata.danConfigDir</td>
			<td>string</td>
			<td><pre lang="json">
"/run/kata-containers/dans"
</pre>
</td>
			<td>host path for Kata DAN config directory; mounted into the container at /var/run/ovn-kubernetes/dans</td>
		</tr>
		<tr>
			<td>global.kata.enableDAN</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>enable Kata Containers DAN (Directly Attachable Network) integration</td>
		</tr>
		<tr>
			<td>global.kubeletCgroupPath</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td>kubelet cgroup path ovnkube-node should use</td>
		</tr>
		<tr>
			<td>global.managedBGPASNumber</td>
			<td>int</td>
			<td><pre lang="json">
64512
</pre>
</td>
			<td>BGP AS number for managed routing mode</td>
		</tr>
		<tr>
			<td>global.managedBGPFRRNamespace</td>
			<td>string</td>
			<td><pre lang="json">
"frr-k8s-system"
</pre>
</td>
			<td>FRR namespace for managed routing mode</td>
		</tr>
		<tr>
			<td>global.managedBGPTopology</td>
			<td>string</td>
			<td><pre lang="json">
"full-mesh"
</pre>
</td>
			<td>BGP topology for managed routing mode (currently only full-mesh)</td>
		</tr>
		<tr>
			<td>global.namespace</td>
			<td>string</td>
			<td><pre lang="json">
"ovn-kubernetes"
</pre>
</td>
			<td>namespace where all the OVN K8s components' resources must be deployed</td>
		</tr>
		<tr>
			<td>global.routingTableIDStart</td>
			<td>int</td>
			<td><pre lang="json">
0
</pre>
</td>
			<td>first routing table ID ovnkube-node may allocate, e.g. 1002</td>
		</tr>
		<tr>
			<td>global.skipCTMarkHostPorts</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td>host ports excluded from conntrack marking, e.g. "6081/udp"</td>
		</tr>
		<tr>
			<td>global.skipCallToK8s</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>whether to skip lookup function or not</td>
		</tr>
		<tr>
			<td>global.tls.enable</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>global.tracing.batchTimeout</td>
			<td>int</td>
			<td><pre lang="json">
5
</pre>
</td>
			<td>Batch span processor timeout in seconds</td>
		</tr>
		<tr>
			<td>global.tracing.endpoint</td>
			<td>string</td>
			<td><pre lang="json">
"127.0.0.1:4317"
</pre>
</td>
			<td>OTLP gRPC endpoint for the trace exporter</td>
		</tr>
		<tr>
			<td>global.tracing.exportTimeout</td>
			<td>int</td>
			<td><pre lang="json">
10
</pre>
</td>
			<td>Exporter timeout in seconds</td>
		</tr>
		<tr>
			<td>global.tracing.maxExportBatchSize</td>
			<td>int</td>
			<td><pre lang="json">
512
</pre>
</td>
			<td>Maximum spans per export batch</td>
		</tr>
		<tr>
			<td>global.tracing.maxQueueSize</td>
			<td>int</td>
			<td><pre lang="json">
2048
</pre>
</td>
			<td>Maximum queued spans before dropping</td>
		</tr>
		<tr>
			<td>global.tracing.propagatedContextAnnotationKey</td>
			<td>string</td>
			<td><pre lang="json">
"tracing.k8s.io/traceparent"
</pre>
</td>
			<td>Pod annotation key for propagated tracing context</td>
		</tr>
		<tr>
			<td>global.tracing.propagatedContextMode</td>
			<td>string</td>
			<td><pre lang="json">
"linked"
</pre>
</td>
			<td>Propagated context mode. Supported values: linked, parent.</td>
		</tr>
		<tr>
			<td>global.tracing.samplingRate</td>
			<td>float</td>
			<td><pre lang="json">
1
</pre>
</td>
			<td>Root trace sampling ratio in range [0.0, 1.0]</td>
		</tr>
		<tr>
			<td>global.tracing.serviceName</td>
			<td>string</td>
			<td><pre lang="json">
"ovn-kubernetes"
</pre>
</td>
			<td>OpenTelemetry service.name resource attribute</td>
		</tr>
		<tr>
			<td>global.tracing.tlsCACert</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td>CA certificate path for TLS client</td>
		</tr>
		<tr>
			<td>global.tracing.tlsInsecureSkipVerify</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Skip server certificate verification (insecure)</td>
		</tr>
		<tr>
			<td>global.tracing.useTLS</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>Enable TLS for OTLP transport</td>
		</tr>
		<tr>
			<td>global.udnAllowedDefaultServices</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td>comma separated list of default network services allowed to be accessed from UDN pods, e.g. "namespace1/service1,namespace2/service2"</td>
		</tr>
		<tr>
			<td>global.useImageDigest</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>embed image digest in container image reference</td>
		</tr>
		<tr>
			<td>global.v4JoinSubnet</td>
			<td>string</td>
			<td><pre lang="json">
"100.64.0.0/16"
</pre>
</td>
			<td>The v4 join subnet used for assigning join switch IPv4 addresses</td>
		</tr>
		<tr>
			<td>global.v4MasqueradeSubnet</td>
			<td>string</td>
			<td><pre lang="json">
"169.254.0.0/17"
</pre>
</td>
			<td>The v4 masquerade subnet used for assigning masquerade IPv4 addresses</td>
		</tr>
		<tr>
			<td>global.v4TransitSubnet</td>
			<td>string</td>
			<td><pre lang="json">
"100.88.0.0/16"
</pre>
</td>
			<td>The v4 transit subnet</td>
		</tr>
		<tr>
			<td>global.vmUplinkInterfaceName</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td>uplink host interface for the vm-uplink Uplink. Only read when advertiseAllTenantCUDNs is enabled.</td>
		</tr>
		<tr>
			<td>k8sAPIServer</td>
			<td>string</td>
			<td><pre lang="json">
null
</pre>
</td>
			<td>K8s API Server address</td>
		</tr>
		<tr>
			<td>k8sCACertData</td>
			<td>string</td>
			<td><pre lang="json">
null
</pre>
</td>
			<td>host k8s cluster's CA certificate</td>
		</tr>
		<tr>
			<td>k8sToken</td>
			<td>string</td>
			<td><pre lang="json">
null
</pre>
</td>
			<td>JWT token for the host k8s cluster</td>
		</tr>
		<tr>
			<td>mtu</td>
			<td>int</td>
			<td><pre lang="json">
1440
</pre>
</td>
			<td>MTU of the primary network of the Pod</td>
		</tr>
		<tr>
			<td>ovnkube-control-plane.ovnkubeControlPlane.image.repository</td>
			<td>string</td>
			<td><pre lang="json">
"ovnkube"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-control-plane.ovnkubeControlPlane.image.sha256</td>
			<td>string</td>
			<td><pre lang="json">
"84a0aee9c07693b22a3b8b1e8e4ed917c573c37412d92641081e292046f6b2c3"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-control-plane.ovnkubeControlPlane.image.tag</td>
			<td>string</td>
			<td><pre lang="json">
"cbcda82f-71385617-7852734"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-identity.ovnkubeIdentity.image.repository</td>
			<td>string</td>
			<td><pre lang="json">
"ovnkube"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-identity.ovnkubeIdentity.image.sha256</td>
			<td>string</td>
			<td><pre lang="json">
"84a0aee9c07693b22a3b8b1e8e4ed917c573c37412d92641081e292046f6b2c3"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-identity.ovnkubeIdentity.image.tag</td>
			<td>string</td>
			<td><pre lang="json">
"cbcda82f-71385617-7852734"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-node-dpu-host.image.repository</td>
			<td>string</td>
			<td><pre lang="json">
"ovnkube"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-node-dpu-host.image.sha256</td>
			<td>string</td>
			<td><pre lang="json">
"84a0aee9c07693b22a3b8b1e8e4ed917c573c37412d92641081e292046f6b2c3"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-node-dpu-host.image.tag</td>
			<td>string</td>
			<td><pre lang="json">
"cbcda82f-71385617-7852734"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZone.image.repository</td>
			<td>string</td>
			<td><pre lang="json">
"ovnkube"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZone.image.sha256</td>
			<td>string</td>
			<td><pre lang="json">
"84a0aee9c07693b22a3b8b1e8e4ed917c573c37412d92641081e292046f6b2c3"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZone.image.tag</td>
			<td>string</td>
			<td><pre lang="json">
"cbcda82f-71385617-7852734"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneNbdb.image.repository</td>
			<td>string</td>
			<td><pre lang="json">
"ovnkube"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneNbdb.image.sha256</td>
			<td>string</td>
			<td><pre lang="json">
"84a0aee9c07693b22a3b8b1e8e4ed917c573c37412d92641081e292046f6b2c3"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneNbdb.image.tag</td>
			<td>string</td>
			<td><pre lang="json">
"cbcda82f-71385617-7852734"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneNorthd.image.repository</td>
			<td>string</td>
			<td><pre lang="json">
"ovnkube"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneNorthd.image.sha256</td>
			<td>string</td>
			<td><pre lang="json">
"84a0aee9c07693b22a3b8b1e8e4ed917c573c37412d92641081e292046f6b2c3"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneNorthd.image.tag</td>
			<td>string</td>
			<td><pre lang="json">
"cbcda82f-71385617-7852734"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneOvnHost.image.repository</td>
			<td>string</td>
			<td><pre lang="json">
"ovnkube"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneOvnHost.image.sha256</td>
			<td>string</td>
			<td><pre lang="json">
"84a0aee9c07693b22a3b8b1e8e4ed917c573c37412d92641081e292046f6b2c3"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneOvnHost.image.tag</td>
			<td>string</td>
			<td><pre lang="json">
"cbcda82f-71385617-7852734"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneSbdb.image.repository</td>
			<td>string</td>
			<td><pre lang="json">
"ovnkube"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneSbdb.image.sha256</td>
			<td>string</td>
			<td><pre lang="json">
"84a0aee9c07693b22a3b8b1e8e4ed917c573c37412d92641081e292046f6b2c3"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneSbdb.image.tag</td>
			<td>string</td>
			<td><pre lang="json">
"cbcda82f-71385617-7852734"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZone.image.repository</td>
			<td>string</td>
			<td><pre lang="json">
"ovnkube"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZone.image.sha256</td>
			<td>string</td>
			<td><pre lang="json">
"84a0aee9c07693b22a3b8b1e8e4ed917c573c37412d92641081e292046f6b2c3"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZone.image.tag</td>
			<td>string</td>
			<td><pre lang="json">
"cbcda82f-71385617-7852734"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneNbdb.image.repository</td>
			<td>string</td>
			<td><pre lang="json">
"ovnkube"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneNbdb.image.sha256</td>
			<td>string</td>
			<td><pre lang="json">
"84a0aee9c07693b22a3b8b1e8e4ed917c573c37412d92641081e292046f6b2c3"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneNbdb.image.tag</td>
			<td>string</td>
			<td><pre lang="json">
"cbcda82f-71385617-7852734"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneNorthd.image.repository</td>
			<td>string</td>
			<td><pre lang="json">
"ovnkube"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneNorthd.image.sha256</td>
			<td>string</td>
			<td><pre lang="json">
"84a0aee9c07693b22a3b8b1e8e4ed917c573c37412d92641081e292046f6b2c3"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneNorthd.image.tag</td>
			<td>string</td>
			<td><pre lang="json">
"cbcda82f-71385617-7852734"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneOvnHost.image.repository</td>
			<td>string</td>
			<td><pre lang="json">
"ovnkube"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneOvnHost.image.sha256</td>
			<td>string</td>
			<td><pre lang="json">
"84a0aee9c07693b22a3b8b1e8e4ed917c573c37412d92641081e292046f6b2c3"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneOvnHost.image.tag</td>
			<td>string</td>
			<td><pre lang="json">
"cbcda82f-71385617-7852734"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneSbdb.image.repository</td>
			<td>string</td>
			<td><pre lang="json">
"ovnkube"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneSbdb.image.sha256</td>
			<td>string</td>
			<td><pre lang="json">
"84a0aee9c07693b22a3b8b1e8e4ed917c573c37412d92641081e292046f6b2c3"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneSbdb.image.tag</td>
			<td>string</td>
			<td><pre lang="json">
"cbcda82f-71385617-7852734"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovs-node.ovsDaemonsContainer.image.repository</td>
			<td>string</td>
			<td><pre lang="json">
"ovs"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovs-node.ovsDaemonsContainer.image.sha256</td>
			<td>string</td>
			<td><pre lang="json">
"655a15134866f70f6410dff0578adc8c183eecfa436619e4c179f6f8f267716c"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovs-node.ovsDaemonsContainer.image.tag</td>
			<td>string</td>
			<td><pre lang="json">
"963f58b0-35519317-af693ae"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>podNetwork</td>
			<td>string</td>
			<td><pre lang="json">
null
</pre>
</td>
			<td>OVN Pod Network CIDR. The primary interface of the Pod is assigned IP from this range. These IPs are overlay IPs. Every K8s node will get a /26 subnet (from the /16) and Pods landing on that node will get IP from the /26 subnet. Based on the cluster size, /16 can be adjusted. Similarly, based on the pod-density on the node, /26 can be adjusted.</td>
		</tr>
		<tr>
			<td>serviceNetwork</td>
			<td>string</td>
			<td><pre lang="json">
"10.96.0.0/12"
</pre>
</td>
			<td>K8s Service IP range</td>
		</tr>
		<tr>
			<td>tags</td>
			<td>object</td>
			<td><pre lang="json">
{
  "ovnkube-control-plane": true,
  "ovnkube-crds": true,
  "ovnkube-identity": false,
  "ovnkube-node-dpu-host": false,
  "ovnkube-single-node-zone": true,
  "ovnkube-single-node-zone-dpu": false,
  "ovs-node": false
}
</pre>
</td>
			<td>Subchart toggles.</td>
		</tr>
		<tr>
			<td>ovnkube-control-plane.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].key</td>
			<td>string</td>
			<td><pre lang="json">
"node-role.kubernetes.io/control-plane"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-control-plane.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].operator</td>
			<td>string</td>
			<td><pre lang="json">
"Exists"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-control-plane.affinity.podAntiAffinity.requiredDuringSchedulingIgnoredDuringExecution[0].labelSelector.matchExpressions[0].key</td>
			<td>string</td>
			<td><pre lang="json">
"name"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-control-plane.affinity.podAntiAffinity.requiredDuringSchedulingIgnoredDuringExecution[0].labelSelector.matchExpressions[0].operator</td>
			<td>string</td>
			<td><pre lang="json">
"In"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-control-plane.affinity.podAntiAffinity.requiredDuringSchedulingIgnoredDuringExecution[0].labelSelector.matchExpressions[0].values[0]</td>
			<td>string</td>
			<td><pre lang="json">
"ovnkube-control-plane"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-control-plane.affinity.podAntiAffinity.requiredDuringSchedulingIgnoredDuringExecution[0].topologyKey</td>
			<td>string</td>
			<td><pre lang="json">
"kubernetes.io/hostname"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-control-plane.monitoring</td>
			<td>object</td>
			<td><pre lang="json">
{
  "serviceMonitorSelectorLabels": {
    "k8s-app": "ovnkube-control-plane",
    "release": "prometheus"
  }
}
</pre>
</td>
			<td>prometheus monitoring related fields</td>
		</tr>
		<tr>
			<td>ovnkube-control-plane.monitoring.serviceMonitorSelectorLabels</td>
			<td>object</td>
			<td><pre lang="json">
{
  "k8s-app": "ovnkube-control-plane",
  "release": "prometheus"
}
</pre>
</td>
			<td>specify the labels for serviceMonitors to be selected for target discovery. Prometheus operator defines what namespaces and what servicemonitors within these namespaces must be selected for target discovery. The fields defined below helps in defining that.</td>
		</tr>
		<tr>
			<td>ovnkube-control-plane.ovnkubeControlPlane.logging.level</td>
			<td>string</td>
			<td><pre lang="json">
"5"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-control-plane.replicas</td>
			<td>int</td>
			<td><pre lang="json">
1
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-control-plane.tolerations</td>
			<td>list</td>
			<td><pre lang="json">
[
  {
    "operator": "Exists"
  }
]
</pre>
</td>
			<td>tolerations for the ovnkube-control-plane pods.</td>
		</tr>
		<tr>
			<td>ovnkube-identity.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].key</td>
			<td>string</td>
			<td><pre lang="json">
"node-role.kubernetes.io/control-plane"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-identity.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].operator</td>
			<td>string</td>
			<td><pre lang="json">
"Exists"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-identity.logLevel</td>
			<td>int</td>
			<td><pre lang="json">
4
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-identity.tolerations</td>
			<td>list</td>
			<td><pre lang="json">
[
  {
    "operator": "Exists"
  }
]
</pre>
</td>
			<td>tolerations for the ovnkube-identity pods.</td>
		</tr>
		<tr>
			<td>ovnkube-node-dpu-host.disableFirewalld</td>
			<td>bool</td>
			<td><pre lang="json">
true
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-node-dpu-host.gatewayOpts</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-node-dpu-host.mgmtPortVFResourceName</td>
			<td>string</td>
			<td><pre lang="json">
"nvidia.com/asap2_mport_vf"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-node-dpu-host.mgmtPortVFsCount</td>
			<td>int</td>
			<td><pre lang="json">
1
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-node-dpu-host.nodeMgmtPortNetdev</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-node-dpu-host.nodeSelector."k8s.ovn.org/dpu-host"</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-node-dpu-host.tolerations</td>
			<td>list</td>
			<td><pre lang="json">
[
  {
    "operator": "Exists"
  }
]
</pre>
</td>
			<td>tolerations for the ovnkube-node-dpu-host pods.</td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.adminFirewalldZone</td>
			<td>string</td>
			<td><pre lang="json">
"admin"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].key</td>
			<td>string</td>
			<td><pre lang="json">
"k8s.ovn.org/dpu-host"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].operator</td>
			<td>string</td>
			<td><pre lang="json">
"DoesNotExist"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.disableFirewalld</td>
			<td>bool</td>
			<td><pre lang="json">
true
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.mgmtPortVFResourceName</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.mgmtPortVFsCount</td>
			<td>int</td>
			<td><pre lang="json">
4
</pre>
</td>
			<td>set the count of VFs required to handle management ports for UDN</td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.monitoring</td>
			<td>object</td>
			<td><pre lang="json">
{
  "serviceMonitorSelectorLabels": {
    "k8s-app": "ovnkube-node",
    "release": "kube-prometheus-stack"
  }
}
</pre>
</td>
			<td>prometheus monitoring related fields</td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.monitoring.serviceMonitorSelectorLabels</td>
			<td>object</td>
			<td><pre lang="json">
{
  "k8s-app": "ovnkube-node",
  "release": "kube-prometheus-stack"
}
</pre>
</td>
			<td>specify the labels for serviceMonitors to be selected for target discovery. Prometheus operator defines what namespaces and what servicemonitors within these namespaces must be selected for target discovery. The fields defined below helps in defining that.</td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.nodeMgmtPortNetdev</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZone.logging.level</td>
			<td>string</td>
			<td><pre lang="json">
"5"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneNbdb.logging.level</td>
			<td>string</td>
			<td><pre lang="json">
"-vconsole:info -vfile:info"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneNorthd.logging.level</td>
			<td>string</td>
			<td><pre lang="json">
"-vconsole:info -vfile:info"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneOvnHost.logging.level</td>
			<td>string</td>
			<td><pre lang="json">
"-vconsole:info"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.ovnkubeSingleNodeZoneSbdb.logging.level</td>
			<td>string</td>
			<td><pre lang="json">
"-vconsole:info -vfile:info"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone.tolerations</td>
			<td>list</td>
			<td><pre lang="json">
[
  {
    "operator": "Exists"
  }
]
</pre>
</td>
			<td>tolerations for the ovnkube-node pods.</td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.dpuHostClusterK8sSkipTLSVerify</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.execInVrf</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td>run the ovnkube containers inside this VRF, e.g. mgmt. Empty runs them in the default VRF.</td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.nodeSelector</td>
			<td>object</td>
			<td><pre lang="json">
{}
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnDatapathType</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td>OVS datapath type used on the DPU, e.g. netdev</td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnGwInterface</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnGwVlanId</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnMetricsBindPort</td>
			<td>string</td>
			<td><pre lang="json">
"9410"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnMetricsEndpointIp</td>
			<td>string</td>
			<td><pre lang="json">
"127.0.0.1"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnMetricsWorkerPort</td>
			<td>string</td>
			<td><pre lang="json">
"9410"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnVtepIface</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZone.logging.level</td>
			<td>string</td>
			<td><pre lang="json">
"5"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneNbdb.logging.level</td>
			<td>string</td>
			<td><pre lang="json">
"-vconsole:info -vfile:info"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneNorthd.logging.level</td>
			<td>string</td>
			<td><pre lang="json">
"-vconsole:info -vfile:info"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneOvnHost.logging.level</td>
			<td>string</td>
			<td><pre lang="json">
"-vconsole:info"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.ovnkubeSingleNodeZoneSbdb.logging.level</td>
			<td>string</td>
			<td><pre lang="json">
"-vconsole:info -vfile:info"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.serviceDaemonSet.annotations</td>
			<td>object</td>
			<td><pre lang="json">
{}
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.serviceDaemonSet.labels</td>
			<td>object</td>
			<td><pre lang="json">
{}
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.serviceDaemonSet.nodeSelector.nodeSelectorTerms[0].matchExpressions[0].key</td>
			<td>string</td>
			<td><pre lang="json">
"kubernetes.io/arch"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.serviceDaemonSet.nodeSelector.nodeSelectorTerms[0].matchExpressions[0].operator</td>
			<td>string</td>
			<td><pre lang="json">
"In"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.serviceDaemonSet.nodeSelector.nodeSelectorTerms[0].matchExpressions[0].values[0]</td>
			<td>string</td>
			<td><pre lang="json">
"arm64"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.serviceDaemonSet.resources</td>
			<td>object</td>
			<td><pre lang="json">
{}
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.serviceDaemonSet.updateStrategy.rollingUpdate.maxUnavailable</td>
			<td>string</td>
			<td><pre lang="json">
"25%"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.serviceDaemonSet.updateStrategy.type</td>
			<td>string</td>
			<td><pre lang="json">
"RollingUpdate"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.spiffeDerivedK8sToken.enabled</td>
			<td>bool</td>
			<td><pre lang="json">
false
</pre>
</td>
			<td>read the K8s token from a file injected by DSX instead of the ServiceAccount token. Requires OVN images that support it and DSX injection running before ovnkube-init, with a startup probe that waits for the initial token to be written. Disabled preserves the existing K8S_TOKEN configuration.</td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.spiffeDerivedK8sToken.initContainerExtraVolumeMounts</td>
			<td>list</td>
			<td><pre lang="json">
[]
</pre>
</td>
			<td>additional mounts for ovnkube-init. When enabled, the chart declares the dsx-token volume and DSX admission mounts it into the regular containers, so ovnkube-init has to mount it explicitly. Referenced volumes must exist in the Pod.</td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.spiffeDerivedK8sToken.k8sTokenFile</td>
			<td>string</td>
			<td><pre lang="json">
""
</pre>
</td>
			<td>token file available to both ovnkube-init and ovnkube-controller</td>
		</tr>
		<tr>
			<td>ovnkube-single-node-zone-dpu.tolerations[0].operator</td>
			<td>string</td>
			<td><pre lang="json">
"Exists"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovs-node.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].key</td>
			<td>string</td>
			<td><pre lang="json">
"k8s.ovn.org/dpu-host"
</pre>
</td>
			<td></td>
		</tr>
		<tr>
			<td>ovs-node.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].operator</td>
			<td>string</td>
			<td><pre lang="json">
"DoesNotExist"
</pre>
</td>
			<td></td>
		</tr>
	</tbody>
</table>

