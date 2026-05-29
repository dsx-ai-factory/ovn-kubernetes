// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/cni"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/node/controllers/nadconfig"
	ovntypes "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
	utilerrors "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util/errors"
)

// Check if the Pod is ready so that we can add its associated DPU to br-int.
// If true, return its dpuConnDetails, otherwise return nil
func (bnnc *BaseNodeNetworkController) podReadyToAddDPU(pod *corev1.Pod, nadKey string) *util.DPUConnectionDetails {
	if bnnc.name != pod.Spec.NodeName {
		klog.V(5).Infof("Pod %s/%s is not scheduled on this node %s", pod.Namespace, pod.Name, bnnc.name)
		return nil
	}

	dpuCD, err := util.UnmarshalPodDPUConnDetails(pod.Annotations, nadKey)
	if err != nil {
		if !util.IsAnnotationNotSetError(err) {
			klog.Errorf("Failed to get DPU annotation for pod %s/%s NAD %s: %v",
				pod.Namespace, pod.Name, nadKey, err)
		} else {
			klog.V(5).Infof("DPU connection details annotation still not found for %s/%s for NAD %s",
				pod.Namespace, pod.Name, nadKey)
		}
		return nil
	}

	// Get the `pfMAC` from the pod annotation, see if this pfMAC belongs to this DPU
	for _, pfMAC := range bnnc.pfMACs {
		if pfMAC == dpuCD.PfMAC {
			return dpuCD
		}
	}
	klog.V(5).Infof("Pod %s/%s on NAD %s is not associated with this dpu", pod.Namespace, pod.Name, nadKey)
	return nil
}

func (bnnc *BaseNodeNetworkController) addDPUPodForNAD(pod *corev1.Pod, dpuCD *util.DPUConnectionDetails,
	netName, nadKey string, getter cni.PodInfoGetter) error {
	podDesc := fmt.Sprintf("pod %s/%s for NAD %s", pod.Namespace, pod.Name, nadKey)
	klog.Infof("Adding %s on DPU", podDesc)
	podInterfaceInfo, err := cni.PodAnnotation2PodInfo(pod.Annotations, nil,
		string(pod.UID), "", bnnc.GetAnnotationKey(nadKey), netName)
	if err != nil {
		return fmt.Errorf("failed to get pod interface information of %s: %v. retrying", podDesc, err)
	}
	err = bnnc.addRepPort(pod, dpuCD, nadKey, podInterfaceInfo, getter)
	if err != nil {
		return fmt.Errorf("failed to add rep port for %s, %v. retrying", podDesc, err)
	}
	return nil
}

func (bnnc *BaseNodeNetworkController) delDPUPodForNAD(pod *corev1.Pod, dpuCD *util.DPUConnectionDetails, nadKey string, podDeleted bool) error {
	var errs []error
	podDesc := fmt.Sprintf("pod %s/%s for NAD %s", pod.Namespace, pod.Name, nadKey)
	klog.Infof("Deleting %s from DPU", podDesc)

	// no need to unset connection status annotation if pod is deleted anyway
	if !podDeleted {
		err := bnnc.updatePodDPUConnStatusWithRetry(pod, nil, bnnc.GetAnnotationKey(nadKey))
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to remove the old DPU connection status annotation for %s: %v", podDesc, err))
		}
	}
	vfRepName, err := util.GetDPUOps().GetPortRepresentor(dpuCD.PfId, dpuCD.VfId)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to get old VF representor for %s, dpuConnDetail %+v Representor port may have been deleted: %v", podDesc, dpuCD, err))
	} else {
		err = bnnc.delRepPort(pod, dpuCD, vfRepName, nadKey)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to delete VF representor for %s: %v", podDesc, err))
		}
	}
	return utilerrors.Join(errs...)
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
func (bnnc *BaseNodeNetworkController) watchPodsDPU() (*factory.Handler, error) {
	clientSet := cni.NewClientSet(bnnc.client, corev1listers.NewPodLister(bnnc.watchFactory.LocalPodInformer().GetIndexer()))

	netName := bnnc.GetNetworkName()
	return bnnc.watchFactory.AddPodHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			var activeNetwork util.NetInfo
			var err error

			pod := obj.(*corev1.Pod)
			if util.PodWantsHostNetwork(pod) {
				return
			}

			// lock pod to avoid racing on `servedCache`
			unlock := util.LockByKey.Acquire(string(pod.UID))
			defer unlock()
			// add all the Pod's NADs into Pod's nadToDPUCDMap
			// For default network, NAD name is DefaultNetworkName.
			nadToDPUCDMap := map[string]*util.DPUConnectionDetails{}
			if bnnc.IsPrimaryNetwork() {
				activeNetwork, err = bnnc.networkManager.GetActiveNetworkForNamespace(pod.Namespace)
				if err != nil {
					klog.Errorf("Failed looking for the active network for namespace %s: %v", pod.Namespace, err)
					return
				}
				if activeNetwork == nil {
					klog.Errorf("Unable to find an active network for namespace %s", pod.Namespace)
					return
				}
				if activeNetwork.GetNetworkName() != netName {
					return
				}
			}
			on, networkMap, err := util.GetPodNADToNetworkMappingWithActiveNetwork(
				pod,
				bnnc.GetNetInfo(),
				activeNetwork,
				bnnc.networkManager.GetNetworkNameForNADKey,
				bnnc.networkManager.GetPrimaryNADForNamespace,
			)
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

			klog.V(5).Infof("Add for Pod: %s/%s for network %s", pod.Namespace, pod.Name, netName)
			for nadKey := range networkMap {
				nadToDPUCDMap[nadKey] = nil
			}
			if !bnnc.IsUserDefinedNetwork() && len(nadToDPUCDMap) == 0 {
				nadToDPUCDMap[ovntypes.DefaultNetworkName] = nil
			}

			for nadKey := range nadToDPUCDMap {
				dpuCD := bnnc.podReadyToAddDPU(pod, bnnc.GetAnnotationKey(nadKey))
				if dpuCD != nil {
					err = bnnc.addDPUPodForNAD(pod, dpuCD, netName, nadKey, clientSet)
					if err != nil {
						klog.Errorf("Error adding pod %s/%s for for network %s: %v", pod.Namespace, pod.Name, bnnc.GetNetworkName(), err)
					} else {
						nadToDPUCDMap[nadKey] = dpuCD
					}
				}
			}
			bnnc.podNADToDPUCDMap.Store(pod.UID, nadToDPUCDMap)
		},
		UpdateFunc: func(old, newer interface{}) {
			oldPod := old.(*corev1.Pod)
			newPod := newer.(*corev1.Pod)
			if util.PodWantsHostNetwork(newPod) {
				return
			}
			// lock pod to avoid racing on `podNADToDPUCDMap`
			unlock := util.LockByKey.Acquire(string(oldPod.UID))
			defer unlock()
			v, ok := bnnc.podNADToDPUCDMap.Load(newPod.UID)
			if !ok {
				klog.V(6).Infof("Skipping update for Pod %s/%s as it is not attached to network: %s",
					newPod.Namespace, newPod.Name, netName)
				return
			}
			klog.V(5).Infof("Update for Pod: %s/%s for network %s", newPod.Namespace, newPod.Name, netName)
			nadToDPUCDMap := v.(map[string]*util.DPUConnectionDetails)
			for nadKey := range nadToDPUCDMap {
				oldDPUCD := nadToDPUCDMap[nadKey]
				newDPUCD := bnnc.podReadyToAddDPU(newPod, bnnc.GetAnnotationKey(nadKey))
				if !dpuConnectionDetailChanged(oldDPUCD, newDPUCD) {
					continue
				}
				if oldDPUCD != nil {
					// VF already added, but new Pod has changed, we'd need to delete the old VF
					klog.Infof("Deleting the old VF since either kubelet issued cmdDEL or assigned a new VF or "+
						"the sandbox id itself changed. Old connection details (%v), New connection details (%v)",
						oldDPUCD, newDPUCD)
					err := bnnc.delDPUPodForNAD(oldPod, oldDPUCD, nadKey, false)
					if err != nil {
						klog.Errorf("Error deleting pod %s/%s for for network %s: %v", oldPod.Namespace, oldPod.Name, bnnc.GetNetworkName(), err)
					}
					nadToDPUCDMap[nadKey] = nil
				}
				if newDPUCD != nil {
					klog.Infof("Adding VF during update because either during Pod Add we failed to add VF or "+
						"connection details weren't present or the VF ID has changed. Old connection details (%v), "+
						"New connection details (%v)", oldDPUCD, newDPUCD)
					err := bnnc.addDPUPodForNAD(newPod, newDPUCD, netName, nadKey, clientSet)
					if err != nil {
						klog.Errorf("Error adding pod %s/%s for for network %s: %v", newPod.Namespace, newPod.Name, bnnc.GetNetworkName(), err)
					} else {
						nadToDPUCDMap[nadKey] = newDPUCD
					}
				}
			}
			bnnc.podNADToDPUCDMap.Store(newPod.UID, nadToDPUCDMap)
		},
		DeleteFunc: func(obj interface{}) {
			pod := obj.(*corev1.Pod)
			// lock pod to avoid racing on `podNADToDPUCDMap`
			unlock := util.LockByKey.Acquire(string(pod.UID))
			defer unlock()
			v, ok := bnnc.podNADToDPUCDMap.Load(pod.UID)
			if !ok {
				klog.V(6).Infof("Skipping delete for Pod %s/%s as it is not attached to network: %s",
					pod.Namespace, pod.Name, netName)
				return
			}
			klog.V(5).Infof("Delete for Pod: %s/%s for network %s", pod.Namespace, pod.Name, netName)
			nadToDPUCDMap := v.(map[string]*util.DPUConnectionDetails)
			bnnc.podNADToDPUCDMap.Delete(pod.UID)
			for nadKey, dpuCD := range nadToDPUCDMap {
				if dpuCD != nil {
					err := bnnc.delDPUPodForNAD(pod, dpuCD, nadKey, true)
					if err != nil {
						klog.Errorf("Error deleting pod %s/%s for for network %s: %v", pod.Namespace, pod.Name, bnnc.GetNetworkName(), err)
					}
				}
			}
		},
	}, nil)
}

// updatePodDPUConnStatusWithRetry update the pod annotion with the givin connection details
func (bnnc *BaseNodeNetworkController) updatePodDPUConnStatusWithRetry(origPod *corev1.Pod,
	dpuConnStatus *util.DPUConnectionStatus, nadKey string) error {
	podDesc := fmt.Sprintf("pod %s/%s", origPod.Namespace, origPod.Name)
	klog.Infof("Updating pod %s with connection status (%+v) for NAD %s", podDesc, dpuConnStatus, nadKey)
	err := util.UpdatePodDPUConnStatusWithRetry(
		bnnc.watchFactory.PodCoreInformer().Lister(),
		bnnc.Kube,
		origPod,
		dpuConnStatus,
		nadKey,
	)
	return err
}

// addRepPort adds the representor of the VF to the ovs bridge, nadKey is the real NAD key even for the default network
func (bnnc *BaseNodeNetworkController) addRepPort(pod *corev1.Pod, dpuCD *util.DPUConnectionDetails, nadKey string,
	ifInfo *cni.PodInterfaceInfo, getter cni.PodInfoGetter) error {

	podDesc := fmt.Sprintf("pod %s/%s for NAD %s", pod.Namespace, pod.Name, nadKey)
	vfRepName, err := util.GetDPUOps().GetPortRepresentor(dpuCD.PfId, dpuCD.VfId)
	if err != nil {
		klog.Infof("Failed to get VF representor for %s dpuConnDetail %+v: %v", podDesc, dpuCD, err)
		return err
	}

	dpuCD.ConnPrivateInfo.ConnVFRepName = vfRepName

	// set netdevName so OVS interface can be added with external_ids:netdev-name, and is able to
	// be part of healthcheck.
	ifInfo.NetdevName = vfRepName
	deviceID, err := util.GetDPUOps().GetDeviceAddress(vfRepName)
	if err != nil {
		klog.Infof("Failed to get PCI address of VF rep %s: %v", vfRepName, err)
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	klog.Infof("Adding VF representor %s for %s", vfRepName, podDesc)
	defer cancel()
	err = cni.ConfigureOVS(ctx, pod.Namespace, pod.Name, "", vfRepName, ifInfo, dpuCD.SandboxId,
		deviceID, false, getter)
	if err != nil {
		// Note(adrianc): we are lenient with cleanup in this method as pod is going to be retried anyway.
		_ = bnnc.delRepPort(pod, dpuCD, vfRepName, nadKey)
		return err
	}
	klog.Infof("Port %s added to bridge br-int", vfRepName)

	// set the Pod interface's MAC address on the corresponding VF Port
	err = util.GetSriovnetOps().SetRepresentorPeerMacAddress(vfRepName, ifInfo.MAC)
	if err != nil {
		_ = bnnc.delRepPort(pod, dpuCD, vfRepName, nadKey)
		return fmt.Errorf("failed to set the MAC address %s on VF reprentor %s: %v",
			ifInfo.MAC.String(), vfRepName, err)
	}

	// Update connection-status annotation
	// TODO(adrianc): we should update Status in case of error as well
	connStatus := util.DPUConnectionStatus{Status: util.DPUConnectionStatusReady, Reason: ""}
	nadConf, ok := bnnc.getNADConfig(nadKey)
	if ok && nadConf != nil {
		maxNewConnPPS, maxNewConnBurst, disableDoSCheck := nadConf.GetMissRateLimitConfig(bnnc.hostType)
		if maxNewConnPPS > 0 && !disableDoSCheck {
			dpuCD.ConnPrivateInfo.MissRateDoSCheck = true
			//
			// We use the Pod annotation to see if it is clamped down for this NAD instead of checking the existing
			// value on the VF. Reason being if the DPU reboots, we'll lose the VF configuration so we can't rely
			// on that.
			nadClampedDown := util.IsNadForPodClampedDown(pod.Annotations, bnnc.GetAnnotationKey(nadKey))
			if nadClampedDown {
				maxNewConnPPS = ClampdownDoSRate
				maxNewConnBurst = ClampdownDoSBurst
				dpuCD.ConnPrivateInfo.ConnClampedDown = true
				connStatus = util.DPUConnectionStatus{Status: util.DPUConnectionStatusClampedDown, Reason: ""}
			} else {
				// Collect the drop statistics so we can initialize it.
				if dpuCD.ConnPrivateInfo.MissRateLimitDropInitial, err = util.GetSriovnetOps().GetRepresentorVFMissPktDrops(vfRepName); err != nil {
					_ = bnnc.delRepPort(pod, dpuCD, vfRepName, nadKey)
					return fmt.Errorf("failed to get initial Miss RL drops for %s dpuConnDetail +%v: %v", podDesc, dpuCD, err)
				} else {
					klog.V(5).Infof("DoS: Initial Drop limit for VF representor %s for %s: %v", vfRepName, podDesc, dpuCD.ConnPrivateInfo.MissRateLimitDropInitial)
				}
			}
		}
		klog.Infof("Adding Limit %v/%v for VF representor %s for %s", maxNewConnPPS, maxNewConnBurst, vfRepName, podDesc)
		// set the VF rate limit configured for this network. This rate is for the allowed no. of new connections.
		if err = util.GetSriovnetOps().SetRepresentorVFMissPktRate(vfRepName, maxNewConnPPS, maxNewConnBurst); err != nil {
			_ = bnnc.delRepPort(pod, dpuCD, vfRepName, nadKey)
			return fmt.Errorf("failed to setup Rate limiting  for interface %s: %v", vfRepName, err)
		}
	}
	err = bnnc.updatePodDPUConnStatusWithRetry(pod, &connStatus, bnnc.GetAnnotationKey(nadKey))
	if err != nil {
		_ = bnnc.delRepPort(pod, dpuCD, vfRepName, nadKey)
		return fmt.Errorf("failed to setup representor port. failed to set pod annotations. %v", err)
	}
	return nil
}

// delRepPort delete the representor of the VF from the ovs bridge
func (bnnc *BaseNodeNetworkController) delRepPort(pod *corev1.Pod, dpuCD *util.DPUConnectionDetails, vfRepName, nadKey string) error {
	//TODO(adrianc): handle: clearPodBandwidth(pr.SandboxID), pr.deletePodConntrack()
	podDesc := fmt.Sprintf("pod %s/%s for NAD %s", pod.Namespace, pod.Name, nadKey)
	klog.Infof("Delete VF representor %s for %s", vfRepName, podDesc)
	ifExists, sandbox, expectedNADKey, err := util.GetOVSPortPodInfo(vfRepName)
	if err != nil {
		return err
	}
	if !ifExists {
		klog.Infof("VF representor %s for %s is not an OVS interface, nothing to do", vfRepName, podDesc)
		return nil
	}
	if sandbox != dpuCD.SandboxId {
		return fmt.Errorf("OVS port %s was added for sandbox (%s), expecting (%s)", vfRepName, sandbox, dpuCD.SandboxId)
	}
	if expectedNADKey != bnnc.GetAnnotationKey(nadKey) {
		return fmt.Errorf("OVS port %s was added for NAD key (%s), expecting (%s)", vfRepName, expectedNADKey, nadKey)
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
	return wait.PollUntilContextTimeout(context.Background(), 500*time.Millisecond, 60*time.Second, true, func(_ context.Context) (bool, error) {
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

// updateRateLimitingForPod updates per-NAD rate limiting configuration, nadKey is the real NAD key even even for default network
func (bnnc *BaseNodeNetworkController) updateRateLimitingForPod(pod *corev1.Pod, nadKey string) error {
	// acquire a lock per pod to avoid racing on `servedCache` in pod watcher
	unlock := util.LockByKey.Acquire(string(pod.UID))
	defer unlock()
	val, ok := bnnc.podNADToDPUCDMap.Load(pod.UID)
	if !ok {
		klog.V(5).Infof("DPUConnectionDetails for pod %s/%s not found in cache, skip", pod.Namespace, pod.Name)
		return nil
	}
	nadToDPUCDMap := val.(map[string]*util.DPUConnectionDetails)
	dpuCD, ok := nadToDPUCDMap[nadKey]
	if !ok || dpuCD == nil {
		klog.V(5).Infof("DPUConnectionDetails for pod %s/%s, net-attach-def %s not found in cache, skip", pod.Namespace, pod.Name, nadKey)
		return nil
	}
	nadConf, ok := bnnc.getNADConfig(nadKey)
	if !ok || nadConf == nil {
		klog.V(5).Infof("NAD config not found in cache: %s, skip", nadKey)
		return nil
	}
	vfRepName := dpuCD.ConnPrivateInfo.ConnVFRepName
	maxNewConnPPS, maxNewConnBurst, disableDoSCheck := nadConf.GetMissRateLimitConfig(bnnc.hostType)
	if !disableDoSCheck && dpuCD.ConnPrivateInfo.ConnClampedDown {
		klog.V(5).Infof("Skip setting limit for VF representor %s/%s/%s on NAD %s since it is clamped down", pod.Namespace, pod.Name, vfRepName, nadKey)
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
			err = bnnc.updatePodDPUConnStatusWithRetry(pod, &connStatus, bnnc.GetAnnotationKey(nadKey))
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
	klog.V(4).Infof("Rate limit of %s/%s/%s updated to %v/%v based on NAD %s", pod.Namespace, pod.Name, vfRepName, maxNewConnPPS, maxNewConnBurst, nadKey)
	return nil
}

// Caller has lock on the interested pod
// Walk the pods and get the pod with the interested uid
func (bnnc *BaseNodeNetworkController) getPodforUID(uid types.UID) (*corev1.Pod, error) {
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
func (bnnc *BaseNodeNetworkController) checkPodForDoS(uid types.UID, nadToDPUCDMap map[string]*util.DPUConnectionDetails) {
	for nadKey, dpuCD := range nadToDPUCDMap {
		if dpuCD == nil {
			continue
		}
		if !dpuCD.ConnPrivateInfo.MissRateDoSCheck || dpuCD.ConnPrivateInfo.ConnClampedDown {
			continue
		}
		vfRepName := dpuCD.ConnPrivateInfo.ConnVFRepName
		newDrop, err := util.GetSriovnetOps().GetRepresentorVFMissPktDrops(vfRepName)
		if err != nil {
			klog.Errorf("Failed to get drop Count for representor %s for dpuConnDetail %+v: %v", vfRepName, dpuCD, err)
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
				klog.Errorf("Failed to Clamp down rate for Representor %s for dpuConnDetail %+v: %v", vfRepName, dpuCD, err)
				continue
			}
			dpuCD.ConnPrivateInfo.ConnClampedDown = true
			connStatus := util.DPUConnectionStatus{Status: util.DPUConnectionStatusClampedDown, Reason: ""}
			err = bnnc.updatePodDPUConnStatusWithRetry(pod, &connStatus, bnnc.GetAnnotationKey(nadKey))
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
		nadToDPUCDMap := val.(map[string]*util.DPUConnectionDetails)
		bnnc.checkPodForDoS(podUID, nadToDPUCDMap)
		unlock()
		return true
	})
}

// go through pods to update rate limit config
func (bnnc *BaseNodeNetworkController) updateRateLimitingForPods(nadKey string) {
	// informer cache has pods filtered by node name
	pods, err := bnnc.watchFactory.GetAllPods()
	if err != nil {
		klog.Errorf("Failed to list pods: %v", err)
		return
	}
	for _, pod := range pods {
		klog.V(5).Infof("Updating rate limit config for pod %s/%s", pod.Namespace, pod.Name)
		if err := bnnc.updateRateLimitingForPod(pod, nadKey); err != nil {
			klog.Error(err)
		}
	}
}

func (bnnc *BaseNodeNetworkController) SetNADConfig(nadKey string, nadConf *util.NADConfig) error {
	if config.OvnKubeNode.Mode == ovntypes.NodeModeDPU {
		var oldMaxNewConnPPS uint
		oldNADConfig, ok := bnnc.getNADConfig(nadKey)
		if !ok || ((nadConf != nil || oldNADConfig != nil) &&
			(nadConf == nil || oldNADConfig == nil || !reflect.DeepEqual(*oldNADConfig, *nadConf))) {
			bnnc.NADConfigMap.Store(nadKey, nadConf)
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
			bnnc.updateRateLimitingForPods(nadKey)
		}
	}
	return nil
}

func (bnnc *BaseNodeNetworkController) DeleteNAD(nadKey string) {
	if config.OvnKubeNode.Mode == ovntypes.NodeModeDPU {
		v, ok := bnnc.NADConfigMap.Load(nadKey)
		if ok && v != nil {
			nadConfig := v.(*util.NADConfig)
			oldTotalMaxNewConnPPS := bnnc.totalMaxNewConnPPS
			bnnc.totalMaxNewConnPPS -= nadConfig.MaxNewConnPPS
			if oldTotalMaxNewConnPPS > 0 && bnnc.totalMaxNewConnPPS == 0 {
				// TBD: stop rate limiting?
				bnnc.disableDoSChecker()
			}
		}
		bnnc.NADConfigMap.Delete(nadKey)
	}
}

func (bnnc *BaseNodeNetworkController) getNADConfig(nadKey string) (*util.NADConfig, bool) {
	var nadConfig *util.NADConfig
	if config.OvnKubeNode.Mode == ovntypes.NodeModeDPU {
		v, ok := bnnc.NADConfigMap.Load(nadKey)
		if ok {
			if v != nil {
				nadConfig = v.(*util.NADConfig)
			}
		}
		return nadConfig, ok
	}
	return nil, false
}

func (bnnc *BaseNodeNetworkController) startNADController() error {
	if config.OvnKubeNode.Mode == ovntypes.NodeModeDPU {
		var err error
		bnnc.nadConfigController, err = nadconfig.NewController("node-nad-configuration-controller",
			bnnc.watchFactory.NADInformer(), bnnc)
		if err != nil {
			return fmt.Errorf("failed to initialize NAD controller for DPU node: %w", err)
		}

		err = bnnc.nadConfigController.Start()
		if err != nil {
			return fmt.Errorf("failed to start NAD controller for DPU node: %w", err)
		}

		bnnc.enableDoSChecker()
	}
	return nil
}

func (bnnc *BaseNodeNetworkController) stopNADController() {
	if config.OvnKubeNode.Mode == ovntypes.NodeModeDPU {
		bnnc.nadConfigController.Stop()
	}
}
