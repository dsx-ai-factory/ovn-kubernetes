package node

import (
	"context"
	"fmt"
	"reflect"
	"time"

	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	apierrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

// Check if the Pod is ready so that we can add its associated DPU to br-int.
// If true, return its dpuConnDetails, otherwise return nil
func (bnnc *BaseNodeNetworkController) podReadyToAddDPU(pod *kapi.Pod, nadName string) *util.DPUConnectionDetails {
	if bnnc.name != pod.Spec.NodeName {
		klog.V(5).Infof("Pod %s/%s is not scheduled on this node %s", pod.Namespace, pod.Name, bnnc.name)
		return nil
	}

	dpuCD, err := util.UnmarshalPodDPUConnDetails(pod.Annotations, nadName)
	if err != nil {
		if !util.IsAnnotationNotSetError(err) {
			klog.Errorf("Failed to get DPU annotation for pod %s/%s NAD %s: %v",
				pod.Namespace, pod.Name, nadName, err)
		} else {
			klog.V(5).Infof("DPU connection details annotation still not found for %s/%s for NAD %s",
				pod.Namespace, pod.Name, nadName)
		}
		return nil
	}

	// Get the `pfMAC` from the pod annotation, see if this pfMAC belongs to this DPU
	for _, pfMAC := range bnnc.pfMACs {
		if pfMAC == dpuCD.PfMAC {
			return dpuCD
		}
	}
	klog.V(5).Infof("Pod %s/%s on NAD %s is not associated with this dpu", pod.Namespace, pod.Name, nadName)
	return nil
}

func (bnnc *BaseNodeNetworkController) addDPUPodForNAD(pod *kapi.Pod, dpuCD *util.DPUConnectionDetails,
	netName, nadName string, getter cni.PodInfoGetter,
	addFunc func(*kapi.Pod, string) (any, error),
	delFunc func(*kapi.Pod, string, any) error) (any, error) {
	podDesc := fmt.Sprintf("pod %s/%s for NAD %s", pod.Namespace, pod.Name, nadName)
	klog.Infof("Adding %s on DPU", podDesc)
	podInterfaceInfo, err := cni.PodAnnotation2PodInfo(pod.Annotations, nil,
		string(pod.UID), "", bnnc.GetAnnotationKey(nadName), netName)
	if err != nil {
		return nil, fmt.Errorf("failed to get pod interface information of %s: %v. retrying", podDesc, err)
	}
	anyInfo, err := bnnc.addRepPort(pod, dpuCD, nadName, podInterfaceInfo, getter, addFunc, delFunc)
	if err != nil {
		return nil, fmt.Errorf("failed to add rep port for %s, %v. retrying", podDesc, err)
	}
	return anyInfo, nil
}

func (bnnc *BaseNodeNetworkController) delDPUPodForNAD(pod *kapi.Pod, dpuCD *util.DPUConnectionDetails, anyInfo any, nadName string,
	podDeleted bool, delFunc func(*kapi.Pod, string, any) error) error {
	var errs []error
	podDesc := fmt.Sprintf("pod %s/%s for NAD %s", pod.Namespace, pod.Name, nadName)
	klog.Infof("Deleting %s from DPU", podDesc)

	if podDeleted {
		// no need to unset connection status annotation if pod is deleted anyway
		err := bnnc.updatePodDPUConnStatusWithRetry(pod, nil, bnnc.GetAnnotationKey(nadName))
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to remove the old DPU connection status annotation for %s: %v", podDesc, err))
		}
	}
	vfRepName, err := util.GetSriovnetOps().GetVfRepresentorDPU(dpuCD.PfId, dpuCD.VfId)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to get old VF representor for %s, dpuConnDetail %+v Representor port may have been deleted: %v", podDesc, dpuCD, err))
	} else {
		err = bnnc.delRepPort(pod, dpuCD, anyInfo, vfRepName, nadName, delFunc)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to delete VF representor for %s: %v", podDesc, err))
		}
	}
	return apierrors.NewAggregate(errs)
}

func dpuConnectionDetailChanged(oldDPUCD, newDPUCD *util.DPUConnectionDetails) bool {
	if oldDPUCD == nil && newDPUCD == nil {
		return false
	}
	if (oldDPUCD != nil && newDPUCD == nil) || (oldDPUCD == nil && newDPUCD != nil) {
		return true
	}
	if oldDPUCD.PfId != newDPUCD.PfId ||
		oldDPUCD.VfId != newDPUCD.VfId || oldDPUCD.SandboxId != newDPUCD.SandboxId {
		return true
	}
	return false
}

// watchPodsDPU watch updates for pod DPU annotations
func (bnnc *BaseNodeNetworkController) watchPodsDPU(addFunc func(*kapi.Pod, string) (any, error),
	delFunc func(*kapi.Pod, string, any) error, updateFunc func(*kapi.Pod, string, any) (any, error)) (*factory.Handler, error) {
	clientSet := cni.NewClientSet(bnnc.client, corev1listers.NewPodLister(bnnc.watchFactory.LocalPodInformer().GetIndexer()))

	netName := bnnc.GetNetworkName()
	return bnnc.watchFactory.AddPodHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod := obj.(*kapi.Pod)
			klog.V(5).Infof("Add for Pod: %s/%s for network %s", pod.Namespace, pod.Name, netName)
			if util.PodWantsHostNetwork(pod) || pod.Status.Phase == kapi.PodRunning {
				return
			}

			// lock pod to avoid racing on `servedCache`
			unlock := util.LockByKey.Acquire(string(pod.UID))
			defer unlock()
			// add all the Pod's NADs into Pod's nadToDPUCDMap
			// For default network, NAD name is DefaultNetworkName.
			var nadToDPUCDMap map[string]*podNADInfo
			on, networkMap, err := util.GetPodNADToNetworkMapping(pod, bnnc.NetInfo)
			if err != nil || !on {
				if err != nil {
					// configuration error, no need to retry, do not return error
					klog.Errorf("Error getting network-attachment for pod %s/%s network %s: %v",
						pod.Namespace, pod.Name, bnnc.GetNetworkName(), err)
				} else {
					klog.V(5).Infof("Skipping Pod %s/%s as it is not attached to network: %s",
						pod.Namespace, pod.Name, netName)
				}
				return
			}
			for nadName := range networkMap {
				nadToDPUCDMap = map[string]*podNADInfo{nadName: {}}
			}
			if !bnnc.IsSecondary() && len(nadToDPUCDMap) == 0 {
				nadToDPUCDMap = map[string]*podNADInfo{ovntypes.DefaultNetworkName: {}}
			}

			for nadName := range nadToDPUCDMap {
				dpuCD := bnnc.podReadyToAddDPU(pod, bnnc.GetAnnotationKey(nadName))
				if dpuCD != nil {
					anyInfo, err := bnnc.addDPUPodForNAD(pod, dpuCD, netName, nadName, clientSet, addFunc, delFunc)
					if err != nil {
						klog.Errorf(err.Error())
					} else {
						nadToDPUCDMap[nadName] = &podNADInfo{dpuCD: dpuCD, anyInfo: anyInfo}
					}
				}
			}
			bnnc.podNADToDPUCDMap.Store(pod.UID, nadToDPUCDMap)
		},
		UpdateFunc: func(old, newer interface{}) {
			oldPod := old.(*kapi.Pod)
			newPod := newer.(*kapi.Pod)
			// lock pod to avoid racing on `servedCache`
			unlock := util.LockByKey.Acquire(string(oldPod.UID))
			defer unlock()
			klog.V(5).Infof("Update for Pod: %s/%s for network %s", newPod.Namespace, newPod.Name, netName)
			v, ok := bnnc.podNADToDPUCDMap.Load(newPod.UID)
			if !ok {
				klog.V(6).Infof("Skipping update for Pod %s/%s as it is not attached to network: %s",
					newPod.Namespace, newPod.Name, netName)
				return
			}
			nadToDPUCDMap := v.(map[string]*podNADInfo)
			for nadName, info := range nadToDPUCDMap {
				oldDPUCD := info.dpuCD
				newDPUCD := bnnc.podReadyToAddDPU(newPod, bnnc.GetAnnotationKey(nadName))
				if !dpuConnectionDetailChanged(oldDPUCD, newDPUCD) {
					// no change in connection Details, but may need to update something else
					if updateFunc != nil {
						var err error
						info.anyInfo, err = updateFunc(newPod, nadName, info.anyInfo)
						if err != nil {
							klog.Errorf(err.Error())
						}
					}
					continue
				}
				if oldDPUCD != nil {
					// VF already added, but new Pod has changed, we'd need to delete the old VF
					klog.Infof("Deleting the old VF since either kubelet issued cmdDEL or assigned a new VF or "+
						"the sandbox id itself changed. Old connection details (%v), New connection details (%v)",
						oldDPUCD, newDPUCD)
					err := bnnc.delDPUPodForNAD(oldPod, oldDPUCD, info.anyInfo, nadName, false, delFunc)
					if err != nil {
						klog.Errorf(err.Error())
					}
					nadToDPUCDMap[nadName] = &podNADInfo{}
				}
				if newDPUCD != nil {
					klog.Infof("Adding VF during update because either during Pod Add we failed to add VF or "+
						"connection details weren't present or the VF ID has changed. Old connection details (%v), "+
						"New connection details (%v)", oldDPUCD, newDPUCD)
					anyInfo, err := bnnc.addDPUPodForNAD(newPod, newDPUCD, netName, nadName, clientSet, addFunc, delFunc)
					if err != nil {
						klog.Errorf(err.Error())
					} else {
						nadToDPUCDMap[nadName] = &podNADInfo{dpuCD: newDPUCD, anyInfo: anyInfo}
					}
				}
			}
			// TBD: when to call updateXDPInterfaceConfig()?
			bnnc.podNADToDPUCDMap.Store(newPod.UID, nadToDPUCDMap)
		},
		DeleteFunc: func(obj interface{}) {
			pod := obj.(*kapi.Pod)
			// lock pod to avoid racing on `servedCache`
			unlock := util.LockByKey.Acquire(string(pod.UID))
			defer unlock()
			v, ok := bnnc.podNADToDPUCDMap.Load(pod.UID)
			if !ok {
				klog.V(5).Infof("Skipping delete for Pod %s/%s as it is not attached to network: %s",
					pod.Namespace, pod.Name, netName)
				return
			}
			klog.V(5).Infof("Delete for Pod: %s/%s for network %s", pod.Namespace, pod.Name, netName)
			nadToDPUCDMap := v.(map[string]*podNADInfo)
			bnnc.podNADToDPUCDMap.Delete(pod.UID)
			for nadName, info := range nadToDPUCDMap {
				if info.dpuCD != nil {
					err := bnnc.delDPUPodForNAD(pod, info.dpuCD, info.anyInfo, nadName, true, delFunc)
					if err != nil {
						klog.Errorf(err.Error())
					}
				}
			}
		},
	}, nil)
}

// updatePodDPUConnStatusWithRetry update the pod annotion with the givin connection details
func (bnnc *BaseNodeNetworkController) updatePodDPUConnStatusWithRetry(origPod *kapi.Pod,
	dpuConnStatus *util.DPUConnectionStatus, nadName string) error {
	podDesc := fmt.Sprintf("pod %s/%s", origPod.Namespace, origPod.Name)
	klog.Infof("Updating pod %s with connection status (%+v) for NAD %s", podDesc, dpuConnStatus, nadName)
	err := util.UpdatePodDPUConnStatusWithRetry(
		bnnc.watchFactory.PodCoreInformer().Lister(),
		bnnc.Kube,
		origPod,
		dpuConnStatus,
		nadName,
	)
	if util.IsAnnotationAlreadySetError(err) {
		return nil
	}

	return err
}

// addRepPort adds the representor of the VF to the ovs bridge, nadName is the real NAD name even for the default network
func (bnnc *BaseNodeNetworkController) addRepPort(pod *kapi.Pod, dpuCD *util.DPUConnectionDetails, nadName string,
	ifInfo *cni.PodInterfaceInfo, getter cni.PodInfoGetter,
	addFunc func(*kapi.Pod, string) (any, error),
	delFunc func(*kapi.Pod, string, any) error) (any, error) {

	podDesc := fmt.Sprintf("pod %s/%s for NAD %s", pod.Namespace, pod.Name, nadName)
	vfRepName, err := util.GetSriovnetOps().GetVfRepresentorDPU(dpuCD.PfId, dpuCD.VfId)
	if err != nil {
		klog.Infof("Failed to get VF representor for %s dpuConnDetail %+v: %v", podDesc, dpuCD, err)
		return nil, err
	}

	dpuCD.ConnPrivateInfo.ConnVFRepName = vfRepName

	// set netdevName so OVS interface can be added with external_ids:netdev-name, and is able to
	// be part of healthcheck.
	ifInfo.NetdevName = vfRepName

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	klog.Infof("Adding VF representor %s for %s", vfRepName, podDesc)
	defer cancel()
	err = cni.ConfigureOVS(ctx, pod.Namespace, pod.Name, vfRepName, ifInfo, dpuCD.SandboxId, getter)
	if err != nil {
		// Note(adrianc): we are lenient with cleanup in this method as pod is going to be retried anyway.
		_ = bnnc.delRepPort(pod, dpuCD, nil, vfRepName, nadName, nil)
		return nil, err
	}
	klog.Infof("Port %s added to bridge br-int", vfRepName)

	// set the Pod interface's MAC address on the corresponding VF Port
	err = util.GetSriovnetOps().SetRepresentorPeerMacAddress(vfRepName, ifInfo.MAC)
	if err != nil {
		_ = bnnc.delRepPort(pod, dpuCD, nil, vfRepName, nadName, nil)
		return nil, fmt.Errorf("failed to set the MAC address %s on VF reprentor %s: %v",
			ifInfo.MAC.String(), vfRepName, err)
	}

	link, err := util.GetNetLinkOps().LinkByName(vfRepName)
	if err != nil {
		_ = bnnc.delRepPort(pod, dpuCD, nil, vfRepName, nadName, nil)
		return nil, fmt.Errorf("failed to get link device for interface %s", vfRepName)
	}

	if err = util.GetNetLinkOps().LinkSetMTU(link, ifInfo.MTU); err != nil {
		_ = bnnc.delRepPort(pod, dpuCD, nil, vfRepName, nadName, nil)
		return nil, fmt.Errorf("failed to setup representor port. failed to set MTU for interface %s", vfRepName)
	}

	if err = util.GetNetLinkOps().LinkSetUp(link); err != nil {
		_ = bnnc.delRepPort(pod, dpuCD, nil, vfRepName, nadName, nil)
		return nil, fmt.Errorf("failed to setup representor port. failed to set link up for interface %s", vfRepName)
	}

	// Update connection-status annotation
	// TODO(adrianc): we should update Status in case of error as well
	connStatus := util.DPUConnectionStatus{Status: util.DPUConnectionStatusReady, Reason: ""}
	nadConf, ok := bnnc.GetNADConfig(nadName)
	if ok && nadConf != nil {
		maxNewConnPPS, maxNewConnBurst, disableDoSCheck := nadConf.GetMissRateLimitConfig(bnnc.hostType)
		if maxNewConnPPS > 0 && !disableDoSCheck {
			dpuCD.ConnPrivateInfo.MissRateDoSCheck = true
			//
			// We use the Pod annotation to see if it is clamped down for this NAD instead of checking the existing
			// value on the VF. Reason being if the DPU reboots, we'll lose the VF configuration so we can't rely
			// on that.
			nadClampedDown := util.IsNadForPodClampedDown(pod.Annotations, bnnc.GetAnnotationKey(nadName))
			if nadClampedDown {
				maxNewConnPPS = ClampdownDoSRate
				maxNewConnBurst = ClampdownDoSBurst
				dpuCD.ConnPrivateInfo.ConnClampedDown = true
				connStatus = util.DPUConnectionStatus{Status: util.DPUConnectionStatusClampedDown, Reason: ""}
			} else {
				// Collect the drop statistics so we can initialize it.
				if dpuCD.ConnPrivateInfo.MissRateLimitDropInitial, err = util.GetSriovnetOps().GetRepresentorVFMissPktDrops(vfRepName); err != nil {
					_ = bnnc.delRepPort(pod, dpuCD, nil, vfRepName, nadName, nil)
					return nil, fmt.Errorf("failed to get initial Miss RL drops for %s dpuConnDetail +%v: %v", podDesc, dpuCD, err)
				} else {
					klog.V(5).Infof("DoS: Initial Drop limit for VF representor %s for %s: %v", vfRepName, podDesc, dpuCD.ConnPrivateInfo.MissRateLimitDropInitial)
				}
			}
		}
		klog.Infof("Adding Limit %v/%v for VF representor %s for %s", maxNewConnPPS, maxNewConnBurst, vfRepName, podDesc)
		// set the VF rate limit configured for this network. This rate is for the allowed no. of new connections.
		if err = util.GetSriovnetOps().SetRepresentorVFMissPktRate(vfRepName, maxNewConnPPS, maxNewConnBurst); err != nil {
			_ = bnnc.delRepPort(pod, dpuCD, nil, vfRepName, nadName, nil)
			return nil, fmt.Errorf("failed to setup Rate limiting  for interface %s: %v", vfRepName, err)
		}
	}
	var anyInfo any
	if addFunc != nil {
		if anyInfo, err = addFunc(pod, nadName); err != nil {
			_ = bnnc.delRepPort(pod, dpuCD, nil, vfRepName, nadName, nil)
			return nil, err
		}
	}
	err = bnnc.updatePodDPUConnStatusWithRetry(pod, &connStatus, bnnc.GetAnnotationKey(nadName))
	if err != nil {
		_ = util.GetNetLinkOps().LinkSetDown(link)
		_ = bnnc.delRepPort(pod, dpuCD, anyInfo, vfRepName, nadName, delFunc)
		return nil, fmt.Errorf("failed to setup representor port. failed to set pod annotations. %v", err)
	}
	return anyInfo, nil
}

// delRepPort delete the representor of the VF from the ovs bridge
func (bnnc *BaseNodeNetworkController) delRepPort(pod *kapi.Pod, dpuCD *util.DPUConnectionDetails, anyInfo any,
	vfRepName, nadName string, delFunc func(*kapi.Pod, string, any) error) error {
	//TODO(adrianc): handle: clearPodBandwidth(pr.SandboxID), pr.deletePodConntrack()
	podDesc := fmt.Sprintf("pod %s/%s for NAD %s", pod.Namespace, pod.Name, nadName)
	klog.Infof("Delete VF representor %s for %s", vfRepName, podDesc)
	ifExists, sandbox, expectedNADName, err := util.GetOVSPortPodInfo(vfRepName)
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
	if expectedNADName != bnnc.GetAnnotationKey(nadName) {
		return fmt.Errorf("OVS port %s was added for NAD (%s), expecting (%s)", vfRepName, expectedNADName, nadName)
	}

	if delFunc != nil {
		err = delFunc(pod, nadName, anyInfo)
		if err != nil {
			return err
		}
	}

	// Set link down for representor port
	link, err := util.GetNetLinkOps().LinkByName(vfRepName)
	if err != nil {
		klog.Warningf("Failed to get link device for representor port %s. %v", vfRepName, err)
	} else {
		if err = util.GetNetLinkOps().LinkSetDown(link); err != nil {
			klog.Warningf("Failed to set link down for representor port %s. %v", vfRepName, err)
		}
		if err = util.GetNetLinkOps().LinkSetMTU(link, config.DefaultVFMTU); err != nil {
			klog.Warningf("Failed to reset the link MTU for representor port %s. %v", vfRepName, err)
		}
	}

	// reset the VF rate limit configured so that it doesn't get carried over to other users of this VF.
	// We should also reset the dropped information since that is cumulative; there is a bug that doesn't
	// allow clearing the value currently.
	if err = util.GetSriovnetOps().SetRepresentorVFMissPktRate(vfRepName, 0, 0); err != nil {
		klog.Warningf("Failed to reset VF rate limits on representor port %s. %v", vfRepName, err)
	}

	// remove from br-int
	return wait.PollUntilContextTimeout(context.Background(), 500*time.Millisecond, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		_, _, err := util.RunOVSVsctl("--if-exists", "del-port", "br-int", vfRepName)
		if err != nil {
			return false, nil
		}
		klog.Infof("Port %s deleted from bridge br-int", vfRepName)
		return true, nil
	})
}

// We'll start a checker when any nad on this controller has PPS limit > 0; but we don't
// disable it when the limits get reset for all nads.. assuming it is possible for that NAD
// to be  configured with the limits again. Primarily to keep the logic simple.
func (bnnc *BaseNodeNetworkController) enableDoSChecker() {
	// Only supported on DPU;
	// Start rate limiting only when it is requested and the controller is started
	if bnnc.totalMaxNewConnPPS == 0 || bnnc.DoSCheckStopChan != nil {
		return
	}
	klog.V(5).Infof("Enabling DoS checker for %s", bnnc.GetNetworkName())
	// Check if we need to start the doscheck thread if this NAD has a limit configured
	klog.Infof("Starting DoS checker for %s", bnnc.GetNetworkName())
	bnnc.DoSCheckStopChan = make(chan struct{})
	bnnc.wg.Add(1)
	go func() {
		defer bnnc.wg.Done()
		timer := time.NewTicker(time.Duration(config.Default.DoSCheckInterval) * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				bnnc.checkforDoSSuspects()
			case <-bnnc.stopChan:
				return
			case <-bnnc.DoSCheckStopChan:
				return
			}
		}
	}()
}

func (bnnc *BaseNodeNetworkController) disableDoSChecker() {
	// Only supported on DPU;
	// Stop rate limiting
	if bnnc.totalMaxNewConnPPS == 0 && bnnc.DoSCheckStopChan != nil {
		klog.V(5).Infof("Disabling DoS checker for %s", bnnc.GetNetworkName())
		close(bnnc.DoSCheckStopChan)
		bnnc.DoSCheckStopChan = nil
	}
}

// updateRateLimitingForPod updates per-NAD rate limiting configuration, nadName is the real NAD name even even for default network
func (bnnc *BaseNodeNetworkController) updateRateLimitingForPod(pod *kapi.Pod, nadName string) error {
	// acquire a lock per pod to avoid racing on `servedCache` in pod watcher
	unlock := util.LockByKey.Acquire(string(pod.UID))
	defer unlock()
	val, ok := bnnc.podNADToDPUCDMap.Load(pod.UID)
	if !ok {
		klog.V(5).Infof("DPUConnectionDetails for pod %s/%s not found in cache, skip", pod.Namespace, pod.Name)
		return nil
	}
	nadToDPUCDMap := val.(map[string]*podNADInfo)
	info, ok := nadToDPUCDMap[nadName]
	if !ok || info.dpuCD == nil {
		klog.V(5).Infof("DPUConnectionDetails for pod %s/%s, net-attach-def %s not found in cache, skip", pod.Namespace, pod.Name, nadName)
		return nil
	}
	nadConf, ok := bnnc.GetNADConfig(nadName)
	if !ok || nadConf == nil {
		klog.V(5).Infof("NAD config not found in cache: %s, skip", nadName)
		return nil
	}
	dpuCD := info.dpuCD
	vfRepName := dpuCD.ConnPrivateInfo.ConnVFRepName
	maxNewConnPPS, maxNewConnBurst, disableDoSCheck := nadConf.GetMissRateLimitConfig(bnnc.hostType)
	if !disableDoSCheck && dpuCD.ConnPrivateInfo.ConnClampedDown {
		klog.V(5).Infof("Skip setting limit for VF representor %s/%s/%s on NAD %s since it is clamped down", pod.Namespace, pod.Name, vfRepName, nadName)
		return nil
	}
	err := util.GetSriovnetOps().SetRepresentorVFMissPktRate(vfRepName, maxNewConnPPS, maxNewConnBurst)
	if err != nil {
		return fmt.Errorf("failed to update Rate limiting (%d/%d) for interface %s: %v", maxNewConnPPS, maxNewConnBurst, vfRepName, err)
	}
	// Disable doscheck, and lift the clampdown, if needed.
	if dpuCD.ConnPrivateInfo.MissRateDoSCheck && disableDoSCheck {
		dpuCD.ConnPrivateInfo.MissRateDoSCheck = false
		if dpuCD.ConnPrivateInfo.ConnClampedDown {
			dpuCD.ConnPrivateInfo.ConnClampedDown = false
			connStatus := util.DPUConnectionStatus{Status: util.DPUConnectionStatusReady, Reason: ""}
			err = bnnc.updatePodDPUConnStatusWithRetry(pod, &connStatus, bnnc.GetAnnotationKey(nadName))
			if err != nil {
				klog.Errorf("Failed to update connection status annotation the pod %s/%s: %v", pod.Namespace, pod.Name, err)
			}
		}
		// Enable doscheck, if needed.
	} else if !disableDoSCheck && !dpuCD.ConnPrivateInfo.MissRateDoSCheck {
		// Collect the drop statistics so we can initialize it. We could do it when the
		// rep was added regardless of disableDoSCheck and use that to determine if the
		// interface needs to be clamped, but given we enable disableDoSCheck here, we might
		// want to start accounting from now. Note, if we fail to get drop count we'll
		// flag an error and not set doSCheck, otherwise if the drop count is non-0,
		// we'll clamp down the interface right away.
		if dpuCD.ConnPrivateInfo.MissRateLimitDropInitial, err = util.GetSriovnetOps().GetRepresentorVFMissPktDrops(vfRepName); err != nil {
			klog.Errorf("Failed to get initial Miss RL drops for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		} else {
			dpuCD.ConnPrivateInfo.MissRateDoSCheck = true
			klog.Infof("DoS: Initial Drop limit for VF representor %s for %s/%s: %v", vfRepName, pod.Namespace, pod.Name, dpuCD.ConnPrivateInfo.MissRateLimitDropInitial)
		}
	}
	klog.V(4).Infof("Rate limit of %s/%s/%s updated to %v/%v based on NAD %s", pod.Namespace, pod.Name, vfRepName, maxNewConnPPS, maxNewConnBurst, nadName)
	return nil
}

// Caller has lock on the interested pod
// Walk the pods and get the pod with the interested uid
func (bnnc *BaseNodeNetworkController) getPodforUID(uid types.UID) (*kapi.Pod, error) {
	// informer cache has pods filtered by node name
	pods, err := bnnc.watchFactory.GetAllPods()
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
func (bnnc *BaseNodeNetworkController) checkPodForDoS(uid types.UID, nadToDPUCDMap map[string]*podNADInfo) {
	for nadName, info := range nadToDPUCDMap {
		dpuCD := info.dpuCD
		if dpuCD == nil {
			continue
		}
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
			pod, err := bnnc.getPodforUID(uid)
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
			err = bnnc.updatePodDPUConnStatusWithRetry(pod, &connStatus, bnnc.GetAnnotationKey(nadName))
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

func (bnnc *BaseNodeNetworkController) checkforDoSSuspects() {
	bnnc.podNADToDPUCDMap.Range(func(key, val interface{}) bool {
		podUID := key.(types.UID)
		unlock := util.LockByKey.Acquire(string(podUID))
		nadToDPUCDMap := val.(map[string]*podNADInfo)
		bnnc.checkPodForDoS(podUID, nadToDPUCDMap)
		unlock()
		return true
	})
}

// go through pods to update rate limit config
func (bnnc *BaseNodeNetworkController) updateRateLimitingForPods(nadName string) {
	// informer cache has pods filtered by node name
	pods, err := bnnc.watchFactory.GetAllPods()
	if err != nil {
		klog.Errorf("Failed to list pods: %v", err)
		return
	}
	for _, pod := range pods {
		klog.V(5).Infof("Updating rate limit config for pod %s/%s", pod.Namespace, pod.Name)
		if err := bnnc.updateRateLimitingForPod(pod, nadName); err != nil {
			klog.Error(err)
		}
	}
}

func (bnnc *BaseNodeNetworkController) AddNAD(nadName string, nadConf *util.NADConfig) {
	if config.OvnKubeNode.Mode == ovntypes.NodeModeDPU {
		var oldMaxNewConnPPS uint
		oldNADConfig, ok := bnnc.GetNADConfig(nadName)
		if !ok || ((nadConf != nil || oldNADConfig != nil) &&
			(nadConf == nil || oldNADConfig == nil || !reflect.DeepEqual(*oldNADConfig, *nadConf))) {
			bnnc.NADConfigMap.Store(nadName, nadConf)
			// Node that NAD update are done serialized, so no locking is needed
			if oldNADConfig != nil {
				oldMaxNewConnPPS = oldNADConfig.MaxNewConnPPS
			}
			bnnc.totalMaxNewConnPPS -= oldMaxNewConnPPS
			if nadConf != nil {
				bnnc.totalMaxNewConnPPS += nadConf.MaxNewConnPPS
			}
			// We'll start a checker when any nad on this controller has PPS limit > 0
			bnnc.enableDoSChecker()
			bnnc.updateRateLimitingForPods(nadName)
		}
	}
	bnnc.NetInfo.AddNAD(nadName, nadConf)
}

func (bnnc *BaseNodeNetworkController) DeleteNAD(nadName string) {
	if config.OvnKubeNode.Mode == ovntypes.NodeModeDPU {
		v, ok := bnnc.NADConfigMap.Load(nadName)
		if ok && v != nil {
			nadConfig := v.(*util.NADConfig)
			oldTotalMaxNewConnPPS := bnnc.totalMaxNewConnPPS
			bnnc.totalMaxNewConnPPS -= nadConfig.MaxNewConnPPS
			if oldTotalMaxNewConnPPS > 0 && bnnc.totalMaxNewConnPPS == 0 {
				// TBD: stop rate limiting?
				bnnc.disableDoSChecker()
			}
		}
		bnnc.NADConfigMap.Delete(nadName)
	}
	bnnc.NetInfo.DeleteNAD(nadName)
}

func (bnnc *BaseNodeNetworkController) GetNADConfig(nadName string) (*util.NADConfig, bool) {
	var nadConfig *util.NADConfig
	if config.OvnKubeNode.Mode == ovntypes.NodeModeDPU {
		v, ok := bnnc.NADConfigMap.Load(nadName)
		if ok {
			if v != nil {
				nadConfig = v.(*util.NADConfig)
			}
		}
		return nadConfig, ok
	}
	return nil, false
}
