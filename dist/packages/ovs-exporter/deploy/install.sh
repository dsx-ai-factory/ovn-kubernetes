#!/usr/bin/bash

set -e

PACKAGE_DIRECTORY=$(dirname $(readlink -f $0))
INSTALLATION_DIRECTORY=/opt/asgard/ovs-exporter
LISTENING_INTERFACE=brbond0
DEFAULT_LISTENING_IP="0.0.0.0"

echo "[INFO] Starting ovs-exporter installation process"

echo "[INFO] Determining IP of interface to listen on"
LISTENING_IP=""
if [[ -e /sys/class/net/$LISTENING_INTERFACE ]]; then
    # Output of ip command will look like this:
    # 14: brbond0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UNKNOWN group default qlen 1000
    #     inet 10.0.1.1/16 brd 10.0.255.255 scope global brbond0
    #     valid_lft forever preferred_lft forever
    #
    # We'll extract the IP from that.
    LISTENING_IP=`ip -4 addr show brbond0 | grep inet | awk '{ print $2; }' | sed 's|/.*||g'`
    echo "[INFO] Found $LISTENING_INTERFACE"
else
    echo "[WARN] Couldn't find $LISTENING_INTERFACE"
fi

if [[ -z "$LISTENING_IP" ]]; then
    LISTENING_IP=$DEFAULT_LISTENING_IP
fi

echo "[INFO] Will listen on $LISTENING_IP"

echo "[INFO] Installing files"
install -p -m 0755 -D $PACKAGE_DIRECTORY/../bin/ovn-kube-util $INSTALLATION_DIRECTORY/ovn-kube-util
install -p -m 0644 -D $PACKAGE_DIRECTORY/../bin/git_info $INSTALLATION_DIRECTORY/git_info

echo "[INFO] Setting up service"
install -p -m 0644 $PACKAGE_DIRECTORY/../config/ovs-exporter.service /etc/systemd/system/ovs-exporter.service
sed -i 's/<LISTENING_IP>/'"$LISTENING_IP"'/' /etc/systemd/system/ovs-exporter.service
systemctl daemon-reload
systemctl enable ovs-exporter
systemctl start ovs-exporter

echo "[INFO] Successfully installed ovs-exporter"

exit 0
