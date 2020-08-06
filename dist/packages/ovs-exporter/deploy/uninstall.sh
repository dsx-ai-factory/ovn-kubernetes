#!/usr/bin/bash

set -e

INSTALLATION_DIRECTORY=/opt/asgard/ovs-exporter

echo "[INFO] Starting ovs-exporter uninstallation process"

echo "[INFO] Removing service"
systemctl stop ovs-exporter
systemctl disable ovs-exporter
rm -f /etc/systemd/system/ovs-exporter.service
systemctl daemon-reload

echo "[INFO] Removing files"
rm -rf $INSTALLATION_DIRECTORY

echo "[INFO] Successfully uninstalled ovs-exporter"

exit 0
