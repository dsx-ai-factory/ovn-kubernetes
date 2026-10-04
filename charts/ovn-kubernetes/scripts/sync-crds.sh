#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Usage: sync-crds.sh [source]
# Sources: multi-net-policy, net-attach-def, policy, all (default)

usage() {
    echo "Usage: $0 [source]"
    echo "Sources:"
    echo "  multi-net-policy - Multi-NetworkPolicy CRD (not included in 'all')"
    echo "  net-attach-def  - Network Attachment Definition CRD"
    echo "  policy          - Admin Network Policy CRDs"
    echo "  all             - All external CRDs except multi-net-policy (default)"
    exit 1
}

SYNC_SOURCE="${1:-all}"
case "$SYNC_SOURCE" in
    multi-net-policy|net-attach-def|policy|all) ;;
    -h|--help) usage ;;
    *) echo "Error: Unknown source '$SYNC_SOURCE'"; usage ;;
esac

# Determine which sources to sync
sync_multi_net_policy=false
sync_net_attach_def=false
sync_policy=false

case "$SYNC_SOURCE" in
    all)
        # TODO: include multi-net-policy in 'all' once downstream differences are resolved
        # multi-net-policy excluded from 'all' due to downstream differences;
        # use 'multi-net-policy' explicitly to sync it
        sync_multi_net_policy=false
        sync_net_attach_def=true
        sync_policy=true
        ;;
    multi-net-policy) sync_multi_net_policy=true ;;
    net-attach-def) sync_net_attach_def=true ;;
    policy) sync_policy=true ;;
esac

# Configuration - OVN-Kubernetes
# OVN_K_PATH selects the ovn-kubernetes tree. It defaults to this repository.
WORKDIR="/tmp/ovn-k-sync"

# Configuration - External CRD Sources (versions extracted dynamically from ovn-k's go.mod)
# Multi-NetworkPolicy CRD
MULTI_NET_POLICY_REPO="https://github.com/k8snetworkplumbingwg/multi-networkpolicy.git"

# Network Attachment Definition CRD
NET_ATTACH_DEF_REPO="https://github.com/k8snetworkplumbingwg/network-attachment-definition-client.git"

# Admin Network Policy CRDs
NETWORK_POLICY_API_REPO="https://github.com/kubernetes-sigs/network-policy-api.git"

# Get the absolute path to the helm chart's crds directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HELM_CRDS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)/charts/ovnkube-crds/crds"

set -e  # Exit on error

# External CRD versions come from the selected repository's go.mod.
# The repository root is three levels above this script.
if [[ -z "${OVN_K_PATH:-}" ]]; then
    OVN_K_PATH="$(cd "$SCRIPT_DIR/../../.." && pwd)"
fi
if [[ ! -d "$OVN_K_PATH" ]]; then
    echo "Error: OVN_K_PATH does not exist: $OVN_K_PATH"
    exit 1
fi
OVN_K_PATH="$(cd "$OVN_K_PATH" && pwd)"
OVN_K_CHECKOUT_DIR="$OVN_K_PATH"

# Cleanup function
cleanup() {
    if [ -d "$WORKDIR" ]; then
        echo ""
        echo "Cleaning up temporary directory..."
        rm -rf "$WORKDIR"
    fi
}

# Register cleanup on exit
trap cleanup EXIT

# Function to extract a dependency version from ovn-k go.mod (assumes OVN_K_CHECKOUT_DIR is set)
# Usage: get_go_mod_version <module_path>
# Example: get_go_mod_version "sigs.k8s.io/network-policy-api"
get_go_mod_version() {
    local module_path="$1"
    local go_mod="$OVN_K_CHECKOUT_DIR/go-controller/go.mod"

    if [ ! -f "$go_mod" ]; then
        echo "Error: go.mod not found at $go_mod" >&2
        return 1
    fi

    # Extract version from go.mod
    # go.mod format: github.com/example/module v1.2.3
    local version=$(grep "$module_path" "$go_mod" | awk '{print $2}')
    if [ -z "$version" ]; then
        echo "Error: Could not find $module_path in go.mod" >&2
        return 1
    fi

    echo "$version"
}

# Helper function to sync a CRD from a git repository
sync_crd_from_repo() {
    local repo_url="$1"
    local branch="$2"
    local source_path="$3"
    local dest_filename="$4"
    local repo_name=$(basename "$repo_url" .git)
    local checkout_dir="$WORKDIR/$repo_name"

    echo "  Syncing $dest_filename from $repo_name..."

    if [ ! -d "$checkout_dir" ]; then
        git clone --depth 1 --branch "$branch" "$repo_url" "$checkout_dir" 2>/dev/null
    fi

    if [ -f "$checkout_dir/$source_path" ]; then
        cp "$checkout_dir/$source_path" "$HELM_CRDS_DIR/$dest_filename"
        return 0
    else
        echo "    Error: Source file not found at $source_path"
        return 1
    fi
}

# Function to sync network-policy-api CRDs (ANP and BANP)
sync_network_policy_api_crds() {
    local repo_url="$1"
    local tag="$2"
    local repo_name=$(basename "$repo_url" .git)
    local checkout_dir="$WORKDIR/$repo_name"

    if [ ! -d "$checkout_dir" ]; then
        git clone --depth 1 --branch "$tag" "$repo_url" "$checkout_dir" 2>/dev/null
    fi

    # Copy CRD files from experimental (includes extended features: nodes, networks, namedPort)
    local crd_dir="$checkout_dir/config/crd/experimental"
    local crd_files="policy.networking.k8s.io_adminnetworkpolicies.yaml policy.networking.k8s.io_baselineadminnetworkpolicies.yaml"

    for crd_file in $crd_files; do
        if [ -f "$crd_dir/$crd_file" ]; then
            echo "  Syncing $crd_file from $repo_name..."
            cp "$crd_dir/$crd_file" "$HELM_CRDS_DIR/$crd_file"
        else
            echo "  Error: $crd_file not found"
        fi
    done
}

echo "=========================================="
echo "CRD Sync Script"
echo "=========================================="
echo "Sync source: $SYNC_SOURCE"
echo "OVN-K Path: $OVN_K_PATH"
echo "Helm CRDs directory: $HELM_CRDS_DIR"
echo ""

if [ "$SYNC_SOURCE" = "all" ]; then
    echo "WARNING: multi-net-policy CRD is excluded from 'all' due to downstream differences."
    echo "         Use './scripts/sync-crds.sh multi-net-policy' to sync it explicitly."
    echo ""
fi

# Clean up any existing work directory and create fresh
rm -rf "$WORKDIR"
mkdir -p "$WORKDIR"

external_count=0

# Multi-NetworkPolicy CRD
if $sync_multi_net_policy; then
    echo "Syncing Multi-NetworkPolicy CRD..."
    MULTI_NET_POLICY_TAG=$(get_go_mod_version "github.com/k8snetworkplumbingwg/multi-networkpolicy")
    echo "  Using multi-networkpolicy version from ovn-k go.mod: $MULTI_NET_POLICY_TAG"
    if sync_crd_from_repo "$MULTI_NET_POLICY_REPO" "$MULTI_NET_POLICY_TAG" \
        "scheme.yml" "k8s.cni.cncf.io_multinetworkpolicy.yaml"; then
        external_count=$((external_count + 1))
    fi
fi

# Network Attachment Definition CRD
if $sync_net_attach_def; then
    echo "Syncing Network Attachment Definition CRD..."
    NET_ATTACH_DEF_TAG=$(get_go_mod_version "github.com/k8snetworkplumbingwg/network-attachment-definition-client")
    echo "  Using network-attachment-definition-client version from ovn-k go.mod: $NET_ATTACH_DEF_TAG"
    if sync_crd_from_repo "$NET_ATTACH_DEF_REPO" "$NET_ATTACH_DEF_TAG" \
        "artifacts/networks-crd.yaml" "k8s.cni.cncf.io_netattachdef.yaml"; then
        external_count=$((external_count + 1))
    fi
fi

# Admin Network Policy CRDs
if $sync_policy; then
    echo "Syncing Admin Network Policy CRDs..."
    NETWORK_POLICY_API_TAG=$(get_go_mod_version "sigs.k8s.io/network-policy-api")
    echo "  Using network-policy-api version from ovn-k go.mod: $NETWORK_POLICY_API_TAG"

    sync_network_policy_api_crds "$NETWORK_POLICY_API_REPO" "$NETWORK_POLICY_API_TAG"
    [ -f "$HELM_CRDS_DIR/policy.networking.k8s.io_adminnetworkpolicies.yaml" ] && external_count=$((external_count + 1))
    [ -f "$HELM_CRDS_DIR/policy.networking.k8s.io_baselineadminnetworkpolicies.yaml" ] && external_count=$((external_count + 1))
fi

echo ""
echo "=========================================="
echo "Sync complete! Synced $external_count external CRDs."
echo "=========================================="
echo ""
echo "Next steps:"
echo "1. Review the changes: cd $HELM_CRDS_DIR && git diff"
echo "2. Test the updated CRDs in your helm chart"
echo "3. Commit the changes if everything looks good"
