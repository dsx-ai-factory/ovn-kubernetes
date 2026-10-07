// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package patch

import (
	"strings"
	"testing"
)

func TestGenerateDashboardTemplatePreservesObjectOrder(t *testing.T) {
	input := []byte(`{
  "uid": "dashboard-uid",
  "title": "test",
  "panels": [
    {
      "type": "timeseries",
      "targets": [
        {
          "expr": "sum by (instance) (rate(namedprocess_namegroup_cpu_seconds_total{cluster=\"$cluster\", groupname=\"ovs-vswitchd\",instance=~\"$instance\"}[$__rate_interval]))",
          "legendFormat": "{{instance}}",
          "refId": "A"
        }
      ],
      "title": "CPU"
    },
    {
      "type": "stat",
      "targets": [
		{
          "expr": "rate(ovs_vswitchd_interface_rx_sw_bytes{cluster=\"$cluster\", instance=~\"$instance\"}[5m])>100000",
          "refId": "A"
        }
      ],
      "title": "Build"
    }
  ],
  "templating": {
    "list": [
      {
        "name": "cluster",
        "type": "query",
        "query": {
          "qryType": 1,
          "query": "label_values(up{job=\"ovnkube-node\"}, cluster)",
          "refId": "prometheus-cluster-Variable-Query"
        }
      },
      {
        "name": "pod",
        "type": "query",
        "query": "label_values(kube_pod_info{cluster=\"$cluster\", namespace=\"ovn-kubernetes\"},pod)"
      }
    ]
  }
}
`)

	gotBytes, err := GenerateDashboardTemplate(input)
	if err != nil {
		t.Fatalf("GenerateDashboardTemplate: %v", err)
	}
	got := string(gotBytes)

	if strings.Index(got, `"uid"`) > strings.Index(got, `"title"`) {
		t.Fatalf("object key order changed:\n%s", got)
	}
	if strings.Index(got, `"type"`) > strings.Index(got, `"targets"`) {
		t.Fatalf("panel key order changed:\n%s", got)
	}
	for _, want := range []string{
		"{{`",
		"`}}{{ .Values.clusterLabel.label }}{{`",
		helmRawSplice(`{{ printf "label_values(%s, %s)" .Values.clusterLabel.discoveryMetric .Values.clusterLabel.label | toJson }}`),
		`{{ if eq .Values.processMetrics.profile "otel_process_scraper" }}`,
		`process_cpu_time_seconds_total`,
		`namedprocess_namegroup_cpu_seconds_total`,
		`{{host_name}}`,
		`{{instance}}`,
		`>100000`,
		`label_values(kube_pod_info{` + helmRawSplice(`{{ .Values.clusterLabel.label }}`) + `=\"$cluster\", namespace=\"ovn-kubernetes\"},pod)`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated template does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "cluster=") {
		t.Errorf("cluster label name was not replaced:\n%s", got)
	}
	if strings.Contains(got, `up{job=\"ovnkube-node\"}`) {
		t.Errorf("built-in cluster variable query was not replaced:\n%s", got)
	}
	if strings.Contains(got, `\u003e`) {
		t.Fatalf("greater-than was rewritten with a unicode escape: %s", got)
	}
	for _, sentinel := range introducedSentinels {
		if strings.Contains(got, sentinel) {
			t.Errorf("generated template contains unreplaced sentinel %q:\n%s", sentinel, got)
		}
	}
}

func TestGenerateDashboardTemplateRejectsReservedSentinel(t *testing.T) {
	input := []byte(`{"title":"` + helmIfOtelSentinel + `"}`)
	if _, err := GenerateDashboardTemplate(input); err == nil {
		t.Fatal("expected a reserved-sentinel error")
	}
}

func TestTargetPatchLeavesMalformedPromQLUnchanged(t *testing.T) {
	target := map[string]any{"expr": "not valid {"}

	patch, ok := targetPatchFor(target)
	if ok && patch.exprTo != "not valid {" {
		t.Fatalf("malformed expression changed to %q", patch.exprTo)
	}
}
