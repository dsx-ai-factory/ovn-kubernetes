# SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# build image for ovn overlay network cni plugin

# ovnkube-db.yaml, ovnkube-node.yaml, and onvkube-master.yaml use this image.
# This image is built from files in this directory and pushed to
# a docker registry that is accesseble on each node.

# For a user created registry, the registry must be setup ahead of time.
# The registry is configured in /etc/containers/registries.conf
# on each node in both "registries:" and "insecure_registries:" sections.

all: ubuntu-image fedora-image

SLASH = -
ARCH ?= $(subst aarch64,arm64,$(subst x86_64,amd64,$(patsubst i%86,386,$(shell uname -m))))
IMAGE_ARCH = $(SLASH)$(ARCH)
DOCKERFILE_ARCH =
ifeq ($(ARCH),arm64)
        DOCKERFILE_ARCH=.arm64
endif
K8S_VER ?=

IMAGE ?= ovn-daemonset-fedora

OVN_REPO ?= https://github.com/ovn-org/ovn
OVN_GITREF ?=
ifeq ($(OVN_GITREF),)
OVN_FROM := koji
else
OVN_FROM := source
OVN_GITSHA := $(shell git ls-remote "${OVN_REPO}" "${OVN_GITREF}" | sort -k2  -V  |tail -1 | awk '{ print $$1 }')
endif
GO_VERSION ?= 1.25
GO_IMAGE = quay.io/projectquay/golang:${GO_VERSION}

OCI_BIN ?= docker

# The image of ovnkube/ovn-daemonset-ubuntu should be multi-arched before using it on arm64
ubuntu-image: bld
	${OCI_BIN} build -t ovn-kube-ubuntu$(IMAGE_ARCH) --build-arg ARTIFACTORY_CERT=${JFROG_CREDENTIAL} -f Dockerfile.ubuntu$(DOCKERFILE_ARCH) .
ifeq ($(ARCH),amd64)
	${OCI_BIN} tag "ovn-kube-ubuntu$(IMAGE_ARCH):latest" \
              "ovn-kube-ubuntu:latest"
endif

ubuntu-nv-image: NV_UBUNTU_BASE_IMAGE ?= urm.nvidia.com/sw-ipp-lws2-internal-docker/base-container/ubuntu:24.04-fips-stig_lws_pb

ubuntu-nv-image:
	cd $(abspath $(CURDIR)/../..) && ${OCI_BIN} build \
		-t ovn-kube-ubuntu$(IMAGE_ARCH) \
		--build-arg ARTIFACTORY_CERT=${JFROG_CREDENTIAL} \
		-f dist/images/Dockerfile.ubuntu.nv \
		--build-arg OVN_VER=${OVN_VER} \
		--build-arg OVS_VER=${OVS_VER} \
		--build-arg BASE_IMAGE=${NV_UBUNTU_BASE_IMAGE} .
	${OCI_BIN} tag "ovn-kube-ubuntu$(IMAGE_ARCH):latest" \
		"${OVNKUBE_NV_IMAGE}"

define docker_build_args
       $(if $(K8S_VER), --build-arg K8S_VER=-$(K8S_VER))
endef

define docker_build_ver_args
       --build-arg OVS_VER=-3.1.2.0f9272b6.18a441f6 --build-arg OVN_VER=-23.06.0.0fe26713.36b0b8ca
endef

centos: bld
	${OCI_BIN} build $(docker_build_args) \
		$(docker_build_ver_args) \
		--build-arg ARCH=$(ARCH) \
                --build-arg ARTIFACTORY_CERT=${JFROG_CREDENTIAL} \
		--target ovn -t quay.io/nvidia/ovn-c:latest .
	${OCI_BIN} build $(docker_build_args) \
		$(docker_build_ver_args) \
		--build-arg ARCH=$(ARCH) \
                --build-arg ARTIFACTORY_CERT=${JFROG_CREDENTIAL} \
		--target ovnkube -t quay.io/nvidia/ovnkube-c:latest .
	./daemonset.sh --image=quay.io/nvidia/ovnkube-c:latest \
		       --imagec=quay.io/nvidia/ovn-c:latest

FEDORA_BASE_IMAGE ?= quay.io/fedora/fedora:43

fedora-image:
	${OCI_BIN} pull --platform=linux/${ARCH} ${FEDORA_BASE_IMAGE} 2>/dev/null || true; \
	FEDORA_BASE_DIGEST=$$(${OCI_BIN} inspect --format='{{index .RepoDigests 0}}' ${FEDORA_BASE_IMAGE} 2>/dev/null || echo ""); \
	${OCI_BIN} build \
		--build-arg OVN_FROM=${OVN_FROM} \
		--build-arg OVN_REPO=${OVN_REPO} \
		--build-arg OVN_GITREF=${OVN_GITSHA} \
		--build-arg OVN_KUBERNETES_DIR=${OVN_KUBERNETES_DIR} \
		--build-arg BUILDER_IMAGE=${GO_IMAGE} \
		--build-arg FEDORA_BASE_DIGEST=$${FEDORA_BASE_DIGEST} \
		--platform=linux/${ARCH} \
		-t ${IMAGE} \
		-f Dockerfile.fedora ./../..

DOCKER_IMAGE_TAG = latest

# Multi-arch the ubuntu based image with fat-manifest
ubuntu-image-multi-arch:
	./push_manifest.sh ovn-daemonset-ubuntu $(DOCKER_IMAGE_TAG)

.PHONY: ../../go-controller/_output/go/bin/ovnkube

../../go-controller/_output/go/bin/ovnkube:
	cd ../../go-controller ; make

BRANCH = $(shell git rev-parse  --symbolic-full-name HEAD)
COMMIT = $(shell git rev-parse  HEAD)
bld: ../../go-controller/_output/go/bin/ovnkube
	find ../../go-controller/_output/go/bin/ -maxdepth 1 -type f -exec cp -f {} . \;
	rm -rf LICENSES
	cp -R ../../LICENSES .
	echo "ref: ${BRANCH}  commit: ${COMMIT}" > git_info
