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
test: test-sdk test-sidecar test-archivist test-monitor test-eventbus test-frictionledger test-librarian test-nodes ## Run all unit tests.

.PHONY: test-sdk
test-sdk: ## Run SDK unit tests.
	CGO_ENABLED=1 go test -v ./sdk/go/...

.PHONY: test-sidecar
test-sidecar: ## Run Sidecar unit tests.
	go test -v ./platform/sidecar/...

.PHONY: test-archivist
test-archivist: ## Run Archivist unit tests.
	CGO_ENABLED=1 go test -v ./platform/archivist/...

.PHONY: test-monitor
test-monitor: ## Run Monitor unit tests.
	CGO_ENABLED=1 go test -v ./platform/monitor/...

.PHONY: test-eventbus
test-eventbus: ## Run Event Bus unit tests.
	CGO_ENABLED=1 go test -v ./platform/eventbus/...

.PHONY: test-frictionledger
test-frictionledger: ## Run Friction Ledger unit tests.
	CGO_ENABLED=1 go test -v ./platform/frictionledger/...

.PHONY: test-librarian
test-librarian: ## Run Librarian unit tests.
	CGO_ENABLED=1 go test -v ./platform/librarian/...

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

.PHONY: build
build: build-sidecar build-null-node build-forge build-sort build-appraise build-reviewer build-refine build-arbiter build-juror build-codify-smt build-codification build-rule-router build-facilitator build-hitl build-law-applicator build-tribunal build-friction-watcher build-ttl-watcher build-archivist build-monitor build-eventbus build-frictionledger build-librarian ## Build all binaries.

.PHONY: build-sidecar
build-sidecar: ## Build the Sidecar binary.
	go build -o bin/sidecar ./platform/sidecar/cmd

.PHONY: build-null-node
build-null-node: ## Build the Null Node binary.
	go build -o bin/null-node ./nodes/null-node

.PHONY: build-forge
build-forge: ## Build the Forge node binary.
	CGO_ENABLED=1 go build -o bin/forge ./nodes/forge

.PHONY: build-sort
build-sort: ## Build the Sort node binary.
	CGO_ENABLED=1 go build -o bin/sort ./nodes/sort

.PHONY: build-appraise
build-appraise: ## Build the Appraise node binary.
	CGO_ENABLED=1 go build -o bin/appraise ./nodes/appraise

.PHONY: build-reviewer
build-reviewer: ## Build the Reviewer node binary.
	CGO_ENABLED=1 go build -o bin/reviewer ./nodes/reviewer

.PHONY: build-refine
build-refine: ## Build the Refine node binary.
	CGO_ENABLED=1 go build -o bin/refine ./nodes/refine

.PHONY: build-arbiter
build-arbiter: ## Build the Arbiter node binary.
	CGO_ENABLED=1 go build -o bin/arbiter ./nodes/arbiter

.PHONY: build-juror
build-juror: ## Build the Juror node binary.
	CGO_ENABLED=1 go build -o bin/juror ./nodes/juror

.PHONY: build-codify-smt
build-codify-smt: ## Build the codify-smt node binary.
	CGO_ENABLED=1 go build -o bin/codify-smt ./nodes/codify-smt

.PHONY: build-codification
build-codification: ## Build the Codification node binary.
	CGO_ENABLED=1 go build -o bin/codification ./nodes/codification

.PHONY: build-friction-watcher
build-friction-watcher: ## Build the Friction Watcher node binary.
	CGO_ENABLED=1 go build -o bin/friction-watcher ./nodes/friction-watcher

.PHONY: build-ttl-watcher
build-ttl-watcher: ## Build the TTL Watcher node binary.
	CGO_ENABLED=1 go build -o bin/ttl-watcher ./nodes/ttl-watcher

.PHONY: build-rule-router
build-rule-router: ## Build the Rule Router node binary.
	CGO_ENABLED=1 go build -o bin/rule-router ./nodes/rule-router

.PHONY: build-facilitator
build-facilitator: ## Build the Facilitator node binary.
	CGO_ENABLED=1 go build -o bin/facilitator ./nodes/facilitator

.PHONY: build-hitl
build-hitl: ## Build the HITL node binary.
	CGO_ENABLED=1 go build -o bin/hitl ./nodes/hitl

.PHONY: build-law-applicator
build-law-applicator: ## Build the Law Applicator node binary.
	CGO_ENABLED=1 go build -o bin/law-applicator ./nodes/law-applicator

.PHONY: build-tribunal
build-tribunal: ## Build the Tribunal node binary.
	CGO_ENABLED=1 go build -o bin/tribunal ./nodes/tribunal

.PHONY: build-archivist
build-archivist: ## Build the Archivist binary.
	CGO_ENABLED=1 go build -o bin/archivist ./platform/archivist/cmd

.PHONY: build-monitor
build-monitor: ## Build the Monitor binary.
	CGO_ENABLED=1 go build -o bin/monitor ./platform/monitor/cmd

.PHONY: build-eventbus
build-eventbus: ## Build the Event Bus binary.
	CGO_ENABLED=1 go build -o bin/eventbus ./platform/eventbus/cmd

.PHONY: build-frictionledger
build-frictionledger: ## Build the Friction Ledger binary.
	CGO_ENABLED=1 go build -o bin/frictionledger ./platform/frictionledger/cmd

.PHONY: build-librarian
build-librarian: ## Build the Librarian binary.
	CGO_ENABLED=1 go build -o bin/librarian ./platform/librarian/cmd

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
	go vet ./...

.PHONY: lint
lint: golangci-lint ## Run golangci-lint across the workspace (excludes operator).
	"$(GOLANGCI_LINT)" run ./sdk/go/... ./platform/sidecar/... ./platform/archivist/... ./platform/monitor/... ./platform/eventbus/... ./platform/federation/... ./platform/frictionledger/... ./platform/librarian/... ./nodes/... ./tools/haiku-watch/...

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint with auto-fix (excludes operator).
	"$(GOLANGCI_LINT)" run --fix ./sdk/go/... ./platform/sidecar/... ./platform/archivist/... ./platform/monitor/... ./platform/eventbus/... ./platform/federation/... ./platform/frictionledger/... ./platform/librarian/... ./nodes/... ./tools/haiku-watch/...

.PHONY: lint-operator
lint-operator: ## Run golangci-lint for the operator (delegates to operator/Makefile).
	$(MAKE) -C platform/operator lint

.PHONY: lint-all
lint-all: lint lint-operator ## Run golangci-lint across every module including the operator.

.PHONY: check
check: fmt vet lint ## Run fmt, vet, and lint in sequence.

.PHONY: check-fix
check-fix: tidy lint-fix ## Run tidy, fmt (via goimports), and lint with auto-fix.

.PHONY: check-fix-all
check-fix-all: check-fix ## Run check-fix across every module including the operator.
	$(MAKE) -C platform/operator lint-fix

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
	@for mod in gen sdk/go platform/sidecar platform/archivist platform/monitor platform/eventbus platform/federation platform/frictionledger platform/librarian platform/pkg/eventbus nodes platform/operator tools/haiku-watch; do \
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
