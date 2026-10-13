// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dpu

import (
	"context"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/tracing"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

// Special reserved rate/burst a representor is clamped to once it is flagged as
// a DoS suspect. Offloaded traffic keeps flowing; only new connection misses are
// throttled to the minimum.
const (
	clampdownDoSRate  uint = 1
	clampdownDoSBurst uint = 1
)

// applyRateLimit sets the miss-rate limit of a representor from its NAD config
// and returns the connection status to report for it: Ready, or ClampedDown when
// the pod is already clamped for this NAD. It also arms the DoS checker state on
// the representor. A NAD with no config (including the default network) is left
// unlimited and reported Ready.
func (c *Controller) applyRateLimit(pod *corev1.Pod, state *dpuConnectionState, nadKey string) (*util.DPUConnectionStatus, error) {
	status := &util.DPUConnectionStatus{Status: util.DPUConnectionStatusReady}
	// Leave the representor alone until some NAD defines a real limit, so with no
	// configuration the representor paths behave exactly as upstream.
	if !c.rateLimitingActive() {
		return status, nil
	}
	nadConf := c.getNADConfig(nadKey)
	if nadConf == nil {
		return status, nil
	}

	vfRepName := state.vfRepName
	maxNewConnPPS, maxNewConnBurst, disableDoSCheck := nadConf.GetMissRateLimitConfig(c.hostType)
	if maxNewConnPPS > 0 && !disableDoSCheck {
		state.missRateDoSCheck = true
		// Use the pod annotation, not the live VF value, to tell whether this NAD
		// is already clamped down: a DPU reboot loses the VF configuration.
		if util.IsNadForPodClampedDown(pod.Annotations, nadKey) {
			maxNewConnPPS = clampdownDoSRate
			maxNewConnBurst = clampdownDoSBurst
			state.connClampedDown = true
			status = &util.DPUConnectionStatus{Status: util.DPUConnectionStatusClampedDown}
		} else {
			// Record the drop count so the checker can tell new drops apart.
			drops, err := util.GetSriovnetOps().GetRepresentorVFMissPktDrops(vfRepName)
			if err != nil {
				return nil, fmt.Errorf("failed to get initial miss-rate drops for representor %s: %w", vfRepName, err)
			}
			state.missRateLimitDropInitial = drops
		}
	}

	klog.Infof("Setting miss-rate limit %d/%d for representor %s (pod %s/%s NAD %s)",
		maxNewConnPPS, maxNewConnBurst, vfRepName, pod.Namespace, pod.Name, nadKey)
	if err := util.GetSriovnetOps().SetRepresentorVFMissPktRate(vfRepName, maxNewConnPPS, maxNewConnBurst); err != nil {
		return nil, fmt.Errorf("failed to set miss-rate limit for representor %s: %w", vfRepName, err)
	}
	return status, nil
}

// resetRepPortRateLimit clears the miss-rate limit of a representor being torn
// down so it does not carry over to the next user of the VF. Best effort.
func resetRepPortRateLimit(vfRepName string) {
	if err := util.GetSriovnetOps().SetRepresentorVFMissPktRate(vfRepName, 0, 0); err != nil {
		klog.Warningf("Failed to reset miss-rate limit on representor port %s: %v", vfRepName, err)
	}
}

// getNADConfig returns the cached config of a NAD, keyed by the NAD name
// (namespace/name), which is also the pod's NAD key for the single-attachment
// case this supports. It is nil for a NAD with no cached config, including the
// default network, which has no NAD resource. Rate limiting a NAD attached more
// than once to the same pod is not supported (SDN-3998).
func (c *Controller) getNADConfig(nadKey string) *util.NADConfig {
	c.dosMu.Lock()
	defer c.dosMu.Unlock()
	return c.nadConfigs[nadKey]
}

// reconcileNADRateLimit refreshes the cached config for a NAD and re-applies the
// resulting miss-rate limit to the pods already plumbed for it.
func (c *Controller) reconcileNADRateLimit(nadName string) {
	namespace, name, err := cache.SplitMetaNamespaceKey(nadName)
	if err != nil {
		klog.Errorf("DPU miss-rate limit: cannot parse NAD key %s: %v", nadName, err)
		return
	}

	var nadConf *util.NADConfig
	nad, err := c.wf.NADInformer().Lister().NetworkAttachmentDefinitions(namespace).Get(name)
	switch {
	case err == nil:
		if nadConf, err = util.GetNADConfig(nad); err != nil {
			klog.Errorf("DPU miss-rate limit: cannot parse config of NAD %s: %v", nadName, err)
			return
		}
	case apierrors.IsNotFound(err):
		// NAD deleted; leave nadConf nil to drop its config.
	default:
		klog.Errorf("DPU miss-rate limit: cannot get NAD %s: %v", nadName, err)
		return
	}

	// Re-apply to live representors only when the NAD's rate-limit config actually
	// changed; unrelated NAD updates must not churn the representors.
	if c.updateNADConfig(nadName, nadConf) {
		c.updateRateLimitingForPods(nadName)
	}
}

// rateLimitingActive reports whether any NAD currently defines a real miss-rate
// limit, which arms the representor rate-limit and DoS paths. It clears again
// once every such limit is removed.
func (c *Controller) rateLimitingActive() bool {
	c.dosMu.Lock()
	defer c.dosMu.Unlock()
	return c.totalMaxNewConnPPS > 0
}

// rateLimitEver reports whether a real miss-rate limit has ever been active on
// this controller. Teardown uses it rather than rateLimitingActive so a VF that
// was limited or clamped is still scrubbed after the feature goes dormant, while
// a controller that never saw a limit touches no VF (identical to the feature
// being absent).
func (c *Controller) rateLimitEver() bool {
	c.dosMu.Lock()
	defer c.dosMu.Unlock()
	return c.rateLimitEverActive
}

// updateNADConfig stores (or drops, when nadConf is nil) a NAD's config, keeps
// the running total of configured rate, and starts or stops the DoS checker
// accordingly. It reports whether the NAD's miss-rate-limit config changed, so
// the caller can decide to re-apply it to live representors.
func (c *Controller) updateNADConfig(nadName string, nadConf *util.NADConfig) bool {
	c.dosMu.Lock()
	old := c.nadConfigs[nadName]
	changed := rateLimitConfigChanged(old, nadConf)
	if old != nil {
		c.totalMaxNewConnPPS -= old.MaxNewConnPPS
	}
	if nadConf != nil {
		c.nadConfigs[nadName] = nadConf
		c.totalMaxNewConnPPS += nadConf.MaxNewConnPPS
	} else {
		delete(c.nadConfigs, nadName)
	}
	if c.totalMaxNewConnPPS > 0 {
		c.rateLimitEverActive = true
	}
	c.dosMu.Unlock()

	// enableDoSChecker and disableDoSChecker each no-op unless the total crossed
	// their threshold, so calling both here is safe.
	c.enableDoSChecker()
	c.disableDoSChecker()
	return changed
}

// rateLimitConfigChanged reports whether the miss-rate-limit part of a NAD config
// differs between two revisions. A nil config counts as the zero config.
func rateLimitConfigChanged(oldConf, newConf *util.NADConfig) bool {
	var oldRL, newRL util.MissRateLimitConfig
	if oldConf != nil {
		oldRL = oldConf.MissRateLimitConfig
	}
	if newConf != nil {
		newRL = newConf.MissRateLimitConfig
	}
	return !reflect.DeepEqual(oldRL, newRL)
}

// updateRateLimitingForPods re-applies the miss-rate limit to every pod plumbed
// for nadName, after the NAD's config changed.
func (c *Controller) updateRateLimitingForPods(nadName string) {
	for _, podKey := range c.nadPods.pods(nadName) {
		_ = c.podStates.DoWithLock(podKey, func(key string) error {
			c.updateRateLimitingForPod(key, nadName)
			return nil
		})
	}
}

// updateRateLimitingForPod re-applies the miss-rate limit to each tracked NAD of
// the pod that belongs to nadName. Unlike applyRateLimit it keys off the live
// clamp state rather than the pod annotation, lifting a clamp when the DoS check
// is disabled and arming it when newly enabled.
// The podStates lock for podKey must be held by the caller.
func (c *Controller) updateRateLimitingForPod(podKey, nadName string) {
	ps, ok := c.podStates.Load(podKey)
	if !ok {
		return
	}
	nadConf := c.getNADConfig(nadName)
	if nadConf == nil {
		return
	}

	var pod *corev1.Pod
	for nadKey, state := range ps.nadStates {
		if nadKey != nadName {
			continue
		}
		maxNewConnPPS, maxNewConnBurst, disableDoSCheck := nadConf.GetMissRateLimitConfig(c.hostType)
		if !disableDoSCheck && state.connClampedDown {
			klog.V(5).Infof("Skip miss-rate update for clamped representor %s (pod %s NAD %s)", state.vfRepName, podKey, nadKey)
			continue
		}
		if err := util.GetSriovnetOps().SetRepresentorVFMissPktRate(state.vfRepName, maxNewConnPPS, maxNewConnBurst); err != nil {
			klog.Errorf("Failed to update miss-rate limit (%d/%d) for representor %s (pod %s NAD %s): %v",
				maxNewConnPPS, maxNewConnBurst, state.vfRepName, podKey, nadKey, err)
			continue
		}
		switch {
		case state.missRateDoSCheck && disableDoSCheck:
			state.missRateDoSCheck = false
			if state.connClampedDown {
				state.connClampedDown = false
				c.setPodNADStatus(podKey, nadKey, &pod, util.DPUConnectionStatusReady)
			}
		case !disableDoSCheck && !state.missRateDoSCheck:
			drops, err := util.GetSriovnetOps().GetRepresentorVFMissPktDrops(state.vfRepName)
			if err != nil {
				klog.Errorf("Failed to get initial miss-rate drops for pod %s NAD %s: %v", podKey, nadKey, err)
			} else {
				state.missRateLimitDropInitial = drops
				state.missRateDoSCheck = true
			}
		}
		klog.V(4).Infof("Miss-rate limit of representor %s (pod %s NAD %s) updated to %d/%d",
			state.vfRepName, podKey, nadKey, maxNewConnPPS, maxNewConnBurst)
	}
}

// enableDoSChecker starts the DoS checker goroutine when any NAD has a rate
// configured and it is not already running.
func (c *Controller) enableDoSChecker() {
	c.dosMu.Lock()
	defer c.dosMu.Unlock()
	if c.totalMaxNewConnPPS == 0 || c.dosStopCh != nil {
		return
	}
	klog.Infof("Starting DPU DoS checker")
	stopCh := make(chan struct{})
	c.dosStopCh = stopCh
	c.dosWg.Add(1)
	go c.runDoSChecker(stopCh)
}

// disableDoSChecker stops the DoS checker goroutine once no NAD has a rate
// configured.
func (c *Controller) disableDoSChecker() {
	c.dosMu.Lock()
	defer c.dosMu.Unlock()
	if c.totalMaxNewConnPPS == 0 && c.dosStopCh != nil {
		klog.Infof("Stopping DPU DoS checker")
		close(c.dosStopCh)
		c.dosStopCh = nil
	}
}

// stopDoSChecker stops the DoS checker goroutine, if running, and waits for it to
// exit. Used on controller shutdown.
func (c *Controller) stopDoSChecker() {
	c.dosMu.Lock()
	if c.dosStopCh != nil {
		close(c.dosStopCh)
		c.dosStopCh = nil
	}
	c.dosMu.Unlock()
	c.dosWg.Wait()
}

func (c *Controller) runDoSChecker(stopCh chan struct{}) {
	defer c.dosWg.Done()
	timer := time.NewTicker(time.Duration(config.Default.DoSCheckInterval) * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			c.checkforDoSSuspects()
		case <-stopCh:
			return
		}
	}
}

// checkforDoSSuspects walks the pods this DPU tracks and clamps the representors
// whose miss-drop counter has risen since it was last recorded.
func (c *Controller) checkforDoSSuspects() {
	for _, podKey := range c.podStates.GetKeys() {
		_ = c.podStates.DoWithLock(podKey, func(key string) error {
			ps, ok := c.podStates.Load(key)
			if !ok {
				return nil
			}
			c.checkPodForDoS(key, ps)
			return nil
		})
	}
}

// checkPodForDoS clamps every watched representor of a pod whose miss-drop count
// has risen. Clamping keeps offloaded traffic flowing while throttling new
// connection misses, a lighter touch than bringing the interface down.
// The podStates lock for podKey must be held by the caller.
func (c *Controller) checkPodForDoS(podKey string, ps *podDPUState) {
	var pod *corev1.Pod
	for nadKey, state := range ps.nadStates {
		if !state.missRateDoSCheck || state.connClampedDown {
			continue
		}
		newDrop, err := util.GetSriovnetOps().GetRepresentorVFMissPktDrops(state.vfRepName)
		if err != nil {
			klog.Errorf("DoS: failed to get drop count for representor %s (pod %s NAD %s): %v", state.vfRepName, podKey, nadKey, err)
			continue
		}
		if newDrop <= state.missRateLimitDropInitial {
			continue
		}
		klog.V(5).Infof("DoS: representor %s drops rose from %d to %d, clamping down (pod %s NAD %s)",
			state.vfRepName, state.missRateLimitDropInitial, newDrop, podKey, nadKey)
		if err := util.GetSriovnetOps().SetRepresentorVFMissPktRate(state.vfRepName, clampdownDoSRate, clampdownDoSBurst); err != nil {
			klog.Errorf("DoS: failed to clamp down representor %s (pod %s NAD %s): %v", state.vfRepName, podKey, nadKey, err)
			continue
		}
		state.connClampedDown = true
		c.setPodNADStatus(podKey, nadKey, &pod, util.DPUConnectionStatusClampedDown)
	}
}

// setPodNADStatus writes a single NAD's connection status on the pod, fetching
// the pod lazily through the shared pointer so repeated calls reuse it.
func (c *Controller) setPodNADStatus(podKey, nadKey string, pod **corev1.Pod, status string) {
	if *pod == nil {
		p, err := c.getPod(podKey)
		if err != nil {
			klog.Errorf("Failed to get pod %s to update connection status: %v", podKey, err)
			return
		}
		*pod = p
	}
	statusMap := map[string]*util.DPUConnectionStatus{nadKey: {Status: status}}
	// The DoS checker is a background task outside any pod-reconcile trace, so
	// this status write is not instrumented.
	ctx := tracing.ContextWithSpansDisabled(context.Background())
	if err := util.UpdatePodDPUConnStatusWithRetry(ctx, c.podLister, c.kube, *pod, statusMap); err != nil &&
		!util.IsAnnotationAlreadySetError(err) {
		klog.Errorf("Failed to update connection status for pod %s NAD %s: %v", podKey, nadKey, err)
	}
}

func (c *Controller) getPod(podKey string) (*corev1.Pod, error) {
	namespace, name, err := cache.SplitMetaNamespaceKey(podKey)
	if err != nil {
		return nil, err
	}
	return c.podLister.Pods(namespace).Get(name)
}
