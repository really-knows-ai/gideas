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
# The CGO service suites (especially cartographer) exceed Go's default 10m
# package test timeout on slower machines: each file-backed test performs git
# checkouts and LadybugDB DDL, so the ceiling is raised. -timeout is an upper
# bound, never a delay.
$(foreach srv,$(CGO_TEST_SERVICES),$(eval test-$(srv): ; $(if $(filter cartographer,$(srv)),GOWORK="$(CURDIR)/.cache/ladybug/go.work" )CGO_ENABLED=1 go test -timeout 30m -v ./platform/$(srv)/...))

test-cartographer: ladybug-lib

# The cartographer suite is split into a fast in-memory unit subset and a
# file-backed/git integration subset. Tests that open a file-backed LadybugDB
# store and/or do real git operations are guarded with `if testing.Short()`,
# so they are skipped under `-short`:
#   - test-cartographer-unit       -> go test -short  (fast, in-memory only)
#   - test-cartographer-integration-> go test (no -short, runs EVERY test)
#   - test-cartographer (the gate) -> go test (no -short, runs EVERY test)
# Coverage is preserved: the guarded tests still run under every non-short
# target, so the full gate exercises the whole suite unchanged.
.PHONY: test-cartographer-unit test-cartographer-integration
test-cartographer-unit: ladybug-lib
	GOWORK="$(CURDIR)/.cache/ladybug/go.work" CGO_ENABLED=1 go test -short -timeout 30m -v ./platform/cartographer/...

test-cartographer-integration: ladybug-lib
	GOWORK="$(CURDIR)/.cache/ladybug/go.work" CGO_ENABLED=1 go test -timeout 30m -v ./platform/cartographer/...

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
CGO_NODE_BINS = appraisal appraiser arbiter codification codify-smt embassy facilitator forge friction-watcher haiku-quench hitl hitl-appraise hitl-arbiter juror law-applicator petition-watcher refine rule-router sort tribunal ttl-watcher

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
	GOWORK="$(CURDIR)/.cache/ladybug/go.work" go vet ./sdk/go/... ./platform/sidecar/... ./platform/archivist/... ./platform/cartographer/... ./platform/monitor/... ./platform/eventbus/... ./platform/federation/... ./platform/frictionledger/... ./platform/librarian/... ./nodes/...

.PHONY: lint
lint: golangci-lint ## Run golangci-lint across the workspace (excludes operator).
	GOWORK="$(CURDIR)/.cache/ladybug/go.work" "$(GOLANGCI_LINT)" run ./sdk/go/... ./platform/sidecar/... ./platform/archivist/... ./platform/cartographer/... ./platform/monitor/... ./platform/eventbus/... ./platform/federation/... ./platform/frictionledger/... ./platform/librarian/... ./nodes/...

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint with auto-fix (excludes operator).
	GOWORK="$(CURDIR)/.cache/ladybug/go.work" "$(GOLANGCI_LINT)" run --fix ./sdk/go/... ./platform/sidecar/... ./platform/archivist/... ./platform/cartographer/... ./platform/monitor/... ./platform/eventbus/... ./platform/federation/... ./platform/frictionledger/... ./platform/librarian/... ./nodes/...

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

.PHONY: verify-check
verify-check: test vet lint ## Read-only quality gate: run tests, vet, and lint without modifying the tree (no tidy/lint-fix auto-fix, no build artifacts). Use where verify's mutating write is not permitted.

# ---------------------------------------------------------------------------
##@ Code Generation
# ---------------------------------------------------------------------------

.PHONY: proto
proto: ## Regenerate Go code from proto definitions using buf.
	buf generate

.PHONY: flowctl-manifests
flowctl-manifests: ## Regenerate flowctl's embedded manifests from operator sources.
	# The embedded directories are exact snapshots of their sources: drop any
	# pre-existing copies first so a renamed or removed source file never
	# lingers (e.g. the stale flow.gideas.io_* CRD names).
	rm -f tools/flowctl/manifestfs/crd/*.yaml tools/flowctl/manifestfs/operator/*.yaml
	cp platform/operator/config/crd/bases/*.yaml tools/flowctl/manifestfs/crd/
	# manager.yaml is a multi-document stream (Namespace + Deployment); extract
	# the single Deployment document (the doc after the first `---` separator)
	# and rewrite its namespace from "system" to "foundry-system" so the
	# embedded copy keeps the single-doc shape manifestfs parses at init.
	sed '1,/^---$$/d; s/namespace: system/namespace: foundry-system/' platform/operator/config/manager/manager.yaml > tools/flowctl/manifestfs/operator/deployment.yaml
	# namespace.yaml is the leading Namespace document of manager.yaml with its
	# name rewritten from "system" to "foundry-system".
	sed -n '1,/^---$$/{/^---$$/d;s/name: system/name: foundry-system/;p}' platform/operator/config/manager/manager.yaml > tools/flowctl/manifestfs/operator/namespace.yaml
	# The ClusterRole is cluster-scoped, so a verbatim copy of config/rbac/role.yaml.
	cp platform/operator/config/rbac/role.yaml tools/flowctl/manifestfs/operator/role.yaml
	# The ClusterRoleBinding's subject and the ServiceAccount live in the
	# foundry-system namespace, so their "system" reference is rewritten.
	sed 's/namespace: system/namespace: foundry-system/' platform/operator/config/rbac/role_binding.yaml > tools/flowctl/manifestfs/operator/rolebinding.yaml
	sed 's/namespace: system/namespace: foundry-system/' platform/operator/config/rbac/service_account.yaml > tools/flowctl/manifestfs/operator/serviceaccount.yaml

# ---------------------------------------------------------------------------
##@ Housekeeping
# ---------------------------------------------------------------------------

.PHONY: clean
clean: ## Remove build artefacts.
	rm -rf bin/

.PHONY: tidy
tidy: ## Run go mod tidy in every workspace module.
	@for mod in gen sdk/go platform/sidecar platform/archivist platform/cartographer platform/monitor platform/eventbus platform/federation platform/frictionledger platform/librarian platform/pkg/eventbus platform/pkg/metadata platform/pkg/relay nodes platform/operator tools/flowctl; do \
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
# The macro is idempotent: it skips install when the versioned binary $(1)-$(3) already exists, since the
# trailing "ln -sf" re-asserts the $(1) symlink. (A prior BSD/readlink target-equality guard was
# non-portable: macOS readlink lacks GNU "--" and forced a re-install on every repeated run.)
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef
