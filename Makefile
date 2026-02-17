# Foundry Flow — Root Makefile
#
# Orchestrates builds and tests across the Go workspace.
# The operator has its own Makefile for Kubebuilder-specific targets.

SHELL := /usr/bin/env bash -o pipefail
.SHELLFLAGS := -ec

# ---------------------------------------------------------------------------
##@ General
# ---------------------------------------------------------------------------

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

# ---------------------------------------------------------------------------
##@ Testing
# ---------------------------------------------------------------------------

.PHONY: test
test: test-sdk test-sidecar test-archivist ## Run all unit tests.

.PHONY: test-sdk
test-sdk: ## Run SDK unit tests.
	go test -v ./sdk/go/...

.PHONY: test-sidecar
test-sidecar: ## Run Sidecar unit tests.
	go test -v ./sidecar/...

.PHONY: test-archivist
test-archivist: ## Run Archivist unit tests.
	go test -v ./archivist/...

.PHONY: test-operator
test-operator: ## Run Operator unit tests (delegates to operator/Makefile).
	$(MAKE) -C operator test

.PHONY: test-all
test-all: test test-operator ## Run every test suite including the operator.

# ---------------------------------------------------------------------------
##@ Building
# ---------------------------------------------------------------------------

.PHONY: build
build: build-sidecar build-null-node build-archivist ## Build all binaries.

.PHONY: build-sidecar
build-sidecar: ## Build the Sidecar binary.
	go build -o bin/sidecar ./sidecar/cmd

.PHONY: build-null-node
build-null-node: ## Build the Null Node binary.
	go build -o bin/null-node ./nodes/null-node/cmd

.PHONY: build-archivist
build-archivist: ## Build the Archivist binary.
	go build -o bin/archivist ./archivist/cmd

.PHONY: build-operator
build-operator: ## Build the Operator binary (delegates to operator/Makefile).
	$(MAKE) -C operator build

# ---------------------------------------------------------------------------
##@ Code Quality
# ---------------------------------------------------------------------------

.PHONY: fmt
fmt: ## Run go fmt across the workspace.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet across the workspace.
	go vet ./...

# ---------------------------------------------------------------------------
##@ Code Generation
# ---------------------------------------------------------------------------

.PHONY: proto
proto: ## Regenerate Go code from proto definitions using buf.
	buf generate

# ---------------------------------------------------------------------------
##@ Housekeeping
# ---------------------------------------------------------------------------

.PHONY: clean
clean: ## Remove build artefacts.
	rm -rf bin/

.PHONY: tidy
tidy: ## Run go mod tidy in every workspace module.
	@for mod in gen sdk/go sidecar archivist nodes operator; do \
		echo "==> tidy $$mod"; \
		(cd $$mod && go mod tidy); \
	done
