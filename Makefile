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
test: test-sdk test-sidecar test-archivist test-monitor test-eventbus test-librarian test-jury test-clerk ## Run all unit tests.

.PHONY: test-sdk
test-sdk: ## Run SDK unit tests.
	CGO_ENABLED=1 go test -v ./sdk/go/...

.PHONY: test-sidecar
test-sidecar: ## Run Sidecar unit tests.
	go test -v ./sidecar/...

.PHONY: test-archivist
test-archivist: ## Run Archivist unit tests.
	CGO_ENABLED=1 go test -v ./archivist/...

.PHONY: test-monitor
test-monitor: ## Run Monitor unit tests.
	CGO_ENABLED=1 go test -v ./monitor/...

.PHONY: test-eventbus
test-eventbus: ## Run Event Bus unit tests.
	CGO_ENABLED=1 go test -v ./eventbus/...

.PHONY: test-librarian
test-librarian: ## Run Librarian unit tests.
	CGO_ENABLED=1 go test -v ./librarian/...

.PHONY: test-jury
test-jury: ## Run Jury unit tests.
	CGO_ENABLED=1 go test -v ./jury/...

.PHONY: test-clerk
test-clerk: ## Run Clerk unit tests.
	go test -v ./clerk/...

.PHONY: test-operator
test-operator: ## Run Operator unit tests (delegates to operator/Makefile).
	$(MAKE) -C operator test

.PHONY: test-all
test-all: test test-operator ## Run every test suite including the operator.

# ---------------------------------------------------------------------------
##@ Building
# ---------------------------------------------------------------------------

.PHONY: build
build: build-sidecar build-null-node build-archivist build-monitor build-eventbus build-librarian build-jury build-clerk ## Build all binaries.

.PHONY: build-sidecar
build-sidecar: ## Build the Sidecar binary.
	go build -o bin/sidecar ./sidecar/cmd

.PHONY: build-null-node
build-null-node: ## Build the Null Node binary.
	go build -o bin/null-node ./nodes/null-node/cmd

.PHONY: build-archivist
build-archivist: ## Build the Archivist binary.
	CGO_ENABLED=1 go build -o bin/archivist ./archivist/cmd

.PHONY: build-monitor
build-monitor: ## Build the Monitor binary.
	CGO_ENABLED=1 go build -o bin/monitor ./monitor/cmd

.PHONY: build-eventbus
build-eventbus: ## Build the Event Bus binary.
	CGO_ENABLED=1 go build -o bin/eventbus ./eventbus/cmd

.PHONY: build-librarian
build-librarian: ## Build the Librarian binary.
	CGO_ENABLED=1 go build -o bin/librarian ./librarian/cmd

.PHONY: build-jury
build-jury: ## Build the Jury binary.
	CGO_ENABLED=1 go build -o bin/jury ./jury/cmd

.PHONY: build-clerk
build-clerk: ## Build the Clerk binary.
	go build -o bin/clerk ./clerk/cmd

.PHONY: build-operator
build-operator: ## Build the Operator binary (delegates to operator/Makefile).
	$(MAKE) -C operator build

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
	"$(GOLANGCI_LINT)" run ./sdk/go/... ./sidecar/... ./archivist/... ./monitor/... ./eventbus/... ./librarian/... ./jury/... ./clerk/... ./nodes/... ./tools/haiku-watch/...

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint with auto-fix (excludes operator).
	"$(GOLANGCI_LINT)" run --fix ./sdk/go/... ./sidecar/... ./archivist/... ./monitor/... ./eventbus/... ./librarian/... ./jury/... ./clerk/... ./nodes/... ./tools/haiku-watch/...

.PHONY: lint-operator
lint-operator: ## Run golangci-lint for the operator (delegates to operator/Makefile).
	$(MAKE) -C operator lint

.PHONY: lint-all
lint-all: lint lint-operator ## Run golangci-lint across every module including the operator.

.PHONY: check
check: fmt vet lint ## Run fmt, vet, and lint in sequence.

.PHONY: check-fix
check-fix: tidy lint-fix ## Run tidy, fmt (via goimports), and lint with auto-fix.

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
	@for mod in gen sdk/go sidecar archivist monitor eventbus librarian jury clerk nodes operator; do \
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
