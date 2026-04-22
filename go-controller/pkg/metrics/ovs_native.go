// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"fmt"
	"io"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

var (
	RunOvsVswitchdAppCtlMetricsShow = util.RunOvsVswitchdAppCtlMetricsShow
)

// collectOvsNativeMetrics collects the OVS native metrics by running the
// `ovs-appctl metrics/show` command.
func collectOvsNativeMetrics() (io.ReadCloser, error) {
	// TODO: the size of the output of metrics/show could be too large, consider
	// using a pipe to read the output and running `ovs-appctl metrics/show` non-blocking.
	stdout, stderr, err := util.RunOvsVswitchdAppCtlMetricsShow()
	if err != nil {
		return nil, fmt.Errorf("failed to run metrics/show: error %v; stderr: %s", err, stderr)
	}

	return io.NopCloser(stdout), nil
}

func registerOvsInterfaceExtraMetrics(registry prometheus.Registerer, metricNamespace, metricSubsystem string) {
	for InterfaceMetricName, InterfaceMetricInfo := range ovsInterfaceExtraMetricsDataMap {
		InterfaceMetricInfo.metric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: metricSubsystem,
			Name:      InterfaceMetricName,
			Help:      InterfaceMetricInfo.help,
		},
			[]string{
				"bridge",
				"port",
				// Note that OVS native metrics uses "name" label instead of "interface"
				// If some nodes still emit legacy ovs interface metrics with "interface" label,
				// prometheus config can be tuned to rename it to "name" to simplify the PromQL queries
				// used in dashboard and alerting rules.
				"name",
			})
		registry.MustRegister(InterfaceMetricInfo.metric)
	}
	ovsInterfaceMetricsDataMap = ovsInterfaceExtraMetricsDataMap
}

var registerOvsNativeMetricsOnce sync.Once

func RegisterAdditionalOvsMetrics(registry prometheus.Registerer) {
	registerOvsNativeMetricsOnce.Do(func() {
		registry.MustRegister(metricOvsVersion)

		// Register OVS datapath metrics.
		registry.MustRegister(metricOvsDpTotal)
		registry.MustRegister(metricOvsDp)
		registry.MustRegister(metricOvsDpIfTotal)
		registry.MustRegister(metricOvsDpIf)
		registry.MustRegister(metricOvsDpMasksHitRatio)
		registry.MustRegister(metricOvsDpOffloadedFlowsTotal)

		// Register OVS HW offload metrics
		registry.MustRegister(metricOvsHwOffload)
		registry.MustRegister(metricOvsTcPolicy)
		// Register OVS Interface metrics
		registerOvsInterfaceExtraMetrics(registry, types.MetricOvsNamespace, types.MetricOvsSubsystemVswitchd)

		registry.MustRegister(MetricOvsInterfaceUpWait)
		// Register the OVS coverage/show metrics
		componentCoverageShowMetricsMap[ovsVswitchd] = ovsVswitchdCoverageShowMetricsMap
		registerCoverageShowMetrics(registry, ovsVswitchd, types.MetricOvsNamespace, types.MetricOvsSubsystemVswitchd)

		// When ovnkube-node is running in privileged mode, the hostPID will be set to true,
		// and therefore it can monitor OVS running on the host using PID.
		if !config.UnprivilegedMode {
			registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{
				PidFn:     prometheus.NewPidFileFn("/var/run/openvswitch/ovs-vswitchd.pid"),
				Namespace: fmt.Sprintf("%s_%s", types.MetricOvsNamespace, types.MetricOvsSubsystemVswitchd),
			}))
			registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{
				PidFn:     prometheus.NewPidFileFn("/var/run/openvswitch/ovsdb-server.pid"),
				Namespace: fmt.Sprintf("%s_%s", types.MetricOvsNamespace, types.MetricOvsSubsystemOvsDB),
			}))
		}

	})
}
