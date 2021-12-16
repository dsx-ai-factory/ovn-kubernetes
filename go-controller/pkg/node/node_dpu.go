package node

import (
	"context"
	"fmt"
	"sync"
	"time"

	netattchdefapi "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	kapi "k8s.io/api/core/v1"
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
		return nil
	}

	dpuCD, err := util.UnmarshalPodDPUConnDetails(pod.Annotations, nadName)
	if err != nil {
		if !util.IsAnnotationNotSetError(err) {
			klog.Errorf("Failed to get dpu annotation for pod %s/%s nad %s: %v",
				pod.Namespace, pod.Name, nadName, err)
		}
		return nil
	}

	// Get the `pfMAC` from the pod annotation, see if this pfMAC belongs to this DPU
	for _, pfMAC := range pfMACs {
		if pfMAC == dpuCD.PfMAC {
			return dpuCD
		}
	}
	return nil
}

func (nc *ovnNodeController) addDPUPod4Nad(pod *kapi.Pod, dpuCD *util.DPUConnectionDetails, isOvnUpEnabled bool, nadName string,
	podLister corev1listers.PodLister, kclient kubernetes.Interface) error {
	podDesc := fmt.Sprintf("pod %s/%s for nad %s", pod.Namespace, pod.Name, nadName)
	klog.Infof("Adding %s on DPU", podDesc)
	podInterfaceInfo, err := cni.PodAnnotation2PodInfo(pod.Annotations, isOvnUpEnabled, string(pod.UID), "",
		nadName, nc.nadInfo.NetNameInfo)
	if err != nil {
		klog.Infof("Failed to get pod interface information of %s: %v. retrying", podDesc, err)
		return err
	}
	err = nc.addRepPort(pod, dpuCD, podInterfaceInfo, podLister, kclient)
	if err != nil {
		klog.Infof("Failed to add rep port for %s, %v. retrying", podDesc, err)
	}
	return err
}

//watchPodsDPU watch updates for pod dpu annotations
func (nc *ovnNodeController) watchPodsDPU(isOvnUpEnabled bool, pfMACs []string) {
	// servedPods tracks the pods that got a VF
	var servedPods sync.Map
	// podNadCache stores all the net-attach-defs that the given Pod is attached for this controller,
	// we assume that Pod's Network Attachment Selection Annotation will not change over time.
	// key is pod.UUID, value is networkMap
	var podNadCache sync.Map

	n := nc.node
	podLister := corev1listers.NewPodLister(n.watchFactory.LocalPodInformer().GetIndexer())
	kclient := n.Kube.(*kube.Kube)

	nc.podHandler = n.watchFactory.AddPodHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod := obj.(*kapi.Pod)
			klog.Infof("Add for Pod: %s/%s for network %s", pod.ObjectMeta.GetNamespace(), pod.ObjectMeta.GetName(), nc.nadInfo.NetName)
			// Is this pod based on hostNetwork, return directly
			if !util.PodWantsNetwork(pod) {
				return
			}
			on, networkMap, err := util.IsNetworkOnPod(pod, nc.nadInfo)
			if err != nil || !on {
				// the Pod is not attached to this specific network
				klog.V(5).Infof("Pod %s/%s is not attached to this network: %s", pod.Namespace, pod.Name, nc.nadInfo.NetName)
				return
			}
			// add all the Pod's Nad into Pod's podNadCache
			podNadCache.Store(pod.UID, networkMap)

			// initialize serverCache to be empty
			servedCache := map[string]bool{}
			for nadName := range networkMap {
				dpuCD := nc.podReadyToAddDPU(pod, nadName, pfMACs)
				if dpuCD != nil {
					err = nc.addDPUPod4Nad(pod, dpuCD, isOvnUpEnabled, nadName, podLister, kclient.KClient)
					if err == nil {
						servedCache[nadName] = true
					}
				}
			}
			servedPods.Store(pod.UID, servedCache)
		},
		UpdateFunc: func(old, newer interface{}) {
			oldPod := old.(*kapi.Pod)
			newPod := newer.(*kapi.Pod)
			klog.Infof("Update for Pod: %s/%s for network %s", newPod.ObjectMeta.GetNamespace(), newPod.ObjectMeta.GetName(), nc.nadInfo.NetName)
			v, ok := podNadCache.Load(newPod.UID)
			if !ok {
				klog.V(5).Infof("Pod %s/%s is not attached to this network: %s", newPod.Namespace, newPod.Name, nc.nadInfo.NetName)
				return
			}
			networkMap := v.(map[string]*netattchdefapi.NetworkSelectionElement)

			servedCache := map[string]bool{}
			v, ok = servedPods.Load(newPod.UID)
			if ok {
				servedCache = v.(map[string]bool)
			}
			for nadName := range networkMap {
				podDesc := fmt.Sprintf("pod %s/%s for nad %s", newPod.Namespace, newPod.Name, nadName)
				oldDpuCD := nc.podReadyToAddDPU(oldPod, nadName, pfMACs)
				newDpuCD := nc.podReadyToAddDPU(newPod, nadName, pfMACs)
				if oldDpuCD == nil && newDpuCD == nil {
					return
				}
				_, ok := servedCache[nadName]
				if oldDpuCD != nil {
					// VF already added, but new Pod has changed, we'd need to delete the old VF
					if ok && (newDpuCD == nil || oldDpuCD.PfId != newDpuCD.PfId ||
						oldDpuCD.VfId != newDpuCD.VfId || oldDpuCD.SandboxId != newDpuCD.SandboxId) {
						err := nc.updatePodDPUConnStatusWithRetry(nc.node.Kube, oldPod, nil, nadName)
						if err != nil {
							klog.Errorf("Failed to remove the old DPU connection status annotation for %s: %v", podDesc, err)
						}
						vfRepName, err := util.GetSriovnetOps().GetVfRepresentorDPU(oldDpuCD.PfId, oldDpuCD.VfId)
						if err != nil {
							klog.Errorf("Failed to get old VF Representor for %s, dpuConnDetail %+v Representor port may have been deleted", podDesc, oldDpuCD, err)
						} else {
							err = nc.delRepPort(oldDpuCD, vfRepName, nadName, podDesc)
							if err != nil {
								klog.Errorf("Failed to delete VF representor for %s: %v", podDesc, err)
							}
						}
						delete(servedCache, nadName)
					}
				}
				if newDpuCD != nil {
					// if VF was failed to be added before or, if new Pod has changed, we'd need to add the new VF
					if !ok || (oldDpuCD == nil || oldDpuCD.PfId != newDpuCD.PfId ||
						oldDpuCD.VfId != newDpuCD.VfId || oldDpuCD.SandboxId != newDpuCD.SandboxId) {
						err := nc.addDPUPod4Nad(newPod, newDpuCD, isOvnUpEnabled, nadName, podLister, kclient.KClient)
						if err == nil {
							servedCache[nadName] = true
						}
					}
				}
			}
			servedPods.Store(newPod.UID, servedCache)
		},
		DeleteFunc: func(obj interface{}) {
			pod := obj.(*kapi.Pod)
			klog.Infof("Delete for Pod: %s/%s for network %s", pod.ObjectMeta.GetNamespace(), pod.ObjectMeta.GetName(), nc.nadInfo.NetName)
			_, ok := podNadCache.Load(pod.UID)
			if !ok {
				klog.V(5).Infof("Pod %s/%s is not attached to this network: %s", pod.Namespace, pod.Name, nc.nadInfo.NetName)
				return
			}
			podNadCache.Delete(pod.UID)
			v, ok := servedPods.Load(pod.UID)
			if !ok {
				klog.V(5).Infof("Pod %s/%s is not attached to this network: %s", pod.Namespace, pod.Name, nc.nadInfo.NetName)
				return
			}
			servedCache := v.(map[string]bool)
			servedPods.Delete(pod.UID)
			for nadName := range servedCache {
				podDesc := fmt.Sprintf("pod %s/%s for nad %s", pod.Namespace, pod.Name, nadName)
				klog.Infof("Deleling %s from DPU", podDesc)
				dpuCD, err := util.UnmarshalPodDPUConnDetails(pod.Annotations, nadName)
				if err != nil {
					klog.Errorf("Failed to get dpu annotation for %s: %v", podDesc, err)
					continue
				}
				vfRepName, err := util.GetSriovnetOps().GetVfRepresentorDPU(dpuCD.PfId, dpuCD.VfId)
				if err != nil {
					klog.Errorf("Failed to get VF Representor for %s, dpuConnDetail %+v. Representor port may have been deleted", podDesc, dpuCD, err)
					continue
				}
				err = nc.delRepPort(dpuCD, vfRepName, nadName, podDesc)
				if err != nil {
					klog.Errorf("Failed to delete VF representor for %s: %v", podDesc, err)
				}
			}
		},
	}, nil)
}

// updatePodDPUConnStatusWithRetry update the pod annotion with the givin connection details
func (nc *ovnNodeController) updatePodDPUConnStatusWithRetry(kube kube.Interface, origPod *kapi.Pod,
	dpuConnStatus *util.DPUConnectionStatus, nadName string) error {
	podDesc := fmt.Sprintf("pod %s/%s for nad %s", origPod.Namespace, origPod.Name, nadName)
	resultErr := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		pod, err := kube.GetPod(origPod.Namespace, origPod.Name)
		if err != nil {
			return err
		}
		// Informer cache should not be mutated, so get a copy of the object
		cpod := pod.DeepCopy()
		err = util.MarshalPodDPUConnStatus(&cpod.Annotations, dpuConnStatus, nadName)
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

// addRepPort adds the representor of the VF to the ovs bridge
func (nc *ovnNodeController) addRepPort(pod *kapi.Pod, dpuCD *util.DPUConnectionDetails, ifInfo *cni.PodInterfaceInfo, podLister corev1listers.PodLister, kclient kubernetes.Interface) error {
	nadName := ifInfo.NadName
	podDesc := fmt.Sprintf("pod %s/%s for nad %s", pod.Namespace, pod.Name, nadName)
	vfRepName, err := util.GetSriovnetOps().GetVfRepresentorDPU(dpuCD.PfId, dpuCD.VfId)
	if err != nil {
		klog.Infof("Failed to get rep name of %s dpuConnDetail +%v: %v", podDesc, dpuCD, err)
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	klog.Infof("Adding VF representor %s for %s", vfRepName, podDesc)
	err = cni.ConfigureOVS(ctx, pod.Namespace, pod.Name, vfRepName, ifInfo, dpuCD.SandboxId, podLister, kclient)
	if err != nil {
		// Note(adrianc): we are lenient with cleanup in this method as pod is going to be retried anyway.
		_ = nc.delRepPort(dpuCD, vfRepName, nadName, podDesc)
		return err
	}
	klog.Infof("Port %s added to bridge br-int", vfRepName)

	// set the Pod interface's MAC address on the corresponding VF Port
	err = util.GetSriovnetOps().SetRepresentorPeerMacAddress(vfRepName, ifInfo.MAC)
	if err != nil {
		_ = nc.delRepPort(dpuCD, vfRepName, nadName, podDesc)
		return fmt.Errorf("failed to set the MAC address %s on VF reprentor %s: %v",
			ifInfo.MAC.String(), vfRepName, err)
	}

	link, err := util.GetNetLinkOps().LinkByName(vfRepName)
	if err != nil {
		_ = nc.delRepPort(dpuCD, vfRepName, nadName, podDesc)
		return fmt.Errorf("failed to get link device for interface %s: %v", vfRepName, err)
	}

	if err = util.GetNetLinkOps().LinkSetMTU(link, ifInfo.MTU); err != nil {
		_ = nc.delRepPort(dpuCD, vfRepName, nadName, podDesc)
		return fmt.Errorf("failed to setup representor port. failed to set MTU %d for interface %s: %v", ifInfo.MTU, vfRepName, err)
	}

	if err = util.GetNetLinkOps().LinkSetUp(link); err != nil {
		_ = nc.delRepPort(dpuCD, vfRepName, nadName, podDesc)
		return fmt.Errorf("failed to setup representor port. failed to set link up for interface %s: %v", vfRepName, err)
	}

	maxNewConnPPS := config.OvnKubeNode.MaxNewConnPPS
	maxNewConnBurst := config.OvnKubeNode.MaxNewConnBurst
	if nc.nadInfo.IsSecondary {
		maxNewConnPPS = nc.nadInfo.MaxNewConnPPS
		maxNewConnBurst = nc.nadInfo.MaxNewConnBurst
	}
	klog.Infof("Adding Limit %v/%v for VF representor %s for pod %s/%s network %s", maxNewConnPPS, maxNewConnBurst, vfRepName, pod.Namespace, pod.Name, ifInfo.NadName)
	// set the VF rate limit configured for this network. This rate is for the allowed no. of new connections.
	if err = util.GetSriovnetOps().SetRepresentorVFMissPktRate(vfRepName, maxNewConnPPS, maxNewConnBurst); err != nil {
		_ = nc.delRepPort(dpuCD, vfRepName, nadName, podDesc)
		return fmt.Errorf("failed to setup Rate limiting  for interface %s: %v", vfRepName, err)
	}

	// Update connection-status annotation
	// TODO(adrianc): we should update Status in case of error as well
	connStatus := util.DPUConnectionStatus{Status: util.DPUConnectionStatusReady, Reason: ""}
	err = nc.updatePodDPUConnStatusWithRetry(nc.node.Kube, pod, &connStatus, ifInfo.NadName)
	if err != nil {
		_ = util.GetNetLinkOps().LinkSetDown(link)
		_ = nc.delRepPort(dpuCD, vfRepName, nadName, podDesc)
		return fmt.Errorf("failed to setup representor port. failed to set pod annotations. %v", err)
	}
	return nil
}

// delRepPort delete the representor of the VF from the ovs bridge
func (nc *ovnNodeController) delRepPort(dpuCD *util.DPUConnectionDetails, vfRepName, nadName, podDesc string) error {
	//TODO(adrianc): handle: clearPodBandwidth(pr.SandboxID), pr.deletePodConntrack()
	klog.Infof("Deleting VF representor %s for %s", vfRepName, podDesc)
	ifExists, sandbox, networkName, err := util.GetOVSPortPodInfo(vfRepName)
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
	if networkName != nadName {
		return fmt.Errorf("OVS port %s was added for nad (%s), expecting (%s)", vfRepName, networkName, nadName)
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
