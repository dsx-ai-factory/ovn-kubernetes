// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lib

import (
	"fmt"

	"github.com/grafana/grafana-foundation-sdk/go/prometheus"
)

func BaseQuery(d SdnDashboard) *prometheus.DataqueryBuilder {
	return prometheus.NewDataqueryBuilder()
}

// TopKQuery keeps only the $topk series with the highest average value over the
// dashboard's selected time range, while expr keeps its own plotted resolution.
// Ranking over the whole range avoids plain topk(), which re-ranks at every
// graph step and draws more than $topk series, each with gaps.
func TopKQuery(expr string) string {
	return TopKQueryBy(expr, fmt.Sprintf(`avg_over_time((%s)[$__range:$__interval] @ end())`, expr))
}

// SimpleTopKQuery re-ranks at every graph step, which can draw more than $topk
// series. Prefer TopKQuery unless the ranking is expected to change over time.
func SimpleTopKQuery(expr string) string {
	return fmt.Sprintf(`topk($topk, %s)`, expr)
}

// TopKQueryBy ranks with rankExpr instead of the range average of expr. rankExpr
// must produce the same label set as expr, otherwise `and` matches nothing.
func TopKQueryBy(expr string, rankExpr string) string {
	return fmt.Sprintf(`%s
and
topk($topk, %s)`, expr, rankExpr)
}
