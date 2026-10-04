// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lib

import (
	"fmt"
	"strings"

	"github.com/grafana/grafana-foundation-sdk/go/bargauge"
	"github.com/grafana/grafana-foundation-sdk/go/cog"
	"github.com/grafana/grafana-foundation-sdk/go/common"
	"github.com/grafana/grafana-foundation-sdk/go/dashboard"
	"github.com/grafana/grafana-foundation-sdk/go/heatmap"
	"github.com/grafana/grafana-foundation-sdk/go/prometheus"
	"github.com/grafana/grafana-foundation-sdk/go/table"
	"github.com/grafana/grafana-foundation-sdk/go/text"
	"github.com/grafana/grafana-foundation-sdk/go/timeseries"
)

func NewSdnPanelBuilder() *timeseries.PanelBuilder {
	return timeseries.NewPanelBuilder().
		Interval("120s")
}

func NewSdnBarGaugePanelBuilder(d SdnDashboard, title, description string) *bargauge.PanelBuilder {
	return bargauge.NewPanelBuilder().
		Title(title).
		Description(description).
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(8).
		DisplayMode(common.BarGaugeDisplayModeLcd).
		ValueMode(common.BarGaugeValueModeColor).
		NamePlacement(common.BarGaugeNamePlacementAuto).
		ShowUnfilled(true).
		Sizing(common.BarGaugeSizingAuto).
		MinVizWidth(8).
		MinVizHeight(16).
		MaxVizHeight(300).
		Orientation(common.VizOrientationHorizontal).
		ReduceOptions(common.NewReduceDataOptionsBuilder().Calcs([]string{"lastNotNull"}).Values(false)).
		Legend(common.NewVizLegendOptionsBuilder().
			DisplayMode(common.LegendDisplayModeList).
			Placement(common.LegendPlacementBottom).
			ShowLegend(false),
		).
		ColorScheme(dashboard.NewFieldColorBuilder().Mode(dashboard.FieldColorModeIdThresholds)).
		Thresholds(
			dashboard.NewThresholdsConfigBuilder().
				Mode(dashboard.ThresholdsModeAbsolute).
				Steps([]dashboard.Threshold{
					{Value: nil, Color: "green"},
				}),
		)
}

func NewSdnHeatmapPanelBuilder(d SdnDashboard, title, description string) *heatmap.PanelBuilder {
	builder := heatmap.NewPanelBuilder().
		Title(title).
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(12).
		Interval("120s").
		YAxis(heatmap.NewYAxisConfigBuilder().
			AxisPlacement(common.AxisPlacementLeft).
			Reverse(false),
		).
		RowsFrame(heatmap.NewRowsHeatmapOptionsBuilder().
			Layout(common.HeatmapCellLayoutAuto),
		).
		Color(NewSdnHeatmapColor(64)).
		CellGap(1).
		FilterValues(heatmap.NewFilterValueRangeBuilder().Le(1e-9)).
		Mode(common.TooltipDisplayModeSingle).
		HideYHistogram().
		ShowColorScale(false).
		ShowLegend().
		ExemplarsColor("rgba(255,0,255,0.7)").
		ScaleDistribution(common.NewScaleDistributionConfigBuilder().Type(common.ScaleDistributionLinear))
	if description != "" {
		builder.Description(description)
	}
	return builder
}

func NewSdnHeatmapColor(steps uint64) *heatmap.HeatmapColorOptionsBuilder {
	return heatmap.NewHeatmapColorOptionsBuilder().
		Mode(heatmap.HeatmapColorModeScheme).
		Fill("dark-orange").
		Scale(heatmap.HeatmapColorScaleExponential).
		Exponent(0.5).
		Scheme("Oranges").
		Steps(steps).
		Reverse(false)
}

// Timeseries panel
func BaseTimeseries(d SdnDashboard) *timeseries.PanelBuilder {
	return NewSdnPanelBuilder().
		Title("Base Timeseries").
		Description("Base Timeseries").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(24).
		LineWidth(2).
		FillOpacity(10).
		LineInterpolation(common.LineInterpolationSmooth).
		ShowPoints(common.VisibilityModeNever).
		DrawStyle(common.GraphDrawStyleLine).
		Legend(common.NewVizLegendOptionsBuilder().
			DisplayMode(common.LegendDisplayModeList).
			Placement(common.LegendPlacementBottom).
			ShowLegend(true),
		).
		Tooltip(common.NewVizTooltipOptionsBuilder().
			Mode(common.TooltipDisplayModeMulti).
			Sort(common.SortOrderDescending),
		).
		ThresholdsStyle(
			common.NewGraphThresholdsStyleConfigBuilder().
				Mode(common.GraphThresholdsStyleModeOff),
		).
		SpanNulls(common.BoolOrFloat64{Bool: cog.ToPtr(false)}).
		AxisBorderShow(false)
}

// 2 column per row
func BaseTimeseries_C2(d SdnDashboard) *timeseries.PanelBuilder {
	return BaseTimeseries(d).
		Span(12)
}

// 3 coloum per row
func BaseTimeseries_C3(d SdnDashboard) *timeseries.PanelBuilder {
	return BaseTimeseries(d).
		Span(8)
}

// 4 column per row
func BaseTimeseries_C4(d SdnDashboard) *timeseries.PanelBuilder {
	return BaseTimeseries(d).
		Span(6)
}

func ContainerCpuUsageTimeseries(d SdnDashboard, topk bool, container string, labels []string) *timeseries.PanelBuilder {
	labels = append(labels, fmt.Sprintf(`container="%s"`, container))
	title := "CPU Usage"
	description := fmt.Sprintf("CPU usage of container %s.", container)
	expr := fmt.Sprintf(`sum by (pod) (rate(container_cpu_usage_seconds_total{namespace="ovn-kubernetes", %s}[5m]))`, strings.Join(labels, ","))

	if topk {
		title += " (Top K)"
		description += " Only the $topk pods with the highest average usage over the selected time range are shown."
		// Rank on the counter directly instead of the default subquery, which is
		// cheaper and equivalent for a rate.
		expr = TopKQueryBy(expr, fmt.Sprintf(`sum by (pod) (rate(container_cpu_usage_seconds_total{namespace="ovn-kubernetes", %s}[$__range] @ end()))`, strings.Join(labels, ",")))
	}

	return BaseTimeseries_C2(d).
		Title(title).
		Description(description).
		Unit("cores").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(expr).
				LegendFormat("{{pod}}").
				RefId("A"),
		)
}

func ContainerMemoryUsageTimeseries(d SdnDashboard, topk bool, container string, labels []string) *timeseries.PanelBuilder {
	labels = append(labels, fmt.Sprintf(`container="%s"`, container))
	title := "Memory Usage"
	description := fmt.Sprintf("Working set memory usage of container %s.", container)
	expr := fmt.Sprintf(`sum by (pod) (container_memory_working_set_bytes{namespace="ovn-kubernetes", %s}) / 1024^3`, strings.Join(labels, ","))

	if topk {
		title += " (Top K)"
		description += " Only the $topk pods with the highest average usage over the selected time range are shown."
		// Rank on the raw gauge instead of the default subquery, which is
		// cheaper and equivalent for a plain sum.
		expr = TopKQueryBy(expr, fmt.Sprintf(`sum by (pod) (avg_over_time(container_memory_working_set_bytes{namespace="ovn-kubernetes", %s}[$__range] @ end())) / 1024^3`, strings.Join(labels, ",")))
	}

	return BaseTimeseries_C2(d).
		Title(title).
		Description(description).
		Unit("decgbytes").
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(expr).
				LegendFormat("{{pod}}").
				RefId("A"),
		)
}

func ContainerRestartTimeseries(d SdnDashboard, topk bool, container string, labels []string) *timeseries.PanelBuilder {
	labels = append(labels, fmt.Sprintf(`container="%s"`, container))
	title := fmt.Sprintf("%s Container Restart", container)
	description := "Indicates the occurrence of a container restart."
	expr := fmt.Sprintf(`sum by (pod) (rate(kube_pod_container_status_restarts_total{namespace="ovn-kubernetes", %s}[5m]))`, strings.Join(labels, ","))

	if topk {
		title += " (Top K)"
		description += " Only the $topk pods that restarted most over the selected time range are shown."
		// Rank on the counter directly instead of the default subquery, which is
		// cheaper and equivalent for a rate.
		expr = TopKQueryBy(expr, fmt.Sprintf(`sum by (pod) (rate(kube_pod_container_status_restarts_total{namespace="ovn-kubernetes", %s}[$__range] @ end()))`, strings.Join(labels, ",")))
	}

	return BaseTimeseries_C2(d).
		Title(title).
		Description(description).
		Unit("none").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(expr).
				LegendFormat("{{pod}}").
				RefId("A"),
		)
}

// NotConnectedTimeseries draws only while a pod reports the connection status
// metric as down, so an empty panel means everything is connected. metric must
// report 1 when connected.
func NotConnectedTimeseries(d SdnDashboard, title, description, metric string) *timeseries.PanelBuilder {
	return NewSdnPanelBuilder().
		Title(title).
		Description(description + " A pod that stopped exporting metrics altogether does not show up here.").
		Datasource(d.DataSourceRef()).
		Height(8).
		Span(8).
		Unit("none").
		Min(0).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				// == bool 0 reports 1 while a pod is disconnected, > 0 then drops
				// the connected pods.
				Expr(fmt.Sprintf(`sum by (instance,pod) (%s{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"} == bool 0) > 0`, metric)).
				LegendFormat("{{instance}}/{{pod}}").
				RefId("A"),
		)
}

func ContainerReadinessBarGauge(d SdnDashboard, container string) *bargauge.PanelBuilder {
	readyExpr := fmt.Sprintf(`sum(kube_pod_container_status_ready{namespace="ovn-kubernetes", container="%s"})`, container)
	return NewSdnBarGaugePanelBuilder(d,
		fmt.Sprintf("Container %s Readiness", container),
		fmt.Sprintf("The summary of ready and not ready %s containers.", container),
	).
		WithTarget(
			prometheus.NewDataqueryBuilder().
				Expr(readyExpr).
				LegendFormat("READY").
				RefId("A"),
		).
		WithTarget(
			// count() - sum() instead of sum(... == 0), which returns an empty
			// vector and hides the bar when every container is ready.
			prometheus.NewDataqueryBuilder().
				Expr(fmt.Sprintf(`count(kube_pod_container_status_ready{namespace="ovn-kubernetes", container="%s"}) - %s`, container, readyExpr)).
				LegendFormat("NOT READY").
				RefId("B"),
		).
		OverrideByName("NOT READY", RedWhenNonZero())
}

// RedWhenNonZero turns a bar red as soon as its value leaves zero. The shared
// bar gauge only defines a green base step, so a series counting unhealthy
// objects would otherwise still render as green.
func RedWhenNonZero() []dashboard.DynamicConfigValue {
	return []dashboard.DynamicConfigValue{
		{
			Id: "thresholds",
			Value: map[string]any{
				"mode": "absolute",
				"steps": []map[string]any{
					{"color": "green", "value": nil},
					{"color": "red", "value": 1},
				},
			},
		},
	}
}

type BuildInfoField struct {
	Label string
	Name  string
}

func OvnkubeBuildInfoFields() []BuildInfoField {
	return []BuildInfoField{
		{Label: "version", Name: "Version"},
		{Label: "branch", Name: "Branch"},
		{Label: "revision", Name: "Revision"},
		{Label: "goversion", Name: "Go"},
	}
}

// BuildInfoTable shows one row per unique combination of the given build info
// labels, plus how many pods report it. Reducing the matched series to a single
// value instead would hide a version skew during a rolling upgrade.
func BuildInfoTable(d SdnDashboard, desc, expr string, fields []BuildInfoField) *table.PanelBuilder {
	names := make([]string, 0, len(fields)+1)
	indexByName := make(map[string]any, len(fields)+1)
	renameByName := make(map[string]any, len(fields))
	groupByFields := make(map[string]any, len(fields)+1)
	for i, field := range fields {
		names = append(names, field.Label)
		indexByName[field.Label] = i
		renameByName[field.Label] = field.Name
		groupByFields[field.Name] = map[string]any{
			"operation":    "groupby",
			"aggregations": []string{},
		}
	}
	names = append(names, "pod")
	indexByName["pod"] = len(fields)
	groupByFields["pod"] = map[string]any{
		"operation":    "aggregate",
		"aggregations": []string{"count"},
	}

	return table.NewPanelBuilder().
		Title("Build Info").
		Description(desc).
		Datasource(d.DataSourceRef()).
		Height(6).
		Span(6).
		WithTarget(
			// An instant query gives one row per pod, so counting pods below
			// counts pods rather than samples.
			prometheus.NewDataqueryBuilder().
				Expr(expr).
				Format(prometheus.PromQueryFormatTable).
				Instant().
				RefId("A"),
		).
		WithTransformation(dashboard.DataTransformerConfig{
			Id: "filterFieldsByName",
			Options: map[string]any{
				"include": map[string]any{
					"names": names,
				},
			},
		}).
		// Rename before grouping so the group-by keys are the displayed names.
		WithTransformation(dashboard.DataTransformerConfig{
			Id: "organize",
			Options: map[string]any{
				"excludeByName": map[string]any{},
				"includeByName": map[string]any{},
				"indexByName":   indexByName,
				"renameByName":  renameByName,
			},
		}).
		WithTransformation(dashboard.DataTransformerConfig{
			Id: "groupBy",
			Options: map[string]any{
				"fields": groupByFields,
			},
		}).
		WithTransformation(dashboard.DataTransformerConfig{
			Id: "organize",
			Options: map[string]any{
				"excludeByName": map[string]any{},
				"includeByName": map[string]any{},
				"indexByName":   map[string]any{},
				"renameByName": map[string]any{
					"pod (count)": "Pods",
				},
			},
		})
}

// Text panel
func PerformanceCountersText() *text.PanelBuilder {
	return text.NewPanelBuilder().
		Title("Performance Counters Stats").
		Height(8).
		Span(24).
		Mode(text.TextModeMarkdown).
		Content(`Each performance counter has below stats:

<table>
  <thead>
    <tr>
      <th>Counter</th>
      <th>Description</th>
    </tr>
  </thead>
  <tbody>
    <tr>
      <td>95th_percentile</td>
      <td>The 95th percentile of execution times.</td>
    </tr>
    <tr>
      <td>maximum</td>
      <td>The maximum (worst-case) execution time observed across all samples.</td>
    </tr>
    <tr>
      <td>minimum</td>
      <td>The minimum (best-case) execution time observed across all samples.</td>
    </tr>
    <tr>
      <td>short_term_avg</td>
      <td>Uses an Exponentially Weighted Moving Average (alpha=0.50) for a smoothed average that quickly reflects recent changes by weighting newer samples more heavily.</td>
    </tr>
    <tr>
      <td>long_term_avg</td>
      <td>A stable, long-term average is calculated using an Exponentially Weighted Moving Average with alpha=0.01, making it less sensitive to recent fluctuations.</td>
    </tr>
    <tr>
      <td>total_samples</td>
      <td>Total number of measurements recorded by the stopwatch.</td>
    </tr>
  </tbody>
</table>`)
}

func AppendPerformanceCounterPanels(b *dashboard.DashboardBuilder, d SdnDashboard, title, desc string, component, stat string) {
	for _, panel := range PerformanceCounterPanels(d, title, desc, component, stat) {
		b.WithPanel(panel)
	}
}

// AppendPerformanceCounterPanelsToRow nests the panels in the row, which is what
// Grafana needs to keep the row collapsible.
func AppendPerformanceCounterPanelsToRow(r *dashboard.RowBuilder, d SdnDashboard, title, desc string, component, stat string) {
	for _, panel := range PerformanceCounterPanels(d, title, desc, component, stat) {
		r.WithPanel(panel)
	}
}

func PerformanceCounterPanels(d SdnDashboard, title, desc string, component, stat string) []cog.Builder[dashboard.Panel] {
	return []cog.Builder[dashboard.Panel]{
		NewSdnPanelBuilder().
			Title(fmt.Sprintf("%s Sample Rate (Top K)", title)).
			Description(desc).
			Datasource(d.DataSourceRef()).
			Height(8).
			Span(8).
			Unit("recps").
			Min(0).
			WithTarget(
				prometheus.NewDataqueryBuilder().
					Expr(fmt.Sprintf(`topk($topk, rate(%s_%s_total_samples{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"}[5m]))`, component, stat)).
					LegendFormat("{{instance}}/{{pod}}").
					RefId("A"),
			),
		NewSdnPanelBuilder().
			Title(fmt.Sprintf("%s Time (Top K)", title)).
			Description(desc).
			Datasource(d.DataSourceRef()).
			Height(8).
			Span(8).
			Unit("s").
			Min(0).
			WithTarget(
				prometheus.NewDataqueryBuilder().
					Expr(fmt.Sprintf(`topk($topk, %s_%s_minimum{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"})`, component, stat)).
					LegendFormat("MIN: {{instance}}/{{pod}}").
					RefId("A"),
			).
			WithTarget(
				prometheus.NewDataqueryBuilder().
					Expr(fmt.Sprintf(`topk($topk, %s_%s_maximum{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"})`, component, stat)).
					LegendFormat("MAX: {{instance}}/{{pod}}").
					RefId("B"),
			).
			WithTarget(
				prometheus.NewDataqueryBuilder().
					Expr(fmt.Sprintf(`topk($topk, %s_%s_95th_percentile{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"})`, component, stat)).
					LegendFormat("P95: {{instance}}/{{pod}}").
					RefId("C"),
			),
		NewSdnPanelBuilder().
			Title(fmt.Sprintf("%s Average Time (Top K)", title)).
			Description(desc).
			Datasource(d.DataSourceRef()).
			Height(8).
			Span(8).
			Unit("s").
			Min(0).
			WithTarget(
				prometheus.NewDataqueryBuilder().
					Expr(fmt.Sprintf(`topk($topk, %s_%s_short_term_avg{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"})`, component, stat)).
					LegendFormat("short-term: {{instance}}/{{pod}}").
					RefId("A"),
			).
			WithTarget(
				prometheus.NewDataqueryBuilder().
					Expr(fmt.Sprintf(`topk($topk, %s_%s_long_term_avg{instance=~"$instance", pod=~"$pod", namespace="ovn-kubernetes"})`, component, stat)).
					LegendFormat("long-term: {{instance}}/{{pod}}").
					RefId("B"),
			),
	}
}

func Thresholds_80(d SdnDashboard) *dashboard.ThresholdsConfigBuilder {
	return dashboard.NewThresholdsConfigBuilder().
		Mode(dashboard.ThresholdsModeAbsolute).
		Steps([]dashboard.Threshold{
			{
				Color: "green",
			},
			{
				Value: cog.ToPtr(80.0),
				Color: "red",
			},
		})
}

func ValueMapping_Leader() dashboard.ValueMapping {
	vm := dashboard.NewValueMapping()
	vm.ValueMap = dashboard.NewValueMap()
	vm.ValueMap.Type = dashboard.MappingTypeValueToText
	vm.ValueMap.Options = map[string]dashboard.ValueMappingResult{
		"0": {
			Color: cog.ToPtr("orange"),
			Index: cog.ToPtr(int32(1)),
			Text:  cog.ToPtr("FOLLOWER"),
		},
		"1": {
			Color: cog.ToPtr("green"),
			Index: cog.ToPtr(int32(0)),
			Text:  cog.ToPtr("LEADER"),
		},
	}

	return *vm
}
