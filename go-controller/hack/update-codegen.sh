#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
# SPDX-License-Identifier: Apache-2.0


set -o errexit
set -o nounset
set -o pipefail

# Add GOBIN to PATH so installed tools can be found
GOPATH=${GOPATH:-$(go env GOPATH)}
export PATH="${GOPATH}/bin:${PATH}"

crds=$(find pkg/crd -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort)
if [ -z "${crds}" ]; then
  exit
fi

SCRIPT_ROOT=$(dirname ${BASH_SOURCE})/..
olddir="${PWD}"
builddir="$(mktemp -d)"
cd "${builddir}"
GO111MODULE=on go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0
kube_openapi_version=$(go -C "${olddir}" list -m -f '{{.Version}}' k8s.io/kube-openapi)
GO111MODULE=on go install k8s.io/kube-openapi/cmd/openapi-gen@"${kube_openapi_version}"
BINS=(
    deepcopy-gen
    applyconfiguration-gen
    client-gen
    informer-gen
    lister-gen
)
GO111MODULE=on go install $(printf "k8s.io/code-generator/cmd/%s@v0.36.2 " "${BINS[@]}")
cd "${olddir}"
if [[ "${builddir}" == /tmp/* ]]; then #paranoia
    rm -rf "${builddir}"
fi

# Helper function to get API version for a given CRD
get_crd_version() {
  case "$1" in
    uplink)
      echo "v1alpha1"
      ;;
    networkqos)
      echo "v1alpha1"
      ;;
    adminpbr|ipreservation)
      echo "v1beta1"
      ;;
    *)
      echo "v1"
      ;;
  esac
}

# deepcopy for types
deepcopy-gen \
  --go-header-file hack/boilerplate.go.txt \
  --output-file zz_generated.deepcopy.go \
  github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/types

mkdir -p _output/crds
rm -f _output/crds/k8s.ovn.org_*.yaml

for crd in ${crds}; do

  # for types we already generated deepcopy above which is all we need
  [ "$crd" = "types" ] && continue
  
  api_version=$(get_crd_version "${crd}")

  # Clean up previously generated files to avoid stale copies
  echo "Cleaning up existing generated files for $crd ($api_version)"
  rm -rf "${SCRIPT_ROOT}"/pkg/crd/$crd/${api_version}/apis

  echo "Generating deepcopy funcs for $crd ($api_version)"
  deepcopy-gen \
    --go-header-file hack/boilerplate.go.txt \
    --output-file zz_generated.deepcopy.go \
    github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/$crd/${api_version} \
    "$@"

  echo "Generating openapi for $crd ($api_version)"
  # this can be moved to apis/ folder when the cmd becomes auto-generated
  api_violations_report="$(mktemp -t "$(basename "$0").api_violations.XXXXXX")"
  openapi-gen \
    --output-file zz_generated.openapi.go \
    --output-dir "${SCRIPT_ROOT}"/pkg/crd/$crd/${api_version}/openapi \
    --output-pkg  github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/$crd/${api_version}/openapi \
    --go-header-file hack/boilerplate.go.txt \
    --output-model-name-file="zz_generated.model_name.go" \
    --report-filename "${api_violations_report}" \
    --readonly-pkg k8s.io/apimachinery/pkg/apis/meta/v1 \
    --readonly-pkg k8s.io/apimachinery/pkg/runtime \
    --readonly-pkg k8s.io/apimachinery/pkg/version \
    --readonly-pkg k8s.io/apimachinery/pkg/api/resource \
    --readonly-pkg k8s.io/api/networking/v1 \
    k8s.io/apimachinery/pkg/apis/meta/v1 \
    k8s.io/apimachinery/pkg/runtime \
    k8s.io/apimachinery/pkg/version \
    k8s.io/apimachinery/pkg/api/resource \
    k8s.io/api/networking/v1 \
    github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/types \
    github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/$crd/${api_version} \
    "$@"

  # Show the API-linter report, but fail if it contains any violation other than
  # the expected apimachinery (and sometimes our own) names_match ones.
  if grep -vE '^API rule violation: names_match,' "${api_violations_report}" | grep -q '[^[:space:]]'; then
    echo "ERROR: openapi-gen reported unexpected API rule violations for ${crd} (only names_match is allowed):" >&2
    grep -vE '^API rule violation: names_match,' "${api_violations_report}" >&2
    exit 1
  fi

  echo "Generating apply configuration for $crd ($api_version)"
  applyconfiguration-gen \
    --openapi-schema <(go run github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/$crd/${api_version}/openapi/cmd/models-schema) \
    --go-header-file hack/boilerplate.go.txt \
    --output-dir "${SCRIPT_ROOT}"/pkg/crd/$crd/${api_version}/apis/applyconfiguration \
    --output-pkg github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/$crd/${api_version}/apis/applyconfiguration \
    github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/$crd/${api_version} \
    "$@"

  echo "Generating clientset for $crd ($api_version)"
  client-gen \
    --go-header-file hack/boilerplate.go.txt \
    --clientset-name "${CLIENTSET_NAME_VERSIONED:-versioned}" \
    --input-base "" \
    --input github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/$crd/${api_version} \
    --output-dir "${SCRIPT_ROOT}"/pkg/crd/$crd/${api_version}/apis/clientset \
    --output-pkg github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/$crd/${api_version}/apis/clientset \
    --apply-configuration-package github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/$crd/${api_version}/apis/applyconfiguration \
    --plural-exceptions="EgressQoS:EgressQoSes,RouteAdvertisements:RouteAdvertisements,NetworkQoS:NetworkQoSes" \
    "$@"

  echo "Generating listers for $crd ($api_version)"
  lister-gen \
    --go-header-file hack/boilerplate.go.txt \
    --output-dir "${SCRIPT_ROOT}"/pkg/crd/$crd/${api_version}/apis/listers \
    --output-pkg github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/$crd/${api_version}/apis/listers \
    --plural-exceptions="EgressQoS:EgressQoSes,RouteAdvertisements:RouteAdvertisements,NetworkQoS:NetworkQoSes" \
    github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/$crd/${api_version} \
    "$@"

  echo "Generating informers for $crd ($api_version)"
  informer-gen \
    --go-header-file hack/boilerplate.go.txt \
    --versioned-clientset-package github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/$crd/${api_version}/apis/clientset/versioned \
    --listers-package  github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/$crd/${api_version}/apis/listers \
    --output-dir "${SCRIPT_ROOT}"/pkg/crd/$crd/${api_version}/apis/informers \
    --output-pkg github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/$crd/${api_version}/apis/informers \
    --plural-exceptions="EgressQoS:EgressQoSes,RouteAdvertisements:RouteAdvertisements,NetworkQoS:NetworkQoSes" \
    github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/$crd/${api_version} \
    "$@"

  echo "Generating CRDs for $crd ($api_version)"
  controller-gen crd:crdVersions="v1"  paths=./pkg/crd/$crd/${api_version} output:crd:dir=_output/crds
done

echo "Editing egressFirewall CRD"
## We desire that only egressFirewalls with the name "default" are accepted by the apiserver. The only
## way that we can put a pattern for validation on the name of the object which is embedded in
## metav1.ObjectMeta it is required that we add it after the generation of the CRD.
sed -i -e':begin;$!N;s/.*metadata:\n.*type: object/&\n            properties:\n              name:\n                type: string\n                pattern: ^default$/;P;D' \
	_output/crds/k8s.ovn.org_egressfirewalls.yaml

echo "Editing EgressQoS CRD"
## We desire that only EgressQoS with the name "default" are accepted by the apiserver.
sed -i -e':begin;$!N;s/.*metadata:\n.*type: object/&\n            properties:\n              name:\n                type: string\n                pattern: ^default$/;P;D' \
	_output/crds/k8s.ovn.org_egressqoses.yaml

CRDS_DIR=../charts/ovn-kubernetes/charts/ovnkube-crds/crds
echo "Copying the CRDs to ${CRDS_DIR}... Add them to your commit..."
rm -f "${CRDS_DIR}"/k8s.ovn.org_*.yaml
cp _output/crds/k8s.ovn.org_*.yaml "${CRDS_DIR}/"
