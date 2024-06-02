/*
Copyright 2023.

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

type PacketSpec struct {
	DSCP        string   `json:"dscp,omitempty"`
	PayloadSize ByteSize `json:"payloadSize,omitempty"`
}

type DNSProbe struct {
	LookupName string     `json:"lookupName"`
	NameServer string     `json:"nameServer"`
	Interval   Duration   `json:"interval,omitempty"`
	PacketSpec PacketSpec `json:"packetSpec,omitempty"`
}

type HTTPProbe struct {
	URL        string     `json:"url"`
	TLSConfig  *TLSConfig `json:"tlsConfig,omitempty"`
	Interval   Duration   `json:"interval,omitempty"`
	PacketSpec PacketSpec `json:"packetSpec,omitempty"`
}

type TCPProbe struct {
	Host       string     `json:"host"`
	Port       *int32     `json:"port"`
	Interval   Duration   `json:"interval,omitempty"`
	PacketSpec PacketSpec `json:"packetSpec,omitempty"`
}

type UDPStreamProbe struct {
	Host        string   `json:"host"`
	Port        *int32   `json:"port"`
	Interval    Duration `json:"interval"`
	PacketCount *int32   `json:"packetCount"`
	// TODO: This has to be in MilliSeconds only
	PacketInterval Duration   `json:"packetInterval"`
	PacketSpec     PacketSpec `json:"packetSpec,omitempty"`
}

// NetworkProbeSpec defines the desired state of NetworkProbe
type NetworkProbeSpec struct {
	// NodeSelector selects the node(s) whose label matches this definition.
	// The probes defined in the spec are run from the selected nodes.
	// This field is optional. If not specified, then the probes defined in
	// the spec are run from all the nodes in the K8s cluster.
	NodeSelector metav1.LabelSelector `json:"nodeSelector,omitempty"`

	// if not specified, we will default to 60s
	Interval Duration `json:"interval,omitempty"`

	// Todo: Atleast one of these MUST be specified
	DNSProbes       []DNSProbe       `json:"dnsProbes"`
	HTTPProbes      []HTTPProbe      `json:"httpProbes"`
	TCPProbes       []TCPProbe       `json:"tcpProbes"`
	UDPStreamProbes []UDPStreamProbe `json:"udpStreamProbes"`
}

// NetworkProbeStatus defines the observed state of NetworkProbe
type NetworkProbeStatus struct {
	// An array of Human-readable messages indicating details about the status of the object.
	// +optional
	Messages []string `json:"messages,omitempty"`

	// A concise indication of whether the NetworkProbe resource is applied or not
	// +optional
	Status ovntypes.OvnK8sStatus `json:"status,omitempty"`
}

// NetworkProbe cluster-scoped API provides a way for the users to define network probes
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

	Spec   NetworkProbeSpec   `json:"spec,omitempty"`
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

// TLSConfig specifies safe TLS configuration parameters.
// +k8s:openapi-gen=true
type TLSConfig struct {
	// ConfigMap containing data to use for the targets.
	CAConfigMap *v1.ConfigMapKeySelector `json:"caConfigMap,omitempty"`

	// Disable target certificate validation.
	//+optional
	InsecureSkipVerify *bool `json:"insecureSkipVerify,omitempty"`
}
