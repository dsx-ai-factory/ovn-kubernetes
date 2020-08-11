#!/usr/bin/bash

set -e

INSTALLATION_DIRECTORY=/opt/asgard/ovs-exporter

echo "[INFO] Starting ovs-exporter uninstallation process"

echo "[INFO] Removing service"
set +e
systemctl status ovs-exporter > /dev/null 2>&1
EXPORTER_RUNNING=$?
set -e
# 4 is the rc when the service can't be found.
# Skip the next bit if that's the case.
if [[ $EXPORTER_RUNNING -ne 4 ]]; then
    systemctl stop ovs-exporter
    systemctl disable ovs-exporter
fi
rm -f /etc/systemd/system/ovs-exporter.service
systemctl daemon-reload

echo "[INFO] Removing files"
rm -rf $INSTALLATION_DIRECTORY

echo "[INFO] Successfully uninstalled ovs-exporter"

exit 0
