# Restructure Plan: Separate Build Contexts

## Problem

The monorepo has a single `go.work` listing 14 modules. Every Dockerfile must copy `go.mod`/`go.sum` stubs for **all** workspace members, even if the service being built only depends on 1-2 of them. Adding a new module to `go.work` breaks every Dockerfile.

Nodes should depend exclusively on `sdk/go` + `gen`. They currently drag in the entire workspace.

## Principle

**Three distinct concerns, four build contexts:**

1. **SDK** (`gen` + `sdk/go`) — The contract and the developer toolkit.
2. **Platform** (operator, sidecar, eventbus, frictionledger, monitor, librarian, archivist, clerk, pkg/eventbus) — The runtime infrastructure.
3. **Nodes** (forge, sort, haiku-quench, refine, appraise, ...) — The agents built with the SDK.
4. **Tools** (haiku-watch, ...) — Dev/debug utilities.

Each context gets its own scoped `go.work`. Dockerfiles use `GOWORK=off` and copy only real dependencies.

## Key Constraint

**All Go module paths stay the same.** `github.com/gideas/flow/operator` remains `github.com/gideas/flow/operator` even though the directory moves from `/operator` to `/platform/operator`. The `go.work` `use` directives and `replace` directives in `go.mod` handle the filesystem-to-module mapping. This means **zero Go source file changes**.

## Dependency Graph

```
gen  <──  pkg/eventbus  <──  {archivist, librarian, operator, sidecar}
 ^
 ├── sdk/go  <──  nodes
 │
 └── {clerk, eventbus, frictionledger, monitor, tools/haiku-watch}
```

- All arrows point toward `gen` (the universal root).
- `pkg/eventbus` is a Platform-internal shared library.
- No reverse dependencies: SDK never depends on Platform or Nodes. Nodes never depend on Platform.
- `jury` is **parked** — excluded from all workspaces until rework is complete.

## Target Layout

```
/
├── proto/                          # Proto sources (unchanged)
├── gen/                            # Generated contract (stays at root)
├── go.work                         # Root workspace — local dev only, lists everything
│
├── sdk/
│   └── go/                         # Go SDK (stays at current path)
│       ├── go.mod                  # module github.com/gideas/flow/sdk/go
│       └── go.work                 # Scoped: sdk/go + gen
│
├── platform/                       # NEW directory
│   ├── go.work                     # Scoped: all platform modules + gen
│   ├── operator/                   # git mv from /operator
│   ├── sidecar/                    # git mv from /sidecar
│   ├── eventbus/                   # git mv from /eventbus
│   ├── frictionledger/             # git mv from /frictionledger
│   ├── monitor/                    # git mv from /monitor
│   ├── librarian/                  # git mv from /librarian
│   ├── archivist/                  # git mv from /archivist
│   ├── clerk/                      # git mv from /clerk
│   └── pkg/
│       └── eventbus/               # git mv from /pkg/eventbus
│
├── nodes/                          # Stays at current path
│   ├── go.work                     # Scoped: nodes + sdk/go + gen
│   ├── forge/
│   ├── sort/
│   └── ...
│
├── tools/                          # Stays at current path
│   ├── go.work                     # Scoped: tools modules + gen
│   └── haiku-watch/
│
├── jury/                           # Parked — excluded from all go.work files
├── charts/                         # Helm charts (unchanged)
├── Makefile                        # Updated to delegate to contexts
└── AGENTS.md                       # Updated doc paths
```

## Changes by Category

| Category | Files Changed | Nature of Change |
|----------|--------------|-----------------|
| **Go source** | **0** | Module paths preserved |
| **go.mod** | ~9 platform modules + pkg/eventbus | Update `replace` relative paths |
| **go.work** | 1 updated (root), 4 new (platform, nodes, sdk, tools) | Scoped workspace files |
| **Dockerfiles** | All rewritten | `GOWORK=off`, copy only real deps |
| **Makefile** | Root + operator | Path references updated |
| **Docs** | AGENTS.md, DEPLOYMENT_PLAN.md | Path references updated |

## Execution Steps

### Step 1: Move Platform Modules

```bash
mkdir -p platform/pkg
git mv operator platform/operator
git mv sidecar platform/sidecar
git mv eventbus platform/eventbus
git mv frictionledger platform/frictionledger
git mv monitor platform/monitor
git mv librarian platform/librarian
git mv archivist platform/archivist
git mv clerk platform/clerk
git mv pkg/eventbus platform/pkg/eventbus
```

### Step 2: Update `replace` Directives

Every moved module's `go.mod` has `replace` directives with relative paths. After moving one level deeper into `platform/`, paths to root-level modules gain one `../`:

| Module (new location) | `replace gen =>` before | `replace gen =>` after |
|---|---|---|
| `platform/operator/go.mod` | `../gen` | `../../gen` |
| `platform/sidecar/go.mod` | `../gen` | `../../gen` |
| `platform/eventbus/go.mod` | `../gen` | `../../gen` |
| `platform/frictionledger/go.mod` | `../gen` | `../../gen` |
| `platform/monitor/go.mod` | `../gen` | `../../gen` |
| `platform/librarian/go.mod` | `../gen` | `../../gen` |
| `platform/archivist/go.mod` | `../gen` | `../../gen` |
| `platform/clerk/go.mod` | `../gen` | `../../gen` |
| `platform/pkg/eventbus/go.mod` | `../../gen` | `../../../gen` |

For `replace pkg/eventbus =>` in modules that also moved into `platform/`:
- `platform/operator/go.mod`: `../pkg/eventbus` stays `../pkg/eventbus` (both moved together)
- `platform/sidecar/go.mod`: same
- `platform/librarian/go.mod`: same
- `platform/archivist/go.mod`: same

### Step 3: Create Scoped `go.work` Files

**`platform/go.work`:**
```go
go 1.25.3

use (
    ./operator
    ./sidecar
    ./eventbus
    ./frictionledger
    ./monitor
    ./librarian
    ./archivist
    ./clerk
    ./pkg/eventbus
    ../gen
)
```

**`nodes/go.work`:**
```go
go 1.25.3

use (
    .
    ../gen
    ../sdk/go
)
```

**`sdk/go.work`:**
```go
go 1.25.3

use (
    ./go
    ../gen
)
```

**`tools/go.work`:**
```go
go 1.25.3

use (
    ./haiku-watch
    ../gen
)
```

### Step 4: Update Root `go.work`

```go
go 1.25.3

use (
    ./gen
    ./sdk/go
    ./platform/operator
    ./platform/sidecar
    ./platform/eventbus
    ./platform/frictionledger
    ./platform/monitor
    ./platform/librarian
    ./platform/archivist
    ./platform/clerk
    ./platform/pkg/eventbus
    ./nodes
    ./tools/haiku-watch
)
```

Note: `jury` is excluded.

### Step 5: Rewrite Dockerfiles

Each Dockerfile sets `GOWORK=off` and copies only what it actually compiles.

**Example — nodes/Dockerfile:**
```dockerfile
FROM golang:1.25 AS builder
ARG NODE
WORKDIR /workspace
ENV GOWORK=off

COPY gen/go.mod gen/go.sum gen/
COPY gen/flow/ gen/flow/
COPY sdk/go/ sdk/go/
COPY nodes/go.mod nodes/go.sum nodes/
RUN cd nodes && go mod download
COPY nodes/ nodes/

RUN cd nodes && CGO_ENABLED=0 go build -o /node ./${NODE}

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /node /node
USER 65532:65532
ENTRYPOINT ["/node"]
```

**Example — platform/operator/Dockerfile:**
```dockerfile
FROM golang:1.25 AS builder
WORKDIR /workspace
ENV GOWORK=off

COPY gen/go.mod gen/go.sum gen/
COPY gen/flow/ gen/flow/
COPY platform/pkg/eventbus/ platform/pkg/eventbus/
COPY platform/operator/go.mod platform/operator/go.sum platform/operator/
RUN cd platform/operator && go mod download
COPY platform/operator/ platform/operator/

RUN cd platform/operator && CGO_ENABLED=0 go build -o /manager ./cmd/main.go

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
```

Build context for all Dockerfiles is the repo root.

### Step 6: Update Makefile

- `$(MAKE) -C operator` becomes `$(MAKE) -C platform/operator`
- Lint/test/build paths updated from `./sidecar/...` to `./platform/sidecar/...` etc.
- Tidy loop updated with new paths.

### Step 7: Update Documentation

- `AGENTS.md` repository structure section.
- `DEPLOYMENT_PLAN.md` image build commands and Dockerfile paths.

### Step 8: Quality Gates

```bash
go build ./...                          # From root, using root go.work
go test ./...                           # Full test suite
cd platform && go build ./...           # Platform context
cd nodes && go build ./...              # Nodes context
make check-fix                          # Lint
```

## Risks and Mitigations

| Risk | Mitigation |
|------|-----------|
| Operator Kubebuilder paths break | `PROJECT` file uses module path not directory path; validated by `make manifests generate` |
| Helm chart breaks | Chart uses image names and service names, not source paths; no changes needed |
| `buf generate` breaks | `buf.yaml` references `proto/`, not moved directories; no changes needed |
| Relative replace paths miscalculated | Mechanical: count directory depth, verify with `go build` |
| Root `go.work` conflicts with scoped ones | Root `go.work` is for dev only; Docker uses `GOWORK=off`; CI can set `GOWORK` explicitly |
