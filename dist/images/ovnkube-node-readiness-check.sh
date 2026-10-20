#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Every network step below is time-bounded so that the whole script finishes
# within the readiness probe's timeoutSeconds (15s in the helm charts) and can
# report which check failed. Without bounds, a VIP or resolver that silently
# drops packets makes curl wait for TCP's own timeout (tens of seconds) and
# nslookup 15s per name, so the kubelet kills the script first and reports only
# "command timed out" with no detail. The kubelet kills just the shell: the
# children it was waiting on keep running, so an unbounded curl also outlives
# every failed probe run.
#
# Connect timeouts are 2s: 1s equals the kernel's first SYN retransmit, so a
# single lost SYN would fail the step. /readyz on kube-apiserver runs its etcd
# checks with 2s deadlines each and can legitimately take a few seconds.
#
# Worst-case budget: metrics 3s + API server 4s + DNS 2 names x 3s = 13s.
metrics_connect_timeout=2
metrics_timeout=3 # hard ceiling around ncat, in case -w does not cover every phase
api_connect_timeout=2
api_max_time=4
# nslookup's -timeout is per query and a single try (bind9's -retry counts total
# tries). Query only the A record, since without -type it also sends an AAAA query
# with its own timer, and keep a wall-clock ceiling per name as the real bound.
dns_query_timeout=2
dns_timeout=3

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
timeout ${metrics_timeout} ncat -zv -w ${metrics_connect_timeout} --ssl $metrics_endpoint_ip $metrics_worker_port >/root/health 2>&1
return_value=$?
if [ $return_value != 0 ]; then
	echo "connectivity check with ssl on metrics endpoint $(bracketify $metrics_endpoint_ip):$metrics_worker_port failed (rc=$return_value)"
	echo "$(< /root/health)"
	exit $return_value
fi
# -sS: no progress meter, but keep curl's own error so the event names the cause.
k8s_svc_host=$(bracketify $KUBERNETES_SERVICE_HOST)
curl -sSk --connect-timeout ${api_connect_timeout} --max-time ${api_max_time} https://$k8s_svc_host:$KUBERNETES_SERVICE_PORT/readyz >/root/health 2>&1
return_value=$?
if [ $return_value != 0 ]; then
	echo "connectivity check on k8s svc endpoint $k8s_svc_host:$KUBERNETES_SERVICE_PORT failed (rc=$return_value)"
	echo "$(< /root/health)"
	exit $return_value
fi
coredns_cluster_ip=${COREDNS_CLUSTER_IP:-10.223.0.3}
dns_names=${EXTERNAL_DNS_NAMES:-"google.com cloudflare.com"}
: > /root/health
for dns_name in $dns_names; do
	timeout ${dns_timeout} nslookup -type=A -timeout=${dns_query_timeout} -retry=1 $dns_name $coredns_cluster_ip >>/root/health 2>&1
	return_value=$?
	if [ $return_value == 0 ]; then
		break
	fi
	if [ $return_value == 124 ]; then
		# timeout(1) killed nslookup, whose buffered output is lost with it
		echo "nslookup $dns_name $coredns_cluster_ip killed after ${dns_timeout}s" >>/root/health
	fi
done
if [ $return_value != 0 ]; then
	echo "connectivity check on cluster endpoint $(bracketify $coredns_cluster_ip):53 failed resolving [$dns_names] (rc=$return_value)"
	echo "$(< /root/health)"
	exit $return_value
fi
exit 0
