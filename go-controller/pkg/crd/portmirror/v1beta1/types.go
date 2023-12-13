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

const (
	PortMirrorDirectionIn   string = "In"
	PortMirrorDirectionOut  string = "Out"
	PortMirrorDirectionBoth string = "Both"
	PortMirrorSinkDeviceSF  string = "ScalableFunction"
)

// +kubebuilder:validation:Pattern=^[0-9a-zA-Z\-]+\/[0-9a-zA-Z\-]+$
type NetworkAttachmentNameString string

type PortMirrorSource struct {
	PodSelector metav1.LabelSelector `json:"podSelector"`

	// Selects the specific network-attachment-definition for mirroring.
	// +optional
	NetworkAttachmentName []NetworkAttachmentNameString `json:"networkAttachmentNames"`
}

type MirrorSinkDevice struct {
	// Device type; ScalableFunction, VirtualFunction etc. Only ScalableFunction supported
	// +kubebuilder:validation:Pattern=^ScalableFunction
	DeviceType string `json:"deviceType"`

	// ScalableFunction device number
	// +kubebuilder:validation:Minimum=-1
	// +kubebuilder:validation:Maximum=5
	SFNum int `json:"sfNum"`
}

type PortMirrorSinkLocal struct {
	// An empty podSelector matches all pods in this namespace.
	PodSelector metav1.LabelSelector `json:"podSelector"`

	// the netdev name to be used in the sink pod.
	// +optional
	NetDevName string `json:"netDevName"`

	// Specific mirror-id to select target. This will be
	// used when defining multiple PortMirror spec that
	// uses the same target.
	// +kubebuilder:validation:Pattern=^[0-9a-zA-Z\-]+$
	// +optional
	MirrorID string `json:"mirrorID"`

	// Sink Interface info
	// +kubebuilder:default:={deviceType:"ScalableFunction", sfNum:-1}
	// +optional
	DeviceInfo MirrorSinkDevice `json:"deviceInfo"`
}

// PortMirrorSpec defines the desired state of PortMirror
type PortMirrorSpec struct {
	// Selects the pods, and their networks, to mirror from.
	Sources []PortMirrorSource `json:"sources"`

	// Selects the direction for the selected source for mirroring. This could
	// be In, Out or Both, i.e. mirror packets coming "In" for the selected source,
	// going "Out" or "Both" directions. If unspecified, the default is "Both"
	// directions
	// +kubebuilder:validation:Pattern=^In|Out|Both
	// +kubebuilder:default:=Both
	// +optional
	MirrorDirection string `json:"mirrorDirection"`

	// Selects the pods to mirror packets to.
	SinkLocal PortMirrorSinkLocal `json:"sinkLocal"`
}

// PortMirrorStatus describes the current status of the PortMirror.
type PortMirrorStatus struct {
	// An human-readable messages indicating details about the status of the object.
	// +optional
	Messages []string `json:"messages,omitempty"`

	// A concise indication of whether the PortMirror resource is applied or not
	// +optional
	Status ovntypes.OvnK8sStatus `json:"status,omitempty"`
}

// PortMirror API ...
// +genclient
// +resource:path=portmirror
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="MirrorID",type=string,JSONPath=".spec.sinkLocal.mirrorID"
// +kubebuilder:printcolumn:name="SinkDeviceType",type=string,JSONPath=".spec.sinkLocal.deviceInfo.deviceType"
type PortMirror struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PortMirrorSpec   `json:"spec,omitempty"`
	Status PortMirrorStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=portmirror
//+kubebuilder:object:root=true

// PortMirrorList contains a list of PortMirror
type PortMirrorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PortMirror `json:"items"`
}
