package cni

import (
	"fmt"

	nadapi "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"
)

// updatePodDPUConnDetailsWithRetry update the pod annotation with the given connection details for the NAD in
// the PodRequest. If the dpuConnDetails argument is nil, delete the NAD's DPU connection details annotation instead.
func (pr *PodRequest) updatePodDPUConnDetailsWithRetry(kube kube.Interface, podLister corev1listers.PodLister, dpuConnDetails *util.DPUConnectionDetails) error {
	klog.Infof("Updating pod %s/%s with connection details (%+v) for NAD %s", pr.PodNamespace, pr.PodName,
		dpuConnDetails, pr.nadName)
	pod, err := podLister.Pods(pr.PodNamespace).Get(pr.PodName)
	if err != nil {
		return err
	}
	err = util.UpdatePodDPUConnDetailsWithRetry(
		podLister,
		kube,
		pod,
		dpuConnDetails,
		pr.nadName,
	)
	if util.IsAnnotationAlreadySetError(err) {
		return nil
	}

	return err
}

func (pr *PodRequest) addDPUConnectionDetailsAnnot(k kube.Interface, podLister corev1listers.PodLister, vfNetdevName string, deviceInfo nadapi.DeviceInfo) error {
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

	pfNetdev, err := util.GetNetdevNameFromDeviceId(pfPciAddress, deviceInfo)
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
