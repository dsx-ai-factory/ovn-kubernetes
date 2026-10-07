# Observability as Code

The `obs-as-code` tool builds the Grafana dashboards shipped by the
`ovn-k8s-observability` Helm chart. Dashboard definitions are maintained in Go,
generated as raw Grafana JSON, and then patched into Helm-templated JSON.

The generated files follow this pipeline:

```text
obs-as-code/pkg/build/dashboard/*.go
                |
                | build dashboard
                v
files/dashboards/*.json
                |
                | patch dashboard
                v
charts/dashboard/json/*.json
                |
                | Helm tpl
                v
GrafanaDashboard resources
```

Do not edit either generated JSON directory by hand. Make changes to the Go
dashboard definitions or patching code, and then regenerate the files.

## Quick start

From `charts/ovn-k8s-observability`, run:

```bash
make build-dashboard
```

This target builds the tool and runs both dashboard stages:

```bash
./bin/sdn-obs-as-code build dashboard \
  --json-dir files/dashboards

./bin/sdn-obs-as-code patch dashboard \
  --dashboard-dir files/dashboards \
  --out-dir charts/dashboard/json
```

The first command generates raw dashboards in `files/dashboards/`. The second
command reads those files and writes Helm-templated dashboard JSON to
`charts/dashboard/json/`.

## Build the tool

Build `sdn-obs-as-code` with:

```bash
make obs-as-code
```

The binary is written to:

```text
bin/sdn-obs-as-code
```

Command help is available through:

```bash
./bin/sdn-obs-as-code --help
./bin/sdn-obs-as-code build dashboard --help
./bin/sdn-obs-as-code patch dashboard --help
```

## Generate raw dashboard JSON

Dashboard definitions are implemented under:

```text
obs-as-code/pkg/build/dashboard/
```

Shared panel, variable, query, and datasource builders are under:

```text
obs-as-code/pkg/build/dashboard/lib/
```

The set of dashboards to generate is registered in
`obs-as-code/pkg/build/dashboard.go`.

Generate all raw dashboards with:

```bash
./bin/sdn-obs-as-code build dashboard \
  --json-dir files/dashboards
```

The resulting files are normal Grafana dashboard JSON. They are useful for
reviewing dashboard changes independently from Helm templating.

Every generated dashboard includes:

- A Prometheus datasource input named `DS_PROMETHEUS`.
- A `$cluster` query variable.
- `cluster="$cluster"` as the first matcher in panel expressions and other
  query-variable selectors.

### Optional Grafana resource manifests

The build command can also generate Grafana API resource manifests:

```bash
./bin/sdn-obs-as-code build dashboard \
  --json-dir files/dashboards \
  --manifests-dir output/dashboard-manifests
```

These `dashboard.grafana.app/v1beta1` manifests are optional and are separate
from the `GrafanaDashboard` custom resources rendered by this Helm chart.

## Patch raw JSON into Helm templates

Patch every raw dashboard with:

```bash
./bin/sdn-obs-as-code patch dashboard \
  --dashboard-dir files/dashboards \
  --out-dir charts/dashboard/json
```

To patch one dashboard:

```bash
./bin/sdn-obs-as-code patch dashboard \
  --dashboard-file files/dashboards/sdn-host-ovs.json \
  --out-dir charts/dashboard/json
```

`--dashboard-file` and `--dashboard-dir` are mutually exclusive. Always specify
`--out-dir` for the normal chart workflow; without it, the input file is
overwritten.

The patch stage performs targeted, in-place text edits so existing JSON key
order and unrelated content remain unchanged. It:

1. Replaces the literal `cluster=` matcher label with
   `dashboard.clusterLabel.label`.
2. Replaces the built-in cluster query with a query constructed from
   `dashboard.clusterLabel.discoveryMetric` and
   `dashboard.clusterLabel.label`.
3. Adds Helm conditionals for the supported host OVS process-metric profiles.
4. Wraps the JSON in a Helm raw string.

The raw-string wrapper keeps Grafana legend templates such as `{{pod}}`
literal. Helm values are spliced outside that raw string only where chart
customization is required.

The host OVS process profile supports:

- `otel_process_scraper`, using `process_cpu_time_seconds_total` and
  `process_memory_usage_bytes`.
- `process_exporter`, using `namedprocess_namegroup_cpu_seconds_total` and
  `namedprocess_namegroup_memory_bytes`.

This profile affects only the host OVS process CPU and memory panels.

## How Helm consumes the patched JSON

Each dashboard subchart template loads its corresponding patched file with:

```gotemplate
{{- tpl (.Files.Get "json/<dashboard>.json") . | nindent 4 }}
```

The parent chart exposes the relevant settings under `dashboard`:

```yaml
dashboard:
  clusterLabel:
    label: cluster
    discoveryMetric: up{job="ovnkube-node"}
  processMetrics:
    profile: otel_process_scraper
```

The dashboard subchart validates:

- The process profile is `process_exporter` or `otel_process_scraper`.
- The cluster label is a valid Prometheus label name.
- The discovery metric is non-empty and contains no backticks.

Render the chart locally with:

```bash
helm template ovn-k8s-observability .
```

## Development workflow

When changing dashboard content:

1. Update the Go dashboard builders.
2. Run `make build-dashboard`.
3. Review changes in `files/dashboards/` first.
4. Review the corresponding templates in `charts/dashboard/json/`.
5. Run the tests and render the chart.

Useful checks are:

```bash
go test -C obs-as-code ./...
make lint
```

The dashboard rendering test verifies that all dashboards produce valid JSON,
cluster values render correctly, both process profiles work, Grafana legend
templates remain literal, and invalid values are rejected.

## Packaging

`files/dashboards/` and `obs-as-code/` are development inputs and are excluded
from the Helm package by `.helmignore`. The patched files under
`charts/dashboard/json/` are included because the dashboard subchart loads
them while rendering its `GrafanaDashboard` resources.
