#!/usr/bin/env bash

# pin golangci-lint version to 1.56.2
VERSION=v1.56.2
if [ "$#" -ne 1 ]; then
    echo "Expected command line argument - container runtime (docker/podman) got $# arguments: $@"
    exit 1
fi

$1 run --security-opt label=disable --rm \
  -v  ${HOME}/.cache/golangci-lint:/cache -e GOLANGCI_LINT_CACHE=/cache \
  -v $(pwd):/app -w /app -e GO111MODULE=on docker.io/golangci/golangci-lint:${VERSION} \
	golangci-lint run --max-same-issues 0 --verbose --print-resources-usage \
	--modules-download-mode=vendor --timeout=15m0s && \
	echo "lint OK!"
