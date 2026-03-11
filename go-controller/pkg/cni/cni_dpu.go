package cni

import (
	"fmt"

	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

// updatePodDPUConnDetailsWithRetry update the pod annotation with the given connection details for the NAD in
// the PodRequest. If the dpuConnDetails argument is nil, delete the NAD's DPU connection details annotation instead.
func (pr *PodRequest) updatePodDPUConnDetailsWithRetry(kube kube.Interface, podLister corev1listers.PodLister, dpuConnDetails *util.DPUConnectionDetails) error {
	klog.Infof("Updating pod %s/%s with connection details (%+v) for NAD %s", pr.PodNamespace, pr.PodName,
		dpuConnDetails, pr.nadName)
	pod, err := podLister.Pods(pr.PodNamespace).Get(pr.PodName)
	if err != nil {
		return fmt.Errorf("failed to get pod %s/%s to update connection details for NAD %s: %v", pr.PodNamespace, pr.PodName, pr.nadName, err)
	}
	err = util.UpdatePodDPUConnDetailsWithRetry(
		podLister,
		kube,
		pod,
		dpuConnDetails,
		pr.nadKey,
	)
	return err
}

func (pr *PodRequest) addDPUConnectionDetailsAnnot(k kube.Interface, podLister corev1listers.PodLister, vfNetdevName string) error {
	if pr.CNIConf.DeviceID == "" {
		return fmt.Errorf("DeviceID must be set for Pod request with DPU")
	}
	pciAddress := pr.CNIConf.DeviceID

	vfindex, err := util.GetSriovnetOps().GetVfIndexByPciAddress(pciAddress)
	if err != nil {
		return err
	}
	pfindex, err := util.GetSriovnetOps().GetPfIndexByVfPciAddress(pciAddress)
	if err != nil {
		return err
	}

	// Get pfMAC
	pfPciAddress, err := util.GetSriovnetOps().GetPfPciFromVfPci(pciAddress)
	if err != nil {
		return err
	}

	pfNetdev, err := util.GetNetdevNameFromDeviceId(pfPciAddress, pr.deviceInfo)
	if err != nil {
		return fmt.Errorf("failed to get the PF name from the PCI Address: %s, %v", pfPciAddress, err)
	}
	pfLink, err := util.GetNetLinkOps().LinkByName(pfNetdev)
	if err != nil {
		return fmt.Errorf("failed to get netlink object for link name: %s, %v", pfNetdev, err)
	}

	dpuConnDetails := util.DPUConnectionDetails{
		PfId:         fmt.Sprint(pfindex),
		VfId:         fmt.Sprint(vfindex),
		PfMAC:        pfLink.Attrs().HardwareAddr.String(),
		SandboxId:    pr.SandboxID,
		VfNetdevName: vfNetdevName,
	}

	return pr.updatePodDPUConnDetailsWithRetry(k, podLister, &dpuConnDetails)
}
