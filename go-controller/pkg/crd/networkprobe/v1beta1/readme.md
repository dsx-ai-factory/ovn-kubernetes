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

(will Add installation instructions here)


## Note
Before usage custom servers have to be setup,
targets have to be setup

## Usage

To create a Network Probe, define a custom resource of kind `NetworkProbe`. Here's a basic example:

```yaml
apiVersion: k8s.ovn.org/v1beta1
kind: NetworkProbe
metadata:
  name: testprobe
  namespace: ovn-kubernetes
spec:
  dnsProbes:
    - interval: 30s
      nameServer: 8.8.8.8
      dnsLookupName: www.google.com
  httpProbes:
    - url: http://www.nvidia.com
      interval: 30s
      packetSpec:
        dscp: "34"
  udpStreamProbes:
    - host: 10.1.128.61       # Replace with your target host
      interval: 10s           # Interval in seconds between probes
      packetCount: 5          # Number of packets to send per probe
      packetInterval: 1s      # Interval in seconds between packets
      port: 12345             # Target port to probe
      packetSpec:
        dscp: "34"
        payloadSize: "512B"
  tcpProbes:
    - host: 10.1.128.61
      interval: 10s
      port: 12346
      packetSpec:
        dscp: "34"
        payloadSize: "512B"