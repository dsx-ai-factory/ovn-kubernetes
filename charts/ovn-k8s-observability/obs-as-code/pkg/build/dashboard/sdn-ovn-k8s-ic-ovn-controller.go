// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"obs-as-code/pkg/build/dashboard/lib"

	"github.com/grafana/grafana-foundation-sdk/go/cog"
	"github.com/grafana/grafana-foundation-sdk/go/common"
	"github.com/grafana/grafana-foundation-sdk/go/dashboard"
	"github.com/grafana/grafana-foundation-sdk/go/gauge"
	"github.com/grafana/grafana-foundation-sdk/go/prometheus"
	"github.com/grafana/grafana-foundation-sdk/go/table"
	"github.com/grafana/grafana-foundation-sdk/go/timeseries"
)

// OvnControllerDashboard represents the OVN Controller dashboard with interface implementation
type OvnControllerDashboard struct {
	lib.BaseSdnDashboard
}

// NewOvnControllerDashboard creates a new OVN Controller dashboard instance
func NewOvnControllerDashboard() *OvnControllerDashboard {
	return &OvnControllerDashboard{
		BaseSdnDashboard: lib.NewSdnDashboard(
			"sdn-ovn-k8s-ic-ovn-controller",
			"OVN K8S IC / OVN Controller",
			"This dashboard shows the status of the OVN K8S IC / OVN Controller.",
		),
	}
}

// GetFilename returns the filename for this dashboard
func (d *OvnControllerDashboard) GetFilename() string {
	return d.BaseSdnDashboard.GetFilename()
}

// SetMode sets the build mode for the dashboard
func (d *OvnControllerDashboard) SetMode(mode lib.BuildMode) {
	d.BaseSdnDashboard.SetMode(mode)
}

// GetBaseSdnDashboard returns the embedded base dashboard for accessing common fields
func (d *OvnControllerDashboard) GetBaseSdnDashboard() *lib.BaseSdnDashboard {
	return &d.BaseSdnDashboard
}

// Build implements the interface method to build the actual dashboard
func (d *OvnControllerDashboard) Build() (dashboard.Dashboard, error) {
	d.Builder = dashboard.NewDashboardBuilder(d.Title).
		Uid(d.Uid).
		Tags(d.Tags).
		Editable().
		Time("now-30m", "now").
		Timezone(common.TimeZoneBrowser).
		WithVariable(lib.ClusterVariable(d)).
		// Instance variable
		WithVariable(instanceVariable(d)).
		// Pod variable
		WithVariable(podVariable(d)).
		// TopK variable
		WithVariable(lib.TopKVariable()).

		// OVN Controller Overview Row
		WithRow(dashboard.NewRowBuilder("OVN Controller Overview").Id(1)).
		// Build Info Table
		WithPanel(ovnControllerBuildInfo(d)).
		// Container Readiness Overview
		WithPanel(lib.ContainerReadinessBarGauge(d, "ovn-controller")).
		// SB Connection Status Gauge
		WithPanel(ovnControllerSBConnectionOverview(d)).

		// OVN Controller Runtime Row
		WithRow(dashboard.NewRowBuilder("OVN Controller Runtime").Id(2)).
		// CPU Usage
		WithPanel(ovnControllerCPUUsage(d)).
		// Memory Usage
		WithPanel(ovnControllerMemoryUsage(d)).
		// Container Restart
		WithPanel(ovnControllerContainerRestart(d)).
		// SB Connection Status: Not Connected
		WithPanel(ovnControllerSBNoConnection(d)).

		// OVSDB Transactions Row
		WithRow(dashboard.NewRowBuilder("OVSDB Transactions").Id(3)).
		// OVSDB Transaction Success Rate
		WithPanel(ovnControllerTransactionSuccess(d)).
		// OVSDB Transaction Incomplete Rate
		WithPanel(ovnControllerTransactionIncomplete(d)).
		// OVSDB Transaction Abort Rate
		WithPanel(ovnControllerTransactionAbort(d)).
		// OVSDB Transaction Error Rate
		WithPanel(ovnControllerTransactionError(d)).
		// OVSDB Transaction Try Again Rate
		WithPanel(ovnControllerTransactionTryAgain(d)).
		// OVSDB Transaction Uncommitted Rate
		WithPanel(ovnControllerTransactionUncommitted(d)).
		// OVSDB Transaction No Change Rate
		WithPanel(ovnControllerTransactionNoChange(d)).

		// OpenFlow Connections Row
		WithRow(dashboard.NewRowBuilder("OpenFlow Connections").Id(4)).
		// Rconn Message Queued Rate
		WithPanel(ovnControllerRconnQueued(d)).
		// Rconn Message Overflow Rate
		WithPanel(ovnControllerRconnOverflow(d)).
		// Rconn Message Sent Rate
		WithPanel(ovnControllerRconnSent(d)).
		// Rconn Message Discarded Rate
		WithPanel(ovnControllerRconnDiscarded(d)).
		// Vconn Message Sent Rate
		WithPanel(ovnControllerVconnSent(d)).
		// Vconn Message RX Rate
		WithPanel(ovnControllerVconnReceived(d)).
		// Vconn Open Rate
		WithPanel(ovnControllerVconnOpen(d)).
		// STREAM Open Rate
		WithPanel(ovnControllerStreamOpen(d)).
		// Netlink Row
		WithRow(dashboard.NewRowBuilder("Netlink").Id(5)).
		// Netlink Message TX Rate
		WithPanel(ovnControllerNetlinkSent(d)).
		// Netlink Message RX Rate
		WithPanel(ovnControllerNetlinkReceived(d)).
		// Netlink Message Overflow Rate
		WithPanel(ovnControllerNetlinkOverflow(d)).
		// Netlink Jumbo Message RX Rate
		WithPanel(ovnControllerNetlinkJumbo(d)).
		// OpenFlow Row
		WithRow(dashboard.NewRowBuilder("OpenFlow").Id(6)).
		// Packet-In Message Rate
		WithPanel(ovnControllerPacketIn(d)).
		// Packet-In Message Drop Rate
		WithPanel(ovnControllerPacketInDrop(d)).
		// Logical Flow Translate Rate
		WithPanel(ovnControllerLogicalFlowTranslate(d))

	// Performance Counters Row, collapsed by default
	perfRow := dashboard.NewRowBuilder("OVN Controller Performance Counters").
		Id(7).
		Collapsed(true).
		// Performance Counters Stats Text
		WithPanel(lib.PerformanceCountersText())

	// BFD Run Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "BFD Run", "Time spent processing Bidirectional Forwarding Detection (BFD) for tunnel monitoring.", "ovn_controller", "bfd_run")

	// CT Zone Commit Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "CT Zone Commit", "Time spent committing connection tracking zone assignments to the kernel.", "ovn_controller", "ct_zone_commit")

	// Flow Generation Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "Flow Generation", "Time spent generating OpenFlow rules from logical flows and port bindings.", "ovn_controller", "flow_generation")

	// Flow Installation Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "Flow Installation", "Time spent installing/updating OpenFlow rules in the local OVS bridge.", "ovn_controller", "flow_installation")

	// Interface Status Manager Run Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "Interface Status Manager Run", "Time spent managing interface status and lifecycle events.", "ovn_controller", "if_status_mgr_run")

	// Interface Status Manager Update Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "Interface Status Manager Update", "Time spent updating interface status information in the Southbound database.", "ovn_controller", "if_status_mgr_update")

	// Ofctrl Seqno Run Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "OpenFlow Control SeqNo Run", "Time spent managing OpenFlow sequence numbers for flow synchronization.", "ovn_controller", "ofctrl_seqno_run")

	// Ofctrl Seqno Update Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "OpenFlow Control SeqNo Update", "Time spent managing patch ports between integration and provider bridges.", "ovn_controller", "ofctrl_seqno_update")

	// Patch Port Run Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "Patch Run", "Time spent managing patch ports between integration and provider bridges.", "ovn_controller", "patch_run")

	// Pinctrl Run Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "Pinctrl Run", "Time spent processing control plane packets (ARP, DHCP, DNS, etc.) and generating responses.", "ovn_controller", "pinctrl_run")

	d.Builder.WithRow(perfRow)

	return d.Builder.Build()
}

// BuildOvnControllerDashboard provides backward compatibility
func BuildOvnControllerDashboard() (*lib.BaseSdnDashboard, error) {
	dashboardBuilder := NewOvnControllerDashboard()
	_, err := dashboardBuilder.Build()
	if err != nil {
		return nil, err
	}
	return &dashboardBuilder.BaseSdnDashboard, nil
}

// Variables
func instanceVariable(d lib.SdnDashboard) *dashboard.QueryVariableBuilder {
	return dashboard.NewQueryVariableBuilder("instance").
		Label("instance").
		Query(dashboard.StringOrMap{String: cog.ToPtr("label_values(ovn_controller_build_info{cluster=\"$cluster\"},instance)")}).
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

func podVariable(d lib.SdnDashboard) *dashboard.QueryVariableBuilder {
	return dashboard.NewQueryVariableBuilder("pod").
		Label("Pod").
		Query(dashboard.StringOrMap{String: cog.ToPtr("label_values(ovn_controller_build_info{cluster=\"$cluster\", instance=~\"$instance\"},pod)")}).
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

// Main panels

func ovnControllerBuildInfo(d lib.SdnDashboard) *table.PanelBuilder {
	return lib.BuildInfoTable(d, "ovn-controller build info.",
		`ovn_controller_build_info{cluster="$cluster", namespace="ovn-kubernetes"}`,
		[]lib.BuildInfoField{
			{Label: "version", Name: "OVN Version"},
			{Label: "ovs_lib_version", Name: "OVS Lib Version"},
		},
	).Height(6).Span(8)
}

func ovnControllerSBConnectionOverview(d lib.SdnDashboard) *gauge.PanelBuilder {
	return gauge.NewPanelBuilder().
		Title("SB Connection Status: Connected").
		Description("Connection status of OVN controller to OVN southbound database.").
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(8).
		Unit("percentunit").
		Min(0).
		Max(1).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum by (namespace) (ovn_controller_southbound_database_connected{cluster="$cluster", namespace="ovn-kubernetes"} == bool 1) / count by (namespace) (ovn_controller_southbound_database_connected{cluster="$cluster", namespace="ovn-kubernetes"})`).
				LegendFormat("CONNECTED").
				RefId("A"),
		).
		Thresholds(
			dashboard.NewThresholdsConfigBuilder().
				Mode(dashboard.ThresholdsModeAbsolute).
				Steps([]dashboard.Threshold{
					{Value: nil, Color: "red"},
					{Value: cog.ToPtr[float64](0.9), Color: "#EAB839"},
					{Value: cog.ToPtr[float64](1.0), Color: "green"},
				}),
		)
}

func ovnControllerSBNoConnection(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NotConnectedTimeseries(d, "SB Connection Status: Not Connected",
		"Pods whose ovn-controller is not connected to the OVN southbound database.",
		"ovn_controller_southbound_database_connected",
	).Span(12)
}

func ovnControllerCPUUsage(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.ContainerCpuUsageTimeseries(d, true, "ovn-controller", []string{`pod=~"$pod"`})
}

func ovnControllerMemoryUsage(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.ContainerMemoryUsageTimeseries(d, true, "ovn-controller", []string{`pod=~"$pod"`})
}

func ovnControllerContainerRestart(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.ContainerRestartTimeseries(d, true, "ovn-controller", []string{`pod=~"$pod"`})
}

// OVSDB Transaction panels
func ovnControllerTransactionSuccess(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("OVSDB Transaction Success Rate (Top K)").
		Description("The rate of the OVSDB transaction has successfully completed.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(8).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_txn_success{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnControllerTransactionIncomplete(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("OVSDB Transaction Incomplete Rate (Top K)").
		Description("The OVSDB transaction did not complete and the client had to re-try").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(8).
		Unit("ops").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_txn_incomplete{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		).
		Thresholds(
			dashboard.NewThresholdsConfigBuilder().
				Mode(dashboard.ThresholdsModeAbsolute).
				Steps([]dashboard.Threshold{
					{Value: nil, Color: "dark-green"},
					{Value: cog.ToPtr[float64](1.0), Color: "dark-yellow"},
					{Value: cog.ToPtr[float64](1.5), Color: "semi-dark-red"},
				}),
		)
}

func ovnControllerTransactionAbort(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("OVSDB Transaction Abort Rate (Top K)").
		Description("The rate of the OVSDB  transaction has been aborted.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(8).
		Unit("ops").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_txn_aborted{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		).
		Thresholds(
			dashboard.NewThresholdsConfigBuilder().
				Mode(dashboard.ThresholdsModeAbsolute).
				Steps([]dashboard.Threshold{
					{Value: nil, Color: "dark-green"},
					{Value: cog.ToPtr[float64](1.0), Color: "dark-yellow"},
					{Value: cog.ToPtr[float64](1.5), Color: "semi-dark-red"},
				}),
		)
}

func ovnControllerTransactionError(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("OVSDB Transaction Error Rate (Top K)").
		Description("The rate of the OVSDB transaction has errored out.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(8).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_txn_error{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnControllerTransactionTryAgain(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("OVSDB Transaction Try Again Rate (TOP $topk)").
		Description("The rate of the OVSDB transaction failed and the client had to re-try.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(8).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_txn_try_again{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnControllerTransactionUncommitted(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("OVSDB Transaction Uncommitted Rate (Top K)").
		Description("The rate of the OVSDB transaction were uncommitted.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(8).
		Unit("locale").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_txn_uncommitted{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnControllerTransactionNoChange(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("OVSDB Transaction No Change Rate (TOP $topk)").
		Description("The rate of the OVSDB transaction resulted in no change to the database.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(8).
		Unit("locale").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_txn_unchanged{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

// OpenFlow Connection panels
func ovnControllerRconnQueued(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Rconn Message Queued Rate (Top K)").
		Description("The rate of messages that have been queued because it couldn't be sent using the underlying virtual connection to OpenFlow devices.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("short").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_rconn_queued{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnControllerRconnOverflow(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Rconn Message Overflow Rate (Top K)").
		Description("The rate of messages that have been dropped because of the queue overflow.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("short").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_rconn_overflow{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnControllerRconnSent(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Rconn Message Sent Rate (Top K)").
		Description("The rate of messages that have been sent to the underlying. virtual connection.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("short").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_rconn_sent{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnControllerRconnDiscarded(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Rconn Message Discarded Rate (Top K)").
		Description("The rate of messages that have been dropped because the send queue had to be flushed because of reconnection.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("short").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_rconn_discarded{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnControllerVconnSent(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Vconn Message Sent Rate (Top K)").
		Description("The rate of messages sent to the OpenFlow Device.").
		Datasource(d.DataSourceRef()).
		Height(9).
		Span(12).
		Unit("short").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_vconn_sent{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnControllerVconnReceived(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Vconn Message RX Rate (Top K)").
		Description("The rate of messages received from the OpenFlow Device.").
		Datasource(d.DataSourceRef()).
		Height(9).
		Span(12).
		Unit("short").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_vconn_received{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		).
		Thresholds(
			dashboard.NewThresholdsConfigBuilder().
				Mode(dashboard.ThresholdsModeAbsolute).
				Steps([]dashboard.Threshold{
					{Value: nil, Color: "green"},
					{Value: cog.ToPtr[float64](80), Color: "red"},
				}),
		)
}

func ovnControllerVconnOpen(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Vconn Open Rate (Top K)").
		Description("The rate of attempts to connect to an OpenFlow Device.").
		Datasource(d.DataSourceRef()).
		Height(9).
		Span(12).
		Unit("short").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_vconn_open{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnControllerStreamOpen(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("STREAM Open Rate (Top K)").
		Description("Specifies the number of attempts to connect to a remote peer (active connection) per second").
		Datasource(d.DataSourceRef()).
		Height(9).
		Span(12).
		Unit("short").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_stream_open{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

// Netlink panels
func ovnControllerNetlinkSent(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Netlink Message TX Rate (Top K)").
		Description("The rate of netlink messages received by the kernel.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("short").
		Min(0).
		FillOpacity(10).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_netlink_sent{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		).
		Thresholds(
			dashboard.NewThresholdsConfigBuilder().
				Mode(dashboard.ThresholdsModeAbsolute).
				Steps([]dashboard.Threshold{
					{Value: nil, Color: "green"},
					{Value: cog.ToPtr[float64](80), Color: "red"},
				}),
		)
}

func ovnControllerNetlinkReceived(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Netlink Message RX Rate (Top K)").
		Description("The rate of netlink messages received by the kernel.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("short").
		Min(0).
		FillOpacity(10).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_netlink_received{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		).
		Thresholds(
			dashboard.NewThresholdsConfigBuilder().
				Mode(dashboard.ThresholdsModeAbsolute).
				Steps([]dashboard.Threshold{
					{Value: nil, Color: "green"},
					{Value: cog.ToPtr[float64](80), Color: "red"},
				}),
		)
}

func ovnControllerNetlinkOverflow(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Netlink Message Overflow Rate (Top K)").
		Description("The rate of netlink messages dropped by the daemon due to buffer overflow.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("short").
		Min(0).
		FillOpacity(10).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_netlink_overflow{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		).
		Thresholds(
			dashboard.NewThresholdsConfigBuilder().
				Mode(dashboard.ThresholdsModeAbsolute).
				Steps([]dashboard.Threshold{
					{Value: nil, Color: "green"},
					{Value: cog.ToPtr[float64](80), Color: "red"},
				}),
		)
}

func ovnControllerNetlinkJumbo(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Netlink Jumbo Message RX Rate (Top K)").
		Description("The rate of netlink messages that were received from the kernel were more than the allocated buffer.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("short").
		Min(0).
		FillOpacity(10).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_netlink_recv_jumbo{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		).
		Thresholds(
			dashboard.NewThresholdsConfigBuilder().
				Mode(dashboard.ThresholdsModeAbsolute).
				Steps([]dashboard.Threshold{
					{Value: nil, Color: "green"},
					{Value: cog.ToPtr[float64](80), Color: "red"},
				}),
		)
}

// OpenFlow panels
func ovnControllerPacketIn(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Packet-In Message Rate (Top K)").
		Description("The rate of packet-in packets from ovs-vswitchd being handled.").
		Datasource(d.DataSourceRef()).
		Height(9).
		Span(8).
		Unit("short").
		Min(0).
		FillOpacity(10).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_packet_in{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		).
		Thresholds(
			dashboard.NewThresholdsConfigBuilder().
				Mode(dashboard.ThresholdsModeAbsolute).
				Steps([]dashboard.Threshold{
					{Value: nil, Color: "green"},
					{Value: cog.ToPtr[float64](80), Color: "red"},
				}),
		)
}

func ovnControllerPacketInDrop(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Packet-In Message Drop Rate (Top K)").
		Description("The rate of the packet-ins from ovs-vswitchd being dropped due to resource constraints").
		Datasource(d.DataSourceRef()).
		Height(9).
		Span(8).
		Unit("short").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_packet_in_drop{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnControllerLogicalFlowTranslate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Logical Flow Translate Rate (Top K)").
		Description("The rate of translating the Logical_Flow table in the OVN SB database into OpenFlow flows.").
		Datasource(d.DataSourceRef()).
		Height(9).
		Span(8).
		Unit("short").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_controller_lflow_run{cluster="$cluster", instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[2m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		).
		Thresholds(
			dashboard.NewThresholdsConfigBuilder().
				Mode(dashboard.ThresholdsModeAbsolute).
				Steps([]dashboard.Threshold{
					{Value: nil, Color: "green"},
					{Value: cog.ToPtr[float64](80), Color: "red"},
				}),
		)
}
