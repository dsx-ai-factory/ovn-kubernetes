// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package patch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"obs-as-code/pkg/log"

	"github.com/grafana/grafana-foundation-sdk/go/dashboard"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

type ExprRewriter func(expr string) (string, error)

// TargetRewriter mutates a single Grafana panel target (a map containing
// `expr`, `legendFormat`, `refId`, etc.). Modes use this instead of a per-expr
// rewriter when adjusting `expr` requires touching sibling fields too (e.g.
// rewriting `legendFormat` after the underlying metric was translated).
type TargetRewriter func(target map[string]any)
type ExprDiffLogger func(before, after string)
type TemplateVarSpec struct {
	Name       string
	Label      string
	QueryLabel string
}

type DataSourceSpec struct {
	Name string
	UID  string
}

type DashboardModifier interface {
	Apply(root map[string]any) error
}

type dashboardAux struct {
	Inputs   json.RawMessage `json:"__inputs,omitempty"`
	Requires json.RawMessage `json:"__requires,omitempty"`
	dashboard.Dashboard
}

type BaseDashboardModifier struct {
	TemplateVar    TemplateVarSpec
	DataSourceSpec *DataSourceSpec
	PromQLMatcher  *PromQLMatcher
	TemplateVars   []map[string]any
	BeforeApply    func(root map[string]any) error
	AfterApply     func(root map[string]any) error
}

func walkPanelTargets(v any, rewrite TargetRewriter) {
	switch n := v.(type) {
	case map[string]any:
		if _, hasExpr := n["expr"]; hasExpr {
			rewrite(n)
		}
		for _, child := range n {
			walkPanelTargets(child, rewrite)
		}
	case []any:
		for _, el := range n {
			walkPanelTargets(el, rewrite)
		}
	}
}

func splitTopLevelArgs(in string) []string {
	args := make([]string, 0, 2)
	start := 0
	parenDepth := 0
	braceDepth := 0
	bracketDepth := 0
	inString := false
	escaped := false

	for i, r := range in {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inString = false
			}
			continue
		}

		switch r {
		case '"':
			inString = true
		case '(':
			parenDepth++
		case ')':
			if parenDepth > 0 {
				parenDepth--
			}
		case '{':
			braceDepth++
		case '}':
			if braceDepth > 0 {
				braceDepth--
			}
		case '[':
			bracketDepth++
		case ']':
			if bracketDepth > 0 {
				bracketDepth--
			}
		case ',':
			if parenDepth == 0 && braceDepth == 0 && bracketDepth == 0 {
				args = append(args, strings.TrimSpace(in[start:i]))
				start = i + 1
			}
		}
	}

	args = append(args, strings.TrimSpace(in[start:]))
	return args
}

func addPromQLMatcher(node parser.Node, matcher PromQLMatcher) {
	if node == nil {
		return
	}
	if n, ok := node.(*parser.VectorSelector); ok {
		for _, existing := range n.LabelMatchers {
			if existing.Name == matcher.Label {
				return
			}
		}
		m, err := labels.NewMatcher(matcher.Type, matcher.Label, matcher.Value)
		if err != nil {
			return
		}
		n.LabelMatchers = append(n.LabelMatchers, m)
		return
	}
	for _, child := range parser.Children(node) {
		addPromQLMatcher(child, matcher)
	}
}

func injectMatcherIntoPromQLExpr(expr string, matcher PromQLMatcher) string {
	parsed, err := parser.ParseExpr(strings.TrimSpace(expr))
	if err != nil {
		return expr
	}
	addPromQLMatcher(parsed, matcher)
	return parsed.String()
}

func injectMatcherIntoWrappedFunctionArg1(query, functionName string, matcher PromQLMatcher) string {
	trimmed := strings.TrimSpace(query)
	functionPrefix := functionName + "("
	if !strings.HasPrefix(trimmed, functionPrefix) || !strings.HasSuffix(trimmed, ")") {
		return query
	}

	openIdx := strings.Index(trimmed, "(")
	closeIdx := strings.LastIndex(trimmed, ")")
	if openIdx < 0 || closeIdx <= openIdx+1 {
		return query
	}

	args := splitTopLevelArgs(trimmed[openIdx+1 : closeIdx])
	if len(args) == 0 {
		return query
	}

	args[0] = injectMatcherIntoPromQLExpr(args[0], matcher)
	return fmt.Sprintf("%s(%s)", functionName, strings.Join(args, ", "))
}

func rewriteTemplateQuery(query string, matcher PromQLMatcher) string {
	supportedFunctions := []string{"label_values", "query_result"}
	for _, functionName := range supportedFunctions {
		rewritten := injectMatcherIntoWrappedFunctionArg1(query, functionName, matcher)
		if rewritten != query {
			return rewritten
		}
	}
	return query
}

func rewriteTemplateVarQueries(v map[string]any, matcher PromQLMatcher, skipName string) {
	if t, _ := v["type"].(string); t != "query" {
		return
	}
	if name, _ := v["name"].(string); skipName != "" && name == skipName {
		return
	}

	if qs, ok := v["query"].(string); ok {
		v["query"] = rewriteTemplateQuery(qs, matcher)
	} else if qObj, ok := v["query"].(map[string]any); ok {
		if qs, ok := qObj["query"].(string); ok {
			qObj["query"] = rewriteTemplateQuery(qs, matcher)
			v["query"] = qObj
		}
	}

	if def, ok := v["definition"].(string); ok {
		v["definition"] = rewriteTemplateQuery(def, matcher)
	}
}

func (modifier BaseDashboardModifier) applyTemplateVariableFirst(root map[string]any, templateVarObject map[string]any) {
	modifier.applyTemplateVariablesFirst(root, []map[string]any{templateVarObject})
}

func (modifier BaseDashboardModifier) applyTemplateVariablesFirst(root map[string]any, templateVarObjects []map[string]any) {
	if len(templateVarObjects) == 0 {
		return
	}

	templating, ok := root["templating"].(map[string]any)
	if !ok || templating == nil {
		templating = map[string]any{}
	}

	list, _ := templating["list"].([]any)
	newList := make([]any, 0, len(list)+len(templateVarObjects))
	insertedNames := map[string]struct{}{}
	for _, obj := range templateVarObjects {
		if obj == nil {
			continue
		}
		newList = append(newList, obj)
		if name, ok := obj["name"].(string); ok && name != "" {
			insertedNames[name] = struct{}{}
		}
	}

	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			newList = append(newList, item)
			continue
		}
		if name, ok := m["name"].(string); ok {
			if _, exists := insertedNames[name]; exists {
				continue
			}
		}
		newList = append(newList, item)
	}

	templating["list"] = newList
	root["templating"] = templating
}

func (modifier BaseDashboardModifier) Apply(root map[string]any) error {
	if modifier.BeforeApply != nil {
		if err := modifier.BeforeApply(root); err != nil {
			return err
		}
	}

	if modifier.PromQLMatcher != nil {
		modifier.rewriteTemplateVariableQueries(root, *modifier.PromQLMatcher)
	}

	modifier.applyTemplateVariablesFirst(root, modifier.TemplateVars)
	modifier.applyDataSourceUID(root)

	if modifier.AfterApply != nil {
		if err := modifier.AfterApply(root); err != nil {
			return err
		}
	}

	return nil
}

func (modifier BaseDashboardModifier) rewriteTemplateVariableQueries(root map[string]any, matcher PromQLMatcher) {
	templating, ok := root["templating"].(map[string]any)
	if !ok || templating == nil {
		return
	}

	list, _ := templating["list"].([]any)
	for _, item := range list {
		if v, ok := item.(map[string]any); ok {
			rewriteTemplateVarQueries(v, matcher, modifier.TemplateVar.Name)
		}
	}
}

func (modifier BaseDashboardModifier) applyDataSourceUID(v any) {
	if modifier.DataSourceSpec == nil {
		return
	}

	switch n := v.(type) {
	case map[string]any:
		if raw, ok := n["datasource"]; ok {
			if ds, ok := raw.(map[string]any); ok {
				if dsType, ok := ds["type"].(string); ok && dsType == modifier.DataSourceSpec.Name {
					ds["uid"] = modifier.DataSourceSpec.UID
				}
			}
		}
		for _, child := range n {
			modifier.applyDataSourceUID(child)
		}
	case []any:
		for _, el := range n {
			modifier.applyDataSourceUID(el)
		}
	}
}

func ProcessDashboardFile(path, outDir string, modifier DashboardModifier, rewrite TargetRewriter) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}

	rootMap, ok := root.(map[string]any)
	if !ok {
		return fmt.Errorf("dashboard root must be a JSON object")
	}

	if err := modifier.Apply(rootMap); err != nil {
		return err
	}

	walkPanelTargets(rootMap, rewrite)

	out, err := json.MarshalIndent(rootMap, "", "  ")
	if err != nil {
		return err
	}

	var aux dashboardAux
	if err := json.Unmarshal(out, &aux); err != nil {
		return fmt.Errorf("failed to decode dashboard with sdk model: %w", err)
	}

	out, err = json.MarshalIndent(aux, "", "  ")
	if err != nil {
		return err
	}

	outPath := path
	if outDir != "" {
		if err := os.MkdirAll(outDir, 0755); err != nil {
			return err
		}
		outPath = filepath.Join(outDir, filepath.Base(path))
	}

	return os.WriteFile(outPath, append(out, '\n'), 0644)
}

func RunDashboardCmd(dashboardFile, dashboardDir, outDir string) error {
	modifier, targetRewriter := DashboardPatch()

	processOne := func(path string) error {
		log.Infof("processing dashboard file %s, out_dir %s", path, outDir)
		return ProcessDashboardFile(path, outDir, modifier, targetRewriter)
	}

	if dashboardFile != "" {
		return processOne(dashboardFile)
	}

	entries, err := os.ReadDir(dashboardDir)
	if err != nil {
		return fmt.Errorf("fatal error reading --dashboard-dir %s: %w", dashboardDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if err := processOne(filepath.Join(dashboardDir, entry.Name())); err != nil {
			return fmt.Errorf("fatal error processing %s: %w", filepath.Join(dashboardDir, entry.Name()), err)
		}
	}

	return nil
}
