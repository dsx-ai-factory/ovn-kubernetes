/*
Copyright 2022.

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

// VirtualIPSpec defines the desired state of VirtualIP
type VirtualIPSpec struct {
	// VirtualIP specifies the address behind which an highly available
	// service instance is going to run. The HA implementation itself is
	// provided by mechanisms such as keepalived, for example.
	// +kubebuilder:validation:Format=ipv4
	VirtualIP string `json:"virtualIP"`

	// Selects the pods that form the backend for the virtual IP address. That is,
	// the virtual IP would move between these Pods during the failover. This field
	// is NOT optional and follows standard label selector semantics. An empty
	// podSelector matches all pods in this namespace.
	PodSelector metav1.LabelSelector `json:"podSelector"`

	// Selects the network-attachment-definition on which the virtualIP is going
	// to reside. Currently, the support vor Virtual IP is for Layer-2 NADs.
	// +kubebuilder:validation:Pattern=^[0-9a-zA-Z\-]+\/[0-9a-zA-Z\-]+$
	NetworkAttachmentName string `json:"networkAttachmentName"`
}

// VirtualIPStatus describes the current status of the VirtualIP.
type VirtualIPStatus struct {
	// Reference to the Pod that currently owns this virtual IP
	ActivePod corev1.ObjectReference `json:"activePod,omitempty"`

	// Information when was the last time virtualIP moved between the Pods.
	// +optional
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`

	// A list of pointers to all the Pods backing this virtual IP
	// +optional
	BackingPods []corev1.ObjectReference `json:"backingPods,omitempty"`

	// An array of human-readable messages indicating details about the status of the object.
	// +optional
	Messages []string `json:"messages,omitempty"`

	// A concise indication of whether the virtualIP resource is applied or not
	// +optional
	Status ovntypes.OvnK8sStatus `json:"status,omitempty"`
}

// VirtualIP API provides necessary plumbing in the overlay network so that
// the consumers of the API can implement highly available service instances
// using, for example, keepalived.
//
// This API leverages OVN’s Virtual Port feature. This port represents a
// virtual ip that is backed by one or more Pods configured in active-standby
// setup. The virtual ip resides on one of the Pods. When the active pod dies, the
// virtual IP moves to one of the standby Pods and the OVN SDN control
// plane discovers this move and ensures that all the packets to virtual
// IP are forwarded to the now active Pod.
//
// +genclient
// +resource:path=virtualip
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="VirtualIP",type=string,JSONPath=".spec.virtualIP"
// +kubebuilder:printcolumn:name="NetworkName",type=string,JSONPath=".spec.networkAttachmentName"
// +kubebuilder:printcolumn:name="ActivePod",type=string,JSONPath=".status.activePod.name"
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.status"
// +kubebuilder:printcolumn:name="Last-Transition",type=date,JSONPath=".status.lastTransitionTime"
type VirtualIP struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VirtualIPSpec   `json:"spec,omitempty"`
	Status VirtualIPStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=virtualip
//+kubebuilder:object:root=true

// VirtualIPList contains a list of VirtualIP
type VirtualIPList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualIP `json:"items"`
}
