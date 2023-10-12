#!/bin/bash

file="/etc/cni/net.d/10-ovn-kubernetes.conf"
if [[ ! -f ${file} ]] ; then
    echo "ERROR: ${file} does not exist"
    exit 1
fi

curl --unix-socket /var/run/ovn-kubernetes/cni/ovn-cni-server.sock -X POST http://dummy/ > /dev/null 2>err.txt
if [ $? -ne 0 ]; then
    echo "ERROR: ovn-cni-server.sock is not working"
    echo "$(< /root/err.txt)"
    exit 1
fi

exit 0
