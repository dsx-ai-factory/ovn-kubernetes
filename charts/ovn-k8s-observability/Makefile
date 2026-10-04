# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

build: obs-as-code

obs-as-code:
	mkdir -p bin
	go build -C obs-as-code -o ../bin/sdn-obs-as-code ./cmd/sdn-obs-as-code

build-dashboard: obs-as-code
	mkdir -p output/base/dashboard/json
	./bin/sdn-obs-as-code build dashboard --json-dir output/base/dashboard/json
	./bin/sdn-obs-as-code modify dashboard \
		--dashboard-dir output/base/dashboard/json \
		--out-dir charts/dashboard/json

VERSION ?=

helm-package:
	@if [ -z "$(VERSION)" ]; then \
		echo "VERSION is required (e.g. make helm-package VERSION=1.0.0)"; \
		exit 1; \
	fi
	helm package --version "$(VERSION)" .

EXTRA_ARGS ?= --debug

lint:
	mkdir -p output
	helm lint $(EXTRA_ARGS) .
	helm template $(EXTRA_ARGS) . > output/default-manifests.yaml

test-rules:
	bash tests/scripts/test-rules.sh

helm-docs:
	helm-docs -c . -g .

.PHONY: build obs-as-code build-dashboard helm-package lint test-rules helm-docs
