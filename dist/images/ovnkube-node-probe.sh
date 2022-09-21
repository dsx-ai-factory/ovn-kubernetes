#!/bin/bash

# source the functions in ovndb-raft-functions.sh
BASEDIR=$(dirname $0)
. ${BASEDIR}/ovndb-raft-functions.sh

metrics_endpoint_ip=${OVN_METRICS_ENDPOINT_IP}
if [[ -z ${metrics_endpoint_ip} ]]; then
  metrics_endpoint_ip=${K8S_NODE_IP:-0.0.0.0}
fi
metrics_endpoint_ip=$(bracketify $metrics_endpoint_ip)
metrics_worker_port=${OVN_METRICS_WORKER_PORT}
if [[ -z ${metrics_worker_port} ]]; then
	metrics_worker_port=9410
fi
ncat -zv --ssl $metrics_endpoint_ip $metrics_worker_port >/root/health 2>&1
return_value=$?
if [ $return_value != 0 ]; then
	echo "$(< /root/health)"
	exit $return_value
fi
ncat -zv $KUBERNETES_SERVICE_HOST $KUBERNETES_SERVICE_PORT >/root/health 2>&1
return_value=$?
if [ $return_value != 0 ]; then
	echo "$(< /root/health)"
	exit $return_value
fi
exit 0
