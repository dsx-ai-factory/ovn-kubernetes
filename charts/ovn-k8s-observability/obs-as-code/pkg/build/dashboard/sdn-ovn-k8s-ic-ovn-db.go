// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"fmt"
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

const (
	DbNameNorthbound = "Northbound DB"
	DbNameSouthbound = "Southbound DB"
)

// OvnDbDashboard represents the OVN Database dashboard with interface implementation
// DbName should be either "Northbound" or "Southbound"
type OvnDbDashboard struct {
	lib.BaseSdnDashboard
	DbName string
}

// NewOvnNorthboundDbDashboard creates a new OVN Northbound DB dashboard instance
func NewOvnNorthboundDbDashboard() *OvnDbDashboard {
	return &OvnDbDashboard{
		BaseSdnDashboard: lib.NewSdnDashboard(
			"sdn-ovn-k8s-ic-ovn-northbound-db",
			"OVN K8S IC / Northbound DB",
			"This dashboard shows the status of the OVN K8S IC / Northbound DB.",
		),
		DbName: DbNameNorthbound,
	}
}

// NewOvnSouthboundDbDashboard creates a new OVN Southbound DB dashboard instance
func NewOvnSouthboundDbDashboard() *OvnDbDashboard {
	return &OvnDbDashboard{
		BaseSdnDashboard: lib.NewSdnDashboard(
			"sdn-ovn-k8s-ic-ovn-southbound-db",
			"OVN K8S IC / Southbound DB",
			"This dashboard shows the status of the OVN K8S IC / Southbound DB.",
		),
		DbName: DbNameSouthbound,
	}
}

// Interface methods for OvnDbDashboard
func (d *OvnDbDashboard) GetFilename() string {
	return d.BaseSdnDashboard.GetFilename()
}

func (d *OvnDbDashboard) SetMode(mode lib.BuildMode) {
	d.BaseSdnDashboard.SetMode(mode)
}

func (d *OvnDbDashboard) GetBaseSdnDashboard() *lib.BaseSdnDashboard {
	return &d.BaseSdnDashboard
}

func (d *OvnDbDashboard) Build() (dashboard.Dashboard, error) {
	name := d.DbName
	d.Builder = dashboard.NewDashboardBuilder(d.Title).
		Uid(d.Uid).
		Tags(d.Tags).
		Editable().
		Time("now-3h", "now").
		Timezone(common.TimeZoneBrowser).
		// Instance variable
		WithVariable(ovnDbInstanceVariable(d)).
		// Pod variable
		WithVariable(ovnDbPodVariable(d)).
		// TopK variable (with custom values for DB dashboards)
		WithVariable(ovnDbTopKVariable()).
		// Database Row
		WithRow(dashboard.NewRowBuilder(name + " Overview").Id(1)).
		// INFO Table
		WithPanel(ovnDbInfoTable(d, name)).
		// Container Readiness Overview
		WithPanel(ovnDbContainerReadinessOverview(d, name)).
		// DB Size Overview
		WithPanel(ovnDbSizeOverview(d, name)).
		// DB Runtime Row
		WithRow(dashboard.NewRowBuilder(name + " Runtime").Id(2)).
		// CPU Usage
		WithPanel(ovnDbCPUUsagePanel(d, name)).
		// Memory Usage
		WithPanel(ovnDbMemoryUsagePanel(d, name)).
		// Container Restart
		WithPanel(ovnDbContainerRestartPanel(d, name)).
		// E2E Timestamp Delay
		WithPanel(ovnDbE2ETimestampDelayPanel(d, name)).
		// DB Size Panel
		WithPanel(ovnDbSizePanel(d, name)).
		// Active DB Monitors
		WithPanel(ovnDbMonitorsPanel(d, name)).
		// Database Metrics Row
		WithRow(dashboard.NewRowBuilder(fmt.Sprintf("%s Metrics", name)).Id(3)).
		// JSON RPC Sessions
		WithPanel(ovnDbJSONRPCSessionsPanel(d, name)).
		// JSON RPC Request Rate
		WithPanel(ovnDbJSONRPCRequestRatePanel(d, name)).
		// JSON RPC Reply Rate
		WithPanel(ovnDbJSONRPCReplyRatePanel(d, name)).
		// PSTREAM Open Rate
		WithPanel(ovnDbPStreamOpenRatePanel(d, name)).
		// Object Creation Rate
		WithPanel(ovnDbObjectCreationRatePanel(d, name)).
		// Memory Allocation Rate
		WithPanel(ovnDbMemoryAllocationRatePanel(d, name)).
		// Event Process Rate
		WithPanel(ovnDbEventProcessRatePanel(d, name)).
		// HMAP Pathological Rate
		WithPanel(ovnDbHMAPPathologicalRatePanel(d, name)).
		// HMAP Expand Rate
		WithPanel(ovnDbHMAPExpandRatePanel(d, name)).
		// Event Loop Busy Rate
		WithPanel(ovnDbEventLoopBusyRatePanel(d, name)).
		// Lock File Count
		WithPanel(ovnDbLockFileCountPanel(d, name))

	return d.Builder.Build()
}

// BuildOvnDatabaseDashboard provides backward compatibility - creates dashboard based on name parameter
func BuildOvnDatabaseDashboard(name string) (*lib.BaseSdnDashboard, error) {
	// Validate input
	if name != "Northbound DB" && name != "Southbound DB" {
		return nil, fmt.Errorf("invalid name: %s. Must be 'Northbound DB' or 'Southbound DB'", name)
	}

	var dashboardBuilder lib.SdnDashboard
	if name == "Northbound DB" {
		dashboardBuilder = NewOvnNorthboundDbDashboard()
	} else {
		dashboardBuilder = NewOvnSouthboundDbDashboard()
	}

	_, err := dashboardBuilder.Build()
	if err != nil {
		return nil, err
	}

	return dashboardBuilder.GetBaseSdnDashboard(), nil
}

// Helper functions to determine values based on DB name

func getContainerName(name string) string {
	if name == "Northbound DB" {
		return "nb-ovsdb"
	}
	return "sb-ovsdb"
}

func getDbNameLabelValue(name string) string {
	if name == "Northbound DB" {
		return "OVN_Northbound"
	}
	return "OVN_Southbound"
}

// Variables
func ovnDbInstanceVariable(d lib.SdnDashboard) *dashboard.QueryVariableBuilder {
	return dashboard.NewQueryVariableBuilder("instance").
		Label("Instance").
		Query(dashboard.StringOrMap{String: cog.ToPtr("label_values(ovn_db_build_info{},instance)")}).
		Datasource(d.DataSourceRef()).
		Current(dashboard.VariableOption{
			Selected: cog.ToPtr(true),
			Text:     dashboard.StringOrArrayOfString{String: cog.ToPtr("All")},
			Value:    dashboard.StringOrArrayOfString{ArrayOfString: []string{"$__all"}},
		}).
		Refresh(dashboard.VariableRefreshOnTimeRangeChanged).
		Multi(true).
		IncludeAll(true).
		AllValue(".*")
}

func ovnDbPodVariable(d lib.SdnDashboard) *dashboard.QueryVariableBuilder {
	return dashboard.NewQueryVariableBuilder("pod").
		Label("Pod").
		Query(dashboard.StringOrMap{String: cog.ToPtr(`label_values(ovn_db_build_info{instance=~"$instance"},pod)`)}).
		Datasource(d.DataSourceRef()).
		Current(dashboard.VariableOption{
			Selected: cog.ToPtr(true),
			Text:     dashboard.StringOrArrayOfString{String: cog.ToPtr("All")},
			Value:    dashboard.StringOrArrayOfString{ArrayOfString: []string{"$__all"}},
		}).
		Refresh(dashboard.VariableRefreshOnTimeRangeChanged).
		Multi(true).
		IncludeAll(true).
		AllValue(".*")
}

func ovnDbTopKVariable() *dashboard.CustomVariableBuilder {
	return dashboard.NewCustomVariableBuilder("topk").
		Label("Topk").
		Values(dashboard.StringOrMap{String: cog.ToPtr("1,3,5,10,20,50")}).
		Current(dashboard.VariableOption{
			Selected: cog.ToPtr(true),
			Text:     dashboard.StringOrArrayOfString{String: cog.ToPtr("5")},
			Value:    dashboard.StringOrArrayOfString{String: cog.ToPtr("5")},
		})
}

// Panel functions
func ovnDbInfoTable(d lib.SdnDashboard, name string) *table.PanelBuilder {
	schemaVersionLabel := "nb_schema_version"
	schemaVersionName := "NB Schema Version"
	if name == DbNameSouthbound {
		schemaVersionLabel = "sb_schema_version"
		schemaVersionName = "SB Schema Version"
	}

	return lib.BuildInfoTable(d,
		"A metric with a constant '1' value labeled by ovsdb-server version and NB and SB schema version.",
		`ovn_db_build_info{namespace="ovn-kubernetes"}`,
		[]lib.BuildInfoField{
			{Label: "version", Name: "OVS Version"},
			{Label: schemaVersionLabel, Name: schemaVersionName},
		},
	).Height(6).Span(8)
}

func ovnDbContainerReadinessOverview(d lib.SdnDashboard, name string) *bargauge.PanelBuilder {
	return lib.ContainerReadinessBarGauge(d, getContainerName(name))
}

func ovnDbSizeOverview(d lib.SdnDashboard, name string) *heatmap.PanelBuilder {
	dbNameLabelValue := getDbNameLabelValue(name)
	return lib.NewSdnHeatmapPanelBuilder(d, "DB Size Overview", "The size of the database file associated with the OVN DB component.").
		Unit("decbytes").
		Calculate(true).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(fmt.Sprintf(`ovn_db_db_size_bytes{namespace="ovn-kubernetes", db_name="%s"}`, dbNameLabelValue)).
				LegendFormat("__auto").
				Format(prometheus.PromQueryFormatHeatmap).
				Range().
				RefId("A"),
		).Height(6).Span(8)
}

func ovnDbSizePanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	dbNameLabelValue := getDbNameLabelValue(name)
	return lib.NewSdnPanelBuilder().
		Title("DB Size(Top K)").
		Description("The size of the database file associated with the OVN DB component.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("decbytes").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(lib.TopKQuery(fmt.Sprintf(`ovn_db_db_size_bytes{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes", db_name="%s"}`, dbNameLabelValue))).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnDbMonitorsPanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	dbNameLabelValue := getDbNameLabelValue(name)
	title := "Active DB Monitors(Top K)"
	if name == "Southbound DB" {
		title = "DB MONITORS(Top K)"
	}
	return lib.NewSdnPanelBuilder().
		Title(title).
		Description("Number of OVSDB Monitors on the server.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(fmt.Sprintf(`topk($topk, sum by (instance,pod) (ovn_db_ovsdb_monitors{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes", db_name="%s"}))`, dbNameLabelValue)).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnDbCPUUsagePanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	return lib.ContainerCpuUsageTimeseries(d, true, getContainerName(name), []string{`pod=~"$pod"`})
}

func ovnDbContainerRestartPanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	return lib.ContainerRestartTimeseries(d, true, getContainerName(name), []string{`pod=~"$pod"`})
}

func ovnDbMemoryUsagePanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	return lib.ContainerMemoryUsageTimeseries(d, true, getContainerName(name), []string{`pod=~"$pod"`})
}

func ovnDbE2ETimestampDelayPanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("NB->SB E2E Timestamp Delay (Top K)").
		Description("The current e2e-timestamp value as observed in this instance of the database. This panel shows the delay between NB and SB timestamps.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("s").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, max by(instance,pod) (ovn_db_e2e_timestamp{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes", db_name="OVN_Northbound"}) - max by(instance,pod) (ovn_db_e2e_timestamp{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes", db_name="OVN_Southbound"}))`).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnDbJSONRPCSessionsPanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	dbNameLabelValue := getDbNameLabelValue(name)
	return lib.NewSdnPanelBuilder().
		Title("JSON RPC Sessions (Top K)").
		Description("Active number of JSON RPC Server sessions to the DB.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("short").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(fmt.Sprintf(`topk($topk, sum by (instance, pod) (ovn_db_jsonrpc_server_sessions{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes", db_name="%s"}))`, dbNameLabelValue)).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnDbJSONRPCRequestRatePanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	dbNameLabelValue := getDbNameLabelValue(name)
	return lib.NewSdnPanelBuilder().
		Title("JSON RPC Request Rate (Top K)").
		Description("How many JSON RPC requests were actually received in OVSDB server.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("reqps").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(fmt.Sprintf(`topk($topk, sum by(instance, pod) (rate(ovn_db_unixctl_received{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes",db_name="%s"}[2m])))`, dbNameLabelValue)).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnDbJSONRPCReplyRatePanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	dbNameLabelValue := getDbNameLabelValue(name)
	return lib.NewSdnPanelBuilder().
		Title("JSON RPC Reply Rate (Top K)").
		Description("How many of received JSON RPC requests OVSDB server actually replied to.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("reqps").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(fmt.Sprintf(`topk($topk, sum by (instance, pod) (rate(ovn_db_unixctl_replied{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes", db_name="%s"}[2m])))`, dbNameLabelValue)).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnDbPStreamOpenRatePanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	dbNameLabelValue := getDbNameLabelValue(name)
	return lib.NewSdnPanelBuilder().
		Title("PSTREAM Open Rate (Top K)").
		Description("The number of times passive connections were opened for the remote peer to connect.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("short").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(fmt.Sprintf(`topk($topk, sum by (instance, pod) (rate(ovn_db_pstream_open{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes", db_name="%s"}[2m])))`, dbNameLabelValue)).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnDbObjectCreationRatePanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	dbNameLabelValue := getDbNameLabelValue(name)
	return lib.NewSdnPanelBuilder().
		Title("Object Creation Rate (Top K)").
		Description("Intensity of new object creations.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(fmt.Sprintf(`topk($topk, sum by(instance, pod) (rate(ovn_db_seq_change{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes", db_name="%s"}[2m])))`, dbNameLabelValue)).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnDbMemoryAllocationRatePanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	dbNameLabelValue := getDbNameLabelValue(name)
	return lib.NewSdnPanelBuilder().
		Title("Memory Allocation Rate (Top K)").
		Description("Intensity of memory allocations in OVSDB server.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(fmt.Sprintf(`topk($topk, sum by (instance, pod) (rate(ovn_db_util_xalloc{instance=~"$instance", namespace="ovn-kubernetes", pod=~"$pod", db_name="%s"}[2m])))`, dbNameLabelValue)).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnDbEventProcessRatePanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	dbNameLabelValue := getDbNameLabelValue(name)
	return lib.NewSdnPanelBuilder().
		Title("Event Process Rate (Top K)").
		Description("How many scheduled events were processed without timeout (event loop effectiveness).").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(fmt.Sprintf(`topk($topk, sum by (instance, pod) (rate(ovn_db_poll_zero_timeout{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes", db_name="%s"}[2m])))`, dbNameLabelValue)).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnDbHMAPPathologicalRatePanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	dbNameLabelValue := getDbNameLabelValue(name)
	return lib.NewSdnPanelBuilder().
		Title("HMAP Pathological Rate (Top K)").
		Description("How many hash map resize calls resulted in copying buckets with 6+ nodes (collision factor).").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(fmt.Sprintf(`topk($topk, sum by (instance, pod) (rate(ovn_db_hmap_pathological{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes", db_name="%s"}[2m])))`, dbNameLabelValue)).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnDbHMAPExpandRatePanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	dbNameLabelValue := getDbNameLabelValue(name)
	return lib.NewSdnPanelBuilder().
		Title("HMAP Expand Rate (Top K)").
		Description("How many hash map resizes have been made.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(fmt.Sprintf(`topk($topk, sum by (instance, pod) (rate(ovn_db_hmap_expand{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes", db_name="%s"}[2m])))`, dbNameLabelValue)).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnDbEventLoopBusyRatePanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	dbNameLabelValue := getDbNameLabelValue(name)
	return lib.NewSdnPanelBuilder().
		Title("Event Loop Busy Rate (Top K)").
		Description("How many scheduled events to wake up blocking poller (event loop busy factor).").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(fmt.Sprintf(`topk($topk, sum by (instance, pod) (rate(ovn_db_poll_create_node{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes", db_name="%s"}[2m])))`, dbNameLabelValue)).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ovnDbLockFileCountPanel(d lib.SdnDashboard, name string) *timeseries.PanelBuilder {
	dbNameLabelValue := getDbNameLabelValue(name)
	return lib.NewSdnPanelBuilder().
		Title("Lock File Count (Top K)").
		Description("How many expensive file lock operations have been made.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(6).
		Unit("short").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(fmt.Sprintf(`topk($topk, sum by(instance, pod) (ovn_db_lockfile_lock{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes", db_name="%s"}))`, dbNameLabelValue)).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}
