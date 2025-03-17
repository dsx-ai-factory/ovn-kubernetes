package cni

import (
	"context"
	"fmt"
	"net"
	"time"

	current "github.com/containernetworking/cni/pkg/types/100"
	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"

	ovncnitypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni/udn"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kubevirt"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/networkmanager"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

var (
	minRsrc           = resource.MustParse("1k")
	maxRsrc           = resource.MustParse("1P")
	BandwidthNotFound = &notFoundError{}

	netDevPollTimeout  = 5 * time.Second
	netDevPollInterval = 200 * time.Millisecond
)

type direction int

func (d direction) String() string {
	if d == Egress {
		return "egress"
	}
	return "ingress"
}

const (
	Egress direction = iota
	Ingress
)

type notFoundError struct{}

func (*notFoundError) Error() string {
	return "not found"
}

func validateBandwidthIsReasonable(rsrc *resource.Quantity) error {
	if rsrc.Value() < minRsrc.Value() {
		return fmt.Errorf("resource is unreasonably small (< 1kbit)")
	}
	if rsrc.Value() > maxRsrc.Value() {
		return fmt.Errorf("resoruce is unreasonably large (> 1Pbit)")
	}
	return nil
}

func extractPodBandwidth(podAnnotations map[string]string, dir direction) (int64, error) {
	annotation := "kubernetes.io/ingress-bandwidth"
	if dir == Egress {
		annotation = "kubernetes.io/egress-bandwidth"
	}

	str, found := podAnnotations[annotation]
	if !found {
		return 0, BandwidthNotFound
	}
	bwVal, err := resource.ParseQuantity(str)
	if err != nil {
		return 0, err
	}
	if err := validateBandwidthIsReasonable(&bwVal); err != nil {
		return 0, err
	}
	return bwVal.Value(), nil
}

func (pr *PodRequest) String() string {
	return fmt.Sprintf("[%s/%s %s network %s NAD %s]", pr.PodNamespace, pr.PodName, pr.SandboxID, pr.netName, pr.nadName)
}

// checkOrUpdatePodUID validates the given pod UID against the request's existing
// pod UID. If the existing UID is empty the runtime did not support passing UIDs
// and the best we can do is use the given UID for the duration of the request.
// But if the existing UID is valid and does not match the given UID then the
// sandbox request is for a different pod instance and should be terminated.
// Static pod UID is a hash of the pod itself that does not match
// the UID of the mirror kubelet creates on the api /server.
// We will use the UID of the mirror.
// The hash is annotated in the mirror pod (kubernetes.io/config.hash)
// and we could match against it, but let's avoid that for now as it is not
// a published standard.
func (pr *PodRequest) checkOrUpdatePodUID(pod *corev1.Pod) error {
	if pr.PodUID == "" || IsStaticPod(pod) {
		// Runtime didn't pass UID, or the pod is a static pod, use the one we got from the pod object
		pr.PodUID = string(pod.UID)
	} else if string(pod.UID) != pr.PodUID {
		// Exit early if the pod was deleted and recreated already
		return fmt.Errorf("pod deleted before sandbox %v operation began. Request Pod UID %s is different from "+
			"the Pod UID (%s) retrieved from the informer/API", pr.Command, pr.PodUID, pod.UID)
	}
	return nil
}

// getNetdevName returns the netdevice name from the passed device ID.
func getNetdevName(deviceId string, deviceInfo v1.DeviceInfo) (string, error) {
	var netdevices []string

	retries := 0
	err := wait.PollUntilContextTimeout(context.Background(), netDevPollInterval, netDevPollTimeout, true, func(_ context.Context) (bool, error) {
		var localError error
		netdevices, localError = util.GetNetdevsNameFromDeviceId(deviceId, deviceInfo)
		retries++
		return len(netdevices) != 0, localError
	})
	if err != nil {
		return "", err
	}

	// Make sure we have 1 netdevice per pci address
	numNetDevices := len(netdevices)
	if numNetDevices != 1 {
		return "", fmt.Errorf("failed to get one netdevice interface (count %d) for %s after %d retries", numNetDevices, deviceId, retries)
	}
	klog.V(6).Infof("Found netdev %s after %d retries", netdevices[0], retries)
	return netdevices[0], nil
}

func (pr *PodRequest) cmdAdd(kubeAuth *KubeAPIAuth, clientset *ClientSet, networkManager networkmanager.Interface) (*Response, error) {
	return pr.cmdAddWithGetCNIResultFunc(kubeAuth, clientset, getCNIResult, networkManager)
}

func (pr *PodRequest) cmdAddWithGetCNIResultFunc(
	kubeAuth *KubeAPIAuth,
	clientset *ClientSet,
	getCNIResultFn getCNIResultFunc,
	networkManager networkmanager.Interface,
) (*Response, error) {
	namespace := pr.PodNamespace
	podName := pr.PodName
	if namespace == "" || podName == "" {
		return nil, fmt.Errorf("required CNI variable missing")
	}

	kubecli := &kube.Kube{KClient: clientset.kclient}
	annotCondFn := isOvnReady
	netdevName := ""
	if pr.CNIConf.DeviceID != "" {
		var err error

		if !pr.IsVFIO {
			netdevName, err = getNetdevName(pr.CNIConf.DeviceID, pr.deviceInfo)
			if err != nil {
				return nil, fmt.Errorf("failed in cmdAdd while getting Netdevice name: %w", err)
			}
		}
		if config.OvnKubeNode.Mode == types.NodeModeDPUHost {
			// Add DPU connection-details annotation so ovnkube-node running on DPU
			// performs the needed network plumbing.
			if err = pr.addDPUConnectionDetailsAnnot(kubecli, clientset.podLister, netdevName, pr.deviceInfo); err != nil {
				return nil, err
			}
			annotCondFn = isDPUReady
		}
		// For CX5 ASAP2 VF case we rely on udev rules to restore the VF name, so skip storing the original VF name
	}
	// Get the IP address and MAC address of the pod
	// for DPU, ensure connection-details is present

	primaryUDN := udn.NewPrimaryNetwork(networkManager)
	if util.IsNetworkSegmentationSupportEnabled() {
		annotCondFn = primaryUDN.WaitForPrimaryAnnotationFn(podName, namespace, annotCondFn)
	}
	pod, annotations, podNADAnnotation, err := GetPodWithAnnotations(pr.ctx, clientset, namespace, podName, pr.nadName, annotCondFn)
	if err != nil {
		return nil, fmt.Errorf("failed to get pod annotation: %v", err)
	}
	if err = pr.checkOrUpdatePodUID(pod); err != nil {
		return nil, err
	}

	podInterfaceInfo, err := pr.buildPodInterfaceInfo(annotations, podNADAnnotation, netdevName)
	if err != nil {
		return nil, err
	}

	podInterfaceInfo.SkipIPConfig = kubevirt.IsPodLiveMigratable(pod)

	response := &Response{KubeAuth: kubeAuth}
	if !config.UnprivilegedMode {
		//TODO: There is nothing technical to run this at unprivileged mode but
		//      we will tackle that later on.
		response.Result, err = getCNIResultFn(pr, clientset, podInterfaceInfo)
		if err != nil {
			return nil, err
		}
		if primaryUDN.Found() {
			primaryUDNPodRequest := pr.buildPrimaryUDNPodRequest(pod, primaryUDN)
			primaryUDNPodInfo, err := primaryUDNPodRequest.buildPodInterfaceInfo(annotations, primaryUDN.Annotation(), primaryUDN.NetworkDevice())
			if err != nil {
				return nil, err
			}
			primaryUDNResult, err := getCNIResultFn(primaryUDNPodRequest, clientset, primaryUDNPodInfo)
			if err != nil {
				return nil, err
			}

			response.Result.Routes = append(response.Result.Routes, primaryUDNResult.Routes...)
			numOfInitialIPs := len(response.Result.IPs)
			numOfInitialIfaces := len(response.Result.Interfaces)
			response.Result.Interfaces = append(response.Result.Interfaces, primaryUDNResult.Interfaces...)
			response.Result.IPs = append(response.Result.IPs, primaryUDNResult.IPs...)

			// Offset the index of the default network IPs to correctly point to the default network interfaces
			for i := numOfInitialIPs; i < len(response.Result.IPs); i++ {
				ifaceIPConfig := response.Result.IPs[i].Copy()
				if response.Result.IPs[i].Interface != nil {
					response.Result.IPs[i].Interface = current.Int(*ifaceIPConfig.Interface + numOfInitialIfaces)
				}
			}
		}
	} else {
		response.PodIFInfo = podInterfaceInfo
	}

	return response, nil
}

func (pr *PodRequest) cmdDel(clientset *ClientSet) (*Response, error) {
	// assume success case, return an empty Result
	response := &Response{}
	response.Result = &current.Result{}

	namespace := pr.PodNamespace
	podName := pr.PodName
	if namespace == "" || podName == "" {
		return nil, fmt.Errorf("required CNI variable missing")
	}

	netdevName := ""
	if pr.CNIConf.DeviceID != "" {
		if config.OvnKubeNode.Mode == types.NodeModeDPUHost {
			pod, err := clientset.getPod(pr.PodNamespace, pr.PodName)
			if err != nil {
				klog.Warningf("Failed to get pod %s/%s: %v", pr.PodNamespace, pr.PodName, err)
				return response, nil
			}
			dpuCD, err := util.UnmarshalPodDPUConnDetails(pod.Annotations, pr.nadName)
			if err != nil {
				klog.Warningf("Failed to get DPU connection details annotation for pod %s/%s NAD %s: %v", pr.PodNamespace,
					pr.PodName, pr.nadName, err)
				return response, nil
			}

			// check if this cmdDel is meant for the current sandbox, if not, directly return
			if dpuCD.SandboxId != pr.SandboxID {
				klog.Infof("The cmdDel request for sandbox %s is not meant for the currently configured "+
					"pod %s/%s on NAD %s with sandbox %s. Ignoring this request.",
					pr.SandboxID, namespace, podName, pr.nadName, dpuCD.SandboxId)
				return response, nil
			}

			// Delete the DPU connection-details annotation
			_ = pr.updatePodDPUConnDetailsWithRetry(&kube.Kube{KClient: clientset.kclient}, clientset.podLister, nil)

			if pr.IsVFIO {
				return response, nil
			}
			netdevName = dpuCD.VfNetdevName
		} else {
			// Find the hostInterface name
			condString := []string{"external-ids:sandbox=" + pr.SandboxID}
			if pr.netName != types.DefaultNetworkName {
				condString = append(condString, fmt.Sprintf("external_ids:%s=%s", types.NADExternalID, pr.nadName))
			} else {
				condString = append(condString, fmt.Sprintf("external_ids:%s{=}[]", types.NADExternalID))
			}
			ovsIfNames, err := ovsFind("Interface", "name", condString...)
			if err != nil || len(ovsIfNames) != 1 {
				klog.Warningf("Couldn't find the OVS interface for pod %s/%s NAD %s: %v",
					pr.PodNamespace, pr.PodName, pr.nadName, err)
			} else {
				ovsIfName := ovsIfNames[0]
				out, err := ovsGet("interface", ovsIfName, "external_ids", "netdev-name")
				if err != nil {
					klog.Warningf("Couldn't find the original Netdev name from OVS interface %s for pod %s/%s: %v",
						ovsIfName, pr.PodNamespace, pr.PodName, err)
				} else {
					netdevName = out
				}
			}
		}
	}

	podInterfaceInfo := &PodInterfaceInfo{
		IsDPUHostMode: config.OvnKubeNode.Mode == types.NodeModeDPUHost,
		NetdevName:    netdevName,
		NetName:       pr.netName,
		NADName:       pr.nadName,
	}
	if !config.UnprivilegedMode {
		err := podRequestInterfaceOps.UnconfigureInterface(pr, podInterfaceInfo)
		if err != nil {
			return nil, err
		}
		// succeed, return an empty Result
	} else {
		// pass the isDPU flag and vfNetdevName back to cniShim
		response.Result = nil
		response.PodIFInfo = podInterfaceInfo
	}
	return response, nil
}

func (pr *PodRequest) cmdCheck() error {
	// noop...CMD check is not considered useful, and has a considerable performance impact
	// to pod bring up times with CRIO. This is due to the fact that CRIO currently calls check
	// after CNI ADD before it finishes bringing the container up
	return nil
}

// HandlePodRequest is the callback for all the requests
// coming to the cniserver after being processed into PodRequest objects
// Argument '*PodRequest' encapsulates all the necessary information
// kclient is passed in so that clientset can be reused from the server
// Return value is the actual bytes to be sent back without further processing.
func HandlePodRequest(
	request *PodRequest,
	clientset *ClientSet,
	kubeAuth *KubeAPIAuth,
	networkManager networkmanager.Interface,
) ([]byte, error) {
	var result, resultForLogging []byte
	var response *Response
	var err, err1 error

	klog.Infof("%s %s starting CNI request (%+v) DeviceID(%q) for pod %s/%s network %s NAD %s and ovnkubemode %s",
		request, request.Command, request, request.CNIConf.DeviceID, request.PodNamespace, request.PodName, request.netName,
		request.nadName, config.OvnKubeNode.Mode)
	switch request.Command {
	case CNIAdd:
		response, err = request.cmdAdd(kubeAuth, clientset, networkManager)
	case CNIDel:
		response, err = request.cmdDel(clientset)
	case CNICheck:
		err = request.cmdCheck()
	default:
	}

	if response != nil {
		if result, err1 = response.Marshal(); err1 != nil {
			return nil, fmt.Errorf("%s %s CNI request %+v failed to marshal result: %v",
				request, request.Command, request, err1)
		}
		if resultForLogging, err1 = response.MarshalForLogging(); err1 != nil {
			klog.Errorf("%s %s CNI request %+v, %v", request, request.Command, request, err1)
		}
	}

	klog.Infof("%s %s finished CNI request %+v, result %q, err %v",
		request, request.Command, request, string(resultForLogging), err)

	if err != nil {
		// Prefix errors with request info for easier failure debugging
		return nil, fmt.Errorf("%s %v", request, err)
	}
	return result, nil
}

// getCNIResult get result from pod interface info.
// PodInfoGetter is used to check if sandbox is still valid for the current
// instance of the pod in the apiserver, see checkCancelSandbox for more info.
// If kube api is not available from the CNI, pass nil to skip this check.
func getCNIResult(pr *PodRequest, getter PodInfoGetter, podInterfaceInfo *PodInterfaceInfo) (*current.Result, error) {
	interfacesArray, err := podRequestInterfaceOps.ConfigureInterface(pr, getter, podInterfaceInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to configure pod interface: %v", err)
	}

	gateways := map[string]net.IP{}
	for _, gw := range podInterfaceInfo.Gateways {
		if gw.To4() != nil && gateways["4"] == nil {
			gateways["4"] = gw
		} else if gw.To4() == nil && gateways["6"] == nil {
			gateways["6"] = gw
		}
	}

	// Build the result structure to pass back to the runtime
	ips := []*current.IPConfig{}
	for _, ipcidr := range podInterfaceInfo.IPs {
		ip := &current.IPConfig{
			Interface: current.Int(1),
			Address:   *ipcidr,
		}
		var ipVersion string
		if utilnet.IsIPv6CIDR(ipcidr) {
			ipVersion = "6"
		} else {
			ipVersion = "4"
		}
		ip.Gateway = gateways[ipVersion]
		ips = append(ips, ip)
	}

	return &current.Result{
		Interfaces: interfacesArray,
		IPs:        ips,
	}, nil
}

func (pr *PodRequest) buildPrimaryUDNPodRequest(
	pod *corev1.Pod,
	primaryUDN *udn.UserDefinedPrimaryNetwork,
) *PodRequest {
	req := &PodRequest{
		Command:      pr.Command,
		PodNamespace: pod.Namespace,
		PodName:      pod.Name,
		PodUID:       string(pod.UID),
		SandboxID:    pr.SandboxID,
		Netns:        pr.Netns,
		IfName:       primaryUDN.InterfaceName(),
		CNIConf: &ovncnitypes.NetConf{
			// primary UDN MTU will be taken from config.Default.MTU
			// if not specified at the NAD
			MTU: primaryUDN.MTU(),
		},
		timestamp:  time.Now(),
		IsVFIO:     pr.IsVFIO,
		netName:    primaryUDN.NetworkName(),
		nadName:    primaryUDN.NADName(),
		deviceInfo: v1.DeviceInfo{},
	}
	req.ctx, req.cancel = context.WithTimeout(context.Background(), 2*time.Minute)
	return req
}

func (pr *PodRequest) buildPodInterfaceInfo(annotations map[string]string, podAnnotation *util.PodAnnotation, netDevice string) (*PodInterfaceInfo, error) {
	return PodAnnotation2PodInfo(
		annotations,
		podAnnotation,
		pr.PodUID,
		netDevice,
		pr.nadName,
		pr.netName,
	)
}
