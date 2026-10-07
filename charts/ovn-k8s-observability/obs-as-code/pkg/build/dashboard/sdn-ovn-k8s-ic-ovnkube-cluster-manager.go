// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"obs-as-code/pkg/build/dashboard/lib"

	"github.com/grafana/grafana-foundation-sdk/go/common"
	"github.com/grafana/grafana-foundation-sdk/go/dashboard"
	"github.com/grafana/grafana-foundation-sdk/go/prometheus"
	"github.com/grafana/grafana-foundation-sdk/go/stat"
	"github.com/grafana/grafana-foundation-sdk/go/statetimeline"
	"github.com/grafana/grafana-foundation-sdk/go/table"
	"github.com/grafana/grafana-foundation-sdk/go/timeseries"
)

type OvnKubeClusterManagerDashboard struct {
	lib.BaseSdnDashboard
}

func NewOvnKubeClusterManagerDashboard() *OvnKubeClusterManagerDashboard {
	return &OvnKubeClusterManagerDashboard{
		BaseSdnDashboard: lib.NewSdnDashboard("sdn-ovn-k8s-ic-ovnkube-cluster-manager",
			"OVN K8S IC / Ovnkube Cluster Manager",
			"This dashboard shows the status of the OVN K8S IC / Ovnkube Cluster Manager."),
	}
}

// GetBaseSdnDashboard returns the embedded base dashboard for accessing common fields
func (d *OvnKubeClusterManagerDashboard) GetBaseSdnDashboard() *lib.BaseSdnDashboard {
	return &d.BaseSdnDashboard
}

// GetFilename returns the filename for this dashboard
func (d *OvnKubeClusterManagerDashboard) GetFilename() string {
	return d.BaseSdnDashboard.GetFilename()
}

// SetMode sets the build mode for the dashboard
func (d *OvnKubeClusterManagerDashboard) SetMode(mode lib.BuildMode) {
	d.BaseSdnDashboard.SetMode(mode)
}

func (d *OvnKubeClusterManagerDashboard) Build() (dashboard.Dashboard, error) {
	d.Builder = dashboard.NewDashboardBuilder(d.Title).
		Uid(d.Uid).
		Tags(d.Tags).
		Editable().
		Time("now-3h", "now").
		Timezone(common.TimeZoneBrowser).

		// Variables
		WithVariable(lib.ClusterVariable(d)).
		// Pod variable
		WithVariable(lib.PodVariable(d)).

		// Panels
		// Cluster Manager Row
		WithRow(dashboard.NewRowBuilder("Cluster Manager").Id(1)).
		// Build Info Panel
		WithPanel(ovnkubeBuildInfoTable(d)).
		// Leadership Status Panel
		WithPanel(ovnkubeLeaderTimeline(d)).
		// CPU Panel
		WithPanel(ovnkubeCpuTimeseries(d)).
		// Memory Panel
		WithPanel(ovnkubeMemoryTimeseries(d)).
		// Host Subnets Panel
		WithPanel(ovnkubeHostSubnetsTable(d)).
		// Resource Retry Failures Panel
		WithPanel(ovnkubeResourceRetryFailuresTimeseries(d)).
		// Egress IPs Panel
		WithPanel(ovnkubeEgressIpsTimeseries(d))

	return d.Builder.Build()
}

func ovnkubeBuildInfoTable(d lib.SdnDashboard) *table.PanelBuilder {
	return lib.BuildInfoTable(d, "ovnkube-cluster-manager build info.",
		`ovnkube_clustermanager_build_info{cluster="$cluster", namespace="ovn-kubernetes"}`,
		lib.OvnkubeBuildInfoFields()).
		Height(6).
		Span(12)
}

func ovnkubeLeaderStat(d lib.SdnDashboard) *stat.PanelBuilder {
	return stat.NewPanelBuilder().
		Title("Leader").
		Description("Represents current leader.").
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(6).
		Mappings([]dashboard.ValueMapping{
			lib.ValueMapping_Leader(),
		}).
		ColorMode(common.BigValueColorModeBackground).
		GraphMode(common.BigValueGraphModeArea).
		JustifyMode(common.BigValueJustifyModeCenter).
		TextMode(common.BigValueTextModeAuto).
		Orientation(common.VizOrientationHorizontal).
		ReduceOptions(common.NewReduceDataOptionsBuilder().Calcs([]string{"lastNotNull"}).Values(false)).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`ovnkube_clustermanager_leader{cluster="$cluster", namespace="ovn-kubernetes"}`).
				LegendFormat("{{pod}}:").
				Instant().
				RefId("A"))
}

func ovnkubeLeaderTimeline(d lib.SdnDashboard) *statetimeline.PanelBuilder {
	return statetimeline.NewPanelBuilder().
		Title("Leader Status").
		Description("Identifies whether the instance of ovnkube-cluster-manager is a leader(1) or not(0).").
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(12).
		Mappings([]dashboard.ValueMapping{
			lib.ValueMapping_Leader(),
		}).
		MergeValues(true).
		ShowValue(common.VisibilityModeAuto).
		AlignValue(common.TimelineValueAlignmentCenter).
		RowHeight(0.9).
		LineWidth(0).
		PerPage(0).
		FillOpacity(70).
		Legend(common.NewVizLegendOptionsBuilder().
			DisplayMode(common.LegendDisplayModeList).
			Placement(common.LegendPlacementBottom).
			ShowLegend(true),
		).
		Tooltip(common.NewVizTooltipOptionsBuilder().Mode(common.TooltipDisplayModeSingle)).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`ovnkube_clustermanager_leader{cluster="$cluster", namespace="ovn-kubernetes"}`).
				LegendFormat("{{pod}}").
				RefId("A"),
		)
}

func ovnkubeCpuTimeseries(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.ContainerCpuUsageTimeseries(d, false, "ovnkube-cluster-manager", []string{`pod=~"$pod"`})
}

func ovnkubeMemoryTimeseries(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.ContainerMemoryUsageTimeseries(d, false, "ovnkube-cluster-manager", []string{`pod=~"$pod"`})
}

func ovnkubeHostSubnetsTable(d lib.SdnDashboard) *table.PanelBuilder {
	return table.NewPanelBuilder().
		Title("Host Subnets").
		Description("The number of host subnets allocated and total number of host subnets.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		ShowHeader(true).
		CellHeight(common.TableCellHeightSm).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`max by (network_name) (ovnkube_clustermanager_allocated_v4_host_subnets{cluster="$cluster", namespace="ovn-kubernetes"})`).
				Instant().
				Format(prometheus.PromQueryFormatTable).
				RefId("A"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`max by (network_name) (ovnkube_clustermanager_num_v4_host_subnets{cluster="$cluster", namespace="ovn-kubernetes"})`).
				Instant().
				Format(prometheus.PromQueryFormatTable).
				RefId("B"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`max by (network_name) (ovnkube_clustermanager_allocated_v6_host_subnets{cluster="$cluster", namespace="ovn-kubernetes"})`).
				Instant().
				Format(prometheus.PromQueryFormatTable).
				RefId("C"),
		).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`max by (network_name) (ovnkube_clustermanager_num_v6_host_subnets{cluster="$cluster", namespace="ovn-kubernetes"})`).
				Instant().
				Format(prometheus.PromQueryFormatTable).
				RefId("D"),
		).
		WithTransformation(dashboard.DataTransformerConfig{
			Id:      "merge",
			Options: map[string]any{},
		}).
		WithTransformation(dashboard.DataTransformerConfig{
			Id: "filterFieldsByName",
			Options: map[string]any{
				"include": map[string]any{
					"names": []string{
						"network_name",
						"Value #A",
						"Value #B",
						"Value #C",
						"Value #D",
					},
				},
			},
		}).
		WithTransformation(dashboard.DataTransformerConfig{
			Id: "organize",
			Options: map[string]any{
				"excludeByName": map[string]any{},
				"includeByName": map[string]any{},
				"indexByName": map[string]any{
					"network_name": 0,
					"Value #A":     1,
					"Value #B":     2,
					"Value #C":     3,
					"Value #D":     4,
				},
				"renameByName": map[string]any{
					"network_name": "Network",
					"Value #A":     "IPv4 Allocated",
					"Value #B":     "IPv4 Total",
					"Value #C":     "IPv6 Allocated",
					"Value #D":     "IPv6 Total",
				},
			},
		})
}

func ovnkubeEgressIpsTimeseries(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.NewSdnPanelBuilder().
		Title("Egress IPs").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`max(ovnkube_clustermanager_num_egress_ips{cluster="$cluster", namespace="ovn-kubernetes"})`).
				LegendFormat("Total").
				RefId("A"),
		)
}

func ovnkubeResourceRetryFailuresTimeseries(d lib.SdnDashboard) *timeseries.PanelBuilder {
	return lib.BaseTimeseries_C2(d).
		Title("Resource Retry Failures Rate").
		Description("The rate oftimes processing a Kubernetes resource reached the maximum retry limit and was no longer processed.").
		Unit("ops").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(`sum by (pod) (rate(ovnkube_resource_retry_failures_total{cluster="$cluster", namespace="ovn-kubernetes", pod=~"ovnkube-control-plane-.*"}[5m]))`).
				LegendFormat("{{pod}}").
				RefId("A"),
		)
}
