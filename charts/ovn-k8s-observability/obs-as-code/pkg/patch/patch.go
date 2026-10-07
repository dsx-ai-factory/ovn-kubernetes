// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package patch

import (
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

const translatedMetricLegend = "{{host_name}}"

var processMetricByName = map[string]string{
	"namedprocess_namegroup_cpu_seconds_total": "process_cpu_time_seconds_total",
	"namedprocess_namegroup_memory_bytes":      "process_memory_usage_bytes",
}

var processCommands = map[string]struct{}{
	"ovs-vswitchd": {},
	"ovsdb-server": {},
}

// TransformProcessExporterExprs rewrites the host-ovs process-exporter
// selectors to OTEL process metrics. Other metrics and processes are left
// unchanged.
func TransformProcessExporterExprs(expr string) (string, bool, error) {
	preprocessed, applied, err := applyTemplateVarPlaceholders(expr)
	if err != nil {
		return "", false, err
	}

	parsed, err := parser.ParseExpr(preprocessed)
	if err != nil {
		return "", false, err
	}

	rewrittenCPU := map[*parser.VectorSelector]struct{}{}
	didTranslate := false
	parser.Inspect(parsed, func(node parser.Node, _ []parser.Node) error {
		selector, ok := node.(*parser.VectorSelector)
		if !ok {
			return nil
		}

		metricName := selector.Name
		newMetricName, supported := processMetricByName[metricName]
		if !supported {
			return nil
		}

		processCommand := ""
		var hostMatcher *labels.Matcher
		for _, labelMatcher := range selector.LabelMatchers {
			switch labelMatcher.Name {
			case "groupname":
				if labelMatcher.Type == labels.MatchEqual {
					processCommand = labelMatcher.Value
				}
			case "instance":
				hostMatcher = labelMatcher
			}
		}
		if _, supported = processCommands[processCommand]; !supported {
			return nil
		}

		newMatchers := []*labels.Matcher{
			mustMatcher(labels.MatchEqual, "__name__", newMetricName),
			mustMatcher(labels.MatchEqual, "cluster", "$cluster"),
			mustMatcher(labels.MatchEqual, "process_command", processCommand),
		}
		if hostMatcher != nil {
			newMatchers = append(newMatchers, mustMatcher(hostMatcher.Type, "host_name", hostMatcher.Value))
		}

		selector.Name = newMetricName
		selector.LabelMatchers = newMatchers
		if metricName == "namedprocess_namegroup_cpu_seconds_total" {
			rewrittenCPU[selector] = struct{}{}
		}
		didTranslate = true
		return nil
	})

	if !didTranslate {
		return expr, false, nil
	}

	parser.Inspect(parsed, func(node parser.Node, _ []parser.Node) error {
		aggregate, ok := node.(*parser.AggregateExpr)
		if !ok || !containsCPUSelector(aggregate.Expr, rewrittenCPU) {
			return nil
		}
		for i, group := range aggregate.Grouping {
			if group == "instance" {
				aggregate.Grouping[i] = "host_name"
			}
		}
		return nil
	})

	out := parsed.String()
	return restoreTemplateVarPlaceholders(out, applied), true, nil
}

func containsCPUSelector(expr parser.Expr, rewritten map[*parser.VectorSelector]struct{}) bool {
	found := false
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		selector, ok := node.(*parser.VectorSelector)
		if !ok {
			return nil
		}
		if _, ok := rewritten[selector]; ok {
			found = true
		}
		return nil
	})
	return found
}
