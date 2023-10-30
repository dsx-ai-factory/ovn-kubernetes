package util

import (
	"fmt"
	"strings"
	"sync"

	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"

	portmirror "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/portmirror/v1beta1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
)

const (
	MessagePrefixPortMirrorErr = "PortMirrorErrorMsg"
)

type PortMirror struct {
	Name             string
	Namespace        string
	MirrorDirection  string
	SourceDetails    sourceInfo
	SinkLocalDetails sinkInfo

	// uuid value when corresponding mirror is created in nbdb
	MirrorUUID string

	// for retrying failed portMirror ops
	FailedPortMirrorOps map[string]bool
	// portmirror UID
	UID types.UID
}

type sourceNetInfo struct {
	PodSelector labels.Selector
	// podNetAttachDefMirrorInfo consists of net-attach-def that need to be mirrored
	// as key. if nothing is specified, only primary interface will be mirrored.
	// When we support specifying per networkattachdef sink device name we can
	// hold the sinkname in this map
	PodNetAttachDefMirrorInfo map[string]bool
	PodHandler                interface{}
}

type sourceInfo struct {
	SourceNetInfo []*sourceNetInfo
	// map for pods which didn't have
	// networking set when portmirror controller starts with
	// key as podNamespace/podName and value as boolean
	PodRetry sync.Map
	// SourcePodInfo is a map of source pods selected for this port mirror CRD
	// with key as podNamespace/podName and value as map[string]string which
	// consists of portname as key and value as nad to be mirrored.
	SourcePodInfo sync.Map
}

// Only local information for now; later when we add remote, we'll need to revisit
type sinkInfo struct {
	PodSelector labels.Selector
	// if specified, use it from the spec, else use the generated value.
	PortMirrorId   string
	SinkDeviceType string
	SinkSFNum      int
	SinkIfName     string
	PodHandler     interface{}
}

func NewPortMirror(pm *portmirror.PortMirror) (*PortMirror, error) {
	portMir := &PortMirror{
		Name:            pm.Name,
		Namespace:       pm.Namespace,
		MirrorDirection: pm.Spec.MirrorDirection,
		UID:             pm.UID,
	}
	// Get the Source Info
	portMir.SourceDetails.SourcePodInfo = sync.Map{}
	portMir.SourceDetails.SourceNetInfo = make([]*sourceNetInfo, 0)
	// traverse through portmirror sources and get the pod selector for sources
	// and corresponding network-attach-def to be mirrored
	for _, portMirrorSource := range pm.Spec.Sources {
		sNetInfo := &sourceNetInfo{
			PodNetAttachDefMirrorInfo: make(map[string]bool),
		}
		for _, netAttachDef := range portMirrorSource.NetworkAttachmentName {
			sNetInfo.PodNetAttachDefMirrorInfo[string(netAttachDef)] = true
		}
		if !IsEmptySelector(&portMirrorSource.PodSelector) {
			sourcePodSel, err := metav1.LabelSelectorAsSelector(&portMirrorSource.PodSelector)
			if err != nil {
				return nil, fmt.Errorf("failed to parse source pod selector for portmirror %s/%s", pm.Namespace, pm.Name)
			}
			sNetInfo.PodSelector = sourcePodSel
		}
		portMir.SourceDetails.SourceNetInfo = append(portMir.SourceDetails.SourceNetInfo, sNetInfo)
	}
	// Get the Sink Info
	portMir.SinkLocalDetails.SinkIfName = pm.Spec.SinkLocal.NetDevName
	portMir.SinkLocalDetails.PortMirrorId = GetPortMirrorID(pm)
	portMir.SinkLocalDetails.SinkDeviceType = pm.Spec.SinkLocal.DeviceInfo.DeviceType
	portMir.SinkLocalDetails.SinkSFNum = pm.Spec.SinkLocal.DeviceInfo.SFNum
	if !IsEmptySelector(&pm.Spec.SinkLocal.PodSelector) {
		sinkPodSel, err := metav1.LabelSelectorAsSelector(&pm.Spec.SinkLocal.PodSelector)
		if err != nil {
			return nil, fmt.Errorf("failed to parse sink pod selector for portmirror %s/%s", pm.Namespace, pm.Name)
		}
		portMir.SinkLocalDetails.PodSelector = sinkPodSel
	}
	portMir.FailedPortMirrorOps = make(map[string]bool)
	return portMir, nil
}

func GetPortMirrorOVNName(namespace, name string) string {
	return fmt.Sprintf("%s_%s", namespace, name)
}

func GetPortMirrorSourcePodKeyForFailedOps(pod *kapi.Pod) string {
	return fmt.Sprintf("source pod %s/%s", pod.Namespace, pod.Name)
}

func GetPortMirrorSinkPodKeyForFailedOps(pod *kapi.Pod) string {
	return fmt.Sprintf("sink pod %s/%s", pod.Namespace, pod.Name)
}

func GetPortMirrorKeyForFailedOps(namespace, name string) string {
	return fmt.Sprintf("portmirror %s/%s", namespace, name)
}

func NeedsRetry(pod *kapi.Pod, pm *PortMirror) bool {
	_, boolVal := pm.SourceDetails.PodRetry.Load(GetNamespacedName(pod.Namespace, pod.Name))
	return boolVal
}

func GetNamespacedName(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

// if mirrorID is specified in the spec, use the mentioned value.
// else, generate the mirrorID using portMirror name and namespace in (namespace_name) format
func GetPortMirrorID(pm *portmirror.PortMirror) string {
	var portMirrorID string
	if pm.Spec.SinkLocal.MirrorID != "" {
		portMirrorID = pm.Spec.SinkLocal.MirrorID
	} else {
		portMirrorID = fmt.Sprintf("%s_%s", pm.Namespace, pm.Name)
	}
	return portMirrorID
}

func GetLockByPMName(namespace, name string) func() {
	portMirrorName := fmt.Sprintf("_portmirror/%s/%s", namespace, name)
	return LockByKey.Acquire(portMirrorName)
}

func GetLockByPMId(mirrorID string) func() {
	portMirrorIDKey := fmt.Sprintf("_portmirror/%s", mirrorID)
	return LockByKey.Acquire(portMirrorIDKey)
}

func GetLockByPMLogicalPortName(portName string) func() {
	logicalPortNameKey := fmt.Sprintf("_logicalport/%s", portName)
	return LockByKey.Acquire(logicalPortNameKey)
}

// DoesPortMirrorSinkDetailsMatch checks if the new portmirror sinkLocalDetails
// matches with existing portmirror sinkLocalDetails with same mirrorID
func DoesPortMirrorSinkDetailsMatch(pMirror *PortMirror, pmList []*PortMirror) (bool, error) {
	if pMirror.SinkLocalDetails.SinkIfName == "" &&
		pMirror.SinkLocalDetails.SinkSFNum == -1 && pMirror.SinkLocalDetails.SinkDeviceType == "" {
		return true, nil
	}

	for _, pm := range pmList {
		if pMirror.SinkLocalDetails.SinkIfName != "" &&
			pMirror.SinkLocalDetails.SinkIfName != pm.SinkLocalDetails.SinkIfName {
			return false, fmt.Errorf("found a portmirror %s/%s using different sinkNetName with same mirrorID",
				pm.Namespace, pm.Name)
		}

		if pMirror.SinkLocalDetails.SinkSFNum != -1 &&
			pMirror.SinkLocalDetails.SinkSFNum != pm.SinkLocalDetails.SinkSFNum {
			return false, fmt.Errorf("found a portmirror %s/%s using different sfNum using with same mirrorID",
				pm.Namespace, pm.Name)
		}
	}
	return true, nil
}

// updatePortMirrorStatusOnError updates the portmirror status with errorMessage
func UpdatePortMirrorStatusOnError(pm *PortMirror, kube kube.Interface,
	errMsg, failedOpsKey string) {
	pm.FailedPortMirrorOps[failedOpsKey] = true
	err := UpdatePortMirrorStatusWithRetry(kube, pm.Namespace, pm.Name, ovntypes.OvnK8sStatusFailed, "", errMsg)
	if err != nil {
		klog.Errorf(err.Error())
	}
}

// clearPortMirrorErrorMessage removes the corresponding podAdd failure or
// portMirror creation failure from errorMessages list and updates the portmirror
// status.
func ClearPortMirrorErrorMessage(pm *PortMirror, kube kube.Interface, failedOpsKey string) {
	delete(pm.FailedPortMirrorOps, failedOpsKey)
	err := UpdatePortMirrorStatusWithRetry(kube, pm.Namespace, pm.Name, "", failedOpsKey, "")
	if err != nil {
		klog.Errorf(err.Error())
	}
}

func UpdatePortMirrorStatusWithRetry(kube kube.Interface, namespace, name string,
	status ovntypes.OvnK8sStatus, clearErrMsg, errMessage string) error {
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the latest version of portMirror object to modify it
		latestPortMirror, err := kube.GetPortMirror(namespace, name)
		if err != nil {
			klog.Errorf("Unable to get portmirror %s/%s for updating status, most likely it would be deleted",
				namespace, name)
			return err
		}

		latestPortMirror = latestPortMirror.DeepCopy()
		// if clearErrMsg is set, need to clear that msg from portMirror status
		// and update status without that msg
		if clearErrMsg != "" {
			messages := []string{}
			statusContainsErrMsg := false
			for _, msg := range latestPortMirror.Status.Messages {
				if strings.Contains(msg, MessagePrefixPortMirrorErr) && strings.Contains(msg, clearErrMsg) {
					continue
				}
				// to set portmirror status message to success,
				// if this is last error message being cleared off.
				if strings.Contains(msg, MessagePrefixPortMirrorErr) {
					statusContainsErrMsg = true
				}
				messages = append(messages, msg)
			}
			latestPortMirror.Status.Messages = messages
			// set status to success if there are no error messages after
			// clearing the last error message.
			if !statusContainsErrMsg {
				latestPortMirror.Status.Status = ovntypes.OvnK8sStatusSucceeded
			}

		}

		if errMessage != "" {
			latestPortMirror.Status.Messages = append(latestPortMirror.Status.Messages, errMessage)
		}

		if status != "" {
			if status == ovntypes.OvnK8sStatusFailed {
				latestPortMirror.Status.Status = status
			} else {
				// if passed status value is success, need to check
				// if there are no err messages on portmirror status messages
				// and set the status value as success.
				statusContainsErrMsg := false
				for _, msg := range latestPortMirror.Status.Messages {
					if strings.Contains(msg, MessagePrefixPortMirrorErr) {
						statusContainsErrMsg = true
						break
					}
				}

				// update the status to success only if statusContainsErrMsg is false
				if !statusContainsErrMsg {
					latestPortMirror.Status.Status = status
				}
			}
		}
		return kube.UpdatePortMirrorStatus(latestPortMirror)
	})
	if retryErr != nil {
		return fmt.Errorf("error in updating status on portMirror %s/%s: %v", namespace, name, retryErr)
	}
	return nil
}
