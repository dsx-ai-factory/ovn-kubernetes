package networkprobe

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

// Converts the given payload size (string) to correct
// no of bytes
func parsePayloadSize(size string) int {
	var multiplier int
	switch {
	case len(size) >= 2 && size[len(size)-2:] == "Kb":
		multiplier = 1024
	case len(size) >= 2 && size[len(size)-2:] == "Mb":
		multiplier = 1024 * 1024
	case len(size) >= 2 && size[len(size)-2:] == "Gb":
		multiplier = 1024 * 1024 * 1024
	case len(size) >= 1 && size[len(size)-1] == 'B':
		multiplier = 1
		size = size[:len(size)-1]
	default:
		multiplier = 1
	}

	var value int
	_, err := fmt.Sscanf(size, "%d", &value)
	if err != nil {
		klog.Warningf("Failed to parse payload size '%s': %v, Using size 0.",
			size, err)
		return 0
	}

	return value * multiplier
}

// UpdateNetworkProbeStatus updates the status of the networkprobe based on type
// ovnkube-node on each selected will call this, hence let's update status using retryWithConflict
func (c *Controller) UpdateNetworkProbeStatus(namespace, name, nodeName, message string, probeReason types.OvnK8sStatus) error {
	var conditionValue metav1.ConditionStatus
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Fetch the latest NetworkProbe object
		latestNetworkProbe, err := c.networkProbeLister.NetworkProbes(namespace).Get(name)
		if err != nil {
			klog.Errorf("Error fetching networkprobe %s/%s for updating status (%v)", name, namespace, err)
			return err
		}
		// Create a deep copy to avoid modifying the cached object
		latestNetworkProbe = latestNetworkProbe.DeepCopy()

		// set the condition based on whether probeReason is ready or not
		if probeReason == networkProbeNotReadyReason {
			conditionValue = metav1.ConditionFalse
		} else {
			conditionValue = metav1.ConditionTrue
		}

		// Update the condition for the specific node only if message is not empty,
		// if not remove the status
		if message != "" {
			meta.SetStatusCondition(&latestNetworkProbe.Status.Conditions, metav1.Condition{
				Type:    networkProbeReadyStatusType + nodeName,
				Status:  conditionValue,
				Reason:  string(probeReason),
				Message: message,
			})
		} else {
			meta.RemoveStatusCondition(&latestNetworkProbe.Status.Conditions, networkProbeReadyStatusType+nodeName)
		}

		// Update the overall status of network probe
		if probeReason == networkProbeReadyReason {
			statusNotReady := false
			for _, condition := range latestNetworkProbe.Status.Conditions {
				if condition.Type != networkProbeReadyStatusType+nodeName && condition.Reason == networkProbeNotReadyReason {
					statusNotReady = true
					break
				}
			}

			if statusNotReady {
				latestNetworkProbe.Status.Status = networkProbeNotReadyReason
			} else {
				latestNetworkProbe.Status.Status = networkProbeReadyReason
			}
		} else {
			latestNetworkProbe.Status.Status = probeReason
		}

		_, err = c.networkProbeClientSet.K8sV1beta1().NetworkProbes(namespace).UpdateStatus(context.TODO(), latestNetworkProbe, metav1.UpdateOptions{})
		return err
	})

	if retryErr != nil {
		return fmt.Errorf("error in updating status on networkprobe %s/%s: %v", namespace, name, retryErr)
	}

	klog.V(5).Infof("Patched the status of networkprobe %s/%s with condition type %v/%v and reason %s",
		namespace, name, networkProbeReadyStatusType, conditionValue, probeReason)
	return nil
}
