// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package patch

import (
	"strings"
	"testing"
)

func TestTweakExprInjectsClusterMatcherWithGrafanaRangeVars(t *testing.T) {
	matcher := NewClusterPromQLMatcher()
	expr := `sum by (pod) (rate(container_cpu_usage_seconds_total{namespace="ovn-kubernetes", pod=~"$pod",container="nb-ovsdb"}[5m]))
and
topk($topk, sum by (pod) (rate(container_cpu_usage_seconds_total{namespace="ovn-kubernetes", pod=~"$pod",container="nb-ovsdb"}[$__range] @ end())))`

	got, err := TweakExpr(expr, matcher)
	if err != nil {
		t.Fatalf("TweakExpr: %v", err)
	}
	if strings.Count(got, `k8s_cluster_name=~"$cluster"`) != 2 {
		t.Fatalf("expected cluster matcher on both selectors, got %s", got)
	}
	if !strings.Contains(got, "$topk") || !strings.Contains(got, "$__range") {
		t.Fatalf("grafana vars were not restored: %s", got)
	}
	if !strings.Contains(got, "[5m]") {
		t.Fatalf("existing [5m] window was rewritten: %s", got)
	}
}

func TestTweakExprInjectsClusterMatcherWithRangeStepSubquery(t *testing.T) {
	matcher := NewClusterPromQLMatcher()
	expr := `ovn_db_db_size_bytes{namespace="ovn-kubernetes"} and topk($topk, avg_over_time((ovn_db_db_size_bytes{namespace="ovn-kubernetes"})[$__range:$__interval] @ end()))`

	got, err := TweakExpr(expr, matcher)
	if err != nil {
		t.Fatalf("TweakExpr: %v", err)
	}
	if strings.Count(got, `k8s_cluster_name=~"$cluster"`) != 2 {
		t.Fatalf("expected cluster matcher on both selectors, got %s", got)
	}
	if !strings.Contains(got, "$__range") || !strings.Contains(got, "$__interval") {
		t.Fatalf("grafana vars were not restored: %s", got)
	}
}
