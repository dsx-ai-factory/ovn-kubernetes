/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1beta1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

// Duration represents amount of time
// Supported units: y, w, d, h, m, s, ms
// Examples: `30s`, `1m`, `1h20m15s`, `15d`
// +kubebuilder:validation:Pattern:="^(0|(([0-9]+)y)?(([0-9]+)w)?(([0-9]+)d)?(([0-9]+)h)?(([0-9]+)m)?(([0-9]+)s)?(([0-9]+)ms)?)$"
type Duration string

// ByteSize is a valid memory size type based on powers-of-2, so 1KB is 1024B.
// Supported units: B, KB, KiB, MB, MiB, GB, GiB, TB, TiB, PB, PiB, EB, EiB Ex: `512MB`.
// +kubebuilder:validation:Pattern:="(^0|([0-9]*[.])?[0-9]+((K|M|G|T|E|P)i?)?B)$"
type ByteSize string

// PacketSpec Captures various packet settings such as DSCP or payload size
type PacketSpec struct {
	// +kubebuilder:validation:Maximum:=63
	// +kubebuilder:validation:Minimum:=0
	// DSCP specifies that packets will be sent out with DSCP set to this value.
	DSCP int `json:"dscp,omitempty"`
	// PayloadSize specifies that packets will be sent out with this number of bytes as payload
	PayloadSize ByteSize `json:"payloadSize,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.lookupName) != has(self.ipAddress)", message="Exactly one of lookupName or ipAddress must be specified"
type DNSProbe struct {
	// LookupName specifies the host name to look up against a DNS name server.
	LookupName string `json:"lookupName,omitempty"`
	// IPAddress specifies the ip for reverse lookup to get the DNS name
	IPAddress string `json:"ipAddress,omitempty"`
	// NameServer sepcfies the host name or IP address of the DNS name server.
	// +kubebuilder:validation:Required
	NameServer string `json:"nameServer"`
	// Interval can be used to override the default interval with which the probes should be sent out.
	Interval   Duration   `json:"interval,omitempty"`
	PacketSpec PacketSpec `json:"packetSpec,omitempty"`
}

type HTTPProbe struct {
	// URL to perform HTTP request against
	URL       string     `json:"url"`
	TLSConfig *TLSConfig `json:"tlsConfig,omitempty"`
	// Interval can be used to override the default interval with which the probes should be sent out.
	Interval   Duration          `json:"interval,omitempty"`
	PacketSpec PacketSpec        `json:"packetSpec,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`

	// HTTP Method to use against the URL.
	// The only allowed value for now is GET and is also the default value.
	// +optional
	// +kubebuilder:default=GET
	Method string `json:"method,omitempty"`
}

type TCPProbe struct {
	// Host specifies the hostname or IP address of the server to connect to.
	// Host must be a valid hostname, IPv4 address, or IPv6 address
	Host string `json:"host"`
	// Port specifies the TCP port number to connect to.
	Port *int32 `json:"port"`
	// Interval can be used to override the default interval with which the probes should be sent out.
	Interval   Duration   `json:"interval,omitempty"`
	PacketSpec PacketSpec `json:"packetSpec,omitempty"`
}

type UDPStreamProbe struct {
	// Host is a host name or IP address of the server to send UDP packets to.
	// Host must be a valid hostname, IPv4 address, or IPv6 address
	Host string `json:"host"`
	// Port specifies the UDP port number to send the packets to.
	Port *int32 `json:"port"`
	// Interval can be used to override the default interval with which the probes should be sent out.
	Interval Duration `json:"interval,omitempty"`
	// PacketCount specifies number of packets that needs to be sent for this method of probing
	PacketCount *int32 `json:"packetCount"`
	// PacketInterval specifies the inter packet delay in ms that must be used between the packets
	PacketInterval Duration   `json:"packetInterval"`
	PacketSpec     PacketSpec `json:"packetSpec,omitempty"`
}

// NetworkProbeSpec defines the desired state of NetworkProbe.
// NetworkProbeSpec should have one of  DNSProbes, HTTPProbes, TCPProbes, UDPStreamProbes defined in spec, otherwise spec will not be created.
// +kubebuilder:validation:XValidation:rule="self.dnsProbes.size() > 0 || self.httpProbes.size() > 0 || self.tcpProbes.size() > 0 || self.udpStreamProbes.size() > 0",message="At least one probe type must be specified"
type NetworkProbeSpec struct {
	// NodeSelector selects the node(s) whose label matches this definition.
	// The probes defined in the spec are run from the selected nodes.
	// +kubebuilder:validation:Required
	// +required
	NodeSelector metav1.LabelSelector `json:"nodeSelector"`

	// Interval at which probes should be sent.
	// if not specified, default value is 60s
	// +kubebuilder:default="60s"
	Interval Duration `json:"interval,omitempty"`

	// Suspend indicates whether the probe should be suspended
	// since nodes might be undergoing maintenance
	// +optional
	// +kubebuilder:default=false
	Suspend bool `json:"suspend,omitempty"`

	// DNSProbes contains collection of DNS probe objects to perform DNS lookup
	DNSProbes []DNSProbe `json:"dnsProbes,omitempty"`
	// HTTPProbes contains collection of http probe objects to perform HTTP GET
	HTTPProbes []HTTPProbe `json:"httpProbes,omitempty"`
	// TCPProbes contains collection of tcp probe objects to perform latency measurement of TCP Connection operation
	TCPProbes []TCPProbe `json:"tcpProbes,omitempty"`
	// UDPStreamProbes contains collection of udp probe objects to perform latency, jitter, and packet loss measurement of UDP stream operation
	UDPStreamProbes []UDPStreamProbe `json:"udpStreamProbes,omitempty"`
}

// NetworkProbeStatus defines the observed state of NetworkProbe
type NetworkProbeStatus struct {
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// contains details for one aspect of the current state of this API Resource.
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	// A concise indication of whether the NetworkProbe resource is applied or not
	// +optional
	Status ovntypes.OvnK8sStatus `json:"status,omitempty"`
}

// NetworkProbe is a namespace-scoped API to provide a way for the users to define network probes
// that generates different type of network traffic every set interval and captures performance metrics
// such as latency, jitter, and packet loss for every execution of the Probe. The API allows generating
// following types of traffic -- DNS, HTTP, TCP, and UDPStream on a selected set of K8s nodes.
//
// +genclient
// +resource:path=networkprobe
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=netprobe
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type NetworkProbe struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	// +required
	Spec   NetworkProbeSpec   `json:"spec"`
	Status NetworkProbeStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=networkprobe
// +kubebuilder:object:root=true

// NetworkProbeList contains a list of NetworkProbe
type NetworkProbeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetworkProbe `json:"items"`
}

// TLS configuration to use when scraping the endpoint.
// Either skip verification or provide certificate information to validate the server
// +k8s:openapi-gen=true
type TLSConfig struct {
	// Struct containing the CA cert to use for the targets.
	CACert SecretOrConfigMap `json:"caCert,omitempty"`

	// InsecureSkipVerify is for disabling target certificate validation.
	//+optional
	InsecureSkipVerify *bool `json:"insecureSkipVerify,omitempty"`
}

// SecretOrConfigMap allows to specify ca cert authority data as a Secret or ConfigMap. Fields are mutually exclusive.
// +kubebuilder:validation:XValidation:rule="!(has(self.secret) && has(self.configMap))",message="Both Secret and ConfigMap cannot be specified at the same time."
type SecretOrConfigMap struct {
	// Secret containing data to use for the targets.
	// Secret should be created in the namespace where the NetworkProbe controller Pod is running.
	Secret *v1.SecretKeySelector `json:"secret,omitempty"`
	// ConfigMap containing data to use for the targets.
	// ConfigMap should be created in the namespace where the NetworkProbe controller Pod is running.
	ConfigMap *v1.ConfigMapKeySelector `json:"configMap,omitempty"`
}
