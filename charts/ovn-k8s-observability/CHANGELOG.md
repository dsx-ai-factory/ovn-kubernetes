# 2.4.3
- alert-rule, dashboard: add `global.clusterLabel`, defaulting to `cluster`,
  and include it in the aggregation and join clauses of the 16 alert rules
  that previously merged series from different clusters when several clusters
  forward metrics to one store. `OvnKubeClusterManagerNotRunning` no longer
  uses `absent()` and `OvnControllerGeneveTunnelCountIsLow` compares each node
  against its own cluster's node count. A shared helper fails the render on an
  empty or invalid label name.
- dashboard: flatten `dashboard.clusterLabel.discoveryMetric` to
  `dashboard.clusterDiscoveryMetric`, and regenerate the dashboard JSON.
- alert-rule: fix the comparison order of
  `OvnKubeIncreaseInPodCreationLatencyP100` so a rising latency fires it, and
  reduce its pending duration below the comparison window.
- dashboard: correct the `ovsdb-server` CPU panel unit and the NOT READY and
  UNAVAILABLE colours.
- helm: select the subcharts with conditions, and add
  `monitoring.metricsPortName`.
- alert-rule: declare `rules.ovsNoOffloadIntraNodeBytes.filter` in
  `values.yaml`, which the template requires when the rule is enabled but
  neither the values nor the README documented.
- docs: use a `<VERSION>` placeholder in the packaging example rather than
  rendering the chart version, which went stale on every bump.
- chore: bump the `obs-as-code` Go modules for the Go 1.26.10 and 1.26.11
  toolchain CVEs, `go-jose`, `golang.org/x/net`, and `klauspost/compress`.
  These affect only the tooling that generates the alert rules and dashboards.

# 2.4.2
- chore: bump the `obs-as-code` Go modules to resolve a CVE, and tidy the module
  files afterwards. The rendered chart is unchanged in this release; the bump
  affects only the tooling that generates the alert rules and dashboards.

# 2.4.1
- chore: bump the crypto modules of the `obs-as-code` generator to resolve a
  CVE. The rendered chart is unchanged in this release; the bump affects the
  tooling that generates the alert rules and dashboards.

# 2.4.0
- Initial release of Helm Chart tracking ovn-kubernetes@release-2.4 branch
