#!/usr/bin/bash

set -e

PACKAGE_DIRECTORY=$(dirname $(readlink -f $0))
INSTALLATION_DIRECTORY=/opt/asgard/ovs-exporter

echo "[INFO] Starting ovs-exporter installation process"

echo "[INFO] Installing files"
install -p -m 0755 -D $PACKAGE_DIRECTORY/../bin/ovn-kube-util $INSTALLATION_DIRECTORY/ovn-kube-util
install -p -m 0644 -D $PACKAGE_DIRECTORY/../bin/git_info $INSTALLATION_DIRECTORY/git_info

echo "[INFO] Setting up service"
install -p -m 0644 $PACKAGE_DIRECTORY/../config/ovs-exporter.service /etc/systemd/system/ovs-exporter.service
systemctl daemon-reload
systemctl enable ovs-exporter
systemctl start ovs-exporter

echo "[INFO] Successfully installed ovs-exporter"

exit 0
