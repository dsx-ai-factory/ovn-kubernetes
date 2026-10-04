#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -u

readonly EXIT_SUCCESS=0
readonly EXIT_FAILURE=1

function fail {
    printf "ERROR: %s\n" "$@"
    exit $EXIT_FAILURE
}

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root_dir=$(dirname "$(dirname "${script_dir}")")
rules_dir="${repo_root_dir}/tests/rules"
ic_rules_file="${rules_dir}/rules-ic.yaml"
ic_values_file="${repo_root_dir}/tests/values.test-alert-rule.yaml"

status=$EXIT_SUCCESS

function cleanup {
    rm -f "${ic_rules_file}"
}

trap cleanup EXIT

command -v helm >/dev/null 2>&1 || fail "helm is required but not installed"
command -v promtool >/dev/null 2>&1 || fail "promtool is required but not installed"
command -v yq >/dev/null 2>&1 || fail "yq is required but not installed"

function extract_promtool_rules {
    local input_file="$1"
    local output_file="$2"
    local slurp_file
    slurp_file="$(mktemp)"

    if ! yq eval-all '[.]' "${input_file}" -N -o yaml > "${slurp_file}"; then
        rm -f "${slurp_file}"
        return 1
    fi

    if ! yq '{
      "groups": [
        .[] | select(.kind == "PrometheusRule") |
        .metadata.name as $n |
        .spec.groups[] |
        .name = ($n + "." + .name)
      ]
    }' -o yaml -N "${slurp_file}" > "${output_file}"; then
        rm -f "${slurp_file}"
        return 1
    fi
    rm -f "${slurp_file}"

    local group_count
    group_count="$(yq '.groups | length' "${output_file}")"
    [[ "${group_count}" != "0" && "${group_count}" != "null" ]]
}

function render_rule_fixtures {
    pushd "${repo_root_dir}" >/dev/null

    local rendered_ic_file
    rendered_ic_file="$(mktemp)"

    if ! helm template test-release . \
        -f "${ic_values_file}" \
        --debug=false > "${rendered_ic_file}" 2>/dev/null; then
        rm -f "${rendered_ic_file}"
        fail "Failed to render interconnect alert rules"
    fi

    if ! extract_promtool_rules "${rendered_ic_file}" "${ic_rules_file}"; then
        rm -f "${rendered_ic_file}"
        fail "Failed to extract PrometheusRule groups"
    fi

    rm -f "${rendered_ic_file}"
    popd >/dev/null
}

function run_test_file {
    local test_file="$1"
    echo "==> promtool test rules ${test_file}"
    promtool test rules "${test_file}" || status=$EXIT_FAILURE
}

function run_rule_tests {
    local files=()
    shopt -s nullglob
    files=( "${rules_dir}"/test*.yaml )
    shopt -u nullglob

    if (( ${#files[@]} == 0 )); then
        echo "No rule tests found in ${rules_dir}"
        return
    fi

    echo "Checking generated rule fixtures..."
    promtool check rules "${ic_rules_file}" || status=$EXIT_FAILURE

    for test_file in "${files[@]}"; do
        run_test_file "${test_file}"
    done
}

echo "Rendering rule fixtures..."
render_rule_fixtures

if [[ -n "${TESTSPEC:-}" ]]; then
    run_test_file "${TESTSPEC}"
    exit ${status}
fi

echo "Running rule tests..."
run_rule_tests

exit ${status}
