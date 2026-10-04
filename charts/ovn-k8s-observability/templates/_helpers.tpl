{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}
{{/*
SDN dashboard paths by component.
*/}}
{{- define "getSdnDashboardPaths" -}}
ovn-controller: /d/952a078d1f7d2aa9eefb171c871191e8/sdn-ovn-k8s-ic-ovn-controller
ovn-northbound-db: /d/b246596647bea0b5a3fa81f15322b436/sdn-ovn-k8s-ic-ovn-northbound-db
ovn-northd: /d/0adc42da26932ac7c24b0f1992d882ae/sdn-ovn-k8s-ic-ovn-northd
ovn-southbound-db: /d/dae6b61a41fb86efb94d91a712787324/sdn-ovn-k8s-ic-ovn-southbound-db
ovnkube-cluster-manager: /d/7ae56dbe010ad8befe6861dcfd23e42b/sdn-ovn-k8s-ic-ovnkube-cluster-manager
ovnkube-controller: /d/o8snlkteorlnvcor/sdn-ovn-k8s-ic-ovnkube-controller
sdn-ovs: /d/9ad6c1e913ff02dfc8599fc61903318b/sdn-host-ovs
{{- end }}

{{/*
Render common alert rule labels (pduuid, component, etc.) from .Values.labels.

Usage:
  {{ include "alertRuleLabels" . }}
  {{ include "alertRuleLabels" (merge (dict "indent" 8) .) }}
  {{ include "alertRuleLabels" (merge (dict "labels" $ruleLabels "indent" 8) .) }}
*/}}
{{- define "alertRuleLabels" -}}
{{- $labels := default (dict) .Values.labels -}}
{{- if hasKey . "labels" -}}
  {{- $labels = .labels -}}
{{- end -}}
{{- $indent := .indent | default 7 -}}
{{- with $labels }}
{{- toYaml . | nindent $indent }}
{{- end }}
{{- end -}}
