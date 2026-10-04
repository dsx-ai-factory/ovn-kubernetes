// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"obs-as-code/pkg/build/dashboard/lib"

	"github.com/grafana/grafana-foundation-sdk/go/bargauge"
	"github.com/grafana/grafana-foundation-sdk/go/cog"
	"github.com/grafana/grafana-foundation-sdk/go/common"
	"github.com/grafana/grafana-foundation-sdk/go/dashboard"
	"github.com/grafana/grafana-foundation-sdk/go/heatmap"
	"github.com/grafana/grafana-foundation-sdk/go/prometheus"
	"github.com/grafana/grafana-foundation-sdk/go/table"
	"github.com/grafana/grafana-foundation-sdk/go/timeseries"
)

// OvnkubeControllerDashboard represents the OVNkube Controller dashboard with interface implementation
type OvnkubeControllerDashboard struct {
	lib.BaseSdnDashboard
}

// NewOvnkubeControllerDashboard creates a new OVNkube Controller dashboard instance
func NewOvnkubeControllerDashboard() *OvnkubeControllerDashboard {
	return &OvnkubeControllerDashboard{
		BaseSdnDashboard: lib.NewSdnDashboard(
			"sdn-ovn-k8s-ic-ovnkube-controller",
			"OVN K8S IC / Ovnkube Controller",
			"This dashboard shows the status of the OVN K8S IC / Ovnkube Controller.",
		),
	}
}

// GetFilename returns the filename for this dashboard
func (d *OvnkubeControllerDashboard) GetFilename() string {
	return d.BaseSdnDashboard.GetFilename()
}

// SetMode sets the build mode for the dashboard
func (d *OvnkubeControllerDashboard) SetMode(mode lib.BuildMode) {
	d.BaseSdnDashboard.SetMode(mode)
}

// GetBaseSdnDashboard returns the embedded base dashboard for accessing common fields
func (d *OvnkubeControllerDashboard) GetBaseSdnDashboard() *lib.BaseSdnDashboard {
	return &d.BaseSdnDashboard
}

// Build implements the interface method to build the actual dashboard
func (d *OvnkubeControllerDashboard) Build() (dashboard.Dashboard, error) {
	d.Builder = dashboard.NewDashboardBuilder(d.Title).
		Uid(d.Uid).
		Tags(d.Tags).
		Editable().
		Time("now-30m", "now").
		Timezone(common.TimeZoneBrowser).
		// Instance variable
		WithVariable(ovnkubeControllerInstanceVariable(d)).
		// Pod variable
		WithVariable(ovnkubeControllerPodVariable(d)).
		// TopK variable
		WithVariable(lib.TopKVariable()).
		// Overview Row
		WithRow(dashboard.NewRowBuilder("Overview").Id(1)).
		// Build Info Table
		WithPanel(ovnkubeControllerBuildInfo(d)).
		// Node Agent Readiness
		WithPanel(ovnkubeControllerNodeAgentReadiness(d)).
		// Node Agent Availability
		WithPanel(ovnkubeControllerNodeAgentAvailability(d)).
		// Container Restart Overview
		WithPanel(ovnkubeControllerContainerRestartOverview(d)).
		// NB-SB E2E Timestamp Difference Overview
		WithPanel(ovnkubeControllerNBSBTimestampDiffOverview(d)).
		// CNI Request Rate Overview
		WithPanel(ovnkubeControllerCNIRequestRateOverview(d)).
		// CNI Request Success Rate Overview
		WithPanel(ovnkubeControllerCNIRequestSuccessRateOverview(d)).
		// CNI CMD ADD Duration Overview
		WithPanel(ovnkubeControllerCniCmdAddDurationOverview(d)).
		// CNI CMD DEL Duration Overview
		WithPanel(ovnkubeControllerCniCmdDelDurationOverview(d)).
		// Pod Creation Duration Overview
		WithPanel(ovnkubeControllerPodCreationDurationOverview(d)).
		// Ovnkube Controller Runtime Row
		WithRow(dashboard.NewRowBuilder("Ovnkube Controller Runtime").Id(2)).
		// CPU Usage
		WithPanel(ovnkubeControllerCPUUsage(d)).
		// Memory Usage
		WithPanel(ovnkubeControllerMemoryUsage(d)).
		// NB-SB E2E Timestamp Difference
		WithPanel(ovnkubeControllerNBSBTimestampDiff(d)).
		// Pod Creation Duration
		WithPanel(ovnkubeControllerPodCreationDuration(d)).
		// CNI Request Rate
		WithPanel(ovnkubeControllerCNIRequestRate(d)).
		// CNI Request Duration
		WithPanel(ovnkubeControllerCNIRequestDuration(d)).
		// CNI Request Success Rate
		WithPanel(ovnkubeControllerCNIRequestSuccessRate(d)).
		WithRow(dashboard.NewRowBuilder("Resource").Id(3).Collapsed(true).
			WithPanel(ovnkubeControllerServiceSyncRate(d)).
			WithPanel(ovnkubeControllerServiceSyncLatency(d)).
			WithPanel(ovnkubeControllerServiceReQueueRate(d)).
			WithPanel(ovnkubeControllerResourceUpdateRate(d))).
		// Workqueue Row
		WithRow(dashboard.NewRowBuilder("Workqueue").Id(4).Collapsed(true).
			WithPanel(ovnkubeControllerWorkQueueDepth(d)).
			WithPanel(ovnkubeControllerWorkQueueAddRate(d)).
			WithPanel(ovnkubeControllerWorkQueueRetryRate(d)).
			WithPanel(ovnkubeControllerWorkQueueProcessingDuration(d)).
			WithPanel(ovnkubeControllerWorkQueuePendingDuration(d)).
			WithPanel(ovnkubeControllerWorkQueueLongestRunningTime(d)).
			WithPanel(ovnkubeControllerWorkQueueUnfinishedWorkTime(d)))

	return d.Builder.Build()
}

// BuildOvnkubeControllerDashboard provides backward compatibility
func BuildOvnkubeControllerDashboard() (*lib.BaseSdnDashboard, error) {
	dashboardBuilder := NewOvnkubeControllerDashboard()
	_, err := dashboardBuilder.Build()
	if err != nil {
		return nil, err
	}
	return &dashboardBuilder.BaseSdnDashboard, nil
}

// Variables
func ovnkubeControllerInstanceVariable(d lib.SdnDashboard) *dashboard.QueryVariableBuilder {
	return dashboard.NewQueryVariableBuilder("instance").
		Label("instance").
		Query(dashboard.StringOrMap{String: cog.ToPtr("label_values(ovnkube_controller_build_info{},instance)")}).
		Datasource(d.DataSourceRef()).
		Current(dashboard.VariableOption{
			Selected: cog.ToPtr(true),
			Text:     dashboard.StringOrArrayOfString{String: cog.ToPtr("All")},
			Value:    dashboard.StringOrArrayOfString{ArrayOfString: []string{"$__all"}},
		}).
		Refresh(dashboard.VariableRefreshOnTimeRangeChanged).
		Sort(dashboard.VariableSortNumericalAsc).
		Multi(true).
		IncludeAll(true).
		AllValue(".*")
}

func ovnkubeControllerPodVariable(d lib.SdnDashboard) *dashboard.QueryVariableBuilder {
	return dashboard.NewQueryVariableBuilder("pod").
		Label("Pod").
		Query(dashboard.StringOrMap{String: cog.ToPtr("label_values(ovnkube_controller_build_info{instance=~\"$instance\"},pod)")}).
		Datasource(d.DataSourceRef()).
		Description("Pods of ovnkube-node.").
		Current(dashboard.VariableOption{
			Selected: cog.ToPtr(true),
			Text:     dashboard.StringOrArrayOfString{String: cog.ToPtr("All")},
			Value:    dashboard.StringOrArrayOfString{ArrayOfString: []string{"$__all"}},
		}).
		Refresh(dashboard.VariableRefreshOnTimeRangeChanged).
		Sort(dashboard.VariableSortAlphabeticalAsc).
		Multi(true).
		IncludeAll(true).
		AllValue(".*")
}

func ovnkubeControllerBuildInfo(d lib.SdnDashboard) *table.PanelBuilder {
	return lib.BuildInfoTable(d, "ovnkube-controller build info.",
		`ovnkube_controller_build_info{namespace="ovn-kubernetes"}`,
		lib.OvnkubeBuildInfoFields(),
	).Span(8)
}

func ovnkubeControllerNodeAgentReadiness(d lib.SdnDashboard) *bargauge.PanelBuilder {
	return lib.NewSdnBarGaugePanelBuilder(d, "Node Agent Readiness", "shows summary of ovnkube-node pods that are ready").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`kube_daemonset_status_number_ready{namespace="ovn-kubernetes",daemonset="ovnkube-node"}`).
				LegendFormat("READY").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`kube_daemonset_status_number_available{namespace="ovn-kubernetes",daemonset="ovnkube-node"} + kube_daemonset_status_number_unavailable{namespace="ovn-kubernetes",daemonset="ovnkube-node"} - kube_daemonset_status_number_ready{namespace="ovn-kubernetes",daemonset="ovnkube-node"}`).
				LegendFormat("NOT READY").
				RefId("B"),
		)
}

func ovnkubeControllerNodeAgentAvailability(d lib.SdnDashboard) *bargauge.PanelBuilder {
	return lib.NewSdnBarGaugePanelBuilder(d, "Node Agent Availability", "The summary of available and unavailable ovnkube-node pods").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`kube_daemonset_status_number_available{namespace="ovn-kubernetes",daemonset="ovnkube-node"}`).
				LegendFormat("AVAILABLE").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`kube_daemonset_status_number_unavailable{namespace="ovn-kubernetes",daemonset="ovnkube-node"}`).
				LegendFormat("UNAVAILABLE").
				RefId("B"),
		)
}

func ovnkubeControllerContainerRestartOverview(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Container ovnkube-controller Restart Overview").
		Description("The number of restarts of the ovnkube-controller container in the last 5 minutes.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Legend(common.NewVizLegendOptionsBuilder().
			DisplayMode(common.LegendDisplayModeHidden).
			ShowLegend(false),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum(increase(kube_pod_container_status_restarts_total{container="ovnkube-controller", namespace="ovn-kubernetes"}[5m]))`).
				RefId("A"),
		)
}

func ovnkubeControllerNBSBTimestampDiffOverview(d lib.SdnDashboard) *heatmap.PanelBuilder {
	return lib.NewSdnHeatmapPanelBuilder(d, "NB-SB E2E Timestamp Difference Overview", "The difference between the NB and SB E2E timestamps on the ovnkube-controller instance.").
		Calculate(true).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`ovnkube_controller_nb_e2e_timestamp{} - ovnkube_controller_sb_e2e_timestamp{}`).
				LegendFormat("__auto").
				Format(prometheus.PromQueryFormatHeatmap).
				Range().
				RefId("A"),
		)
}

func ovnkubeControllerCPUUsage(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.ContainerCpuUsageTimeseries(d, true, "ovnkube-controller", []string{`pod=~"$pod"`})
}

func ovnkubeControllerMemoryUsage(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.ContainerMemoryUsageTimeseries(d, true, "ovnkube-controller", []string{`pod=~"$pod"`})
}

func ovnkubeControllerNBSBTimestampDiff(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("NB-SB E2E Timestamp Difference (Top K)").
		Description("The difference between the NB and SB E2E timestamps on the ovnkube-controller instance.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, (ovnkube_controller_nb_e2e_timestamp{instance=~"$instance", pod=~"$pod"} - ovnkube_controller_sb_e2e_timestamp{instance=~"$instance", pod=~"$pod"}))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnkubeControllerCNIRequestRateOverview(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("CNI Request Rate Overview").
		Description("The number of CNI requests per second cross all ovnkube-controller instances.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("reqps").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum(rate(ovnkube_node_cni_request_duration_seconds_count{command="ADD"}[5m]))`).
				LegendFormat("ADD").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum(rate(ovnkube_node_cni_request_duration_seconds_count{command="DEL"}[5m]))`).
				LegendFormat("DEL").
				RefId("B"),
		)
}

func ovnkubeControllerCNIRequestRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("CNI Request Rate(Top K)").
		Description("The number of CNI requests per second handled by the ovnkube-controller instance.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("reqps").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovnkube_node_cni_request_duration_seconds_count{instance=~"$instance", pod=~"$pod", command="ADD"}[5m]))`).
				LegendFormat("ADD:{{instance}}/{{pod}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovnkube_node_cni_request_duration_seconds_count{instance=~"$instance", pod=~"$pod", command="DEL"}[5m]))`).
				LegendFormat("DEL:{{instance}}/{{pod}}").
				RefId("B"),
		)
}

func ovnkubeControllerCniCmdAddDurationOverview(d lib.SdnDashboard) *heatmap.PanelBuilder {
	return lib.NewSdnHeatmapPanelBuilder(d, "CNI CMD ADD Duration Overview", "The heatmap of the duration of the CNI CMD ADD requests.").
		Unit("s").
		Calculate(false).
		Color(lib.NewSdnHeatmapColor(128)).
		CellValues(heatmap.NewCellValuesBuilder().Unit("rps")).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum by (le) (rate(ovnkube_node_cni_request_duration_seconds_bucket{command="ADD", err="false"}[5m]))`).
				LegendFormat("{{instance}}").
				Format(prometheus.PromQueryFormatHeatmap).
				Range().
				RefId("A"),
		)
}

func ovnkubeControllerCniCmdDelDurationOverview(d lib.SdnDashboard) *heatmap.PanelBuilder {
	return lib.NewSdnHeatmapPanelBuilder(d, "CNI CMD DEL Duration Overview", "The heatmap of the duration of the CNI CMD DEL requests.").
		Unit("s").
		Calculate(false).
		Color(lib.NewSdnHeatmapColor(128)).
		CellValues(heatmap.NewCellValuesBuilder().Unit("rps")).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum by (le) (rate(ovnkube_node_cni_request_duration_seconds_bucket{command="DEL", err="false"}[5m]))`).
				LegendFormat("{{instance}}").
				Format(prometheus.PromQueryFormatHeatmap).
				Range().
				RefId("A"),
		)
}

func ovnkubeControllerCNIRequestDuration(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("CNI Request Duration P99(Top K)").
		Description("The duration of the CNI requests.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("s").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, histogram_quantile(0.99, sum(rate(ovnkube_node_cni_request_duration_seconds_bucket{instance=~"$instance", pod=~"$pod", err="false"}[5m])) by (instance, pod, command, le)))`).
				LegendFormat("{{command}}:{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnkubeControllerCNIRequestSuccessRateOverview(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("CNI Request Success Rate Overview").
		Description("The portion of CNI requests that finished successfully without returning any error cross all ovnkube-controller instances.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("percentunit").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum by (command) (rate(ovnkube_node_cni_request_duration_seconds_count{err="false"}[5m]))/sum by (command) (rate(ovnkube_node_cni_request_duration_seconds_count{}[5m]))`).
				LegendFormat("{{command}}").
				RefId("A"),
		)
}

func ovnkubeControllerCNIRequestSuccessRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("CNI Request Success Rate(Bottom K)").
		Description("The portion of CNI requests that finished successfully without returning any error").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("percentunit").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`bottomk($topk, sum by (instance, pod, command) (rate(ovnkube_node_cni_request_duration_seconds_count{err="false", command="ADD", instance=~"$instance", pod=~"$pod"}[5m]))/sum by (instance, pod, command) (rate(ovnkube_node_cni_request_duration_seconds_count{command="ADD", instance=~"$instance", pod=~"$pod"}[5m])))`).
				LegendFormat("ADD:{{instance}}/{{pod}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`bottomk($topk, sum by (instance, pod, command) (rate(ovnkube_node_cni_request_duration_seconds_count{err="false", command="DEL", instance=~"$instance", pod=~"$pod"}[5m]))/sum by (instance, pod, command) (rate(ovnkube_node_cni_request_duration_seconds_count{command="DEL", instance=~"$instance", pod=~"$pod"}[5m])))`).
				LegendFormat("DEL:{{instance}}/{{pod}}").
				RefId("B"),
		)
}

func ovnkubeControllerServiceSyncRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Service Sync Rate(Top K)").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, irate(ovnkube_controller_sync_service_total{instance=~"$instance", pod=~"$pod"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func ovnkubeControllerServiceSyncLatency(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Service Sync Latency(Top K)").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("s").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, histogram_quantile(0.99, sum(rate(ovnkube_controller_sync_service_latency_seconds_bucket{instance=~"$instance", pod=~"$pod"}[5m])) by (le, instance)))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func ovnkubeControllerServiceReQueueRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Service Re-Queue Rate(Top K)").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, irate(ovnkube_controller_requeue_service_total{instance=~"$instance", pod=~"$pod"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func ovnkubeControllerResourceUpdateRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Resource Update Rate(Top K)").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, irate(ovnkube_controller_resource_update_total{instance=~"$instance", pod=~"$pod"}[5m]))`).
				LegendFormat("{{instance}} {{kind}}").
				RefId("A"),
		)
}

func ovnkubeControllerPodCreationDurationOverview(d lib.SdnDashboard) *heatmap.PanelBuilder {
	return lib.NewSdnHeatmapPanelBuilder(d, "Pod Creation Duration Overview",
		"The duration between a pod being scheduled and completing its logical switch port configuration").
		Unit("s").
		Calculate(false).
		Color(lib.NewSdnHeatmapColor(128)).
		CellValues(heatmap.NewCellValuesBuilder().Unit("rps")).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum by (le) (rate(ovnkube_controller_pod_creation_latency_seconds_bucket[5m]))`).
				LegendFormat("{{instance}}").
				Format(prometheus.PromQueryFormatHeatmap).
				Range().
				RefId("A"),
		)
}

func ovnkubeControllerPodCreationDuration(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Pod Creation Duration(Top K)").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("s").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, histogram_quantile(0.99, sum(rate(ovnkube_controller_pod_creation_latency_seconds_bucket{instance=~"$instance", pod=~"$pod"}[5m])) by (le, instance)))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

// Workqueue panels
func ovnkubeControllerWorkQueueDepth(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Work Queue Depth(Top K)").
		Description("Current depth of workqueue.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, ovnkube_controller_workqueue_depth{namespace="ovn-kubernetes", instance=~"$instance", pod=~"$pod"})`).
				LegendFormat("{{instance}}/{{pod}}:{{name}}").
				RefId("A"),
		)
}

func ovnkubeControllerWorkQueueAddRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Work Queue ADD Rate(Top K)").
		Description("Rate of adds handled by workqueue.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, irate(ovnkube_controller_workqueue_adds_total{namespace="ovn-kubernetes", instance=~"$instance", pod=~"$pod"}[5m]))`).
				LegendFormat("{{instance}}/{{pod}}:{{name}}").
				RefId("A"),
		)
}

func ovnkubeControllerWorkQueueRetryRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Work Queue Retry Rate(Top K)").
		Description("Rate of retries handled by workqueue.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, irate(ovnkube_controller_workqueue_retries_total{namespace="ovn-kubernetes", instance=~"$instance", pod=~"$pod"}[5m]))`).
				LegendFormat("{{instance}}/{{pod}}:{{name}}").
				RefId("A"),
		)
}

func ovnkubeControllerWorkQueueProcessingDuration(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Work Queue Processing Duration(Top K)").
		Description("How long in seconds processing an item from workqueue takes (p99).").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("s").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, histogram_quantile(0.99, sum(rate(ovnkube_controller_workqueue_work_duration_seconds_bucket{namespace="ovn-kubernetes", instance=~"$instance", pod=~"$pod"}[5m])) by (le, instance, pod, name)))`).
				LegendFormat("{{instance}}/{{pod}}:{{name}}").
				RefId("A"),
		)
}

func ovnkubeControllerWorkQueuePendingDuration(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Work Queue Pending Duration(Top K)").
		Description("How long in seconds an item stays in workqueue before being requested (p99).").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("s").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, histogram_quantile(0.99, sum(rate(ovnkube_controller_workqueue_queue_duration_seconds_bucket{namespace="ovn-kubernetes", instance=~"$instance", pod=~"$pod"}[5m])) by (le, instance, pod, name)))`).
				LegendFormat("{{instance}}/{{pod}}:{{name}}").
				RefId("A"),
		)
}

func ovnkubeControllerWorkQueueLongestRunningTime(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Work Queue Longest Running Time(Top K)").
		Description("How many seconds has the longest running processor for workqueue been running.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("s").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, ovnkube_controller_workqueue_longest_running_processor_seconds{namespace="ovn-kubernetes", instance=~"$instance", pod=~"$pod"})`).
				LegendFormat("{{instance}}/{{pod}}:{{name}}").
				RefId("A"),
		)
}

func ovnkubeControllerWorkQueueUnfinishedWorkTime(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Work Queue Unfinished Work Time(Top K)").
		Description("How many seconds of work has been done that is in progress and hasn't been observed by work duration. Large values indicate stuck threads.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("s").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, ovnkube_controller_workqueue_unfinished_work_seconds{namespace="ovn-kubernetes", instance=~"$instance", pod=~"$pod"})`).
				LegendFormat("{{instance}}/{{pod}}:{{name}}").
				RefId("A"),
		)
}
