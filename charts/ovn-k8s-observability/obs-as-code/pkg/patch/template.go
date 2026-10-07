// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package patch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"obs-as-code/pkg/log"
)

const (
	helmIfOtelSentinel = "__SDN_HELM_IF_OTEL__"
	helmElseSentinel   = "__SDN_HELM_ELSE__"
	helmEndSentinel    = "__SDN_HELM_END__"
)

var introducedSentinels = []string{
	helmIfOtelSentinel,
	helmElseSentinel,
	helmEndSentinel,
}

func conditionalValue(condition, whenTrue, whenFalse string) string {
	if whenTrue == whenFalse {
		return whenTrue
	}
	return condition + whenTrue + helmElseSentinel + whenFalse + helmEndSentinel
}

func targetPatchFor(target map[string]any) (targetPatch, bool) {
	original, ok := target["expr"].(string)
	if !ok || original == "" {
		return targetPatch{}, false
	}

	otel, didTranslate, err := TransformProcessExporterExprs(original)
	if err != nil {
		log.Infof("skipping process-exporter rewrite for %q: %v", original, err)
		didTranslate = false
	}

	patch := targetPatch{exprFrom: original, exprTo: original}
	if didTranslate {
		patch.exprTo = conditionalValue(helmIfOtelSentinel, otel, original)
		legendFrom, _ := target["legendFormat"].(string)
		patch.legendFrom = legendFrom
		patch.legendTo = conditionalValue(helmIfOtelSentinel, translatedMetricLegend, legendFrom)
	}
	if patch.exprTo == original && patch.legendTo == "" {
		return targetPatch{}, false
	}
	return patch, true
}

func jsonStringLiteral(value string) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// clusterVariableQuery is the query written by the dashboard build. The patch
// replaces it with the configured discovery metric and cluster label.
const clusterVariableQuery = `label_values(up{job="ovnkube-node"}, cluster)`

type literalEdit struct {
	start int
	end   int
	value string
}

type jsonStringField struct {
	key   string
	start int
	end   int
	value string
}

func walkJSONValue(dec *json.Decoder, data []byte) ([]literalEdit, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return walkJSONToken(dec, data, token)
}

func walkJSONToken(dec *json.Decoder, data []byte, token json.Token) ([]literalEdit, error) {
	switch delim := token.(type) {
	case json.Delim:
		switch delim {
		case '{':
			return walkJSONObject(dec, data)
		case '[':
			return walkJSONArray(dec, data)
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
		}
	default:
		return nil, nil
	}
}

func walkJSONArray(dec *json.Decoder, data []byte) ([]literalEdit, error) {
	var edits []literalEdit
	for dec.More() {
		childEdits, err := walkJSONValue(dec, data)
		if err != nil {
			return nil, err
		}
		edits = append(edits, childEdits...)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return edits, nil
}

func walkJSONObject(dec *json.Decoder, data []byte) ([]literalEdit, error) {
	var edits []literalEdit
	var fields []jsonStringField
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("JSON object key is not a string (%T)", keyToken)
		}
		valueToken, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if value, ok := valueToken.(string); ok {
			end := int(dec.InputOffset())
			start, err := jsonStringStart(data, end)
			if err != nil {
				return nil, err
			}
			fields = append(fields, jsonStringField{key: key, start: start, end: end, value: value})
			continue
		}
		childEdits, err := walkJSONToken(dec, data, valueToken)
		if err != nil {
			return nil, err
		}
		edits = append(edits, childEdits...)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}

	objectEdits, err := editsForTarget(fields)
	if err != nil {
		return nil, err
	}
	return append(edits, objectEdits...), nil
}

func jsonStringStart(data []byte, end int) (int, error) {
	if end == 0 || data[end-1] != '"' {
		return 0, fmt.Errorf("JSON value at byte %d is not a string", end)
	}
	for i := end - 2; i >= 0; i-- {
		if data[i] != '"' {
			continue
		}
		slashes := 0
		for j := i - 1; j >= 0 && data[j] == '\\'; j-- {
			slashes++
		}
		if slashes%2 == 0 {
			return i, nil
		}
	}
	return 0, fmt.Errorf("JSON string ending at byte %d has no opening quote", end)
}

func editsForTarget(fields []jsonStringField) ([]literalEdit, error) {
	var exprField *jsonStringField
	var legendField *jsonStringField
	for i := range fields {
		switch fields[i].key {
		case "expr":
			exprField = &fields[i]
		case "legendFormat":
			legendField = &fields[i]
		}
	}
	if exprField == nil {
		return nil, nil
	}

	target := map[string]any{"expr": exprField.value}
	if legendField != nil {
		target["legendFormat"] = legendField.value
	}
	patch, ok := targetPatchFor(target)
	if !ok {
		return nil, nil
	}

	edits := []literalEdit{{start: exprField.start, end: exprField.end, value: patch.exprTo}}
	if patch.legendTo == "" {
		return edits, nil
	}
	if legendField == nil || legendField.value != patch.legendFrom {
		return nil, fmt.Errorf("legend %q not found beside expression %s", patch.legendFrom, patch.exprFrom)
	}
	edits = append(edits, literalEdit{start: legendField.start, end: legendField.end, value: patch.legendTo})
	return edits, nil
}

func applyLiteralEdits(data []byte, edits []literalEdit) ([]byte, error) {
	sort.SliceStable(edits, func(i, j int) bool {
		return edits[i].start > edits[j].start
	})
	out := data
	for _, edit := range edits {
		literal, err := jsonStringLiteral(edit.value)
		if err != nil {
			return nil, err
		}
		out = append(append(append([]byte{}, out[:edit.start]...), literal...), out[edit.end:]...)
	}
	return out, nil
}

type targetPatch struct {
	exprFrom   string
	exprTo     string
	legendFrom string
	legendTo   string
}

// helmRawSplice closes the surrounding Helm raw string, emits an action, and
// reopens the raw string. Grafana text such as {{pod}} stays inside the raw
// string and is not evaluated as a template.
func helmRawSplice(action string) string {
	return "`}}" + action + "{{`"
}

func replaceTemplateSentinels(out []byte) ([]byte, error) {
	if bytes.Contains(out, []byte("`")) {
		return nil, fmt.Errorf("dashboard JSON contains a backtick, which cannot appear inside a Helm raw string")
	}
	queryLiteral, err := jsonStringLiteral(clusterVariableQuery)
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(out, queryLiteral) {
		return nil, fmt.Errorf("dashboard is missing cluster variable query %s", clusterVariableQuery)
	}
	queryTemplate := helmRawSplice(
		`{{ printf "label_values(%s, %s)" .Values.clusterLabel.discoveryMetric .Values.clusterLabel.label | toJson }}`,
	)
	out = bytes.ReplaceAll(out, queryLiteral, []byte(queryTemplate))
	// Expressions already contain cluster="$cluster". Replace only the label
	// name so "$cluster" and the rest of the expression stay in place.
	out = bytes.ReplaceAll(out, []byte("cluster="), []byte(helmRawSplice(`{{ .Values.clusterLabel.label }}`)+"="))
	replacements := []struct {
		sentinel    string
		replacement string
	}{
		{helmIfOtelSentinel, helmRawSplice(`{{ if eq .Values.processMetrics.profile "otel_process_scraper" }}`)},
		{helmElseSentinel, helmRawSplice(`{{ else }}`)},
		{helmEndSentinel, helmRawSplice(`{{ end }}`)},
	}
	for _, replacement := range replacements {
		out = bytes.ReplaceAll(out, []byte(replacement.sentinel), []byte(replacement.replacement))
	}
	wrapped := make([]byte, 0, len(out)+6)
	wrapped = append(wrapped, "{{`"...)
	wrapped = append(wrapped, out...)
	wrapped = append(wrapped, "`}}"...)
	return wrapped, nil
}

func GenerateDashboardTemplate(data []byte) ([]byte, error) {
	for _, sentinel := range introducedSentinels {
		if bytes.Contains(data, []byte(sentinel)) {
			return nil, fmt.Errorf("dashboard contains reserved template sentinel %q", sentinel)
		}
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	edits, err := walkJSONValue(decoder, data)
	if err != nil {
		return nil, err
	}
	out, err := applyLiteralEdits(data, edits)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 || out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return replaceTemplateSentinels(out)
}

func ProcessDashboardTemplateFile(path, outDir string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, err := GenerateDashboardTemplate(data)
	if err != nil {
		return err
	}

	outPath := path
	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return err
		}
		outPath = filepath.Join(outDir, filepath.Base(path))
	}
	return os.WriteFile(outPath, out, 0o644)
}
