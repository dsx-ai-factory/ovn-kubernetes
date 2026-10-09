#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="${OVNKUBE_NV_IMAGE:-localhost/ovn-daemonset-ubuntu-nv:dev}"

for arg in "$@"; do
  case "${arg}" in
    -h|--help|--delete)
      exec "${SCRIPT_DIR}/kind.sh" "$@"
      ;;
  esac
done

configure_kubelet_dns() {
  local node

  # musl discards long search lines. Give kubelet a separate resolver file
  # without host search suffixes; it still adds the Kubernetes search domains.
  for node in $(kind_get_nodes); do
    "${OCI_BIN}" exec "${node}" bash -euc '
      awk "/^(nameserver|options)[[:space:]]/" /etc/resolv.conf > /etc/kubelet-resolv.conf

      sed -i "/^resolvConf:/d" /var/lib/kubelet/config.yaml
      printf "resolvConf: /etc/kubelet-resolv.conf\n" >> /var/lib/kubelet/config.yaml
      systemctl restart kubelet
    '
  done
}

make -C "${SCRIPT_DIR}/../dist/images" ubuntu-nv-image \
  OCI_BIN="${KIND_EXPERIMENTAL_PROVIDER:-docker}" \
  OVNKUBE_NV_IMAGE="${IMAGE}" \
  OVS_SOURCE="${OVS_SOURCE:-doca}" \
  OVN_SOURCE="${OVN_SOURCE:-uca}" \
  NV_UBUNTU_BASE_IMAGE="${NV_UBUNTU_BASE_IMAGE:-ubuntu:24.04@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55}"

# kind.sh relies on unset optional parameters. Source it so the Helm arguments
# and parsed cluster settings remain available for deployment and the DNS guard.
set +u

OVN_HELM_EXTRA_ARGS=(
  --set tags.ovs-node=true
  --set-string global.imagePullSecretName=
  --set global.enableEgressIp=true
  --set global.enableEgressQos=true
  --set global.enableEgressService=true
)

# The chart always enables multi-network. Force its setup so the persistent IP
# support enabled by kind.sh has the required IPAMClaim CRD.
source "${SCRIPT_DIR}/kind.sh" "$@" --ovn-image "${IMAGE}" --multi-network-enable

configure_kubelet_dns
