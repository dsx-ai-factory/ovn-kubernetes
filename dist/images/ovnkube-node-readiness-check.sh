#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

bracketify() { case "$1" in *:*) echo "[$1]" ;; *) echo "$1" ;; esac; }

# ovnkube.sh binds the metrics server to METRICS_IP, falling back to
# OVN_METRICS_ENDPOINT_IP, so probe the same address.
metrics_endpoint_ip=${METRICS_IP:-${OVN_METRICS_ENDPOINT_IP}}
if [[ -z ${metrics_endpoint_ip} ]]; then
  metrics_endpoint_ip=${K8S_NODE_IP:-0.0.0.0}
fi
metrics_worker_port=${OVN_METRICS_WORKER_PORT}
if [[ -z ${metrics_worker_port} ]]; then
	metrics_worker_port=9410
fi
# ncat takes a bare IPv6 address, not a bracketed one.
ncat -zv --ssl $metrics_endpoint_ip $metrics_worker_port >/root/health 2>&1
return_value=$?
if [ $return_value != 0 ]; then
	echo "connectivity check with ssl on metrics endpoint $(bracketify $metrics_endpoint_ip):$metrics_worker_port failed"
	echo "$(< /root/health)"
	exit $return_value
fi
k8s_svc_host=$(bracketify $KUBERNETES_SERVICE_HOST)
curl -k https://$k8s_svc_host:$KUBERNETES_SERVICE_PORT/readyz >/root/health 2>&1
return_value=$?
if [ $return_value != 0 ]; then
	echo "connectivity check on k8s svc endpoint $k8s_svc_host:$KUBERNETES_SERVICE_PORT failed"
	echo "$(< /root/health)"
	exit $return_value
fi
coredns_cluster_ip=${COREDNS_CLUSTER_IP:-10.223.0.3}
dns_names=${EXTERNAL_DNS_NAMES:-"google.com cloudflare.com"}
echo "" > /root/health
for dns_name in $dns_names; do
	nslookup $dns_name $coredns_cluster_ip >>/root/health 2>&1
	return_value=$?
	if [ $return_value == 0 ]; then
		break
	fi
done
if [ $return_value != 0 ]; then
	echo "connectivity check on cluster endpoint $(bracketify $coredns_cluster_ip):53 failed"
	echo "$(< /root/health)"
	exit $return_value
fi
exit 0
