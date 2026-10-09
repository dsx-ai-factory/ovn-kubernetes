#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

function fail {
    printf "ERROR: %s\n" "$@" >&2
    exit 1
}

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
chart_dir=$(dirname "$(dirname "${script_dir}")")

command -v git >/dev/null 2>&1 || fail "git is required but not installed"
command -v go >/dev/null 2>&1 || fail "go is required but not installed"
command -v make >/dev/null 2>&1 || fail "make is required but not installed"

echo "Checking generated dashboards..."
make -C "${chart_dir}" build-dashboard

generated_paths=(
    "${chart_dir}/files/dashboards"
    "${chart_dir}/charts/dashboard/json"
)

if ! git -C "${chart_dir}" diff --exit-code -- "${generated_paths[@]}"; then
    fail "Generated dashboards are out of date. Run 'make -C ${chart_dir} build-dashboard' and commit the changes."
fi
echo "Generated dashboards are up to date"

mkdir -p output
EXTRA_ARGS="--debug"

echo "Checking helm lint..."
helm lint ${EXTRA_ARGS} . || fail "Helm lint failed"

echo "Checking helm template..."
helm template ${EXTRA_ARGS} . > output/default-manifests.yaml || fail "Helm template failed"
