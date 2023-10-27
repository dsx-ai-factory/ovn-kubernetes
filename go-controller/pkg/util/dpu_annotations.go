package util

import (
	"encoding/json"
	"fmt"
)

/*
This Handles DPU related annotations in ovn-kubernetes.

The following annotations are handled:

Annotation: "k8s.ovn.org/dpu.connection-details"
Applied on: Pods
Used for: convey the required information to setup network plubming on DPU for a given Pod
Example:
    annotations:
        k8s.ovn.org/dpu.connection-details: |
            {
                "default": {
                    "pfId":         “0”,
                    “vfId”:         "3",
		    "pfMac":        "b7:cf:f6:71:cc:56",
		    "vfNetdevName": "eth2",
                    "sandboxId":    "35b82dbe2c39768d9874861aee38cf569766d4855b525ae02bff2bfbda73392a"
	        }
            }

Annotation: "k8s.ovn.org/dpu.connection-status"
Applied on: Pods
Used for: convey the DPU connection status for a given Pod
Example:
    annotations:
        k8s.ovn.org/dpu.connection-status: |
            {
                "default": {
                    "status": “Ready”,
                    "reason": ""
		}
            }
*/

const (
	DPUConnectionDetailsAnnot = "k8s.ovn.org/dpu.connection-details"
	DPUConnetionStatusAnnot   = "k8s.ovn.org/dpu.connection-status"

	DPUConnectionStatusReady = "Ready"
	DPUConnectionStatusError = "Error"
	DPUMirrorStatusMirrored  = "Mirrored"
	DPUMirrorStatusFailed    = "Failed"
	// maybe use uint so we can check status > some state
	DPUConnectionStatusClampedDown = "ClampedDown"
)

// Private info per connection
// Currently used to keep information about the interface w.r.t
// miss rl config.. specifically, whether DoS check is enabled,
// the initial drop count when DoS check was enabled,
// and the device name (rep) name.
type DPUConnPrivateInfo struct {
	MissRateLimitDropInitial uint64
	MissRateDoSCheck         bool
	ConnVFRepName            string
	ConnClampedDown          bool
}

type DPUConnectionDetails struct {
	PfId         string `json:"pfId"`
	VfId         string `json:"vfId"`
	PfMAC        string `json:"pfMac,omitempty"`
	SandboxId    string `json:"sandboxId"`
	VfNetdevName string `json:"vfNetdevName,omitempty"`
	// Private connection info.
	ConnPrivateInfo DPUConnPrivateInfo `json:"-"`
}

type DPUConnectionStatus struct {
	Status string `json:"Status"`
	Reason string `json:"Reason,omitempty"`
}

type DPUMirrorStatus struct {
	Status  string `json:"Status"`
	SinkPod string `json:"SinkPod,omitempty"`
	Reason  string `json:"Reason,omitempty"`
}

// MarshalPodDPUConnDetails returns a JSON-formatted annotation describing the pod's DPU connection details
func MarshalPodDPUConnDetails(pannotations *map[string]string, dcd *DPUConnectionDetails, annoNadKeyName string) error {
	annotations := *pannotations
	if annotations == nil {
		annotations = make(map[string]string)
		*pannotations = annotations
	}
	podDcds := make(map[string]DPUConnectionDetails)
	ovnAnnotation, ok := annotations[DPUConnectionDetailsAnnot]
	if ok {
		if err := json.Unmarshal([]byte(ovnAnnotation), &podDcds); err != nil {
			return fmt.Errorf("failed to unmarshal ovn pod annotation %q: %v",
				ovnAnnotation, err)
		}
	}
	dc, ok := podDcds[annoNadKeyName]
	if dcd != nil {
		// A bit inefficent way to compare, just to keep the changes
		// simple and local
		if ok && dc.PfId == dcd.PfId && dc.VfId == dcd.VfId &&
			dc.PfMAC == dcd.PfMAC && dc.SandboxId == dcd.SandboxId &&
			dc.VfNetdevName == dcd.VfNetdevName {
			return newAnnotationAlreadySetError("OVN pod %s annotation for nad %s already exists in %v",
				DPUConnectionDetailsAnnot, annoNadKeyName, ovnAnnotation)
		}
		podDcds[annoNadKeyName] = *dcd
	} else {
		if !ok {
			return newAnnotationAlreadySetError("OVN pod %s annotation for nad %s already removed",
				DPUConnectionDetailsAnnot, annoNadKeyName)
		}
		delete(podDcds, annoNadKeyName)
	}
	bytes, err := json.Marshal(podDcds)
	if err != nil {
		return fmt.Errorf("failed marshaling pod annotation map %v: %v", podDcds, err)
	}
	annotations[DPUConnectionDetailsAnnot] = string(bytes)
	return nil
}

// UnmarshalPodDPUConnDetails returns dpu connection details for the specified network
func UnmarshalPodDPUConnDetails(annotations map[string]string, annoNadKeyName string) (*DPUConnectionDetails, error) {
	ovnAnnotation, ok := annotations[DPUConnectionDetailsAnnot]
	if !ok {
		return nil, newAnnotationNotSetError("could not find OVN pod annotation in %v", annotations)
	}

	podDcds := make(map[string]DPUConnectionDetails)
	if err := json.Unmarshal([]byte(ovnAnnotation), &podDcds); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ovn pod annotation %q: %v",
			ovnAnnotation, err)
	}
	dcd, ok := podDcds[annoNadKeyName]
	if !ok {
		return nil, newAnnotationNotSetError("no DPU connection details annotation for nad %s: %q",
			annoNadKeyName, ovnAnnotation)
	}
	return &dcd, nil
}

// MarshalPodDPUConnStatus returns a JSON-formatted annotation describing the pod's DPU connection status
func MarshalPodDPUConnStatus(pannotations *map[string]string, dcs *DPUConnectionStatus, annoNadKeyName string) error {
	annotations := *pannotations
	if annotations == nil {
		annotations = make(map[string]string)
		*pannotations = annotations
	}
	podDcds := make(map[string]DPUConnectionStatus)
	ovnAnnotation, ok := annotations[DPUConnetionStatusAnnot]
	if ok {
		if err := json.Unmarshal([]byte(ovnAnnotation), &podDcds); err != nil {
			return fmt.Errorf("failed to unmarshal ovn pod %s annotation %q: %v",
				DPUConnetionStatusAnnot, ovnAnnotation, err)
		}
	}
	dc, ok := podDcds[annoNadKeyName]
	if dcs != nil {
		if ok && dc == *dcs {
			return newAnnotationAlreadySetError("OVN pod %s annotation for nad %s already exists in %v",
				DPUConnetionStatusAnnot, annoNadKeyName, ovnAnnotation)
		}
		podDcds[annoNadKeyName] = *dcs
	} else {
		if !ok {
			return newAnnotationAlreadySetError("OVN pod %s annotation for nad %s already removed",
				DPUConnetionStatusAnnot, annoNadKeyName)
		}
		delete(podDcds, annoNadKeyName)
	}
	bytes, err := json.Marshal(podDcds)
	if err != nil {
		return fmt.Errorf("failed marshaling pod annotation map %v: %v", podDcds, err)
	}
	annotations[DPUConnetionStatusAnnot] = string(bytes)
	return nil
}

// UnmarshalPodDPUConnStatus returns DPU connection status for the specified network
func UnmarshalPodDPUConnStatus(annotations map[string]string, annoNadKeyName string) (*DPUConnectionStatus, error) {
	ovnAnnotation, ok := annotations[DPUConnetionStatusAnnot]
	if !ok {
		return nil, newAnnotationNotSetError("could not find OVN pod annotation in %v", annotations)
	}

	podDcss := make(map[string]DPUConnectionStatus)
	if err := json.Unmarshal([]byte(ovnAnnotation), &podDcss); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ovn pod annotation %q: %v",
			ovnAnnotation, err)
	}
	dcs, ok := podDcss[annoNadKeyName]
	if !ok {
		return nil, newAnnotationNotSetError("no dpu connection status annotation for nad %s: %q",
			annoNadKeyName, ovnAnnotation)
	}
	return &dcs, nil
}
