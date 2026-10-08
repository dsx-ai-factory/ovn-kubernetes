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

make -C "${SCRIPT_DIR}/../dist/images" ubuntu-nv-image \
  OCI_BIN="${KIND_EXPERIMENTAL_PROVIDER:-docker}" \
  OVNKUBE_NV_IMAGE="${IMAGE}" \
  OVS_SOURCE="${OVS_SOURCE:-doca}" \
  OVN_SOURCE="${OVN_SOURCE:-uca}" \
  NV_UBUNTU_BASE_IMAGE="${NV_UBUNTU_BASE_IMAGE:-ubuntu:24.04}"

# kind.sh relies on unset optional parameters. Source it so the Helm
# arguments array remains available to the shared script.
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
