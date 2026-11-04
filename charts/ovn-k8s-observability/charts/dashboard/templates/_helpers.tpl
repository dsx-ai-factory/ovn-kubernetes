{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}

{{- define "dashboard.validateValues" -}}
{{- $profiles := list "process_exporter" "otel_process_scraper" -}}
{{- if not (has .Values.processMetrics.profile $profiles) -}}
{{- fail (printf "dashboard.processMetrics.profile must be one of %s" (join ", " $profiles)) -}}
{{- end -}}
{{- $_ := include "ovnK8sObservability.clusterLabel" . -}}
{{- if or (empty .Values.clusterDiscoveryMetric) (contains "`" .Values.clusterDiscoveryMetric) -}}
{{- fail "dashboard.clusterDiscoveryMetric must be a non-empty PromQL series selector without backticks" -}}
{{- end -}}
{{- end -}}
