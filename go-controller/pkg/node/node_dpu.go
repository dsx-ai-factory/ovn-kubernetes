package node

import (
	"context"
	"fmt"
	"strings"
	"time"

	netattchdefapi "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

// Check if the Pod is ready so that we can add its associated DPU to br-int.
// If true, return its dpuConnDetails, otherwise return nil
func (nc *ovnNodeController) podReadyToAddDPU(pod *kapi.Pod, nadName string, pfMACs []string) *util.DPUConnectionDetails {
	if nc.node.name != pod.Spec.NodeName {
		klog.V(5).Infof("Pod %s/%s is not scheduled on this node %s", pod.Namespace, pod.Name, nc.node.name)
		return nil
	}

	annoNadKeyName := util.GetAnnotationKeyFromNadName(nadName, !nc.nadInfo.IsSecondary)
	dpuCD, err := util.UnmarshalPodDPUConnDetails(pod.Annotations, annoNadKeyName)
	if err != nil {
		if !util.IsAnnotationNotSetError(err) {
			klog.Errorf("Failed to get dpu annotation for pod %s/%s nad %s: %v",
				pod.Namespace, pod.Name, nadName, err)
		} else {
			klog.V(5).Infof("DPU connection details annotation still not found for %s/%s for NAD %s",
				pod.Namespace, pod.Name, nadName)
		}
		return nil
	}

	// Get the `pfMAC` from the pod annotation, see if this pfMAC belongs to this DPU
	for _, pfMAC := range pfMACs {
		if pfMAC == dpuCD.PfMAC {
			return dpuCD
		}
	}
	klog.V(5).Infof("Pod %s/%s on NAD %s is not associated with this dpu", pod.Namespace, pod.Name, nadName)
	return nil
}

func (nc *ovnNodeController) addDPUPod4Nad(pod *kapi.Pod, dpuCD *util.DPUConnectionDetails, isOvnUpEnabled bool, nadName string,
	podLister corev1listers.PodLister, kclient kubernetes.Interface) error {
	podDesc := fmt.Sprintf("pod %s/%s for nad %s", pod.Namespace, pod.Name, nadName)
	klog.Infof("Adding %s on DPU", podDesc)
	podInterfaceInfo, err := cni.PodAnnotation2PodInfo(pod.Annotations, isOvnUpEnabled, string(pod.UID), "",
		nadName, nc.nadInfo.NetNameInfo)
	if err != nil {
		klog.Errorf("Failed to get pod interface information of %s: %v. retrying", podDesc, err)
		return err
	}
	err = nc.addRepPort(pod, dpuCD, podInterfaceInfo, podLister, kclient)
	if err != nil {
		klog.Errorf("Failed to add rep port for %s, %v. retrying", podDesc, err)
	}
	return err
}

// watchPodsDPU watch updates for pod dpu annotations
func (nc *ovnNodeController) watchPodsDPU(isOvnUpEnabled bool, pfMACs []string) error {
	klog.Infof("Controller %q for NADs %v is starting Pod watch with following DPU PF MACs: %v", nc.nadInfo.NetName,
		util.GetNADNamesFromMap(&nc.nadInfo.NetAttachDefs), pfMACs)

	n := nc.node
	podLister := corev1listers.NewPodLister(n.watchFactory.LocalPodInformer().GetIndexer())
	kclient := n.Kube.(*kube.Kube)

	var err error
	nc.podHandler, err = n.watchFactory.AddPodHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod := obj.(*kapi.Pod)
			// Is this pod based on hostNetwork, return directly
			if !util.PodWantsNetwork(pod) {
				return
			}
			// lock pod to avoid racing on `servedCache`
			unlock := util.LockByKey.Acquire(string(pod.UID))
			defer unlock()
			on, networkMap, err := util.IsNetworkOnPod(pod, nc.nadInfo)
			if err != nil || !on {
				// the Pod is not attached to this specific network
				klog.V(5).Infof("Skipping add for Pod %s/%s as it is not attached to network: %s",
					pod.Namespace, pod.Name, nc.nadInfo.NetName)
				return
			}
			klog.Infof("Add for Pod: %s/%s for network %s", pod.Namespace, pod.Name, nc.nadInfo.NetName)
			// add all the Pod's Nad into Pod's podNadCache
			nc.podNadCache.Store(pod.UID, networkMap)

			// initialize serverCache to be empty
			servedCache := map[string]*util.DPUConnectionDetails{}
			for nadName := range networkMap {
				dpuCD := nc.podReadyToAddDPU(pod, nadName, pfMACs)
				if dpuCD != nil {
					err = nc.addDPUPod4Nad(pod, dpuCD, isOvnUpEnabled, nadName, podLister, kclient.KClient)
					if err == nil {
						servedCache[nadName] = dpuCD
					}
				}
			}
			nc.servedPods.Store(pod.UID, servedCache)
		},
		UpdateFunc: func(old, newer interface{}) {
			oldPod := old.(*kapi.Pod)
			newPod := newer.(*kapi.Pod)
			// lock pod to avoid racing on `servedCache`
			unlock := util.LockByKey.Acquire(string(oldPod.UID))
			defer unlock()
			v, ok := nc.podNadCache.Load(newPod.UID)
			if !ok {
				klog.V(6).Infof("Skipping update for Pod %s/%s as it is not attached to network: %s",
					newPod.Namespace, newPod.Name, nc.nadInfo.NetName)
				return
			}
			klog.Infof("Update for Pod %s/%s for network %s", newPod.Namespace, newPod.Name, nc.nadInfo.NetName)

			networkMap := v.(map[string]*netattchdefapi.NetworkSelectionElement)

			servedCache := map[string]*util.DPUConnectionDetails{}
			v, ok = nc.servedPods.Load(newPod.UID)
			if ok {
				servedCache = v.(map[string]*util.DPUConnectionDetails)
			}
			portSecInfoChanged := oldPod.Annotations[util.PortSecurityInfoAnnotation] != newPod.Annotations[util.PortSecurityInfoAnnotation]
			for nadName := range networkMap {
				podDesc := fmt.Sprintf("pod %s/%s for nad %s", newPod.Namespace, newPod.Name, nadName)
				var oldDpuCD *util.DPUConnectionDetails
				v, ok := servedCache[nadName]
				if ok {
					oldDpuCD = v
				}
				newDpuCD := nc.podReadyToAddDPU(newPod, nadName, pfMACs)
				if oldDpuCD == nil && newDpuCD == nil {
					continue
				}
				// if portSecInfoChanged and we need to del/add the VF for the pod, the del/add of the rep will
				// also take care of updating the xdp config, if any. However, if we don't have any change in
				// the connection info, and only the port security annotation changed, we'll need to do that.
				if oldDpuCD != nil {
					// VF already added, but new Pod has changed, we'd need to delete the old VF
					if newDpuCD == nil || oldDpuCD.PfId != newDpuCD.PfId ||
						oldDpuCD.VfId != newDpuCD.VfId || oldDpuCD.SandboxId != newDpuCD.SandboxId {
						klog.Infof("Deleting the old VF since either kubelet issued cmdDEL or assigned a new VF or "+
							"the sandbox id itself changed. Old connection details (%v), New connection details (%v)",
							oldDpuCD, newDpuCD)
						err := nc.updatePodDPUConnStatusWithRetry(nc.node.Kube, oldPod, nil, nadName)
						if err != nil {
							klog.Errorf("Failed to remove the old DPU connection status annotation for %s: %v", podDesc, err)
						}
						vfRepName, err := util.GetSriovnetOps().GetVfRepresentorDPU(oldDpuCD.PfId, oldDpuCD.VfId)
						if err != nil {
							klog.Errorf("Failed to get old VF Representor for %s, dpuConnDetail %+v Representor port may have been deleted", podDesc, oldDpuCD, err)
						} else {
							err = nc.delRepPort(oldPod, oldDpuCD, vfRepName, nadName, podDesc)
							if err != nil {
								klog.Errorf("Failed to delete VF representor for %s: %v", podDesc, err)
							}
						}
						delete(servedCache, nadName)
					}
				}
				if newDpuCD != nil {
					// if VF was failed to be added before or, if new Pod has changed, we'd need to add the new VF
					if oldDpuCD == nil || oldDpuCD.PfId != newDpuCD.PfId ||
						oldDpuCD.VfId != newDpuCD.VfId || oldDpuCD.SandboxId != newDpuCD.SandboxId {
						klog.Infof("Adding VF during update because either during Pod Add we failed to add VF or "+
							"connection details weren't present or the VF Ids changed. Old connection details (%v), "+
							"New connection details (%v)", oldDpuCD, newDpuCD)
						err := nc.addDPUPod4Nad(newPod, newDpuCD, isOvnUpEnabled, nadName, podLister, kclient.KClient)
						if err == nil {
							servedCache[nadName] = newDpuCD
						}
					} else if portSecInfoChanged && nc.nadInfo.XDPService {
						err := nc.updateXDPInterfaceConfig(oldPod, newPod, nadName)
						if err != nil {
							klog.Errorf("Failed to update XDP config for Pod %s: %v", podDesc, err)
						}
					}
				}
			}
			nc.servedPods.Store(newPod.UID, servedCache)
		},
		DeleteFunc: func(obj interface{}) {
			pod := obj.(*kapi.Pod)
			// lock pod to avoid racing on `servedCache`
			unlock := util.LockByKey.Acquire(string(pod.UID))
			defer unlock()
			_, ok := nc.podNadCache.Load(pod.UID)
			if !ok {
				klog.V(5).Infof("Skipping delete for Pod %s/%s as it is not attached to network: %s",
					pod.Namespace, pod.Name, nc.nadInfo.NetName)
				return
			}
			klog.Infof("Delete for Pod: %s/%s for network %s", pod.Namespace, pod.Name, nc.nadInfo.NetName)
			nc.podNadCache.Delete(pod.UID)
			v, ok := nc.servedPods.Load(pod.UID)
			if !ok {
				klog.V(5).Infof("Pod %s/%s is not attached to network: %s", pod.Namespace, pod.Name, nc.nadInfo.NetName)
				return
			}
			servedCache := v.(map[string]*util.DPUConnectionDetails)
			nc.servedPods.Delete(pod.UID)
			for nadName, dpuCD := range servedCache {
				podDesc := fmt.Sprintf("pod %s/%s for nad %s", pod.Namespace, pod.Name, nadName)
				klog.Infof("Deleting %s from DPU", podDesc)
				vfRepName, err := util.GetSriovnetOps().GetVfRepresentorDPU(dpuCD.PfId, dpuCD.VfId)
				if err != nil {
					klog.Errorf("Failed to get VF Representor for %s, dpuConnDetail %+v. Representor port may have been deleted", podDesc, dpuCD, err)
					continue
				}
				err = nc.delRepPort(pod, dpuCD, vfRepName, nadName, podDesc)
				if err != nil {
					klog.Errorf("Failed to delete VF representor for %s: %v", podDesc, err)
				}
			}
		},
	}, nil)
	return err
}

// updatePodDPUConnStatusWithRetry update the pod annotion with the givin connection details
func (nc *ovnNodeController) updatePodDPUConnStatusWithRetry(kube kube.Interface, origPod *kapi.Pod,
	dpuConnStatus *util.DPUConnectionStatus, nadName string) error {
	podDesc := fmt.Sprintf("pod %s/%s for nad %s", origPod.Namespace, origPod.Name, nadName)
	annoNadKeyName := util.GetAnnotationKeyFromNadName(nadName, !nc.nadInfo.IsSecondary)
	klog.Infof("Updating pod %s with connection status (%+v) for NAD %s", podDesc, dpuConnStatus, nadName)
	resultErr := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		pod, err := kube.GetPod(origPod.Namespace, origPod.Name)
		if err != nil {
			return err
		}
		// Informer cache should not be mutated, so get a copy of the object
		cpod := pod.DeepCopy()
		err = util.MarshalPodDPUConnStatus(&cpod.Annotations, dpuConnStatus, annoNadKeyName)
		if err != nil {
			if util.IsAnnotationAlreadySetError(err) {
				return nil
			}
			return err
		}
		return kube.UpdatePod(cpod)
	})
	if resultErr != nil {
		return fmt.Errorf("failed to update %s annotation for %s: %v", util.DPUConnetionStatusAnnot, podDesc, resultErr)
	}
	return nil
}

func isNadForPodClampedDown(podAnnotation map[string]string, annoNadKeyName string) bool {
	if status, err := util.UnmarshalPodDPUConnStatus(podAnnotation, annoNadKeyName); err == nil {
		if status.Status == util.DPUConnectionStatusClampedDown {
			return true
		}
	}
	return false
}

// get Pod IPs from the PortSecInfo or the Pod
func getPortSecIPsforNAD(pod *kapi.Pod, annoNadKeyName string) ([]string, *util.PodAnnotation) {
	var allowedIPs []string
	netAnnotation, err := util.UnmarshalPodAnnotation(pod.Annotations, annoNadKeyName)
	if err != nil {
		return allowedIPs, nil
	}
	if !util.SkipIPAMForNAD(pod.Annotations, annoNadKeyName) {
		podIP := strings.Split(netAnnotation.IPs[0].String(), "/")
		allowedIPs = append(allowedIPs, podIP[0])
	} else {
		if psInfo, err := util.GetPortSecurityInfo(pod.Annotations); err == nil {
			if ipList := psInfo[annoNadKeyName]; ipList != nil && len(ipList.IPs) > 0 {
				allowedIPs = append(allowedIPs, ipList.IPs...)
			}
		}
	}
	return allowedIPs, netAnnotation
}

// simple function instead of using deepequal
func checkNADPortSecIPsAreDiff(oldIPs, newIPs []string) bool {
	if len(oldIPs) != len(newIPs) {
		return true
	}
	for i, v := range oldIPs {
		if v != newIPs[i] {
			return true
		}
	}
	return false
}

// the portSecurity for the pod is updated, update the XDP configuration, if needed . Note, there is
// a gap when we teardown old and add new; which might have a brief impact
func (nc *ovnNodeController) updateXDPInterfaceConfig(oldPod, newPod *kapi.Pod, nadName string) error {
	klog.Infof("Updating XDP service for pod %s/%s network %s", newPod.Namespace, newPod.Name, nadName)
	annoNadKeyName := util.GetAnnotationKeyFromNadName(nadName, !nc.nadInfo.IsSecondary)
	oldAllowedIPs, _ := getPortSecIPsforNAD(oldPod, annoNadKeyName)
	newAllowedIPs, netAnnotation := getPortSecIPsforNAD(newPod, annoNadKeyName)
	if len(oldAllowedIPs) == 0 || len(newAllowedIPs) == 0 {
		return fmt.Errorf("failed to update XDP for pod %s/%s with oldIPs %v and new IPs %v", newPod.Namespace, newPod.Name, oldAllowedIPs, newAllowedIPs)

	}
	// Check if the IPs are different and call into XDP, if so.
	if checkNADPortSecIPsAreDiff(oldAllowedIPs, newAllowedIPs) {
		gw := nc.gateway.(*gateway)
		if config.OvnKubeNode.IsPrimaryDPU {
			gw = nc.node.gateway.(*gateway)
		}
		if err := UpdateXDPServiceForInterface(netAnnotation, oldAllowedIPs, newAllowedIPs, nc.nadInfo, nc.gateway.(*gateway), gw); err != nil {
			return fmt.Errorf("failed to update XDP for pod %s/%s: %v", newPod.Namespace, newPod.Name, err)
		}
	}
	return nil
}

// addRepPort adds the representor of the VF to the ovs bridge
func (nc *ovnNodeController) addRepPort(pod *kapi.Pod, dpuCD *util.DPUConnectionDetails, ifInfo *cni.PodInterfaceInfo, podLister corev1listers.PodLister, kclient kubernetes.Interface) error {
	nadName := ifInfo.NadName
	annoNadKeyName := util.GetAnnotationKeyFromNadName(nadName, !nc.nadInfo.IsSecondary)
	podDesc := fmt.Sprintf("pod %s/%s for nad %s", pod.Namespace, pod.Name, nadName)
	vfRepName, err := util.GetSriovnetOps().GetVfRepresentorDPU(dpuCD.PfId, dpuCD.VfId)
	if err != nil {
		klog.Infof("Failed to get rep name of %s dpuConnDetail +%v: %v", podDesc, dpuCD, err)
		return err
	}
	dpuCD.ConnPrivateInfo.ConnVFRepName = vfRepName

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ifInfo.NetdevName = vfRepName
	klog.Infof("Adding VF representor %s for %s and cluster [%s]", vfRepName, podDesc, config.Kubernetes.ClusterName)
	err = cni.ConfigureOVS(ctx, pod.Namespace, pod.Name, vfRepName, ifInfo, dpuCD.SandboxId, podLister, kclient)
	if err != nil {
		// Note(adrianc): we are lenient with cleanup in this method as pod is going to be retried anyway.
		_ = nc.delRepPort(pod, dpuCD, vfRepName, nadName, podDesc)
		return err
	}
	klog.Infof("Port %s added to bridge br-int", vfRepName)

	// set the Pod interface's MAC address on the corresponding VF Port
	err = util.GetSriovnetOps().SetRepresentorPeerMacAddress(vfRepName, ifInfo.MAC)
	if err != nil {
		_ = nc.delRepPort(pod, dpuCD, vfRepName, nadName, podDesc)
		return fmt.Errorf("failed to set the MAC address %s on VF reprentor %s: %v",
			ifInfo.MAC.String(), vfRepName, err)
	}

	link, err := util.GetNetLinkOps().LinkByName(vfRepName)
	if err != nil {
		_ = nc.delRepPort(pod, dpuCD, vfRepName, nadName, podDesc)
		return fmt.Errorf("failed to get link device for interface %s: %v", vfRepName, err)
	}

	if err = util.GetNetLinkOps().LinkSetMTU(link, ifInfo.MTU); err != nil {
		_ = nc.delRepPort(pod, dpuCD, vfRepName, nadName, podDesc)
		return fmt.Errorf("failed to setup representor port. failed to set MTU %d for interface %s: %v", ifInfo.MTU, vfRepName, err)
	}

	if err = util.GetNetLinkOps().LinkSetUp(link); err != nil {
		_ = nc.delRepPort(pod, dpuCD, vfRepName, nadName, podDesc)
		return fmt.Errorf("failed to setup representor port. failed to set link up for interface %s: %v", vfRepName, err)
	}

	var nadConf *util.NadConfig
	if v, ok := nc.nadInfo.NetAttachDefs.Load(nadName); ok {
		nadConf = v.(*util.NadConfig)
	} else {
		// Failed to find the per nad configuration
		return fmt.Errorf("failed to find nad configuration for %s", nadName)
	}
	// Update connection-status annotation
	// TODO(adrianc): we should update Status in case of error as well
	connStatus := util.DPUConnectionStatus{Status: util.DPUConnectionStatusReady, Reason: ""}
	maxNewConnPPS, maxNewConnBurst, disableDoSCheck := nadConf.GetMissRateLimitConfig(nc.node.hostType)
	if maxNewConnPPS > 0 && !disableDoSCheck {
		dpuCD.ConnPrivateInfo.MissRateDoSCheck = true
		//
		// We use the Pod annotation to see if it is clamped down for this NAD instead of checking the existing
		// value on the VF. Reason being if the DPU reboots, we'll lose the VF configuration so we can't rely
		// on that.
		nadClampedDown := isNadForPodClampedDown(pod.Annotations, annoNadKeyName)
		if nadClampedDown {
			maxNewConnPPS = ClampdownDoSRate
			maxNewConnBurst = ClampdownDoSBurst
			dpuCD.ConnPrivateInfo.ConnClampedDown = true
			connStatus = util.DPUConnectionStatus{Status: util.DPUConnectionStatusClampedDown, Reason: ""}
		} else {
			// Collect the drop statistics so we can initialize it.
			if dpuCD.ConnPrivateInfo.MissRateLimitDropInitial, err = util.GetSriovnetOps().GetRepresentorVFMissPktDrops(vfRepName); err != nil {
				_ = nc.delRepPort(pod, dpuCD, vfRepName, nadName, podDesc)
				return fmt.Errorf("failed to get initial Miss RL drops for %s dpuConnDetail +%v: %v", podDesc, dpuCD, err)
			} else {
				klog.V(5).Infof("DoS: Initial Drop limit for VF representor %s for %s: %v", vfRepName, podDesc, dpuCD.ConnPrivateInfo.MissRateLimitDropInitial)
			}
		}
	}
	klog.Infof("Adding Limit %v/%v for VF representor %s for %s", maxNewConnPPS, maxNewConnBurst, vfRepName, podDesc)
	// set the VF rate limit configured for this network. This rate is for the allowed no. of new connections.
	if err = util.GetSriovnetOps().SetRepresentorVFMissPktRate(vfRepName, maxNewConnPPS, maxNewConnBurst); err != nil {
		_ = nc.delRepPort(pod, dpuCD, vfRepName, nadName, podDesc)
		return fmt.Errorf("failed to setup Rate limiting  for interface %s: %v", vfRepName, err)
	}
	// Configure XDP for this network
	if nc.nadInfo.XDPService {
		klog.Infof("Setting up XDP service for pod %s/%s network %s", pod.Namespace, pod.Name, ifInfo.NadName)
		allowedIPs, _ := getPortSecIPsforNAD(pod, annoNadKeyName)
		if len(allowedIPs) == 0 {
			_ = nc.delRepPort(pod, dpuCD, vfRepName, nadName, podDesc)
			return fmt.Errorf("failed geting IP info for NAD %s from pod annotation", ifInfo.NadName)
		}
		gw := nc.gateway.(*gateway)
		// Check if the (localnet) patch port is in place
		gwReady, _ := gw.readyFunc()
		if !gwReady {
			_ = nc.delRepPort(pod, dpuCD, vfRepName, nadName, podDesc)
			return fmt.Errorf("failed to setup XDP, gateway not ready: %v", err)
		}
		if config.OvnKubeNode.IsPrimaryDPU {
			gw = nc.node.gateway.(*gateway)
		}
		// If this pod needs Syn-Flooding mitigation on the DPU (to protect DPU cores)
		// by adding a bump-in-the-path kind of service before signalling that pod as ready.
		if err = SetupXDPServiceForInterface(&ifInfo.PodAnnotation, allowedIPs, nc.nadInfo, nc.gateway.(*gateway), gw); err != nil {
			_ = nc.delRepPort(pod, dpuCD, vfRepName, nadName, podDesc)
			return fmt.Errorf("failed to setup XDP for network: %v", err)
		}
	} else {
		klog.Infof("XDP not needed for pod %s/%s network %s", pod.Namespace, pod.Name, ifInfo.NadName)
	}
	err = nc.updatePodDPUConnStatusWithRetry(nc.node.Kube, pod, &connStatus, nadName)
	if err != nil {
		_ = util.GetNetLinkOps().LinkSetDown(link)
		_ = nc.delRepPort(pod, dpuCD, vfRepName, nadName, podDesc)
		return fmt.Errorf("failed to setup representor port. failed to set pod annotations. %v", err)
	}
	return nil
}

// delRepPort delete the representor of the VF from the ovs bridge
func (nc *ovnNodeController) delRepPort(pod *kapi.Pod, dpuCD *util.DPUConnectionDetails, vfRepName, nadName, podDesc string) error {
	//TODO(adrianc): handle: clearPodBandwidth(pr.SandboxID), pr.deletePodConntrack()
	klog.Infof("Deleting VF representor %s for %s", vfRepName, podDesc)
	annoNadKeyName := util.GetAnnotationKeyFromNadName(nadName, !nc.nadInfo.IsSecondary)
	ifExists, sandbox, expectedAnnoNadKeyName, err := util.GetOVSPortPodInfo(vfRepName)
	if err != nil {
		return fmt.Errorf(err.Error())
	}
	if !ifExists {
		klog.Infof("VF representor %s for %s is not an OVS interface, nothing to do", vfRepName, podDesc)
		return nil
	}
	if sandbox != dpuCD.SandboxId {
		return fmt.Errorf("OVS port %s was added for sandbox (%s), expecting (%s)", vfRepName, sandbox, dpuCD.SandboxId)
	}
	if expectedAnnoNadKeyName != annoNadKeyName {
		return fmt.Errorf("OVS port %s was added for nad (%s), expecting (%s)", vfRepName, expectedAnnoNadKeyName, annoNadKeyName)
	}
	// Remove XDP xonfigurationfor this network
	if nc.nadInfo.XDPService {
		klog.Infof("Removing XDP service for pod %s/%s network %s", pod.Namespace, pod.Name, nadName)
		allowedIPs, netAnnotation := getPortSecIPsforNAD(pod, annoNadKeyName)
		if len(allowedIPs) > 0 {
			gw := nc.gateway.(*gateway)
			if config.OvnKubeNode.IsPrimaryDPU {
				gw = nc.node.gateway.(*gateway)
			}
			// If this pod used Syn-Flooding mitigation on the DPU (to protect DPU cores)
			// delete it.
			if err = TeardownXDPServiceForInterface(netAnnotation, allowedIPs, nc.nadInfo, nc.gateway.(*gateway), gw); err != nil {
				return fmt.Errorf("failed to tear down XDP: %v", err)
			}
		} else {
			klog.Infof("Failed getting IP addresses for pod %s/%s network %s for deleting XDP",
				pod.Namespace, pod.Name, nadName)
		}
	} else {
		klog.Infof("XDP service not used for pod %s/%s network %s", pod.Namespace, pod.Name, annoNadKeyName)
	}

	// Set link down for representor port
	link, err := util.GetNetLinkOps().LinkByName(vfRepName)
	if err != nil {
		klog.Warningf("Failed to get link device for representor port %s. %v", vfRepName, err)
	} else {
		if linkDownErr := util.GetNetLinkOps().LinkSetDown(link); linkDownErr != nil {
			klog.Warningf("Failed to set link down for representor port %s. %v", vfRepName, linkDownErr)
		}
	}
	// reset the VF rate limit configured so that it doesn't get carried over to other users of this VF.
	// We should also reset the dropped information since that is cumulative; there is a bug that doesn't
	// allow clearing the value currently.
	if err = util.GetSriovnetOps().SetRepresentorVFMissPktRate(vfRepName, 0, 0); err != nil {
		klog.Warningf("Failed to reset VF rate limits on representor port %s. %v", vfRepName, err)
	}

	// remove from br-int
	return wait.PollImmediate(500*time.Millisecond, 60*time.Second, func() (bool, error) {
		_, _, err := util.RunOVSVsctl("--if-exists", "del-port", "br-int", vfRepName)
		if err != nil {
			return false, nil
		}
		klog.Infof("Port %s deleted from bridge br-int", vfRepName)
		return true, nil
	})
}

func (nc *ovnNodeController) updateNADConfig(key string, newConfig *util.NadConfig) error {
	enableChecker := false
	klog.Infof("DoS check : updating for %s", key)
	val, found := nc.nadInfo.NetAttachDefs.Load(key)
	if !found {
		return fmt.Errorf("NadConfig %s not found in cache", key)
	}
	oldConfig := val.(*util.NadConfig)
	oldConfig.Lock()
	// Enabling Rate limit for this NAD, check if the controller has the checker running
	if oldConfig.MaxNewConnPPS == 0 && newConfig.MaxNewConnPPS > 0 {
		enableChecker = true
	}
	oldConfig.MaxNewConnBurst = newConfig.MaxNewConnBurst
	oldConfig.MaxNewConnPPS = newConfig.MaxNewConnPPS
	oldConfig.HostTypes = newConfig.HostTypes
	oldConfig.DisableDoSCheck = newConfig.DisableDoSCheck
	oldConfig.Unlock()
	if enableChecker {
		nc.enableDoSChecker()
	}
	return nil
}

func (nc *ovnNodeController) updateRateLimitingForPod(pod *kapi.Pod, nadName string) error {
	// acquire a lock per pod to avoid racing on `servedCache` in pod watcher
	unlock := util.LockByKey.Acquire(string(pod.UID))
	defer unlock()
	val, ok := nc.servedPods.Load(pod.UID)
	if !ok {
		klog.V(5).Infof("DPUConnectionDetails for pod %s/%s not found in cache, skip", pod.Namespace, pod.Name)
		return nil
	}
	connDetails := val.(map[string]*util.DPUConnectionDetails)
	connDetail, ok := connDetails[nadName]
	if !ok {
		klog.V(5).Infof("DPUConnectionDetails for pod %s/%s, net-attach-def %s not found in cache, skip", pod.Namespace, pod.Name, nadName)
		return nil
	}
	var nadConfig *util.NadConfig
	if v, ok := nc.nadInfo.NetAttachDefs.Load(nadName); ok {
		nadConfig = v.(*util.NadConfig)
	} else {
		// Failed to find the per nad configuration
		return fmt.Errorf("failed to find nad configuration for %s", nadName)
	}
	vfRepName := connDetail.ConnPrivateInfo.ConnVFRepName
	maxNewConnPPS, maxNewConnBurst, disableDoSCheck := nadConfig.GetMissRateLimitConfig(nc.node.hostType)
	if !disableDoSCheck && connDetail.ConnPrivateInfo.ConnClampedDown {
		klog.V(5).Infof("Skip setting limit for VF representor %s/%s/%s on NAD %s since it is clamped down", pod.Namespace, pod.Name, vfRepName, nadName)
		return nil
	}
	err := util.GetSriovnetOps().SetRepresentorVFMissPktRate(vfRepName, maxNewConnPPS, maxNewConnBurst)
	if err != nil {
		return fmt.Errorf("failed to update Rate limiting (%d/%d) for interface %s: %v", maxNewConnPPS, maxNewConnBurst, vfRepName, err)
	}
	// Disable doscheck, and lift the clampdown, if needed.
	if connDetail.ConnPrivateInfo.MissRateDoSCheck && disableDoSCheck {
		connDetail.ConnPrivateInfo.MissRateDoSCheck = false
		if connDetail.ConnPrivateInfo.ConnClampedDown {
			connDetail.ConnPrivateInfo.ConnClampedDown = false
			connStatus := util.DPUConnectionStatus{Status: util.DPUConnectionStatusReady, Reason: ""}
			err = nc.updatePodDPUConnStatusWithRetry(nc.node.Kube, pod, &connStatus, nadName)
			if err != nil {
				klog.Errorf("Failed to update connection status annotation the pod %s/%s: %v", pod.Namespace, pod.Name, err)
			}
		}
		// Enable doscheck, if needed.
	} else if !disableDoSCheck && !connDetail.ConnPrivateInfo.MissRateDoSCheck {
		// Collect the drop statistics so we can initialize it. We could do it when the
		// rep was added regardless of disableDoSCheck and use that to determine if the
		// interface needs to be clamped, but given we enable disableDoSCheck here, we might
		// want to start accounting from now. Note, if we fail to get drop count we'll
		// flag an error and not set doSCheck, otherwise if the drop count is non-0,
		// we'll clamp down the interface right away.
		if connDetail.ConnPrivateInfo.MissRateLimitDropInitial, err = util.GetSriovnetOps().GetRepresentorVFMissPktDrops(vfRepName); err != nil {
			klog.Errorf("Failed to get initial Miss RL drops for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		} else {
			connDetail.ConnPrivateInfo.MissRateDoSCheck = true
			klog.Infof("DoS: Initial Drop limit for VF representor %s for %s/%s: %v", vfRepName, pod.Namespace, pod.Name, connDetail.ConnPrivateInfo.MissRateLimitDropInitial)
		}
	}
	klog.V(4).Infof("Rate limit of %s/%s/%s updated to %v/%v based on NAD %s", pod.Namespace, pod.Name, vfRepName, maxNewConnPPS, maxNewConnBurst, nadName)
	return nil
}

// Caller has lock on the interested pod
// Walk the pods and get the pod with the interested uid
func (nc *ovnNodeController) getPodforUID(uid types.UID) (*kapi.Pod, error) {
	// informer cache has pods filtered by node name
	pods, err := nc.node.watchFactory.GetAllPods()
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %v", err)
	}
	for _, pod := range pods {
		if pod.UID == uid {
			return pod, nil
		}
	}
	return nil, fmt.Errorf("failed to get pod with uid %v: %v", uid, err)
}

// Check the pods served by this controller, and if the drop count has incremented
// clampdown the VF (LinkSetDown makes sense, but we could get some mileage with
// clampdown, which will still let offloaded traffic to contine, so it is not
// completely stopping the interface).
func (nc *ovnNodeController) checkPodForDoS(uid types.UID, connDetails map[string]*util.DPUConnectionDetails) {
	for nadName, dpuCD := range connDetails {
		if !dpuCD.ConnPrivateInfo.MissRateDoSCheck || dpuCD.ConnPrivateInfo.ConnClampedDown {
			continue
		}
		vfRepName := dpuCD.ConnPrivateInfo.ConnVFRepName
		newDrop, err := util.GetSriovnetOps().GetRepresentorVFMissPktDrops(vfRepName)
		if err != nil {
			klog.Errorf("Failed to get drop Count for representor %s for dpuConnDetail %+v.", vfRepName, dpuCD, err)
			continue
		}
		prevDrop := dpuCD.ConnPrivateInfo.MissRateLimitDropInitial
		// DoS Suspect, clampdown the VF. Alternatively, we can bring down the interface or
		// do something more drastic, this is a simple first step, and we can improve on
		// this as we have more experience.
		if newDrop > prevDrop {
			klog.V(5).Infof("DoS: Drop VF representor %s: old (%v); current(%v)", vfRepName, prevDrop, newDrop)

			// Get the corresponding pod to update it's connection status; slightly inefficient, but
			// this is an infrequent operation.
			// In case of failure, just log an error, no point failing.
			pod, err := nc.getPodforUID(uid)
			if err != nil {
				klog.Errorf("Failed to find pod with %v to update connection status: %v", uid, err)
				continue
			}
			klog.V(5).Infof("Clamping down Limit to 1/1 for VF representor %s", vfRepName)
			// We can do this only if it not already clampeddown (based on annotation), but if the
			// value is somehow reset outside this service, there'll be a mismatch, so we can just
			// do this even if it may be redundant. We could get the value and check, but might
			// be easier just to set it, regardless.
			// set the VF rate limit configured for this network. This rate is for the allowed no. of new connections.
			if err = util.GetSriovnetOps().SetRepresentorVFMissPktRate(vfRepName, ClampdownDoSRate, ClampdownDoSBurst); err != nil {
				klog.Errorf("Failed to Clamp down rate for Representor %s for dpuConnDetail %+v.", vfRepName, dpuCD, err)
				continue
			}
			dpuCD.ConnPrivateInfo.ConnClampedDown = true
			connStatus := util.DPUConnectionStatus{Status: util.DPUConnectionStatusClampedDown, Reason: ""}
			err = nc.updatePodDPUConnStatusWithRetry(nc.node.Kube, pod, &connStatus, nadName)
			// If this  fails, then the rate is already adjusted, so there'll be a mismatch.
			if err != nil {
				klog.Errorf("Failed to update connection status annotation the pod %s/%s: %v", pod.Namespace, pod.Name, err)
			}
		}
	}
}

// XXX-Check in the context of concurrent delete in servedPods
//
// Range does not necessarily correspond to any consistent snapshot of the Map's contents:
// no key will be visited more than once, but if the value for any key is stored or deleted
// concurrently (including by f), Range may reflect any mapping for that key from any point
// during the Range call. Range does not block other methods on the receiver; even f itself
// may call any method on m.

// Alternatively, we could do this per-node; go thru pods and walk thru all the controllers;
// that seems a bit inefficient.

func (nc *ovnNodeController) checkforDoSSuspects() {
	nc.servedPods.Range(func(key, val interface{}) bool {
		podUID := key.(types.UID)
		unlock := util.LockByKey.Acquire(string(podUID))
		connDetails := val.(map[string]*util.DPUConnectionDetails)
		nc.checkPodForDoS(podUID, connDetails)
		unlock()
		return true
	})
}
