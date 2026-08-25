// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cni

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

// wait on a certain pod annotation related condition
// return error to abort the retry attempt
type podAnnotWaitCond = func(*corev1.Pod, string) (*util.PodAnnotation, bool, error)

// isOvnReady is a wait condition for the pod-networks annotation to be set.
func isOvnReady(pod *corev1.Pod, nadKey string) (*util.PodAnnotation, bool, error) {
	podNADAnnotation, err := util.UnmarshalPodAnnotation(pod.Annotations, nadKey)
	if err != nil {
		if util.IsAnnotationNotSetError(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return podNADAnnotation, true, nil
}

// isDPUReady is a wait condition for the pod-networks annotation and the DPU
// connection-status pod annotation to be set to Ready.
func isDPUReady(annotCondFn podAnnotWaitCond, nadKey string) podAnnotWaitCond {
	return func(pod *corev1.Pod, nad string) (annotation *util.PodAnnotation, ready bool, err error) {
		if annotCondFn != nil {
			annotation, ready, err = annotCondFn(pod, nad)
			if err != nil || !ready {
				return
			}
		}
		// check DPU connection status of the given nad name
		status, err := util.UnmarshalPodDPUConnStatus(pod.Annotations, nadKey)
		if err != nil {
			if util.IsAnnotationNotSetError(err) {
				return annotation, false, nil
			}
			return annotation, false, err
		}
		if status.Status == util.DPUConnectionStatusReady || status.Status == util.DPUConnectionStatusClampedDown {
			return annotation, true, nil
		}
		return annotation, false, fmt.Errorf("DPU plumb failed: connection status is %v", status.Status)
	}
}

// getPod tries to read a Pod object from the informer cache, or if the pod
// doesn't exist there, the apiserver. If neither a list or a kube client is
// given, returns no pod and no error
func (c *ClientSet) getPod(namespace, name string) (*corev1.Pod, error) {
	var pod *corev1.Pod
	var err error

	pod, err = c.podLister.Pods(namespace).Get(name)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}

	if pod == nil {
		// If the pod wasn't in our local cache, ask for it directly
		pod, err = c.kclient.CoreV1().Pods(namespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			// unlike the lister, the typed client returns a non-nil empty
			// Pod alongside the error; return nil explicitly so callers
			// that tolerate IsNotFound (cmdDel of a force-deleted pod)
			// can rely on pod != nil
			return nil, err
		}
	}
	return pod, nil
}

// rateLimitedGetPod reads a Pod object from the informer cache. On a cache miss,
// and only when useAPIServer is true, it falls back to a live read from the
// apiserver. liveFetched reports whether the apiserver was queried, so that
// callers can bound the number of live reads they do and avoid hammering the
// apiserver when a pod is slow to appear in the cache.
func (c *ClientSet) rateLimitedGetPod(namespace, name string, useAPIServer bool) (pod *corev1.Pod, liveFetched bool, err error) {
	pod, err = c.podLister.Pods(namespace).Get(name)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, false, err
	}
	if pod != nil {
		return pod, false, nil
	}

	if !useAPIServer {
		// Cache miss and the live-fetch budget is spent; surface the lister's
		// NotFound without touching the apiserver.
		return nil, false, err
	}

	// The pod wasn't in our local cache, ask the apiserver for it directly.
	// This counts as a live fetch whether or not the pod is found there.
	pod, err = c.kclient.CoreV1().Pods(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return nil, true, err
	}
	return pod, true, nil
}

// GetPodAnnotations obtains the pod UID and annotation from the cache or apiserver
func GetPodWithAnnotations(ctx context.Context, getter PodInfoGetter,
	namespace, name, nadName string, annotCond podAnnotWaitCond) (*corev1.Pod, map[string]string, *util.PodAnnotation, error) {
	var notFoundCount uint
	var liveFetchCount uint
	// Cap the number of live apiserver reads so that a pod which is slow to
	// appear in the local informer cache does not make us hammer the apiserver
	// on every retry. After this many live fetches we rely solely on the cache.
	const maxLiveFetches uint = 5

	for {
		select {
		case <-ctx.Done():
			detail := "timed out"
			if ctx.Err() == context.Canceled {
				detail = "canceled while"
			}
			return nil, nil, nil, fmt.Errorf("%s waiting for annotations: %w", detail, ctx.Err())
		default:
			useAPIServer := liveFetchCount < maxLiveFetches
			pod, liveFetched, err := getter.rateLimitedGetPod(namespace, name, useAPIServer)
			if liveFetched {
				liveFetchCount++
			}
			if err != nil {
				if !apierrors.IsNotFound(err) {
					return nil, nil, nil, fmt.Errorf("failed to get pod for annotations: %v", err)
				}
				if liveFetched {
					// A live read is authoritative: the apiserver itself does
					// not have the pod. Fail right away, kubelet will retry the CNI ADD if needed.
					return nil, nil, nil, fmt.Errorf("pod %s/%s not found in the apiserver: %v", namespace, name, err)
				}
				// Cache-only miss (live-fetch budget spent): the pod isn't in
				// the informer cache, because the cache is lagging or the pod
				// was deleted. Tolerate ~4s of this, then give up so kubelet
				// retries rather than holding the request until ctx expires.
				notFoundCount++
				if notFoundCount >= 20 {
					return nil, nil, nil, fmt.Errorf("timed out waiting for pod %s/%s to appear in the informer cache", namespace, name)
				}
				// drop through to try again
			} else if pod != nil {
				podNADAnnotation, ready, err := annotCond(pod, nadName)
				if err != nil {
					return nil, nil, nil, err
				} else if ready {
					return pod, pod.Annotations, podNADAnnotation, nil
				}
			}

			// try again later
			time.Sleep(200 * time.Millisecond)
		}
	}
}

// PodAnnotation2PodInfo creates PodInterfaceInfo from Pod annotations and additional attributes
func PodAnnotation2PodInfo(podAnnotation map[string]string, podNADAnnotation *util.PodAnnotation, podUID,
	netdevname, nadKey, netName string) (*PodInterfaceInfo, error) {
	var err error
	// get pod's annotation of the given NAD if it is not available
	if podNADAnnotation == nil {
		podNADAnnotation, err = util.UnmarshalPodAnnotation(podAnnotation, nadKey)
		if err != nil {
			return nil, err
		}
	}
	ingress, err := extractPodBandwidth(podAnnotation, Ingress)
	if err != nil && !errors.Is(err, BandwidthNotFound) {
		return nil, err
	}
	egress, err := extractPodBandwidth(podAnnotation, Egress)
	if err != nil && !errors.Is(err, BandwidthNotFound) {
		return nil, err
	}

	podInterfaceInfo := &PodInterfaceInfo{
		PodAnnotation:        *podNADAnnotation,
		RoutableMTU:          config.Default.RoutableMTU,
		Ingress:              ingress,
		Egress:               egress,
		IsDPUHostMode:        config.IsModeDPUHost(),
		PodUID:               podUID,
		NetdevName:           netdevname,
		NetName:              netName,
		NADKey:               nadKey,
		EnableUDPAggregation: config.Default.EnableUDPAggregation,
		OvnKubeMode:          config.OvnKubeNode.Mode,
	}
	return podInterfaceInfo, nil
}

// GetPodIfNamesForNAD gets the pod's all interface names of the given secondary NAD name
//
// Note that the names of secondary UDN pod interfaces are determined by multus: it is either specified by
// network selection itself, or it is net<index> (<index> is determined by order of the pod interface,
// started from 1 for the 1st non-default interface).
func GetPodIfNamesForNAD(pod *corev1.Pod, nadName string) ([]string, error) {
	networks, err := util.GetK8sPodAllNetworkSelections(pod)
	if err != nil {
		return nil, fmt.Errorf("failed to getting network selection elements for pod %s/%s: %v",
			pod.Namespace, pod.Name, err)
	}
	ifNames := make([]string, 0, len(networks))
	for idx, network := range networks {
		nad := util.GetNADName(network.Namespace, network.Name)
		if nad != nadName {
			continue
		}
		if network.InterfaceRequest != "" {
			ifNames = append(ifNames, network.InterfaceRequest)
		} else {
			ifNames = append(ifNames, fmt.Sprintf("net%d", idx+1))
		}
	}
	return ifNames, nil
}

// GetCNINADKey gets the pod's nadKey (nadName with index in case there are multiple same NADs in the pod)
// Based on the given ifName, find out which number of this CNI request is for the given nadName, then
// determine its associated NAD key.
func GetCNINADKey(pod *corev1.Pod, ifName, nadName string) (string, error) {
	ifNames, err := GetPodIfNamesForNAD(pod, nadName)
	if err != nil {
		return "", err
	}
	for idx, name := range ifNames {
		if ifName == name {
			return util.GetIndexedNADKey(nadName, idx), nil
		}
	}
	return "", fmt.Errorf("failed to find NAD key associated with CNI request for pod %s/%s with ifName %s",
		pod.Namespace, pod.Name, ifName)
}

// START taken from https://github.com/kubernetes/kubernetes/blob/master/pkg/kubelet/types/pod_update.go
const (
	ConfigSourceAnnotationKey = "kubernetes.io/config.source"
	// ApiserverSource identifies updates from Kubernetes API Server.
	ApiserverSource = "api"
)

// GetPodSource returns the source of the pod based on the annotation.
func GetPodSource(pod *corev1.Pod) (string, error) {
	if pod.Annotations != nil {
		if source, ok := pod.Annotations[ConfigSourceAnnotationKey]; ok {
			return source, nil
		}
	}
	return "", fmt.Errorf("cannot get source of pod %q", pod.UID)
}

// IsStaticPod returns true if the pod is a static pod.
func IsStaticPod(pod *corev1.Pod) bool {
	source, err := GetPodSource(pod)
	return err == nil && source != ApiserverSource
}

//END taken from https://github.com/kubernetes/kubernetes/blob/master/pkg/kubelet/types/pod_update.go
