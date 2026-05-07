#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
# SPDX-License-Identifier: Apache-2.0


set -ex

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
cd "${SCRIPT_DIR}/.."

# setting this env prevents ginkgo e2e from trying to run provider setup
export KUBERNETES_CONFORMANCE_TEST=y
export KUBECONFIG=${KUBECONFIG:-${HOME}/ovn.conf}

# setting these is required to make RuntimeClass tests work ... :/
export KUBE_CONTAINER_RUNTIME=remote
export KUBE_CONTAINER_RUNTIME_ENDPOINT=unix:///run/containerd/containerd.sock
export KUBE_CONTAINER_RUNTIME_NAME=containerd

if [ -n "${E2E_CONFORMANCE_DOWNSTREAM_HOOK:-}" ]; then
  # shellcheck source=/dev/null
  source "${E2E_CONFORMANCE_DOWNSTREAM_HOOK}"
fi

pushd conformance
GO_MOD_DOWNLOAD_TIMEOUT=${GO_MOD_DOWNLOAD_TIMEOUT:-300}
end=$((SECONDS + GO_MOD_DOWNLOAD_TIMEOUT))
until go mod download; do
  if (( SECONDS >= end )); then
    echo "go mod download failed after multiple attempts"
    exit 1
  fi
  echo "Retrying go mod download in 10 seconds..."
  sleep 10
done

go test -timeout=0 -v \
        -kubeconfig ${KUBECONFIG}
popd
