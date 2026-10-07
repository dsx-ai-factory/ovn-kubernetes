// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"obs-as-code/pkg/build/dashboard/lib"

	"github.com/grafana/grafana-foundation-sdk/go/barchart"
	"github.com/grafana/grafana-foundation-sdk/go/bargauge"
	"github.com/grafana/grafana-foundation-sdk/go/cog"
	"github.com/grafana/grafana-foundation-sdk/go/common"
	"github.com/grafana/grafana-foundation-sdk/go/dashboard"
	"github.com/grafana/grafana-foundation-sdk/go/prometheus"
	"github.com/grafana/grafana-foundation-sdk/go/table"
	"github.com/grafana/grafana-foundation-sdk/go/timeseries"
)

// HostOvsDashboard represents the Host OVS dashboard with interface implementation
type HostOvsDashboard struct {
	lib.BaseSdnDashboard
}

// NewHostOvsDashboard creates a new Host OVS dashboard instance
func NewHostOvsDashboard() *HostOvsDashboard {
	return &HostOvsDashboard{
		BaseSdnDashboard: lib.NewSdnDashboard(
			"sdn-host-ovs",
			"Host / OVS",
			"Host OVS monitoring dashboard",
		),
	}
}

// GetFilename returns the filename for this dashboard
func (d *HostOvsDashboard) GetFilename() string {
	return d.BaseSdnDashboard.GetFilename()
}

// SetMode sets the build mode for the dashboard
func (d *HostOvsDashboard) SetMode(mode lib.BuildMode) {
	d.BaseSdnDashboard.SetMode(mode)
}

// GetBaseSdnDashboard returns the embedded base dashboard for accessing common fields
func (d *HostOvsDashboard) GetBaseSdnDashboard() *lib.BaseSdnDashboard {
	return &d.BaseSdnDashboard
}

// Build implements the interface method to build the actual dashboard
func (d *HostOvsDashboard) Build() (dashboard.Dashboard, error) {
	d.Builder = dashboard.NewDashboardBuilder(d.Title).
		Uid(d.Uid).
		Tags(d.Tags).
		Editable().
		Time("now-1h", "now").
		Timezone(common.TimeZoneBrowser).
		// Variables
		WithVariable(lib.ClusterVariable(d)).
		WithVariable(lib.InstanceVariable(d)).
		WithVariable(hostOvsBridgeVariable(d)).
		WithVariable(hostOvsInterfaceVariable(d)).
		WithVariable(hostOvsTopKVariable()).
		WithVariable(hostOvsTopPMDNodeVariable(d)).
		WithVariable(hostOvsTopConntrackNodeVariable(d)).
		// Info Row
		WithRow(dashboard.NewRowBuilder("Info").Id(1)).
		WithPanel(hostOvsBuildInfoTable(d)).
		WithPanel(hostOvsInterfaceDriverVersionTable(d)).
		WithPanel(hostOvsInterfaceFirmwareVersionTable(d)).
		WithPanel(hostOvsNodeDatapathTypeTable(d)).
		// OVS Daemon Row (collapsed)
		WithRow(dashboard.NewRowBuilder("OVS Daemon (TOP K)").Id(2).Collapsed(true).
			WithPanel(hostOvsCpuUtilizationRate(d)).
			WithPanel(hostOvsMemoryUtilization(d)).
			WithPanel(hostOvsFlowLookupsFailedDiscarded(d)).
			WithPanel(hostOvsFlowLookupsUserspace(d)).
			WithPanel(hostOvsFlowLookupsKernel(d)).
			WithPanel(hostOvsTotalOffloadedFlows(d)).
			WithPanel(hostOvsPortActionAddRate(d)).
			WithPanel(hostOvsPortActionDelRate(d)).
			WithPanel(hostOvsDpActionOversizedRate(d)).
			WithPanel(hostOvsDpOpenFlowGetRate(d)).
			WithPanel(hostOvsDpOpenFlowPutRate(d)).
			WithPanel(hostOvsDpOpenFlowDelRate(d)).
			WithPanel(hostOvsNetlinkIORate(d)).
			WithPanel(hostOvsTranslationActionsRate(d)).
			WithPanel(hostOvsRconnSentQueued(d)).
			WithPanel(hostOvsTxnSuccessRate(d)).
			WithPanel(hostOvsAbnormalTransactions(d)).
			WithPanel(hostOvsVconnIORate(d)).
			WithPanel(hostOvsAbnormalConnections(d))).
		// OVS DB Row (collapsed)
		WithRow(dashboard.NewRowBuilder("OVS DB (TOP K)").Id(3).Collapsed(true).
			WithPanel(hostOvsDbCpuUtilizationRate(d)).
			WithPanel(hostOvsDbMemoryUtilization(d)).
			WithPanel(hostOvsJsonRpcDetails(d))).

		// Datapath Row (collapsed)
		WithRow(dashboard.NewRowBuilder("Datapath (TOP K)").Id(4).Collapsed(true).
			WithPanel(hostOvsDpPacketsRate(d)).
			WithPanel(hostOvsPortsConnected(d)).
			WithPanel(hostOvsMasksHitRatio(d)).
			WithPanel(hostOvsTotalDpFlows(d)).
			WithPanel(hostOvsMasks(d)).
			WithPanel(hostOvsUpcallFlowDelRev(d)).
			WithPanel(hostOvsUpcallFlowDelNoRev(d)).
			WithPanel(hostOvsUpcallFlowDelIdleLimit(d)).
			WithPanel(hostOvsRevalidateMissedDpFlow(d))).
		// Bridge Row (collapsed)
		WithRow(dashboard.NewRowBuilder("Bridge (TOP K)").Id(5).Collapsed(true).
			WithPanel(hostOvsTotalBridgeFlows(d)).
			WithPanel(hostOvsBridgePorts(d)).
			WithPanel(hostOvsTotalBridgeFlowsRate(d))).
		// Interface Row (collapsed)
		WithRow(dashboard.NewRowBuilder("Interface (TOP K)").Id(6).Collapsed(true).
			WithPanel(hostOvsLinkResetsRate(d)).
			WithPanel(hostOvsCollisionsRate(d)).
			WithPanel(hostOvsOverrunErrorsRate(d)).
			WithPanel(hostOvsFrameErrorsRate(d)).
			WithPanel(hostOvsCrcErrorsRate(d)).
			WithPanel(hostOvsInterfaceBandwidth(d)).
			WithPanel(hostOvsNetworkPacketRate(d)).
			WithPanel(hostOvsPacketIOErrorRate(d)).
			WithPanel(hostOvsPacketIODroppedRate(d)).
			WithPanel(hostOvsVfMisspktsDropRate(d))).
		// Offloads Row (collapsed)
		WithRow(dashboard.NewRowBuilder("Offloads (TOP K)").Id(7).Collapsed(true).
			WithPanel(hostOvsHwOffloadRateBytes(d)).
			WithPanel(hostOvsHwOffloadRatePackets(d)).
			WithPanel(hostOvsSwOffloadRateBytes(d)).
			WithPanel(hostOvsSwOffloadRatePackets(d)).
			WithPanel(hostOvsSwBusyRateBytes(d)).
			WithPanel(hostOvsSwBusyRatePackets(d))).
		// OVS DOCA Row (collapsed)
		WithRow(dashboard.NewRowBuilder("OVS DOCA (TOP K)").Id(8).Collapsed(true).
			WithPanel(hostOvsFlowTableMissesUpcallFailed(d)).
			WithPanel(hostOvsFlowTableMissesUpcallSucceeded(d)).
			WithPanel(hostOvsFlowTableMatched(d)).
			WithPanel(hostOvsPmdPacketRxRate(d)).
			WithPanel(hostOvsPmdPacketRxRatePerNode(d)).
			WithPanel(hostOvsPmdBusyCycles(d)).
			WithPanel(hostOvsCompletionQueueStalled(d)).
			WithPanel(hostOvsPmdBusyCyclesPerNode(d)).
			WithPanel(hostOvsPacketsDroppedInvalidPort(d)).
			WithPanel(hostOvsPacketsDroppedNoMark(d)).
			WithPanel(hostOvsEswitchPipeResizeRate(d)).
			WithPanel(hostOvsConntrackConnections(d)).
			WithPanel(hostOvsConntrackEntriesUsage(d)).
			WithPanel(hostOvsConntrackBreakdownByProtocol(d)))

	return d.Builder.Build()
}

// BuildHostOvsDashboard provides backward compatibility
func BuildHostOvsDashboard() (*lib.BaseSdnDashboard, error) {
	dashboardBuilder := NewHostOvsDashboard()
	_, err := dashboardBuilder.Build()
	if err != nil {
		return nil, err
	}
	return &dashboardBuilder.BaseSdnDashboard, nil
}

// Variables
func hostOvsBridgeVariable(d lib.SdnDashboard) *dashboard.QueryVariableBuilder {
	return dashboard.NewQueryVariableBuilder("bridge").
		Label("Bridge").
		Query(dashboard.StringOrMap{String: cog.ToPtr("label_values(ovs_vswitchd_bridge{cluster=\"$cluster\", instance=~\"$instance\"},bridge)")}).
		Datasource(d.DataSourceRef()).
		Current(dashboard.VariableOption{
			Selected: cog.ToPtr(false),
			Text:     dashboard.StringOrArrayOfString{String: cog.ToPtr("All")},
			Value:    dashboard.StringOrArrayOfString{ArrayOfString: []string{"$__all"}},
		}).
		Refresh(dashboard.VariableRefreshOnTimeRangeChanged).
		Sort(dashboard.VariableSortAlphabeticalAsc).
		Multi(false).
		IncludeAll(true).
		AllValue(".*")
}

func hostOvsInterfaceVariable(d lib.SdnDashboard) *dashboard.QueryVariableBuilder {
	return dashboard.NewQueryVariableBuilder("interface").
		Label("Interface").
		Query(dashboard.StringOrMap{String: cog.ToPtr("label_values(ovs_vswitchd_interface_admin_state{cluster=\"$cluster\", instance=~\"$instance\", bridge=~\"$bridge\"},interface)")}).
		Datasource(d.DataSourceRef()).
		Current(dashboard.VariableOption{
			Selected: cog.ToPtr(false),
			Text:     dashboard.StringOrArrayOfString{String: cog.ToPtr("All")},
			Value:    dashboard.StringOrArrayOfString{ArrayOfString: []string{"$__all"}},
		}).
		Refresh(dashboard.VariableRefreshOnTimeRangeChanged).
		Sort(dashboard.VariableSortAlphabeticalAsc).
		Multi(false).
		IncludeAll(true).
		AllValue(".*")
}

func hostOvsTopPMDNodeVariable(d lib.SdnDashboard) *dashboard.QueryVariableBuilder {
	return dashboard.NewQueryVariableBuilder("topPMDNode").
		Query(dashboard.StringOrMap{String: cog.ToPtr(`query_result(topk(4, sum(rate(ovs_vswitchd_poll_threads_packets_total{cluster="$cluster", instance=~"$instance"}[2m])) by (instance)))`)}).
		Datasource(d.DataSourceRef()).
		Description("Top 4 nodes have the highest PMD packets rate.").
		Hide(dashboard.VariableHideHideVariable).
		Current(dashboard.VariableOption{
			Selected: cog.ToPtr(false),
			Text:     dashboard.StringOrArrayOfString{String: cog.ToPtr("All")},
			Value:    dashboard.StringOrArrayOfString{ArrayOfString: []string{"$__all"}},
		}).
		AllowCustomValue(false).
		Refresh(dashboard.VariableRefreshOnDashboardLoad).
		Sort(dashboard.VariableSortAlphabeticalAsc).
		Multi(true).
		IncludeAll(true).
		AllValue(".*").
		Regex(`/.*instance="([^"]*).*/`)
}

func hostOvsTopConntrackNodeVariable(d lib.SdnDashboard) *dashboard.QueryVariableBuilder {
	return dashboard.NewQueryVariableBuilder("topConntrackNode").
		Query(dashboard.StringOrMap{String: cog.ToPtr(`query_result(topk(4, ovs_vswitchd_conntrack_n_connections{cluster="$cluster", instance=~"$instance"} / ovs_vswitchd_conntrack_connection_limit{cluster="$cluster", instance=~"$instance"}))`)}).
		Datasource(d.DataSourceRef()).
		Description("Top 4 nodes have the highest conntrack entries usage.").
		Hide(dashboard.VariableHideHideVariable).
		Current(dashboard.VariableOption{
			Selected: cog.ToPtr(false),
			Text:     dashboard.StringOrArrayOfString{String: cog.ToPtr("All")},
			Value:    dashboard.StringOrArrayOfString{ArrayOfString: []string{"$__all"}},
		}).
		AllowCustomValue(false).
		Refresh(dashboard.VariableRefreshOnDashboardLoad).
		Sort(dashboard.VariableSortAlphabeticalAsc).
		Multi(true).
		IncludeAll(true).
		AllValue(".*").
		Regex(`/.*instance="([^"]*).*/`)
}

func hostOvsTopKVariable() *dashboard.CustomVariableBuilder {
	return dashboard.NewCustomVariableBuilder("topk").
		Label("topk").
		Values(dashboard.StringOrMap{String: cog.ToPtr("5, 10, 20, 100, 1000, 2000")}).
		Current(dashboard.VariableOption{
			Selected: cog.ToPtr(true),
			Text:     dashboard.StringOrArrayOfString{String: cog.ToPtr("5")},
			Value:    dashboard.StringOrArrayOfString{String: cog.ToPtr("5")},
		})
}

// Info Panel functions
func hostOvsBuildInfoTable(d lib.SdnDashboard) *table.PanelBuilder {
	return table.NewPanelBuilder().
		Title("OVS BUILD INFO").
		Description("Shows the summary of various ovs versions and number of nodes having a specific OVS version").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum(ovs_build_info{cluster="$cluster"}) by (version)`).
				Format(prometheus.PromQueryFormatTable).
				Instant().
				RefId("A"),
		)
}

func hostOvsInterfaceDriverVersionTable(d lib.SdnDashboard) *table.PanelBuilder {
	return table.NewPanelBuilder().
		Title("INTERFACE DRIVER VERSION").
		Description("Shows summary of various Interface's network driver versions with number of interfaces having that specific version").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum(ovs_vswitchd_interface_driver_version{cluster="$cluster"}) by (bridge, version)`).
				Format(prometheus.PromQueryFormatTable).
				Instant().
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum by (bridge,  version) (label_replace(ovs_vswitchd_interface_info{cluster="$cluster"}, "version", "$1", "driver_version", "(.*)"))`).
				Format(prometheus.PromQueryFormatTable).
				Instant().
				RefId("B"),
		).
		WithTransformation(dashboard.DataTransformerConfig{
			Id:      "merge",
			Options: map[string]any{},
		}).
		WithTransformation(dashboard.DataTransformerConfig{
			Id: "calculateField",
			Options: map[string]any{
				"mode": "reduceRow",
				"reduce": map[string]any{
					"reducer": "sum",
					"include": []string{
						"Value #B",
						"Value #A",
					},
				},
				"alias": "Interface",
			},
		}).
		WithTransformation(dashboard.DataTransformerConfig{
			Id: "filterFieldsByName",
			Options: map[string]any{
				"excludeByName": map[string]any{
					"Value #A": true,
					"Value #B": true,
					"Time":     true,
				},
				"indexByName":   map[string]any{},
				"renameByName":  map[string]any{},
				"includeByName": map[string]any{},
				"include": map[string]any{
					"names": []string{
						"bridge",
						"version",
						"Interface",
					},
				},
			},
		})
}

func hostOvsInterfaceFirmwareVersionTable(d lib.SdnDashboard) *table.PanelBuilder {
	return table.NewPanelBuilder().
		Title("INTERFACE FIRMWARE VERSION").
		Description("Shows summary of various Firmware version of the network adapter and number of interfaces having that specific versions").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum(ovs_vswitchd_interface_firmware_version{cluster="$cluster"}) by (bridge, version)`).
				Format(prometheus.PromQueryFormatTable).
				Instant().
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum by (bridge,  version) (label_replace(ovs_vswitchd_interface_info{cluster="$cluster"}, "version", "$1", "firmware_version", "(.*)"))`).
				Format(prometheus.PromQueryFormatTable).
				Instant().
				RefId("B"),
		).
		WithTransformation(dashboard.DataTransformerConfig{
			Id:      "merge",
			Options: map[string]any{},
		}).
		WithTransformation(dashboard.DataTransformerConfig{
			Id: "calculateField",
			Options: map[string]any{
				"mode": "reduceRow",
				"reduce": map[string]any{
					"reducer": "sum",
					"include": []string{
						"Value #B",
						"Value #A",
					},
				},
				"alias": "Interface",
			},
		}).
		WithTransformation(dashboard.DataTransformerConfig{
			Id: "filterFieldsByName",
			Options: map[string]any{
				"excludeByName": map[string]any{
					"Value #A": true,
					"Value #B": true,
					"Time":     true,
				},
				"indexByName":   map[string]any{},
				"renameByName":  map[string]any{},
				"includeByName": map[string]any{},
				"include": map[string]any{
					"names": []string{
						"bridge",
						"version",
						"Interface",
					},
				},
			},
		})
}

func hostOvsNodeDatapathTypeTable(d lib.SdnDashboard) *table.PanelBuilder {
	return table.NewPanelBuilder().
		Title("Node OVS Datapath Type").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`label_replace(sum(ovs_vswitchd_poll_threads_n{cluster="$cluster"}) by (instance), "DP", "ovs-doca", "", ".*")`).
				Format(prometheus.PromQueryFormatTable).
				Instant().
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`label_replace(sum(ovs_vswitchd_dp{cluster="$cluster"} unless on(instance) ovs_vswitchd_poll_threads_n{cluster="$cluster"}) by (instance), "DP", "ovs-kernel", "", ".*")`).
				Format(prometheus.PromQueryFormatTable).
				Instant().
				RefId("B"),
		).
		WithTransformation(dashboard.DataTransformerConfig{
			Id:      "merge",
			Options: map[string]any{},
		}).
		WithTransformation(dashboard.DataTransformerConfig{
			Id: "organize",
			Options: map[string]any{
				"excludeByName": map[string]any{
					"Time":     true,
					"Value #A": true,
					"Value #B": true,
				},
				"includeByName": map[string]any{},
				"indexByName": map[string]any{
					"instance": 1,
					"DP":       2,
				},
				"renameByName": map[string]any{},
			},
		})
}

// OVS Daemon Panel functions
func hostOvsCpuUtilizationRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("CPU Utilization Rate (TOP K)").
		Description("ovs-vswitchd process CPU usage").
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(24).
		Unit("percent").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, sum(rate(namedprocess_namegroup_cpu_seconds_total{cluster="$cluster", groupname="ovs-vswitchd",instance=~"$instance"}[$__rate_interval])) by (instance) * 100)`).
				LegendFormat("{{instance}}").
				RefId("A"),
		).
		Interval("120s")
}

func hostOvsMemoryUtilization(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Memory Utilization (TOP K)").
		Description("ovs-vswitchd process RSS memory").
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(24).
		Unit("decbytes").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, namedprocess_namegroup_memory_bytes{cluster="$cluster", job="process-exporter",groupname="ovs-vswitchd",pod=~"process-exporter-.*",memtype="resident",instance=~"$instance"})`).
				LegendFormat("{{instance}}").
				RefId("A"),
		).
		Interval("120s")
}

func hostOvsFlowLookupsFailedDiscarded(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Flow Lookups Failed and Discarded (worst) (TOP K)").
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(24).
		ThresholdsStyle(
			common.NewGraphThresholdsStyleConfigBuilder().
				Mode(common.GraphThresholdsStyleModeLineAndArea),
		).
		Thresholds(
			dashboard.NewThresholdsConfigBuilder().
				Mode(dashboard.ThresholdsModeAbsolute).
				Steps([]dashboard.Threshold{
					{
						Color: "transparent",
					},
					{
						Value: cog.ToPtr(50.0),
						Color: "red",
					},
				}),
		).
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, sum(rate(ovs_vswitchd_dp_flows_lookup_lost{cluster="$cluster", instance=~"$instance"}[$__rate_interval])) by (instance))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, sum(rate(ovs_vswitchd_datapath_lost_total{cluster="$cluster", instance=~"$instance"}[$__rate_interval])) by (instance))`).
				LegendFormat("{{instance}}").
				RefId("B"),
		)
}

func hostOvsFlowLookupsUserspace(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Flow Lookups Processed in Userspace (bad) (TOP K)").
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(24).
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, sum(rate(ovs_vswitchd_dp_flows_lookup_missed{cluster="$cluster", instance=~"$instance"}[$__rate_interval])) by (instance))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, sum(rate(ovs_vswitchd_datapath_missed_total{cluster="$cluster", instance=~"$instance"}[$__rate_interval])) by (instance))`).
				LegendFormat("{{instance}}").
				RefId("B"),
		)
}

func hostOvsFlowLookupsKernel(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Flow Lookups Processed in Kernel (TOP K)").
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(24).
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, sum(rate(ovs_vswitchd_dp_flows_lookup_hit{cluster="$cluster", instance=~"$instance"}[$__rate_interval])) by (instance))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, sum(rate(ovs_vswitchd_datapath_hit_total{cluster="$cluster", instance=~"$instance"}[$__rate_interval])) by (instance))`).
				LegendFormat("{{instance}}").
				RefId("B"),
		)
}

func hostOvsTotalOffloadedFlows(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Total Offloaded Flows (TOP K)").
		Description("Represents the total offloaded flows in datapath.").
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(24).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, sum(ovs_vswitchd_dp_offloaded_flows_total{cluster="$cluster", instance=~"$instance"}) by (instance))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsPortActionAddRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("PORT ACTION ADD RATE (TOP K)").
		Description("Rate of number of times a netdev was added as a port from the dpif").
		Datasource(d.DataSourceRef()).
		Height(9).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_dpif_port_add{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsPortActionDelRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("PORT ACTION DEL RATE (TOP K)").
		Description("Rate of number of times a netdev was removed as a port from the dpif").
		Datasource(d.DataSourceRef()).
		Height(9).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_dpif_port_del{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsDpActionOversizedRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("DP ACTION OVERSIZED RATE ( TOP K )").
		Description("rate of number of times the translated OpenFlow actions into a datapath actions were too big for a netlink attribute").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_xlate_actions_oversize{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsDpOpenFlowGetRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("DP OPEN FLOW GET RATE ( TOP K)").
		Description("Rate of number of times flows were retrived from the datapath (Linux kernel datapath module)").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("locale").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_dpif_flow_get{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsDpOpenFlowPutRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("DP OPEN FLOW PUT RATE (TOP K)").
		Description("Rate of number of times flows were added to the datapath (Linux kernel datapath module)").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("locale").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_dpif_flow_put{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsDpOpenFlowDelRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("DP OPEN FLOW DEL RATE ( TOP K)").
		Description("Rate of number of times flows were deleted from the datapath (Linux kernel datapath module)").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("locale").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_dpif_flow_del{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsTranslationActionsRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("TRANSLATION ACTIONS RATE (TOP K)").
		Description("Rate of OpenFlow actions were translated into datapath actions").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_xlate_actions{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsNetlinkIORate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("NETLINK I/O RATE (TOP K AVG)").
		Description("Rate of netlink messages sent/received by the kernel").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("mps").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`avg(topk($topk, rate(ovs_vswitchd_netlink_sent{cluster="$cluster", instance=~"$instance"}[5m])))`).
				LegendFormat("Sent").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`avg(topk($topk, rate(ovs_vswitchd_netlink_received{cluster="$cluster", instance=~"$instance"}[5m])))`).
				LegendFormat("Received").
				RefId("B"),
		)
}

func hostOvsRconnSentQueued(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("RCONN SENT/QUEUED (TOP K)").
		Description("Specifies the number of messages that have been sent to the underlying virtual connection (unix, tcp, or ssl) to OpenFlow devices, or queued  because it couldn't be sent using the underlying virtual connection to OpenFlow devices.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("mps").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_rconn_sent{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("Sent, {{instance}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_rconn_queued{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("Queued, {{instance}}").
				RefId("B"),
		)
}

func hostOvsTxnSuccessRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("TXN SUCCESS RATE (TOP K)").
		Description("Specifies the rate of the OVSDB transaction has successfully completed").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_txn_success{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsAbnormalTransactions(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("ABNORMAL TRANSACTIONS").
		Description("Number of abnormal transactions").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum(ovs_vswitchd_txn_error{cluster="$cluster", instance=~"$instance"})`).
				LegendFormat("Error").
				RefId("D"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum(ovs_vswitchd_txn_aborted{cluster="$cluster", instance=~"$instance"})`).
				LegendFormat("Aborted").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum(ovs_vswitchd_txn_try_again{cluster="$cluster", instance=~"$instance"})`).
				LegendFormat("Try Again").
				RefId("B"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum(ovs_vswitchd_txn_uncommitted{cluster="$cluster", instance=~"$instance"})`).
				LegendFormat("Uncommitted").
				RefId("C"),
		)
}

func hostOvsVconnIORate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("VCONN I/O RATE (TOP K)").
		Description("Specifies the number of messages sent to/received from the OpenFlow Device").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("mps").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_vconn_sent{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("Sent, {{instance}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_vconn_received{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("Received, {{instance}}").
				RefId("B"),
		)
}

func hostOvsAbnormalConnections(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("ABNORMAL CONNECTIONS").
		Description("Number of messages that have been dropped causing by flushed queue when reconnecting or queue overflow.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum(ovs_vswitchd_rconn_discarded{cluster="$cluster", instance=~"$instance"})`).
				LegendFormat("Discarded").
				RefId("B"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum(ovs_vswitchd_rconn_overflow{cluster="$cluster", instance=~"$instance"})`).
				LegendFormat("Overflow").
				RefId("C"),
		)
}

// OVS DB Panel functions
func hostOvsJsonRpcDetails(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("JSONRPC DETAILS (TOP K)").
		Description("The JSON RPC requests processed per second").
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(12).
		Unit("reqps").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_ovsdb_unixctl_received{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("RX:{{instance}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_ovsdb_unixctl_replied{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("TX:{{instance}}").
				RefId("B"),
		)
}

func hostOvsDbCpuUtilizationRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("CPU Utilization Rate (TOP K)").
		Description("TOP K of ovsdb-server process CPU usage").
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(12).
		Unit("percentunit").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, sum(rate(namedprocess_namegroup_cpu_seconds_total{cluster="$cluster", groupname="ovsdb-server",instance=~"$instance"}[$__rate_interval])) by (instance) * 100)`).
				LegendFormat("{{instance}}").
				RefId("A"),
		).
		Interval("120s")
}

func hostOvsDbMemoryUtilization(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Memory Utilization (TOP K)").
		Description("TOP K of ovsdb-server process RSS memory").
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(12).
		Unit("decbytes").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, namedprocess_namegroup_memory_bytes{cluster="$cluster", job="process-exporter",groupname="ovsdb-server",pod=~"process-exporter-.*",memtype="resident",instance=~"$instance"})`).
				LegendFormat("{{instance}}").
				RefId("A"),
		).
		Interval("120s")
}

// Datapath Panel functions
func hostOvsDpPacketsRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("DP PACKETS RATE (TOP K)").
		Description("Represents the total number of packets datapath processed which is the sum of \"hit\" and \"missed\"").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Min(0).
		Unit("pps").
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_dp_packets_total{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_datapath_packets_total{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("B"),
		)
}

func hostOvsPortsConnected(d lib.SdnDashboard) *bargauge.PanelBuilder {
	return bargauge.NewPanelBuilder().
		Title("PORTS CONNECTED (TOP K)").
		Description("Represents the number of ports connected to the datapath").
		Datasource(d.DataSourceRef()).
		Legend(common.NewVizLegendOptionsBuilder().
			DisplayMode(common.LegendDisplayModeList).
			Placement(common.LegendPlacementBottom).
			ShowLegend(true),
		).
		Height(8).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, ovs_vswitchd_dp_if_total{cluster="$cluster", instance=~"$instance"})`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsMasksHitRatio(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("MASKS HIT RATIO (TOP K)").
		Description("Represents the average number of masks visited per packet").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, ovs_vswitchd_dp_masks_hit_ratio{cluster="$cluster", instance=~"$instance"})`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsTotalDpFlows(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("TOTAL DP FLOWS (TOP K)").
		Description("Represents the number of flows in datapath.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Min(0).
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, ovs_vswitchd_dp_flows_total{cluster="$cluster", instance=~"$instance"})`).
				LegendFormat("{{instance}}").
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, ovs_vswitchd_datapath_n_flows{cluster="$cluster", instance=~"$instance"})`).
				LegendFormat("{{instance}}").
				RefId("B"),
		)
}

func hostOvsMasks(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("MASKS (TOP K)").
		Description("Represents the number of masks in a datapath").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, ovs_vswitchd_dp_masks_total{cluster="$cluster", instance=~"$instance"})`).
				LegendFormat("{{instance}}").
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, ovs_vswitchd_datapath_n_masks{cluster="$cluster", instance=~"$instance"})`).
				LegendFormat("{{instance}}").
				RefId("B"),
		)
}

func hostOvsUpcallFlowDelRev(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("upcall_flow_del_rev (TOP K)").
		Description("Rate of flows deleted per second after revalidation determined to delete the flow").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("ops").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_upcall_flow_del_rev{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsUpcallFlowDelNoRev(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("upcall_flow_del_no_rev (TOP K)").
		Description("Rate of flows deleted per second because the flow didn't have min-revalidator-pps fulfilled").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_upcall_flow_del_no_rev{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsUpcallFlowDelIdleLimit(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("upcall_flow_del_idle_or_limit (TOP K)").
		Description("Rate of flows deleted per second because we reached the limit or the flow was idle for more than max-idle time").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_upcall_flow_del_idle_or_limit{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsRevalidateMissedDpFlow(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("revalidate_missed_dp_flow (TOP K)").
		Description("Rate of missed datapath flows per second that had to be revalidated").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_revalidate_missed_dp_flow{cluster="$cluster", instance=~"$instance"}[5m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

// Bridge Panel functions
func hostOvsTotalBridgeFlows(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("TOTAL BRIDGE FLOWS (TOP K)").
		Description("Total number of OpenFlow flows on the OVS bridge").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, ovs_vswitchd_bridge_flows_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge"})`).
				LegendFormat("{{instance}}/{{bridge}}").
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, ovs_vswitchd_bridge_n_flows{cluster="$cluster", instance=~"$instance", name=~"$bridge"})`).
				LegendFormat("{{instance}}/{{name}}").
				RefId("B"),
		)
}

func hostOvsBridgePorts(d lib.SdnDashboard) *bargauge.PanelBuilder {
	return bargauge.NewPanelBuilder().
		Title("BRIDGE PORTS (TOP K)").
		Description("Number of OVS ports on the bridge").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, ovs_vswitchd_bridge_ports_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge"})`).
				LegendFormat("{{instance}}/{{bridge}}").
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, ovs_vswitchd_bridge_n_ports{cluster="$cluster", instance=~"$instance", name=~"$bridge"})`).
				LegendFormat("{{instance}}/{{name}}").
				RefId("B"),
		)
}

func hostOvsTotalBridgeFlowsRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("TOTAL BRIDGE FLOWS RATE (TOP K)").
		Description("Rate of total number of OpenFlow flows on the OVS bridge").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("ops").
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_bridge_flows_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge"}[5m]))`).
				LegendFormat("{{instance}}/{{bridge}}").
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_bridge_n_flows{cluster="$cluster", instance=~"$instance", name=~"$bridge"}[5m]))`).
				LegendFormat("{{instance}}/{{name}}").
				RefId("B"),
		)
}

// Interface Panel functions
func hostOvsLinkResetsRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("LINK RESETS RATE (TOP K)").
		Description("Represents rate of number of times Open vSwitch has observed the link_state of OVS interface change.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_link_resets{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", interface=~"$interface"}[5m]))`).
				LegendFormat("{{instance}}/{{bridge}}/{{interface}}").
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_link_resets_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", name=~"$interface"}[5m]))`).
				LegendFormat("{{instance}}/{{bridge}}/{{name}}").
				RefId("B"),
		)
}

func hostOvsCollisionsRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("COLLISIONS RATE (TOP K)").
		Description("Represents rate of number of collisions on interfaces").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_collisions{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", interface=~"$interface"}[5m]))`).
				LegendFormat("{{instance}}/{{bridge}}/{{interface}}").
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_collisions_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", name=~"$interface"}[5m]))`).
				LegendFormat("{{instance}}/{{bridge}}/{{name}}").
				RefId("B"),
		)
}

func hostOvsOverrunErrorsRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("OVERRUN ERRORS RATE (TOP K)").
		Description("Represents rate of RX overrun errors").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("error/s").
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_over_err{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", interface=~"$interface"}[5m]))`).
				LegendFormat("{{instance}}/{{bridge}}/{{interface}}").
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_over_errors_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", name=~"$interface"}[5m]))`).
				LegendFormat("{{instance}}/{{bridge}}/{{name}}").
				RefId("B"),
		)
}

func hostOvsFrameErrorsRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("FRAME ERRORS RATE (TOP K)").
		Description("Represents rate of frame errors").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("error/s").
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_frame_err{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", interface=~"$interface"}[5m]))`).
				LegendFormat("{{instance}}/{{bridge}}/{{interface}}").
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_frame_errors_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", name=~"$interface"}[5m]))`).
				LegendFormat("{{instance}}/{{bridge}}/{{name}}").
				RefId("B"),
		)
}

func hostOvsCrcErrorsRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("CRC ERRORS RATE (TOP K)").
		Description("Represents rate of CRC errors").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("error/s").
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_crc_err{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", interface=~"$interface"}[5m]))`).
				LegendFormat("{{instance}}/{{bridge}}/{{interface}}").
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_crc_errors_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", name=~"$interface"}[5m]))`).
				LegendFormat("{{instance}}/{{bridge}}/{{name}}").
				RefId("B"),
		)
}

func hostOvsInterfaceBandwidth(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("INSTANCE - INTERFACE BANDWIDTH (TOP K)").
		Description("Represents total received/transmitted bytes by OVS interface.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("Bps").
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_bytes{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", interface=~"$interface"}[5m]))`).
				LegendFormat("RX: {{instance}}/{{interface}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_bytes{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", interface=~"$interface"}[5m]))`).
				LegendFormat("TX: {{instance}}/{{interface}}").
				RefId("B"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_bytes_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", name=~"$interface"}[5m]))`).
				LegendFormat("RX: {{instance}}/{{name}}").
				RefId("C"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_bytes_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", name=~"$interface"}[5m]))`).
				LegendFormat("TX: {{instance}}/{{name}}").
				RefId("D"),
		)
}

func hostOvsNetworkPacketRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("NETWORK PACKET RATE (TOP K)").
		Description("Represents the rate of received/transmitted packets by OVS interface.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("pps").
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_packets{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", interface=~"$interface"}[5m]))`).
				LegendFormat("RX: {{instance}}/{{bridge}}/{{interface}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_packets{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", interface=~"$interface"}[5m]))`).
				LegendFormat("TX: {{instance}}/{{bridge}}/{{interface}}").
				RefId("B"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_packets_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", name=~"$interface"}[5m]))`).
				LegendFormat("RX: {{instance}}/{{bridge}}/{{name}}").
				RefId("C"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_packets_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", name=~"$interface"}[5m]))`).
				LegendFormat("TX: {{instance}}/{{bridge}}/{{name}}").
				RefId("D"),
		)
}

func hostOvsPacketIOErrorRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("PACKET I/O ERROR  RATE (TOP K)").
		Description("Rate of error received/transmitted packets by OVS interface.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("pps").
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_errors{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", interface=~"$interface"}[5m]))`).
				LegendFormat("RX: {{instance}}/{{bridge}}/{{interface}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_errors{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", interface=~"$interface"}[5m]))`).
				LegendFormat("TX: {{instance}}/{{bridge}}/{{interface}}").
				RefId("B"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_errors_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", name=~"$interface"}[5m]))`).
				LegendFormat("RX: {{instance}}/{{bridge}}/{{name}}").
				RefId("C"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_errors_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", name=~"$interface"}[5m]))`).
				LegendFormat("TX: {{instance}}/{{bridge}}/{{name}}").
				RefId("D"),
		)
}

func hostOvsPacketIODroppedRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("PACKET I/O DROPPED RATE (TOP K)").
		Description("Rate of dropped received/transmitted packets by OVS interface.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("pps").
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_dropped{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", interface=~"$interface"}[5m]))`).
				LegendFormat("RX: {{instance}}/{{bridge}}/{{interface}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_dropped{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", interface=~"$interface"}[5m]))`).
				LegendFormat("TX, {{instance}}, {{bridge}}, {{interface}}").
				RefId("B"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_dropped_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", name=~"$interface"}[5m]))`).
				LegendFormat("RX: {{instance}}/{{bridge}}/{{name}}").
				RefId("C"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_dropped_total{cluster="$cluster", instance=~"$instance", bridge=~"$bridge", name=~"$interface"}[5m]))`).
				LegendFormat("TX: {{instance}}/{{bridge}}/{{name}}").
				RefId("D"),
		)
}

func hostOvsVfMisspktsDropRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("VF MISSPKTS DROP RATE (TOP K)").
		Description("Rate of number of new connection packets dropped on the interface backed by VF").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_misspkts_packets_drops {cluster="$cluster", instance=~"$instance", bridge=~"$bridge", interface=~"$interface"}[5m]))`).
				LegendFormat("{{instance}}/{{bridge}}/{{interface}}").
				RefId("A"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_misspkts_packets_drops {cluster="$cluster", instance=~"$instance", bridge=~"$bridge", name=~"$interface"}[5m]))`).
				LegendFormat("{{instance}}/{{bridge}}/{{name}}").
				RefId("B"),
		)
}

// Offloads Panel functions
func hostOvsHwOffloadRateBytes(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("TOP K HW OFFLOAD RATE (BYTES)").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("binBps").
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_total_bytes{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[$__rate_interval]) - rate(ovs_vswitchd_interface_rx_sw_bytes{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[$__rate_interval]))`).
				LegendFormat("RX: {{instance}}/{{interface}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_total_bytes{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[$__rate_interval]) - rate(ovs_vswitchd_interface_tx_sw_bytes{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[$__rate_interval]))`).
				LegendFormat("TX: {{instance}}/{{interface}}").
				RefId("B"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_total_bytes{cluster="$cluster", instance=~"$instance",name=~"$interface"}[$__rate_interval]) - rate(ovs_vswitchd_interface_rx_sw_bytes{cluster="$cluster", instance=~"$instance",name=~"$interface"}[$__rate_interval]))`).
				LegendFormat("RX: {{instance}}/{{name}}").
				RefId("C"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_total_bytes{cluster="$cluster", instance=~"$instance",name=~"$interface"}[$__rate_interval]) - rate(ovs_vswitchd_interface_tx_sw_bytes{cluster="$cluster", instance=~"$instance",name=~"$interface"}[$__rate_interval]))`).
				LegendFormat("TX: {{instance}}/{{name}}").
				RefId("D"),
		)
}

func hostOvsHwOffloadRatePackets(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("TOP K HW OFFLOAD RATE (PACKETS)").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("pps").
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_total_packets{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[$__rate_interval]) - rate(ovs_vswitchd_interface_rx_sw_packets{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[$__rate_interval]))`).
				LegendFormat("RX: {{instance}}/{{interface}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_total_packets{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[$__rate_interval]) - rate(ovs_vswitchd_interface_tx_sw_packets{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[$__rate_interval]))`).
				LegendFormat("TX: {{instance}}/{{interface}}").
				RefId("B"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_total_packets{cluster="$cluster", instance=~"$instance",name=~"$interface"}[$__rate_interval]) - rate(ovs_vswitchd_interface_rx_sw_packets{cluster="$cluster", instance=~"$instance",name=~"$interface"}[$__rate_interval]))`).
				LegendFormat("RX: {{instance}}/{{name}}").
				RefId("C"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_total_packets{cluster="$cluster", instance=~"$instance",name=~"$interface"}[$__rate_interval]) - rate(ovs_vswitchd_interface_tx_sw_packets{cluster="$cluster", instance=~"$instance",name=~"$interface"}[$__rate_interval]))`).
				LegendFormat("TX: {{instance}}/{{name}}").
				RefId("D"),
		)
}

func hostOvsSwOffloadRateBytes(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("TOP K SW OFFLOAD RATE (BYTES)").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("binBps").
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_sw_bytes{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[$__rate_interval]))`).
				LegendFormat("RX: {{instance}}/{{interface}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_sw_bytes{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[$__rate_interval]))`).
				LegendFormat("TX: {{instance}}/{{interface}}").
				RefId("B"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_sw_bytes{cluster="$cluster", instance=~"$instance",name=~"$interface"}[$__rate_interval]))`).
				LegendFormat("RX: {{instance}}/{{name}}").
				RefId("C"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_sw_bytes{cluster="$cluster", instance=~"$instance",name=~"$interface"}[$__rate_interval]))`).
				LegendFormat("TX: {{instance}}/{{name}}").
				RefId("D"),
		)
}

func hostOvsSwOffloadRatePackets(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("TOP K SW OFFLOAD RATE (PACKETS)").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("pps").
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_sw_packets{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[$__rate_interval]))`).
				LegendFormat("RX: {{instance}}/{{interface}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_sw_packets{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[$__rate_interval]))`).
				LegendFormat("TX: {{instance}}/{{interface}}").
				RefId("B"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_sw_packets{cluster="$cluster", instance=~"$instance",name=~"$interface"}[$__rate_interval]))`).
				LegendFormat("RX: {{instance}}/{{name}}").
				RefId("C"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_sw_packets{cluster="$cluster", instance=~"$instance",name=~"$interface"}[$__rate_interval]))`).
				LegendFormat("TX: {{instance}}/{{name}}").
				RefId("D"),
		)
}

func hostOvsSwBusyRateBytes(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("TOP K SW BUSY RATE (BYTES)").
		Description("Top K interfaces that sends greater then 100KB of data per second averaging over 15min interval").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("binBps").
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_sw_bytes{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[5m])>100000)`).
				LegendFormat("RX: {{instance}}/{{interface}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_sw_bytes{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[5m])>100000)`).
				LegendFormat("TX: {{instance}}/{{interface}}").
				RefId("B"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_sw_bytes{cluster="$cluster", instance=~"$instance",name=~"$interface"}[5m])>100000)`).
				LegendFormat("RX: {{instance}}/{{name}}").
				RefId("C"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_sw_bytes{cluster="$cluster", instance=~"$instance",name=~"$interface"}[5m])>100000)`).
				LegendFormat("TX: {{instance}}/{{name}}").
				RefId("D"),
		)
}

func hostOvsSwBusyRatePackets(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("TOP K SW BUSY RATE (PACKETS)").
		Description("Top K interfaces that sends greater then 1000 networking packets of data per second averaging over 15min interval").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("pps").
		// legacy metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_sw_packets{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[5m])>1000)`).
				LegendFormat("RX: {{instance}}/{{interface}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_sw_packets{cluster="$cluster", instance=~"$instance",interface=~"$interface"}[5m])>1000)`).
				LegendFormat("TX: {{instance}}/{{interface}}").
				RefId("B"),
		).
		// OVS native metric
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_rx_sw_packets{cluster="$cluster", instance=~"$instance",name=~"$interface"}[5m])>1000)`).
				LegendFormat("RX: {{instance}}/{{name}}").
				RefId("C"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_interface_tx_sw_packets{cluster="$cluster", instance=~"$instance",name=~"$interface"}[5m])>1000)`).
				LegendFormat("TX: {{instance}}/{{name}}").
				RefId("D"),
		)
}

// OVS DOCA Panel functions
func hostOvsFlowTableMissesUpcallFailed(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Flow table misses and upcall failed (worst) (TOP K)").
		Description("Rate of flow table misses and upcall failed.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(24).
		Unit("pps").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, sum(rate(ovs_vswitchd_poll_threads_lost_total{cluster="$cluster", instance=~"$instance"}[2m])) by (instance))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsFlowTableMissesUpcallSucceeded(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Flow table misses and upcall succeeded (bad) (TOP K)").
		Description("Rate of flow table misses and upcall succeeded.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(24).
		Unit("pps").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, sum(rate(ovs_vswitchd_poll_threads_missed_total{cluster="$cluster", instance=~"$instance"}[2m])) by (instance))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsFlowTableMatched(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Flow table matched (TOP K)").
		Description("Rate of flow table misses and upcall succeeded.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(24).
		Unit("pps").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, sum(rate(ovs_vswitchd_poll_threads_hit_total{cluster="$cluster", instance=~"$instance"}[2m])) by (instance))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsPmdPacketRxRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("PMD Packet RX Rate(TOP K)").
		Description("Rate of received packets.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(24).
		Unit("pps").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, sum(rate(ovs_vswitchd_poll_threads_packets_total{cluster="$cluster", instance=~"$instance"}[2m])) by (instance, core))`).
				LegendFormat("{{instance}}: {{core}}").
				RefId("A"),
		)
}

func hostOvsPmdPacketRxRatePerNode(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("PMD Packet RX Rate Per Node - $topPMDNode").
		Description("Rate of received packets.").
		Datasource(d.DataSourceRef()).
		Height(9).
		Span(24).
		Unit("pps").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, sum(rate(ovs_vswitchd_poll_threads_packets_total{cluster="$cluster", instance=~"$topPMDNode"}[2m])) by (instance, core))`).
				LegendFormat("core: {{core}}").
				RefId("A"),
		)
}

func hostOvsPmdBusyCycles(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("PMD Busy cycles (TOP K)").
		Description("Percent of useful CPU cycles").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("percent").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, sum by (instance, core) (ovs_vswitchd_poll_threads_busy_cycles{cluster="$cluster", instance=~"$instance"}))`).
				LegendFormat("{{instance}}: {{core}}").
				RefId("A"),
		)
}

func hostOvsCompletionQueueStalled(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Completion Queue Stalled (TOP K)").
		Description("Completion queue could not process any packets while the queue was not empty, this cloud be caused by eSwitch pipe resize.\n\nThe ideal rate is 0.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_doca_queue_none_processed{cluster="$cluster", instance=~"$instance"}[2m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsPmdBusyCyclesPerNode(d lib.SdnDashboard) *barchart.PanelBuilder {
	return barchart.NewPanelBuilder().
		Title("PMD Busy cycles Per Node - $topPMDNode").
		Description("Percent of useful CPU cycles").
		Datasource(d.DataSourceRef()).
		Height(10).
		Span(24).
		XField("core").
		Stacking(common.StackingModeNormal).
		ShowValue(common.VisibilityModeNever).
		BarWidth(0.8).
		GroupWidth(0.52).
		Legend(common.NewVizLegendOptionsBuilder().
			DisplayMode(common.LegendDisplayModeList).
			Placement(common.LegendPlacementRight).
			ShowLegend(true),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum by (core) (ovs_vswitchd_poll_threads_busy_cycles{cluster="$cluster", instance=~"$topPMDNode"})`).
				Format(prometheus.PromQueryFormatTable).
				Instant().
				LegendFormat("busy: {{core}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum by (core) (ovs_vswitchd_poll_threads_idle_cycles{cluster="$cluster", instance=~"$topPMDNode"})`).
				Format(prometheus.PromQueryFormatTable).
				Instant().
				LegendFormat("idle: {{core}}").
				RefId("B"),
		)
}

func hostOvsPacketsDroppedInvalidPort(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Packets Dropped - invalid port (TOP K)").
		Description("Number of packets dropped because their receiving port was invalid.\n\nThe ideal rate is 0.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("pps").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_doca_invalid_classify_port{cluster="$cluster", instance=~"$instance"}[2m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsPacketsDroppedNoMark(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Packets Dropped - no mark (TOP K)").
		Description("Number of packets dropped because their receiving port was invalid.\n\nThe ideal rate is 0.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Unit("pps").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_doca_no_mark{cluster="$cluster", instance=~"$instance"}[2m]))`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsEswitchPipeResizeRate(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("eSwitch Pipe Resize Rate (TOP K)").
		Description("Rates of eSwitch resize for:\n- resize: due to reaching the congestion level percentage\n- slow: resize takes over 10ms\n- blocking: not process any packets due to eSwitch pipe resizing\n\nThe ideal rate is 0.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_doca_pipe_resize{cluster="$cluster", instance=~"$instance"}[2m]))`).
				LegendFormat("resize: {{instance}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_doca_pipe_resize_over_10_ms{cluster="$cluster", instance=~"$instance"}[2m]))`).
				LegendFormat("slow: {{instance}}").
				RefId("B"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, rate(ovs_vswitchd_doca_resize_block{cluster="$cluster", instance=~"$instance"}[2m]))`).
				LegendFormat("blocked: {{instance}}").
				RefId("C"),
		)
}

func hostOvsConntrackConnections(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Conntrack - Connections (TOP K)").
		Description("Number of tracked connections.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(24).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, ovs_vswitchd_conntrack_n_connections{cluster="$cluster", instance=~"$instance"})`).
				LegendFormat("{{instance}}").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`min(ovs_vswitchd_conntrack_connection_limit{cluster="$cluster", instance=~"$instance"})`).
				LegendFormat("LIMIT").
				RefId("B"),
		)
}

func hostOvsConntrackEntriesUsage(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Conntrack Entries Usage (TOP K)").
		Description("TOP K Conntrack Entries Usage in OVS DOCA.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(24).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`topk($topk, ovs_vswitchd_conntrack_n_connections{cluster="$cluster", instance=~"$instance"} / ovs_vswitchd_conntrack_connection_limit{cluster="$cluster", instance=~"$instance"})`).
				LegendFormat("{{instance}}").
				RefId("A"),
		)
}

func hostOvsConntrackBreakdownByProtocol(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Conntrack - Breakdown By Protocol - $topConntrackNode").
		Description("Number of tracked connections and Maximum number of connections allowed.\n\nConnections are tracked per-protocol:\n- tcp\n- udp\n- udplite\n- icmp\n- icmp6\n- igmp\n- sctp\n- dccp\n- other\n\nThis panel displays only TCP and UDP, while other protocols are hidden.").
		Datasource(d.DataSourceRef()).
		Height(9).
		Span(24).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`ovs_vswitchd_conntrack_connection_limit{cluster="$cluster", instance=~"$topConntrackNode"}`).
				LegendFormat("LIMIT").
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`ovs_vswitchd_conntrack_n_connections{cluster="$cluster", instance=~"$topConntrackNode"}`).
				LegendFormat("total").
				RefId("B"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`ovs_vswitchd_conntrack_n_tcp{cluster="$cluster", instance=~"$topConntrackNode"}`).
				LegendFormat("tcp").
				RefId("C"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`ovs_vswitchd_conntrack_n_udp{cluster="$cluster", instance=~"$topConntrackNode"}`).
				LegendFormat("udp").
				RefId("D"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`ovs_vswitchd_conntrack_n_udplite{cluster="$cluster", instance=~"$topConntrackNode"}`).
				LegendFormat("udplite").
				RefId("E"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`ovs_vswitchd_conntrack_n_icmp{cluster="$cluster", instance=~"$topConntrackNode"}`).
				LegendFormat("icmp").
				RefId("F"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`ovs_vswitchd_conntrack_n_icmp6{cluster="$cluster", instance=~"$topConntrackNode"}`).
				LegendFormat("icmp6").
				RefId("G"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`ovs_vswitchd_conntrack_n_igmp{cluster="$cluster", instance=~"$topConntrackNode"}`).
				LegendFormat("igmp").
				RefId("H"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`ovs_vswitchd_conntrack_n_sctp{cluster="$cluster", instance=~"$topConntrackNode"}`).
				LegendFormat("sctp").
				RefId("I"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`ovs_vswitchd_conntrack_n_dccp{cluster="$cluster", instance=~"$topConntrackNode"}`).
				LegendFormat("dccp").
				RefId("J"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`ovs_vswitchd_conntrack_n_other{cluster="$cluster", instance=~"$topConntrackNode"}`).
				LegendFormat("other").
				RefId("K"),
		)
}
