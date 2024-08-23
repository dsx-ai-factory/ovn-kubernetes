# Kubernetes Network Probe

## Overview

The Kubernetes Network Probe is a custom resource that allows users to define and run network probes on selected nodes within a Kubernetes cluster. It generates different types of network traffic at specified intervals and captures performance metrics such as latency, jitter, and packet loss for each probe execution.

## Features

- **Multiple Probe Types**: Supports DNS, HTTP, TCP, and UDP Stream probes.
- **Node Selection**: Run probes on specific nodes using label selectors.
- **Customizable Intervals**: Set custom intervals for probe execution.
- **Traffic Customization**: Configure packet size, DSCP, and other traffic parameters.
- **Metrics Collection**: Capture key network performance metrics.
- **Suspend Option**: Ability to suspend probes when needed.

## Probe Types

1. **DNS Probe**: Perform DNS lookups to specified nameservers.
2. **HTTP Probe**: Send HTTP/HTTPS requests to specified URLs.
3. **TCP Probe**: Establish TCP connections to specified hosts and ports.
4. **UDP Stream Probe**: Send UDP packets in a stream to specified destinations.

## Installation
As Network probe is a custom resource, need to see if the network probe CRD has been applied or not. 
```
kubectl api-resources | grep networkprobe
networkprobes                     netprobe           k8s.ovn.org/v1beta1                    true         NetworkProbe
if it's not present apply the network probe CR
```

## Note
For UDP probes, custom target that runs UDP server have to be setup.

## Usage
To create a Network Probe, define a custom resource of kind `NetworkProbe`. Here's a basic example:

```yaml
apiVersion: k8s.ovn.org/v1beta1
kind: NetworkProbe
metadata:
  name: testprobe
  namespace: ovn-kubernetes
spec:
  # Node selctor labels to select which nodes for probe to run
  nodeSelector:
    matchLabels:
      networkprobe: enable
  dnsProbes:
    - nameServer: 10.96.0.10
      lookupName: kubernetes.default.svc.cluster.local
      interval: 20s
    - nameServer: 10.96.0.10
      lookupName: google.com
      interval: 20s
  httpProbes:
    - url: "https://10.96.0.1:443/healthz?verbose=true"
      tlsConfig:
        caCert:
          configMap:
            name: api-server-cacert
            key: ca.crt
      interval: 20s
      packetSpec:
        dscp: 34
    - url: "https://10.96.0.1:443/readyz?verbose=true"
      tlsConfig:
        caCert:
          secret:
            name: api-server-cacert-secret
            key: ca.crt
      interval: 20s
    - url: "http://10.96.0.1:443/healthz?verbose=true"
      interval: 20s
    - url: "http://nvidia.com"
      interval: 30s
  udpStreamProbes:
    - host: 10.104.88.175        # this is the svc ip or target POD IP where
      interval: 20s              # upd server will be running that echoes back the resposne 
      packetCount: 5
      packetInterval: 1s
      port: 12345
      packetSpec:
        dscp: 36
        payloadSize: "512B"
  tcpProbes:
    - host: 10.96.0.1
      interval: 20s
      port: 443
      packetSpec:
        dscp: 34
        payloadSize: "512B"
    - host: 10.96.0.10
      interval: 20s
      port: 53
  suspend: false

