#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
# SPDX-License-Identifier: Apache-2.0
#
# Generate docs/api-reference/*-api-spec.md from the CRD Go types with
# crd-ref-docs.
#
# Usage:
#   hack/update-api-reference.sh            # regenerate in place
#   hack/update-api-reference.sh --verify   # fail if the committed pages differ
#
# Prefer the docs/ Makefile wrapper:
#   make -C docs api-reference
#   make -C docs verify-api-reference

set -euo pipefail

# crd-ref-docs renders a package only when it declares +groupName, and
# --source-path takes a single directory, so each API group is rendered by its
# own invocation. The shared types package is rendered as its own page and the
# groups link to it through render.knownTypes in crd-docs-config.yaml.
readonly PAGES=(
  "adminpolicybasedroute:admin-epbr-api-spec.md"
  "clusternetworkconnect:clusternetworkconnect-api-spec.md"
  "egressfirewall:egress-firewall-api-spec.md"
  "egressip:egress-ip-api-spec.md"
  "egressqos:egress-qos-api-spec.md"
  "egressservice:egress-service-api-spec.md"
  "routeadvertisements:routeadvertisements-api-spec.md"
  "types:networkselectors-api-spec.md"
  "uplink:uplink-api-spec.md"
  "userdefinednetwork:userdefinednetwork-api-spec.md"
  "vtep:vtep-api-spec.md"
)

readonly CRD_REF_DOCS_VERSION="v0.3.0"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

usage() {
  cat <<EOF
Usage: $(basename "$0") [--verify]

Options:
  --verify    Render into a scratch directory and fail if the committed pages
              differ, instead of writing them
  -h, --help  Show this help
EOF
}

VERIFY=false
case "${1:-}" in
  --verify) VERIFY=true ;;
  -h|--help) usage; exit 0 ;;
  "") ;;
  *) echo "ERROR: unknown argument: $1" >&2; usage >&2; exit 1 ;;
esac

die() {
  echo "ERROR: $*" >&2
  exit 1
}

command -v go >/dev/null 2>&1 || die "go is not installed"

# Install into a scratch GOBIN so the pinned version is used regardless of what
# the caller already has on PATH.
TOOL_DIR="$(mktemp -d)"
OUT_DIR="$(mktemp -d)"
trap 'rm -rf "${TOOL_DIR}" "${OUT_DIR}"' EXIT

echo "==> Installing crd-ref-docs ${CRD_REF_DOCS_VERSION}"
GOBIN="${TOOL_DIR}" GOFLAGS=-mod=mod go install \
  "github.com/elastic/crd-ref-docs@${CRD_REF_DOCS_VERSION}"

for page in "${PAGES[@]}"; do
  crd="${page%%:*}"
  name="${page##*:}"
  [[ -d "go-controller/pkg/crd/${crd}" ]] || die "no such CRD package: ${crd}"

  echo "==> Rendering ${name} from pkg/crd/${crd}"
  "${TOOL_DIR}/crd-ref-docs" \
    --source-path="go-controller/pkg/crd/${crd}" \
    --config=crd-docs-config.yaml \
    --templates-dir=hack/crd-ref-docs-templates \
    --renderer=markdown \
    --output-path="${OUT_DIR}/${name}" \
    --log-level=ERROR

  # An empty render means the package lost its +groupName marker; the page
  # would silently become a stub.
  [[ "$(wc -l < "${OUT_DIR}/${name}")" -gt 10 ]] || \
    die "pkg/crd/${crd} rendered empty: does its doc.go still declare +groupName?"
done

if [[ "${VERIFY}" == "true" ]]; then
  failed=()
  for page in "${PAGES[@]}"; do
    name="${page##*:}"
    diff -u "docs/api-reference/${name}" "${OUT_DIR}/${name}" || failed+=("${name}")
  done
  if [[ ${#failed[@]} -gt 0 ]]; then
    die "API reference pages are out of date: ${failed[*]}
Run 'make -C docs api-reference' and commit the result."
  fi
  echo "API reference pages are up to date."
  exit 0
fi

for page in "${PAGES[@]}"; do
  name="${page##*:}"
  cp "${OUT_DIR}/${name}" "docs/api-reference/${name}"
done

echo "API reference pages regenerated."
