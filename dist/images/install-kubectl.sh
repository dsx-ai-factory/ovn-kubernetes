#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

export DEBIAN_FRONTEND=noninteractive

kubectl_apt_signing_fingerprint=DE15B14486CD377B9E876E1A234654DA9A296436
kubectl_key=/tmp/kubernetes-apt-keyring.asc

mkdir -p -m 755 /etc/apt/keyrings
curl -fsSL "https://pkgs.k8s.io/core:/stable:/${K8S_VER}/deb/Release.key" -o "${kubectl_key}"
test "$(gpg --show-keys --with-colons "${kubectl_key}" | awk -F: '/^fpr:/ {print $10; exit}')" = "${kubectl_apt_signing_fingerprint}"
gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg "${kubectl_key}"
rm -f "${kubectl_key}"
chmod 644 /etc/apt/keyrings/kubernetes-apt-keyring.gpg

printf 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/%s/deb/ /\n' \
    "${K8S_VER}" > /etc/apt/sources.list.d/kubernetes.list
chmod 644 /etc/apt/sources.list.d/kubernetes.list

apt-get update
apt-get install -y --no-install-recommends kubectl

rm -f /etc/apt/sources.list.d/kubernetes.list /etc/apt/keyrings/kubernetes-apt-keyring.gpg
