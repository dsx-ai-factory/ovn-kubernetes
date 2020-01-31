#!/bin/sh

set -u
set -e

NODE=${1:?Please enter node name to delete}

echo "deleting $NODE"
SB_DB=`for IP in $(kubectl get ep -n ovn-kubernetes ovnkube-db -o jsonpath='{.subsets[0].addresses[*].ip}') ; do echo  -n "tcp:$IP:6642," ; done | rev | cut -c2- | rev`
export OVN_SB_DB=$SB_DB
CHASSIS=`ovn-sbctl --no-headings --data=bare --columns=name find chassis hostname=$NODE`
echo "Deleing chassis: $CHASSIS of $NODE"
ovn-sbctl --if-exist chassis-del $CHASSIS
