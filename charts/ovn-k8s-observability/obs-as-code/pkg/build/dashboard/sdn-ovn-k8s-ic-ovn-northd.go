// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"obs-as-code/pkg/build/dashboard/lib"

	"github.com/grafana/grafana-foundation-sdk/go/bargauge"
	"github.com/grafana/grafana-foundation-sdk/go/cog"
	"github.com/grafana/grafana-foundation-sdk/go/common"
	"github.com/grafana/grafana-foundation-sdk/go/dashboard"
	"github.com/grafana/grafana-foundation-sdk/go/gauge"
	"github.com/grafana/grafana-foundation-sdk/go/prometheus"
	"github.com/grafana/grafana-foundation-sdk/go/table"
	"github.com/grafana/grafana-foundation-sdk/go/timeseries"
)

// OvnNorthdDashboard represents the OVN Northd dashboard with interface implementation
type OvnNorthdDashboard struct {
	lib.BaseSdnDashboard
}

// NewOvnNorthdDashboard creates a new OVN Northd dashboard instance
func NewOvnNorthdDashboard() *OvnNorthdDashboard {
	return &OvnNorthdDashboard{
		BaseSdnDashboard: lib.NewSdnDashboard(
			"sdn-ovn-k8s-ic-ovn-northd",
			"OVN K8S IC / OVN Northd",
			"This dashboard shows the status of the OVN K8S IC / OVN Northd.",
		),
	}
}

// GetFilename returns the filename for this dashboard
func (d *OvnNorthdDashboard) GetFilename() string {
	return d.BaseSdnDashboard.GetFilename()
}

// SetMode sets the build mode for the dashboard
func (d *OvnNorthdDashboard) SetMode(mode lib.BuildMode) {
	d.BaseSdnDashboard.SetMode(mode)
}

// GetBaseSdnDashboard returns the embedded base dashboard for accessing common fields
func (d *OvnNorthdDashboard) GetBaseSdnDashboard() *lib.BaseSdnDashboard {
	return &d.BaseSdnDashboard
}

// Build implements the interface method to build the actual dashboard
func (d *OvnNorthdDashboard) Build() (dashboard.Dashboard, error) {
	d.Builder = dashboard.NewDashboardBuilder(d.Title).
		Uid(d.Uid).
		Tags(d.Tags).
		Editable().
		Time("now-6h", "now").
		Timezone(common.TimeZoneBrowser).
		// Instance variable
		WithVariable(ovnNorthdInstanceVariable(d)).
		// Pod variable
		WithVariable(ovnNorthdPodVariable(d)).
		// TopK variable
		WithVariable(lib.TopKVariable()).

		// OVN Northd Overview Row
		WithRow(dashboard.NewRowBuilder("OVN Northd Overview").Id(1)).
		// Build Info Table
		WithPanel(ovnNorthdBuildInfo(d)).
		// Container Readiness Overview
		WithPanel(ovnNorthdContainerReadiness(d)).
		// OVN Northd Status: Active
		WithPanel(ovnNorthdStatusActiveGauge(d)).
		// NB Connection Status
		WithPanel(ovnNorthdNBConnectionGauge(d)).
		// SB Connection Status
		WithPanel(ovnNorthdSBConnectionGauge(d)).

		// OVN Northd Runtime Row
		WithRow(dashboard.NewRowBuilder("OVN Northd Runtime").Id(2)).
		// CPU Usage
		WithPanel(ovnNorthdCPUUsage(d)).
		// Memory Usage
		WithPanel(ovnNorthdMemoryUsage(d)).
		// Container Restart
		WithPanel(ovnNorthdContainerRestart(d)).
		// NB Connection Status: Not Connected
		WithPanel(ovnNorthdNBNoConnection(d)).
		// SB Connection Status: Not Connected
		WithPanel(ovnNorthdSBNoConnection(d)).
		// STREAM Open Rate
		WithPanel(ovnNorthdStreamOpenRate(d)).
		// PSTREAM Open Rate
		WithPanel(ovnNorthdPStreamOpenRate(d)).
		// OVSDB Transaction Success Rate
		WithPanel(ovnNorthdTransactionSuccess(d)).
		// OVSDB Transaction Incomplete Rate
		WithPanel(ovnNorthdTransactionIncomplete(d)).
		// OVSDB Transaction Abort Rate
		WithPanel(ovnNorthdTransactionAbort(d)).
		// OVSDB Transaction Error Rate
		WithPanel(ovnNorthdTransactionError(d)).
		// OVSDB Transaction Try Again Rate
		WithPanel(ovnNorthdTransactionTryAgain(d)).
		// OVSDB Transaction Uncommitted Rate
		WithPanel(ovnNorthdTransactionUncommitted(d))

	// Performance Counters Row, collapsed by default
	perfRow := dashboard.NewRowBuilder("OVN Northd Performance Counters").
		Id(3).
		Collapsed(true).
		// Performance Counters Stats Text
		WithPanel(lib.PerformanceCountersText())

	// Main Loop State
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "Main Loop", "Time spent in the main processing loop.", "ovn_northd", "ovn_northd_loop")

	// OVN NB DB Run Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "OVN NB DB Run", "Time spent processing Northbound database updates.", "ovn_northd", "ovnnb_db_run")

	// Build lflows Context Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "Build lflows Context", "Time spent building logical flows context.", "ovn_northd", "build_flows_ctx")

	// Clear lflows Context Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "Clear lflows Context", "Time spent clearing logical flows context.", "ovn_northd", "clear_lflows_ctx")

	// Build Logical Flow Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "Build Logical Flow", "Time spent building logical flows.", "ovn_northd", "build_lflows")

	// Build Datapath Logical Flows Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "Build Datapath Logical Flows", "Time spent building datapath logical flows.", "ovn_northd", "lflows_datapaths")

	// Build Port Logical Flows Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "Build Port Logical Flows", "Time spent building port logical flows.", "ovn_northd", "lflows_ports")

	// Build IP Multicast Flow Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "Build IP Multicast Flow", "Time spent building IP multicast flows.", "ovn_northd", "lflows_igmp")

	// Build LB Flow Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "Build LB Flow", "Time spent building load balancer flows.", "ovn_northd", "lflows_lbs")

	// OVN SB DB Run Performance Counters
	lib.AppendPerformanceCounterPanelsToRow(perfRow, d, "OVN SB DB Run", "Time spent processing southbound database updates.", "ovn_northd", "ovnsb_db_run")

	d.Builder.WithRow(perfRow)

	return d.Builder.Build()
}

// BuildOvnNorthdDashboard provides backward compatibility
func BuildOvnNorthdDashboard() (*lib.BaseSdnDashboard, error) {
	dashboardBuilder := NewOvnNorthdDashboard()
	_, err := dashboardBuilder.Build()
	if err != nil {
		return nil, err
	}
	return &dashboardBuilder.BaseSdnDashboard, nil
}

// Variables
func ovnNorthdInstanceVariable(d lib.SdnDashboard) *dashboard.QueryVariableBuilder {
	return dashboard.NewQueryVariableBuilder("instance").
		Label("instance").
		Query(dashboard.StringOrMap{String: cog.ToPtr(`label_values(ovn_northd_build_info{},instance)`)}).
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

func ovnNorthdPodVariable(d lib.SdnDashboard) *dashboard.QueryVariableBuilder {
	return dashboard.NewQueryVariableBuilder("pod").
		Label("Pod").
		Query(dashboard.StringOrMap{String: cog.ToPtr(`label_values(ovn_northd_build_info{instance=~"$instance"},pod)`)}).
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

func ovnNorthdBuildInfo(d lib.SdnDashboard) *table.PanelBuilder {
	return lib.BuildInfoTable(d, "ovn-northd build info.",
		`ovn_northd_build_info{namespace="ovn-kubernetes"}`,
		[]lib.BuildInfoField{
			{Label: "version", Name: "OVN Version"},
			{Label: "ovs_lib_version", Name: "OVS Lib Version"},
		},
	).Height(6).Span(6)
}

func ovnNorthdStatusActiveGauge(d lib.SdnDashboard) *gauge.PanelBuilder {
	return gauge.NewPanelBuilder().
		Title("OVN Northd Status: Active").
		Description("The number of ovn-northd in status:\n- standby\n- active\n- paused").
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(4).
		Unit("percentunit").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum by (namespace) (ovn_northd_status{namespace="ovn-kubernetes"} == 1) / sum by (namespace) (ovn_northd_status{namespace="ovn-kubernetes"})`).
				LegendFormat("Active").
				RefId("A"),
		).
		Thresholds(
			dashboard.NewThresholdsConfigBuilder().
				Mode(dashboard.ThresholdsModeAbsolute).
				Steps([]dashboard.Threshold{
					{Value: nil, Color: "red"},
					{Value: cog.ToPtr[float64](0.9), Color: "yellow"},
					{Value: cog.ToPtr[float64](1.0), Color: "green"},
				}),
		)
}

func ovnNorthdNBConnectionGauge(d lib.SdnDashboard) *gauge.PanelBuilder {
	return gauge.NewPanelBuilder().
		Title("NB Connection Status: Connected").
		Description("The percentage of OVN northd instances connected to the NB database.").
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(4).
		Unit("percentunit").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum by (namespace) (ovn_northd_nb_connection_status{namespace="ovn-kubernetes"} == 1) / sum by (namespace) (ovn_northd_nb_connection_status{namespace="ovn-kubernetes"})`).
				LegendFormat("CONNECTED").
				RefId("A"),
		).
		Thresholds(
			dashboard.NewThresholdsConfigBuilder().
				Mode(dashboard.ThresholdsModeAbsolute).
				Steps([]dashboard.Threshold{
					{Value: nil, Color: "red"},
					{Value: cog.ToPtr[float64](0.9), Color: "yellow"},
					{Value: cog.ToPtr[float64](1.0), Color: "green"},
				}),
		)
}

func ovnNorthdSBConnectionGauge(d lib.SdnDashboard) *gauge.PanelBuilder {
	return gauge.NewPanelBuilder().
		Title("SB Connection Status: Connected").
		Description("The percentage of OVN northd instances connected to the SB database.").
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(4).
		Unit("percentunit").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum by (namespace) (ovn_northd_sb_connection_status{namespace="ovn-kubernetes"} == 1) / sum by (namespace) (ovn_northd_sb_connection_status{namespace="ovn-kubernetes"})`).
				LegendFormat("CONNECTED").
				RefId("A"),
		).
		Thresholds(
			dashboard.NewThresholdsConfigBuilder().
				Mode(dashboard.ThresholdsModeAbsolute).
				Steps([]dashboard.Threshold{
					{Value: nil, Color: "red"},
					{Value: cog.ToPtr[float64](0.9), Color: "yellow"},
					{Value: cog.ToPtr[float64](1.0), Color: "green"},
				}),
		)
}

func ovnNorthdContainerReadiness(d lib.SdnDashboard) *bargauge.PanelBuilder {
	return lib.ContainerReadinessBarGauge(d, "ovn-northd").Span(6)
}

func ovnNorthdNBNoConnection(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NotConnectedTimeseries(d, "NB Connection Status: Not Connected",
		"Pods whose ovn-northd is not connected to the OVN northbound database.",
		"ovn_northd_nb_connection_status",
	).Span(6)
}

func ovnNorthdSBNoConnection(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NotConnectedTimeseries(d, "SB Connection Status: Not Connected",
		"Pods whose ovn-northd is not connected to the OVN southbound database.",
		"ovn_northd_sb_connection_status",
	).Span(6)
}

func ovnNorthdCPUUsage(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.ContainerCpuUsageTimeseries(d, true, "ovn-northd", []string{`pod=~"$pod"`})
}

func ovnNorthdMemoryUsage(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.ContainerMemoryUsageTimeseries(d, true, "ovn-northd", []string{`pod=~"$pod"`})
}

func ovnNorthdContainerRestart(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.ContainerRestartTimeseries(d, true, "ovn-northd", []string{`pod=~"$pod"`})
}

// Metrics panels
func ovnNorthdStreamOpenRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("STREAM Open Rate (TOP K)").
		Description("The rate of attempts to connect to a remote peer (active connection).").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("short").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_northd_stream_open{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[5m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnNorthdPStreamOpenRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("PSTREAM Open Rate (TOP K)").
		Description("The rate of attempts to accept incoming connections (passive connection).").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("short").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_northd_pstream_open{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[5m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

// OVSDB Transaction panels
func ovnNorthdTransactionSuccess(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("OVSDB transaction Success Rate (TOP K)").
		Description("The rate of the OVSDB transaction has successfully completed.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_northd_txn_success{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[5m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnNorthdTransactionIncomplete(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("OVSDB transaction Incomplete Rate (TOP K)").
		Description("The OVSDB transaction did not complete and the client had to re-try.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("ops").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_northd_txn_incomplete{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[5m]))`).
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

func ovnNorthdTransactionAbort(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("OVSDB transaction Abort Rate (TOP K)").
		Description("The rate of the OVSDB transaction has been aborted.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("ops").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_northd_txn_aborted{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[5m]))`).
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

func ovnNorthdTransactionError(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("OVSDB transaction Error Rate (TOP K)").
		Description("The rate of the OVSDB transaction has errored out.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_northd_txn_error{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[5m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnNorthdTransactionTryAgain(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("OVSDB transaction Try Again Rate (TOP K)").
		Description("The rate of the OVSDB transaction failed and the client had to re-try.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_northd_txn_try_again{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[5m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnNorthdTransactionUncommitted(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("OVSDB Transaction UNcommitted Rate (TOP K)").
		Description("The rate of the OVSDB transaction were uncommitted.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("locale").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovn_northd_txn_uncommitted{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[5m]))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}
