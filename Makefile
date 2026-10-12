# SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Entry point for building this snapshot.
#
# The build itself lives in the component Makefiles — go-controller/ for the
# binaries, dist/images/ for the node image. The targets here forward to them
# so that someone landing at the repository root has somewhere to start.
# Each forwards a single common case; for anything else call the component
# Makefile directly, e.g. `make -C go-controller lint-fix`.

GO_CONTROLLER_DIR := go-controller
IMAGES_DIR := dist/images

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@echo "OVN-Kubernetes"
	@echo
	@awk 'BEGIN {FS = ":.*## "} /^[a-z][a-z-]*:.*## / { printf "  make %-9s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo
	@echo "This repository is a read-only snapshot and accepts no contributions."
	@echo "See README.md and CONTRIBUTING.md."

.PHONY: build
build: ## Build the ovnkube binaries into go-controller/_output
	$(MAKE) -C $(GO_CONTROLLER_DIR) build

.PHONY: test
test: ## Run the go-controller unit tests
	$(MAKE) -C $(GO_CONTROLLER_DIR) check

.PHONY: lint
lint: ## Format and lint the Go code
	$(MAKE) -C $(GO_CONTROLLER_DIR) lint

.PHONY: install
install: ## Install the built binaries under DESTDIR
	$(MAKE) -C $(GO_CONTROLLER_DIR) install

.PHONY: image
image: ## Build the Fedora node image used by contrib/kind.sh
	$(MAKE) -C $(IMAGES_DIR) fedora-image

.PHONY: clean
clean: ## Remove build output
	$(MAKE) -C $(GO_CONTROLLER_DIR) clean
