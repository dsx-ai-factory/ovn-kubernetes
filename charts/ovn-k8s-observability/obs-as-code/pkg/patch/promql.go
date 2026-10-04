// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package patch

import (
	"fmt"
	"strings"

	"github.com/prometheus/prometheus/promql/parser"
)

// grafanaPlaceholder substitutes a Grafana template variable with a PromQL-legal
// literal so prometheus/promql/parser can parse the expression. Year-scale
// durations are unlikely in dashboards, so restore will not rewrite real
// windows like [5m]. Each Grafana var needs a distinct value so expressions
// like [$__range:$__interval] can be restored. $topk is a number, not a duration.
type grafanaPlaceholder struct {
	name  string
	value string
}

// Longer names first so `$__rate_interval` is not confused with `$__interval`.
var grafanaPlaceholders = []grafanaPlaceholder{
	{name: "$__rate_interval", value: "100y"},
	{name: "$__interval", value: "101y"},
	{name: "$__range", value: "102y"},
	{name: "$topk", value: "987"},
}

func applyTemplateVarPlaceholders(expr string) (string, map[string]struct{}, error) {
	applied := map[string]struct{}{}
	out := expr
	for _, p := range grafanaPlaceholders {
		if !strings.Contains(out, p.name) {
			continue
		}
		if strings.Contains(expr, p.value) {
			return "", nil, fmt.Errorf("expression contains reserved placeholder literal %q", p.value)
		}
		applied[p.name] = struct{}{}
		out = strings.ReplaceAll(out, p.name, p.value)
	}
	return out, applied, nil
}

func restoreTemplateVarPlaceholders(expr string, applied map[string]struct{}) string {
	if len(applied) == 0 {
		return expr
	}
	out := expr
	for _, p := range grafanaPlaceholders {
		if _, ok := applied[p.name]; !ok {
			continue
		}
		out = strings.ReplaceAll(out, p.value, p.name)
	}
	return out
}

func TweakExpr(expr string, matcher PromQLMatcher) (string, error) {
	preprocessed, applied, err := applyTemplateVarPlaceholders(expr)
	if err != nil {
		return "", err
	}

	parsedExpr, err := parser.ParseExpr(preprocessed)
	if err != nil {
		return "", err
	}
	addPromQLMatcher(parsedExpr, matcher)

	updated := parsedExpr.String()
	updated = restoreTemplateVarPlaceholders(updated, applied)
	return updated, nil
}
