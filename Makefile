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

.PHONY: ladybug-lib
ladybug-lib: ## Provision the LadybugDB C library and headers for CGo linking.
	@tools/setup-ladybug.sh

# ---------------------------------------------------------------------------
##@ Testing
# ---------------------------------------------------------------------------

# Platform test services that require CGO for SQLite.
CGO_TEST_SERVICES = archivist monitor eventbus frictionledger librarian cartographer

.PHONY: test
test: test-sdk test-sidecar test-flowctl $(addprefix test-,$(CGO_TEST_SERVICES)) test-nodes ## Run all unit tests.

.PHONY: test-sdk
test-sdk: ## Run SDK unit tests.
	CGO_ENABLED=1 go test -v ./sdk/go/...

.PHONY: test-sidecar
test-sidecar: ## Run Sidecar unit tests.
	go test -v ./platform/sidecar/...

.PHONY: test-flowctl
test-flowctl: ## Run flowctl unit tests.
	go test -v ./tools/flowctl/...

$(foreach srv,$(CGO_TEST_SERVICES),$(eval .PHONY: test-$(srv)))
$(foreach srv,$(CGO_TEST_SERVICES),$(eval test-$(srv): ; $(if $(filter cartographer,$(srv)),GOWORK="$(CURDIR)/.cache/ladybug/go.work" )CGO_ENABLED=1 go test -v ./platform/$(srv)/...))

test-cartographer: ladybug-lib

.PHONY: test-nodes
test-nodes: ## Run Node unit tests across the shared nodes module.
	CGO_ENABLED=1 go test -v ./nodes/...

.PHONY: test-operator
test-operator: ## Run Operator unit tests (delegates to operator/Makefile).
	$(MAKE) -C platform/operator test

.PHONY: test-all
test-all: test test-operator ## Run every test suite including the operator.

# ---------------------------------------------------------------------------
##@ Building
# ---------------------------------------------------------------------------

# CGO-enabled node binaries (built from ./nodes/<name>/).
CGO_NODE_BINS = appraisal appraiser arbiter codification codify-smt embassy facilitator forge friction-watcher haiku-quench hitl hitl-appraise hitl-sort human-approval human-arbiter juror law-applicator petition-watcher refine rule-router sort tribunal ttl-watcher

# CGO-enabled platform service binaries (built from ./platform/<name>/cmd/).
CGO_PLATFORM_BINS = archivist monitor eventbus frictionledger librarian cartographer

.PHONY: build
build: build-sidecar build-null-node build-flowctl $(addprefix build-,$(CGO_NODE_BINS)) $(addprefix build-,$(CGO_PLATFORM_BINS)) ## Build all binaries.

.PHONY: build-sidecar
build-sidecar: ## Build the Sidecar binary.
	go build -o bin/sidecar ./platform/sidecar/cmd

.PHONY: build-null-node
build-null-node: ## Build the Null Node binary.
	go build -o bin/null-node ./nodes/null-node

.PHONY: build-flowctl
build-flowctl: ## Build the flowctl binary.
	CGO_ENABLED=0 go build -o bin/flowctl ./tools/flowctl

$(foreach bin,$(CGO_NODE_BINS),$(eval .PHONY: build-$(bin)))
$(foreach bin,$(CGO_NODE_BINS),$(eval build-$(bin): ; CGO_ENABLED=1 go build -o bin/$(bin) ./nodes/$(bin)))

$(foreach bin,$(CGO_PLATFORM_BINS),$(eval .PHONY: build-$(bin)))
$(foreach bin,$(CGO_PLATFORM_BINS),$(eval build-$(bin): ; $(if $(filter cartographer,$(bin)),GOWORK="$(CURDIR)/.cache/ladybug/go.work" )CGO_ENABLED=1 go build -o bin/$(bin) ./platform/$(bin)/cmd))

build-cartographer: ladybug-lib

vet lint lint-fix: ladybug-lib

.PHONY: build-operator
build-operator: ## Build the Operator binary (delegates to operator/Makefile).
	$(MAKE) -C platform/operator build

# ---------------------------------------------------------------------------
##@ Code Quality
# ---------------------------------------------------------------------------

## Location to install local tool binaries
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint

## Tool Versions
GOLANGCI_LINT_VERSION ?= v2.8.0

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: fmt
fmt: ## Run go fmt across the workspace.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet across the workspace.
	GOWORK="$(CURDIR)/.cache/ladybug/go.work" go vet ./...

.PHONY: lint
lint: golangci-lint ## Run golangci-lint across the workspace (excludes operator).
	GOWORK="$(CURDIR)/.cache/ladybug/go.work" "$(GOLANGCI_LINT)" run ./sdk/go/... ./platform/sidecar/... ./platform/archivist/... ./platform/cartographer/... ./platform/monitor/... ./platform/eventbus/... ./platform/federation/... ./platform/frictionledger/... ./platform/librarian/... ./platform/queue/... ./nodes/...

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint with auto-fix (excludes operator).
	GOWORK="$(CURDIR)/.cache/ladybug/go.work" "$(GOLANGCI_LINT)" run --fix ./sdk/go/... ./platform/sidecar/... ./platform/archivist/... ./platform/cartographer/... ./platform/monitor/... ./platform/eventbus/... ./platform/federation/... ./platform/frictionledger/... ./platform/librarian/... ./platform/queue/... ./nodes/...

.PHONY: lint-operator
lint-operator: ## Run golangci-lint for the operator (delegates to operator/Makefile).
	$(MAKE) -C platform/operator lint

.PHONY: lint-all
lint-all: lint lint-operator ## Run golangci-lint across every module including the operator.

.PHONY: check
check: fmt vet lint ## Run fmt, vet, and lint in sequence.

.PHONY: validate-isoflow
validate-isoflow: ## Validate isoflow.json structure and description lengths.
	python3 tools/validate-isoflow.py isoflow.json

.PHONY: check-fix
check-fix: tidy lint-fix ## Run tidy, fmt (via goimports), and lint with auto-fix.

.PHONY: check-fix-all
check-fix-all: check-fix ## Run check-fix across every module including the operator.
	$(MAKE) -C platform/operator lint-fix

.PHONY: verify
verify: test check-fix build ## Run tests, lint, and build in sequence (quality gate).

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
	@for mod in gen sdk/go platform/sidecar platform/archivist platform/cartographer platform/monitor platform/eventbus platform/federation platform/frictionledger platform/librarian platform/pkg/eventbus platform/queue nodes platform/operator tools/flowctl; do \
		echo "==> tidy $$mod"; \
		(cd $$mod && go mod tidy); \
	done

# ---------------------------------------------------------------------------
##@ Tool Installation
# ---------------------------------------------------------------------------

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef
