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
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IPFamily represents the IP Family (IPv4 or IPv6). This type is used
// to express the family of an IP that need to be reserved.
type IPFamily string

const (
	// IPv4Protocol indicates IPv4 addresses must be reserved
	IPv4Protocol IPFamily = "IPv4"
	// IPv6Protocol indicates IPv6 addresses must be reserved
	IPv6Protocol IPFamily = "IPv6"
)

// IPReservationSpec defines the desired state of IPReservation
type IPReservationSpec struct {
	// Selects the network-attachment-definition from whose subnets the IPs will be
	// reserved. The NAD should be of type Layer-2 or Localnet. Specifying anything else
	// will be an invalid configuration.
	// +kubebuilder:validation:Pattern=^[0-9a-zA-Z\-]+\/[0-9a-zA-Z\-]+$
	NetworkAttachmentName string `json:"networkAttachmentName"`

	// IPFamily represents the IP Family (IPv4 or IPv6). This type is used
	// to express the family of an IP that need to be reserved.
	// +kubebuilder:validation:Enum=IPv4;IPv6
	IPFamily IPFamily `json:"ipfamily"`

	// Specifies the total number of IPs to be reserved from the subnet that the networkAttachmentName
	// represents. If the subnet has `count` number of IPs available, then the reservation will be
	// successful. The controller will allocate those many IPs and populate the reservedIPs field in the
	// status section with those IPs.
	//
	// The OVN K8s CNI will not use the reserved IPs for allocating to Pods connecting to the above NAD.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16
	Count int32 `json:"count"`
}

// IPReservationStatus defines the observed state of IPReservation
type IPReservationStatus struct {
	// An array of Human-readable messages indicating details about the status of the object.
	// +optional
	Messages []string `json:"messages,omitempty"`

	// A concise indication of whether the IPReservation resource is applied or not
	// +optional
	Status ovntypes.OvnK8sStatus `json:"status,omitempty"`

	// List of IPs that were reserved
	// +optional
	ReservedIPs []string `json:"reservedIPs,omitempty"`
}

// IPReservation namespace-scoped resource provides a way to reserve certain number of IPs in Layer-2
// and Localnet type NADs. The OVN K8s CNI will reserve specified number of IPs from the NAD's subnet
// and return the list of IPs that were reserved in the status section of the resource.
//
// The reservation is all or none. If the specified number of IPs are not available to reserve, then this
// resource status would be failed.
//
// +genclient
// +resource:path=ipreservation
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ipreserve
// +kubebuilder:printcolumn:name="NetworkName",type=string,JSONPath=".spec.networkAttachmentName"
// +kubebuilder:printcolumn:name="IPFamily",type=string,JSONPath=".spec.ipfamily"
// +kubebuilder:printcolumn:name="Count",type=integer,JSONPath=".spec.count"
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.status"
type IPReservation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IPReservationSpec   `json:"spec,omitempty"`
	Status IPReservationStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=ipreservation
// +kubebuilder:object:root=true

// IPReservationList contains a list of IPReservation
type IPReservationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IPReservation `json:"items"`
}
