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
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

// AdminPolicyBasedRouteStatus describes the current status of the AdminPolicyBasedRoute.
type AdminPolicyBasedRouteStatus struct {
	// An array of Human-readable messages indicating details about the status of the object.
	// +optional
	Messages []string `json:"messages,omitempty"`

	// A concise indication of whether the AdminPolicyBasedRoute resource is applied or not
	// +optional
	Status ovntypes.OvnK8sStatus `json:"status,omitempty"`
}

// AdminPolicyBasedRoute cluster-scoped API provides a way to influence routing decisions
// in the SDN network. The API can be used to match the packets originating from and/or
// -- kubernetes nodes, namespaces, pods -- and forward them to different destination for
// further processing.
//
// This API leverages OVN's Logical Router Policy feature. The AdminPolicyBasedRoute policies
// or translated to OVN policies and these policies override OVN's static routing decision.
// The priority of the policy rules is set to 100 (priority in range of 0 to 32,767, with
// numerically higher priority taking precedence over those with lower), and it processed
// after all the OVN K8s' pre-defined rules.
//
// +genclient
// +genclient:nonNamespaced
// +resource:path=adminpolicybasedroute
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=adminpbr,scope=Cluster
// +kubebuilder:printcolumn:name="NetworkName",type=string,JSONPath=".spec.networkAttachmentName"
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type AdminPolicyBasedRoute struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AdminPolicyBasedRouteSpec   `json:"spec,omitempty"`
	Status AdminPolicyBasedRouteStatus `json:"status,omitempty"`
}

// AdminPolicyBasedRouteSpec defines the desired state of cluster scoped routing policies
type AdminPolicyBasedRouteSpec struct {
	// Selects the network-attachment-definition for which the policy routes need to be
	// applied. The NAD should be of type Layer-3. Specifying anything else (Layer-2 or
	// Localnet) will be an invalid configuration.
	// +kubebuilder:validation:Pattern=^[0-9a-zA-Z\-]+\/[0-9a-zA-Z\-]+$
	NetworkAttachmentName string `json:"networkAttachmentName"`

	// a collection of policy objects to influence routing
	Policies []RoutingPolicyRule `json:"policies"`
}

// RoutingPolicyRule is a single routing policy rule object
type RoutingPolicyRule struct {
	// From matches the packets for the routing policy
	From RoutingPolicyMatch `json:"from"`
	// To specifies destination addresses used to match the same in the packets
	To networkingv1.IPBlock `json:"to"`
	// NextHop defines where the matched packets should be forwarded
	NextHop RoutingPolicyNextHop `json:"nexthop"`
}

// RoutingPolicyMatch provides a way to select nodes, namespaces, and pods such that
// the packets originating from them will be subjected to routing policies.
// +kubebuilder:validation:MinProperties=1
type RoutingPolicyMatch struct {
	// NodeSelector matches the source packets only from the node(s) whose label
	// matches this definition. This field is optional.
	NodeSelector metav1.LabelSelector `json:"nodeSelector,omitempty"`
	// NamespaceSelector matches the source packets only from the namespace(s) whose label
	// matches this definition. This field is optional.
	NamespaceSelector metav1.LabelSelector `json:"namespaceSelector,omitempty"`
	// PodSelector matches the source packets only from the pods whose label
	// matches this definition. This field is optional, and in case it is not set:
	// results in matching packets from all the pods in the namespace(s)
	// matched by the NamespaceSelector. In case it is set: is intersected with
	// the NamespaceSelector, thus matching the packets from the pods
	// (in the namespace(s) already matched by the NamespaceSelector) which
	// match this pod selector.
	// +optional
	PodSelector metav1.LabelSelector `json:"podSelector,omitempty"`
}

// RoutingPolicyNextHop specifies the next-hop IP address for the policy route.
type RoutingPolicyNextHop struct {
	//  NextHopIPs is the list of next-hop IP addresses for this route. With more than
	// one IP address, ECMP will kick in and one of the IP address will be selected
	// based on the 5-tuple hashing of the packet header. Currently, only one IP address
	// is supported. Furthermore, this IP address must be an overlay address, i.e., an
	// address within the OVN Logical Topology.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1
	// +kubebuilder:validation:Format=ipv4
	NextHopIPs []string `json:"ips"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=adminpolicybasedroute
//+kubebuilder:object:root=true

// AdminPolicyBasedRouteList contains a list of AdminPolicyBasedRoute
type AdminPolicyBasedRouteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AdminPolicyBasedRoute `json:"items"`
}
