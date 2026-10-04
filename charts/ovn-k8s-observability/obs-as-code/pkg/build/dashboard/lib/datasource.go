// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lib

import "github.com/grafana/grafana-foundation-sdk/go/dashboard"

var (
	PrometheusDataSourceType = "prometheus"
	PrometheusDataSourceUid  = "${DS_PROMETHEUS}"
)

func PrometheusDataSourceRef() dashboard.DataSourceRef {
	return dashboard.DataSourceRef{Type: &PrometheusDataSourceType, Uid: &PrometheusDataSourceUid}
}
