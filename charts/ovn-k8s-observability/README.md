# ovn-k8s-observability

![Version: 2.4.2](https://img.shields.io/badge/Version-2.4.2-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 2.4.2](https://img.shields.io/badge/AppVersion-2.4.2-informational?style=flat-square)

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| SDN Developers |  |  |

## Source Code

* <https://github.com/dsx-ai-factory/ovn-kubernetes>
* <https://github.com/ovn-kubernetes/ovn-kubernetes>

Helm chart for ovn-kubernetes observability resources: `ServiceMonitor`,
`PrometheusRule`, and `GrafanaDashboard`. It is packaged separately from
[`ovn-kubernetes`](../ovn-kubernetes) because those CRDs are not present when
the CNI chart is installed.

Prometheus Operator and Grafana Operator CRDs must already exist in the cluster.

## Quick start

```bash
helm install ovn-k8s-observability charts/ovn-k8s-observability \
  --namespace ovn-kubernetes --create-namespace
```

By default, this command deploys interconnect alert rules and dashboards,
plus ServiceMonitors for `ovnkube-node` and `ovnkube-control-plane`. Optional
OVS offload and ingress-filter alerts are disabled (`ovsNoOffloadBytes`,
`ovsNoOffloadIntraNodeBytes`, `ovsNoIngressFilter`, and the per-node
`ovsBridgeFlowsTooHigh*` rules); enable them under `alert-rule.rules` when
they apply to the cluster.

Override a setting at install time, for example:

```bash
helm upgrade --install ovn-k8s-observability charts/ovn-k8s-observability \
  --namespace ovn-kubernetes \
  --set dashboard.grafana.datasourceName=prometheus
```

Render manifests without installing:

```bash
helm template ovn-k8s-observability charts/ovn-k8s-observability \
  --namespace ovn-kubernetes
```

## Cluster label

`global.clusterLabel` (default `cluster`) names the Prometheus label that
identifies a cluster. The alert rules keep it in aggregations and vector
matches, so series from different clusters are not mixed when several clusters
share one Prometheus, and the resulting alerts carry it. The dashboards use the
same label for the `$cluster` variable and the panel selectors. Set it to the
label your metrics carry (for example `k8s_cluster_name`):

```yaml
global:
  clusterLabel: k8s_cluster_name
```

The label must be present on the series the alerts and dashboards use,
including `kube_node_info` and the `ovnkube-*` and OVS series. It must not be
empty and must be a valid Prometheus label name; the render fails otherwise.

## Dashboard customization

Dashboard PromQL is rendered from Helm-templated JSON. Every dashboard includes
a `$cluster` variable whose label is `global.clusterLabel`. Set
`dashboard.clusterDiscoveryMetric` to the series used to discover its
values; it may be a metric name or a PromQL series selector.

The `dashboard.processMetrics.profile` value controls only the host OVS process
CPU and memory panels:

- `otel_process_scraper` uses `process_cpu_time_seconds_total` and
  `process_memory_usage_bytes`.
- `process_exporter` uses `namedprocess_namegroup_cpu_seconds_total` and
  `namedprocess_namegroup_memory_bytes`.

Kubernetes container CPU and memory panels are unchanged by this setting.

## Package

```bash
helm package charts/ovn-k8s-observability
```

That produces `ovn-k8s-observability-2.4.2.tgz`. `obs-as-code/` and the
raw `files/dashboards/` sources are excluded by `.helmignore` and are not part
of the package.

## Sub-charts

| Chart | Version | Path |
| --- | --- | --- |
| `alert-rule` | `2.4.2` | `charts/alert-rule` |
| `dashboard` | `2.4.2` | `charts/dashboard` |

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| alert-rule.dashboardBaseURL | string | `""` | Optional base URL used to build per-alert dashboard labels |
| alert-rule.enableShardingRules | bool | `false` | Add shardable labels and extra sharding rules |
| alert-rule.labels.team | string | `"sdn"` | Labels injected into every alert rule |
| alert-rule.namespace | string | `"ovn-kubernetes"` | Namespace for PrometheusRule resources |
| alert-rule.rules.ovnKubePodHealthFilteredNodes.enable | bool | `true` | Alert when ovnkube pods are missing on the filtered node set |
| alert-rule.rules.ovnKubePodHealthRequiredNodes.enable | bool | `false` | Alert when ovnkube pods are missing on required nodes |
| alert-rule.rules.ovsBridgeFlowsTooHighCPUNodes.enable | bool | `false` | Alert on high OVS bridge flow counts on CPU nodes |
| alert-rule.rules.ovsBridgeFlowsTooHighGPUNodes.enable | bool | `false` | Alert on high OVS bridge flow counts on GPU nodes |
| alert-rule.rules.ovsBridgeFlowsTotalTooHigh.enable | bool | `true` | Alert on high OVS bridge flow counts across all nodes |
| alert-rule.rules.ovsBridgeFlowsTotalTooHigh.threshold | int | `200000` | Flow-count threshold for `ovsBridgeFlowsTotalTooHigh` |
| alert-rule.rules.ovsMegaFlows.enable | bool | `true` | Alert on unusually high OVS megaflow counts |
| alert-rule.rules.ovsMegaFlows.filter | string | `""` | Optional PromQL matcher for megaflow alerts |
| alert-rule.rules.ovsMegaFlows.threshold | int | `100000` | Megaflow-count threshold |
| alert-rule.rules.ovsNoIngressFilter.enable | bool | `false` | Alert when ingress policing is missing on offload-capable interfaces |
| alert-rule.rules.ovsNoIngressFilter.podInterfaceFilter | string | `"interface=~\"^pf[01]vf[0-9]+$\""` | PromQL matcher for pod interfaces |
| alert-rule.rules.ovsNoIngressFilter.uplinkFilter | string | `"interface=~\"^p[01]$|^pf[01]hpf$\""` | PromQL matcher for uplink interfaces |
| alert-rule.rules.ovsNoOffloadBytes.enable | bool | `false` | Alert on non-offloaded traffic |
| alert-rule.rules.ovsNoOffloadBytes.filter | string | `"interface=~\"^pf[0|1]vf[0-9]+$\""` | PromQL matcher for non-offloaded traffic alerts |
| alert-rule.rules.ovsNoOffloadIntraNodeBytes.enable | bool | `false` | Alert on non-offloaded traffic through intra-node ports |
| alert-rule.rules.ovsNoOffloadIntraNodeBytes.filter | string | `""` | Required when `enable` is true; rendering fails while it is empty. PromQL matcher selecting the intra-node ports, for example `interface=~"^enp.*_[0-9]+$", port=~".*_app.*"`. The right pattern depends on the deployment, so there is no default. |
| dashboard.clusterDiscoveryMetric | string | `"up{job=\"ovnkube-node\"}"` | PromQL series selector used to discover the values of the cluster variable (the label is `global.clusterLabel`) |
| dashboard.grafana.allowCrossNamespaceImport | bool | `true` | Allow importing dashboards from other namespaces |
| dashboard.grafana.datasourceName | string | `"prom-victoriametrics"` | Grafana datasource mapped to `DS_PROMETHEUS` |
| dashboard.grafana.instanceSelector.matchLabels."grafana.com/selector" | string | `"grafana"` | Grafana instance selector for dashboard provisioning |
| dashboard.namespace | string | `"ovn-kubernetes"` | Namespace for GrafanaDashboard resources |
| dashboard.processMetrics.profile | string | `"otel_process_scraper"` | Host OVS process metric source: process_exporter or otel_process_scraper |
| global.clusterLabel | string | `"cluster"` | Prometheus label identifying a cluster. Preserved by the alert rule aggregations and vector matches, and used by the Grafana cluster variable and panel selectors. Must be non-empty and a valid Prometheus label name. |
| global.ovnkubeNamespace | string | `"ovn-kubernetes"` | Namespace where the OVN Kubernetes workloads run |
| monitoring.metricsPortName | string | `"ovnkube-node-metrics"` | Service port name scraped for ovnkube-node metrics |
| monitoring.namespace | string | `"ovn-kubernetes"` | Namespace for ServiceMonitor resources |
| monitoring.serviceMonitorSelectorLabels.release | string | `"prometheus"` | Labels Prometheus uses to select ServiceMonitors |
| tags.alert-rule | bool | `true` | Enable the alert-rule subchart |
| tags.dashboard | bool | `true` | Enable the dashboard subchart |
