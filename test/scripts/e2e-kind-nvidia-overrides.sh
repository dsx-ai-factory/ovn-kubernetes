#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
# SPDX-License-Identifier: Apache-2.0

# NVIDIA downstream CI policy for Kubernetes e2e shard runs. This file is
# sourced by e2e-kind.sh after upstream skip filters are built and before tests
# run.

install_kubernetes_e2e_binaries() {
  local k8s_version arch tmp_dir

  k8s_version=$(kubectl version -o json 2>/dev/null | jq -r '.serverVersion.gitVersion')
  if [ -z "${k8s_version}" ] || [ "${k8s_version}" = "null" ]; then
    echo "Error: Could not detect cluster version"
    exit 1
  fi
  echo "Using Kubernetes test binary version: ${k8s_version}"

  case "$(uname -m)" in
    x86_64)  arch="amd64" ;;
    aarch64) arch="arm64" ;;
    *)
      echo "Error: unsupported architecture $(uname -m)"
      exit 1
      ;;
  esac

  tmp_dir="$(mktemp -d)"
  pushd "${tmp_dir}"

  curl --fail -sSL --retry 5 --retry-delay 10 \
    -o "kubernetes-test-linux-${arch}.tar.gz" \
    "https://dl.k8s.io/release/${k8s_version}/kubernetes-test-linux-${arch}.tar.gz"
  tar xvzf "kubernetes-test-linux-${arch}.tar.gz"
  sudo mv -f kubernetes/test/bin/e2e.test /usr/local/bin/e2e.test
  sudo mv -f kubernetes/test/bin/ginkgo /usr/local/bin/ginkgo

  popd
  rm -rf "${tmp_dir}"
}

e2e_kind_preflight() {
  install_kubernetes_e2e_binaries
}

# Match the legacy downstream shard runner behavior: list selected tests before
# running the parallel suite, and use the longer downstream timeout.
E2E_KIND_DRY_RUN="${E2E_KIND_DRY_RUN:-true}"
TEST_TIMEOUT="${TEST_TIMEOUT:-3h}"

# Extra downstream skips carried from the old NVIDIA shard runner.
skip "should set TCP CLOSE_WAIT timeout"
skip "should be rejected for evicted pods"

if [ "${OVN_GATEWAY_MODE:-}" = "local" ]; then
  skip "should fallback to local terminating endpoints when there are no ready endpoints with externalTrafficPolicy=Local"
fi
