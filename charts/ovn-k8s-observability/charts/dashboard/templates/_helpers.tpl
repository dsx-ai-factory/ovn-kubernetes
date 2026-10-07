{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}

{{- define "dashboard.validateValues" -}}
{{- $profiles := list "process_exporter" "otel_process_scraper" -}}
{{- if not (has .Values.processMetrics.profile $profiles) -}}
{{- fail (printf "dashboard.processMetrics.profile must be one of %s" (join ", " $profiles)) -}}
{{- end -}}
{{- if not (regexMatch "^[a-zA-Z_][a-zA-Z0-9_]*$" .Values.clusterLabel.label) -}}
{{- fail "dashboard.clusterLabel.label must be a valid Prometheus label name" -}}
{{- end -}}
{{- if or (empty .Values.clusterLabel.discoveryMetric) (contains "`" .Values.clusterLabel.discoveryMetric) -}}
{{- fail "dashboard.clusterLabel.discoveryMetric must be a non-empty PromQL series selector without backticks" -}}
{{- end -}}
{{- end -}}
