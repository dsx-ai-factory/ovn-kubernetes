#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

crds=$(ls pkg/crd 2> /dev/null)
if [ -z "${crds}" ]; then
  exit
fi

SCRIPT_ROOT=$(dirname ${BASH_SOURCE})/..
olddir="${PWD}"
builddir="$(mktemp -d)"
cd "${builddir}"
GO111MODULE=on go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.4
BINS=(
    deepcopy-gen
    applyconfiguration-gen
    client-gen
    informer-gen
    lister-gen
)
GO111MODULE=on go install $(printf "k8s.io/code-generator/cmd/%s@v0.31.1 " "${BINS[@]}")
cd "${olddir}"
if [[ "${builddir}" == /tmp/* ]]; then #paranoia
    rm -rf "${builddir}"
fi

for crd in ${crds}; do
  vers=$(ls pkg/crd/$crd 2> /dev/null)
  echo "Generating deepcopy funcs for $crd:$vers"
  deepcopy-gen \
    --go-header-file hack/boilerplate.go.txt \
    --output-file zz_generated.deepcopy.go \
    --bounding-dirs github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd \
    github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/$crd/$vers \
    "$@"

  echo "Generating apply configuration for $crd"
  applyconfiguration-gen \
    --go-header-file hack/boilerplate.go.txt \
    --output-dir "${SCRIPT_ROOT}"/pkg/crd/$crd/$vers/apis/applyconfiguration \
    --output-pkg github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/$crd/$vers/apis/applyconfiguration \
    github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/$crd/$vers \
    "$@"

  echo "Generating clientset for $crd"
  client-gen \
    --go-header-file hack/boilerplate.go.txt \
    --clientset-name "${CLIENTSET_NAME_VERSIONED:-versioned}" \
    --input-base "" \
    --input github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/$crd/$vers \
    --output-dir "${SCRIPT_ROOT}"/pkg/crd/$crd/$vers/apis/clientset \
    --output-pkg github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/$crd/$vers/apis/clientset \
    --apply-configuration-package github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/$crd/$vers/apis/applyconfiguration \
    --plural-exceptions="EgressQoS:EgressQoSes,RouteAdvertisements:RouteAdvertisements" \
    "$@"

  echo "Generating listers for $crd"
  lister-gen \
    --go-header-file hack/boilerplate.go.txt \
    --output-dir "${SCRIPT_ROOT}"/pkg/crd/$crd/$vers/apis/listers \
    --output-pkg github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/$crd/$vers/apis/listers \
    --plural-exceptions="EgressQoS:EgressQoSes,RouteAdvertisements:RouteAdvertisements" \
    github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/$crd/$vers \
    "$@"

  echo "Generating informers for $crd"
  informer-gen \
    --go-header-file hack/boilerplate.go.txt \
    --versioned-clientset-package github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/$crd/$vers/apis/clientset/versioned \
    --listers-package  github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/$crd/$vers/apis/listers \
    --output-dir "${SCRIPT_ROOT}"/pkg/crd/$crd/$vers/apis/informers \
    --output-pkg github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/$crd/$vers/apis/informers \
    --plural-exceptions="EgressQoS:EgressQoSes,RouteAdvertisements:RouteAdvertisements" \
    github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/$crd/$vers \
    "$@"

done

echo "Generating CRDs"
mkdir -p _output/crds
controller-gen crd:crdVersions="$vers"  paths=./pkg/crd/... output:crd:dir=_output/crds
echo "Editing egressFirewall CRD"
## We desire that only egressFirewalls with the name "default" are accepted by the apiserver. The only
## way that we can put a pattern for validation on the name of the object which is embedded in
## metav1.ObjectMeta it is required that we add it after the generation of the CRD.
sed -i -e':begin;$!N;s/.*metadata:\n.*type: object/&\n            properties:\n              name:\n                type: string\n                pattern: ^default$/;P;D' \
	$(pwd)/_output/crds/k8s.ovn.org_egressfirewalls.yaml

echo "Editing EgressQoS CRD"
## We desire that only EgressQoS with the name "default" are accepted by the apiserver.
sed -i -e':begin;$!N;s/.*metadata:\n.*type: object/&\n            properties:\n              name:\n                type: string\n                pattern: ^default$/;P;D' \
	$(pwd)/_output/crds/k8s.ovn.org_egressqoses.yaml

echo "Copying the CRDs to dist/templates as j2 files... Add them to your commit..."
echo "Copying egressFirewall CRD"
cp $(pwd)/_output/crds/k8s.ovn.org_egressfirewalls.yaml ../dist/templates/k8s.ovn.org_egressfirewalls.yaml.j2
echo "Copying egressIP CRD"
cp _$(pwd)/output/crds/k8s.ovn.org_egressips.yaml ../dist/templates/k8s.ovn.org_egressips.yaml.j2
echo "Copying egressQoS CRD"
cp _output/crds/k8s.ovn.org_egressqoses.yaml ../dist/templates/k8s.ovn.org_egressqoses.yaml.j2
cp $(pwd)/_output/crds/k8s.ovn.org_egressqoses.yaml ../dist/templates/k8s.ovn.org_egressqoses.yaml.j2
echo "Copying networkProbe CRD"
cp $(pwd)/_output/crds/k8s.ovn.org_networkprobes.yaml ../dist/templates/k8s.ovn.org_networkprobes.yaml.j2
# NOTE: When you update vendoring versions for the ANP & BANP APIs, we must update the version of the CRD we pull from in the below URL
echo "Copying Admin Network Policy CRD"
curl -sSL https://raw.githubusercontent.com/kubernetes-sigs/network-policy-api/v0.1.2/config/crd/experimental/policy.networking.k8s.io_adminnetworkpolicies.yaml -o ../dist/templates/policy.networking.k8s.io_adminnetworkpolicies.yaml
echo "Copying Baseline Admin Network Policy CRD"
curl -sSL https://raw.githubusercontent.com/kubernetes-sigs/network-policy-api/v0.1.2/config/crd/experimental/policy.networking.k8s.io_baselineadminnetworkpolicies.yaml -o ../dist/templates/policy.networking.k8s.io_baselineadminnetworkpolicies.yaml
echo "Copying adminpolicybasedexternalroutes CRD"
cp _output/crds/k8s.ovn.org_adminpolicybasedexternalroutes.yaml ../dist/templates/k8s.ovn.org_adminpolicybasedexternalroutes.yaml.j2
echo "Copying egressService CRD"
cp _output/crds/k8s.ovn.org_egressservices.yaml ../dist/templates/k8s.ovn.org_egressservices.yaml.j2
echo "Copying userdefinednetworks CRD"
cp _output/crds/k8s.ovn.org_userdefinednetworks.yaml ../dist/templates/k8s.ovn.org_userdefinednetworks.yaml.j2
echo "Copying clusteruserdefinednetworks CRD"
cp _output/crds/k8s.ovn.org_clusteruserdefinednetworks.yaml ../dist/templates/k8s.ovn.org_clusteruserdefinednetworks.yaml.j2
echo "Copying routeAdvertisements CRD"
cp _output/crds/k8s.ovn.org_routeadvertisements.yaml ../dist/templates/k8s.ovn.org_routeadvertisements.yaml.j2
