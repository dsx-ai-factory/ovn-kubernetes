// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lib

import (
	"github.com/grafana/grafana-foundation-sdk/go/cog"
	"github.com/grafana/grafana-foundation-sdk/go/dashboard"
)

var TopK = "1,3,5,10,20,100"

// ClusterVariableQuery is the built-in cluster dropdown query. The patch
// replaces this text with the configured discovery metric and label. Panel
// expressions and other template queries write cluster="$cluster" as their
// first matcher; the patch replaces that label name.
const ClusterVariableQuery = `label_values(up{job="ovnkube-node"}, cluster)`

func ClusterVariable(d SdnDashboard) *dashboard.QueryVariableBuilder {
	return dashboard.NewQueryVariableBuilder("cluster").
		Label("Cluster").
		Query(dashboard.StringOrMap{Map: map[string]any{
			"qryType": 1,
			"query":   ClusterVariableQuery,
			"refId":   "prometheus-cluster-Variable-Query",
		}}).
		Datasource(d.DataSourceRef()).
		Multi(false).
		Refresh(dashboard.VariableRefreshOnDashboardLoad).
		Sort(dashboard.VariableSortDisabled).
		IncludeAll(false).
		AllValue(`""`).
		Regex("")
}

func InstanceVariable(d SdnDashboard) *dashboard.QueryVariableBuilder {
	return dashboard.NewQueryVariableBuilder("instance").
		Label("Instance").
		Query(dashboard.StringOrMap{String: cog.ToPtr(`label_values(ovs_build_info{cluster="$cluster"}, instance)`)}).
		Datasource(d.DataSourceRef()).
		Refresh(dashboard.VariableRefreshOnTimeRangeChanged).
		Sort(dashboard.VariableSortNumericalAsc).
		Multi(true).
		IncludeAll(false)
}

func PodVariable(d SdnDashboard) *dashboard.QueryVariableBuilder {

	return dashboard.NewQueryVariableBuilder("pod").
		Label("Pod").
		Query(dashboard.StringOrMap{String: cog.ToPtr(`label_values(kube_pod_info{cluster="$cluster", namespace="ovn-kubernetes", created_by_name=~"ovnkube-control-plane-.*"},pod)`)}).
		Datasource(d.DataSourceRef()).
		Description("A list of ovnkube-control-plane Pods.").
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

func TopKVariable() *dashboard.CustomVariableBuilder {
	return dashboard.NewCustomVariableBuilder("topk").
		Label("topk").
		Values(dashboard.StringOrMap{String: cog.ToPtr(TopK)}).
		Current(dashboard.VariableOption{
			Selected: cog.ToPtr(true),
			Text:     dashboard.StringOrArrayOfString{String: cog.ToPtr("5")},
			Value:    dashboard.StringOrArrayOfString{String: cog.ToPtr("5")},
		})
}
