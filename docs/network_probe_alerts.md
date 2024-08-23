
# Network Probe Alerts

This project provides various network probes such as DNS, HTTP, TCP, and UDP probes. Each probe exposes metrics that can be used to monitor and alert on the performance and reliability of your network services. Below are the metrics exposed by each probe, followed by sample Prometheus alerts.

## 1. Metrics

### 1.1 DNS Metrics
**Namespace:** `network_probe`
**Subsystem:** `dns`
**Full-qualified-metric-name:** `network_probe_dns_<metric_name>`

| Metric Name       | Labels                                                       | Type    | Definition                             |
| ----------------- |--------------------------------------------------------------| ------- |----------------------------------------|
| `response_time`    | `name`, `namespace`,`name_server`,`lookup_name`              | Gauge   | Response time of the DNS probe         |
| `attempts_total`   | `name`, `namespace`,`name_server`,`lookup_name`              | Counter | Total number of DNS lookup attempts    |
| `completed_total`  | `name`, `namespace`,`name_server`,`lookup_name`              | Counter | Total number of successful DNS lookups |
| `errors_total`     | `name`, `namespace`,`name_server`,`lookup_name`,`error_type` | Counter | Total number of DNS lookup errors      |

### 1.2 HTTP Metrics
**Namespace:** `network_probe`
**Subsystem:** `http`
**Full-qualified-metric-name:** `network_probe_http_<metric_name>`

| Metric Name       | Labels                                       | Type    | Definition                               |
| ----------------- |----------------------------------------------| ------- |------------------------------------------|
| `response_time`    | `name`, `namespace`, `http_url`              | Gauge   | Response time of the HTTP probe          |
| `attempts_total`   | `name`, `namespace`, `http_url`              | Counter | Total number of HTTP request attempts    |
| `completed_total`  | `name`, `namespace`, `http_url`              | Counter | Total number of successful HTTP requests |
| `errors_total`     | `name`, `namespace`, `http_url` `error_type` | Counter | Total number of HTTP request errors      |

### 1.3 TCP Metrics
**Namespace:** `network_probe`
**Subsystem:** `tcp`
**Full-qualified-metric-name:** `network_probe_tcp_<metric_name>`

| Metric Name       | Labels | Type    | Definition                                |
|-------------------| -- | ------- |-------------------------------------------|
| `rtt_latency`     | `name`, `namespace`, `host`, `port`| Gauge   | Response time of the TCP probe            |
| `attempts_total`  | `name`, `namespace`, `host`, `port` | Counter | Total number of TCP connection attempts   |
| `completed_total` | `name`, `namespace`, `host`, `port` | Counter | Total number of successful TCP connections |
| `errors_total`    | `name`, `namespace`, `host`, `port`, `error_type`| Counter | Total number of TCP connection errors     |

### 1.4 UDP Metrics
**Namespace:** `network_probe`
**Subsystem:** `udp`
**Full-qualified-metric-name:** `network_probe_udp_<metric_name>`

| Metric Name        | Labels                              | Type    | Definition                                                      |
|--------------------|-------------------------------------| ------- |-----------------------------------------------------------------|
| `tx_latency`       | `name`, `namespace`, `host`, `port` | Gauge   | The latency from sender side to receiver side for the UDP probe |
| `rx_latency`       | `name`, `namespace`, `host`, `port` | Gauge   | The latency from receiver to sender side for the UDP probe      |
| `rtt_latency`      | `name`, `namespace`, `host`, `port` | Gauge   | The bi-directional latency of the UDP probe                     |
| `attempts_total`   | `name`, `namespace`, `host`, `port` | Counter | Total number of UDP probe attempts                              |
| `completed_total`  | `name`, `namespace`, `host`, `port` | Counter | Total number of completed UDP probes                            |
| `errors_total`     | `name`, `namespace`, `host`, `port`, `error_type` | Counter | Total number of UDP probe errors                                |
| `packet_loss_rate` | `name`, `namespace`, `host`, `port` | Gauge   | Packet loss in percentage for the UDP probe packets             |
| `jitter`           | `name`, `namespace`, `host`, `port` | Gauge   | Average variation in packet round trip times for UDP probe      |


## 2. Sample Alerts

You can create Prometheus alerts based on the above metrics to monitor network performance. Below are some example alert configurations:

### 2.1 High DNS Response Time
```yaml
alert: HighDNSResponseTime
expr: network_probe_dns_response_time{namespace="ovn-kubernetes",job="ovnkube-node"} > 0.1
for: 4m
labels:
  severity: warning
  pduuid: P10A23N
  dashboard: https://ngn-grafana.thanos.nvidiangn.net/d/UVSzupDGz/host-ovs
annotations:
  message: "Pod {{ $labels.pod }} is experiencing critical DNS response times ({{ $value }} seconds) for more than 4 minutes."
```

### 2.2 High HTTP Response Time
```yaml
alert: HighHTTPResponseTime
expr: network_probe_http_response_time{namespace="ovn-kubernetes",job="ovnkube-node"} > 0.1
for: 4m
labels:
  severity: warning
  pduuid: P10A23N
  dashboard: https://ngn-grafana.thanos.nvidiangn.net/d/UVSzupDGz/host-ovs
annotations:
  message: "Pod {{ $labels.pod }} is experiencing high HTTP response times ({{ $value }} seconds) for more than 4 minutes."
```

### 2.3 High TCP Response Time
```yaml
alert: HighTCPResponseTime
expr: network_probe_tcp_response_time{namespace="ovn-kubernetes",job="ovnkube-node"} > 0.1
for: 4m
labels:
  severity: warning
  pduuid: P10A23N
  dashboard: https://ngn-grafana.thanos.nvidiangn.net/d/UVSzupDGz/host-ovs
annotations:
  message: "Pod {{ $labels.pod }} is experiencing high TCP response times ({{ $value }} seconds) for more than 4 minutes."
```

### 2.4 High UDP Response Time
```yaml
alert: HighUDPResponseTime
expr: network_probe_udp_response_time{namespace="ovn-kubernetes",job="ovnkube-node"} > 0.1
for: 4m
labels:
  severity: warning
  pduuid: P10A23N
  dashboard: https://ngn-grafana.thanos.nvidiangn.net/d/UVSzupDGz/host-ovs
annotations:
  message: "Pod {{ $labels.pod }} is experiencing high UDP response times ({{ $value }} seconds) for more than 4 minutes."
```
