# Librarian Implementation Plan

## Summary

Build the Librarian service end-to-end: SQLite persistence with whole-law versioning, embedding integration via an `Embedder` interface backed by Ollama, all 8 `LibrarianService` RPCs (excluding cross-flow `ReplicateLaws`), a Sidecar `LibrarianProxy`, and SDK convenience methods. Hearing trigger polling is deferred to the Assay implementation.

## Directory Structure

```
librarian/
├── cmd/
│   └── main.go
├── internal/
│   ├── store/
│   │   └── sqlite/
│   │       ├── store.go
│   │       └── store_test.go
│   ├── embed/
│   │   ├── embedder.go          # Interface definition
│   │   ├── ollama.go            # Ollama implementation
│   │   └── ollama_test.go
│   └── service/
│       ├── librarian_server.go
│       └── librarian_server_test.go
├── deployment.yaml
├── go.mod
└── go.sum
```

Module: `github.com/gideas/flow/librarian` with `replace github.com/gideas/flow/gen => ../gen`.

---

## Phase 1: SQLite Store

**Location**: `librarian/internal/store/sqlite/`

### Schema — 4 tables

```sql
-- The active law registry
CREATE TABLE laws (
    id          TEXT PRIMARY KEY,
    goal        TEXT NOT NULL,
    tier        INTEGER NOT NULL CHECK(tier BETWEEN 1 AND 5),
    active      INTEGER NOT NULL DEFAULT 1,  -- 0 = pending/inactive (hearing-created)
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- Scoping: which artefact kinds a law governs (empty = global)
CREATE TABLE law_applies_to (
    law_id        TEXT NOT NULL REFERENCES laws(id),
    artefact_kind TEXT NOT NULL,
    PRIMARY KEY (law_id, artefact_kind)
);

-- Immutable version log. Head = latest row per law_id.
CREATE TABLE law_versions (
    law_id       TEXT NOT NULL REFERENCES laws(id),
    version_hash TEXT NOT NULL,
    goal         TEXT NOT NULL,
    tier         INTEGER NOT NULL,
    representations_json TEXT NOT NULL,  -- JSON array of {type, content}
    applies_to_json      TEXT NOT NULL,  -- JSON array of strings
    embedding    BLOB,                   -- nullable until embedding runs
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (law_id, version_hash)
);

-- Indexes for scope-aware queries and conflict detection
CREATE INDEX idx_law_applies_to_kind ON law_applies_to(artefact_kind);
CREATE INDEX idx_law_versions_law    ON law_versions(law_id);
CREATE INDEX idx_laws_active         ON laws(active);
CREATE INDEX idx_laws_tier           ON laws(tier);
```

### Store API

| Method | Description |
|--------|-------------|
| `New(dsn string) (*Store, error)` | Open SQLite, WAL mode, foreign keys, init schema |
| `Close() error` | Close DB |
| `CreateLaw(ctx, Law) (id, versionHash, error)` | Insert into `laws`, `law_applies_to`, `law_versions`. Compute content hash (SHA-256 of canonical form). Return generated ID and hash. |
| `CreateLawInactive(ctx, Law) (id, versionHash, error)` | Same as `CreateLaw` but `active=0`. For hearing-created laws. |
| `GetLaw(ctx, id) (Law, error)` | Return full law from head version. Error if missing or retired. |
| `UpdateLaw(ctx, id, Law) (versionHash, error)` | Append new version, update `laws.updated_at` and scope entries. |
| `RetireLaw(ctx, id) error` | Delete from `laws` and `law_applies_to`. Versions remain in `law_versions` (audit trail). |
| `ActivateLaw(ctx, id) error` | Set `active=1`. Used by `ApplyLifecycleAction` after hearing. |
| `SetTier(ctx, id, tier) error` | Update tier (for promote/demote). Creates new version. |
| `QueryLaws(ctx, filter) ([]Law, error)` | Three modes: (1) no filter = all active laws, (2) artefact_kind = scoped + global active laws, (3) kind + representation_type = further filtered by MIME type. Filters never strip representations. |
| `GetLawsByScope(ctx, appliesTo []string) ([]Law, error)` | For conflict detection: laws whose scope overlaps the given kinds, plus all global laws. |
| `GetEmbedding(ctx, lawID, versionHash) ([]float32, error)` | Read embedding BLOB for a version. |
| `SetEmbedding(ctx, lawID, versionHash, []float32) error` | Write embedding BLOB for a version. |
| `GetAllActiveEmbeddings(ctx) ([]LawEmbedding, error)` | Return all (lawID, versionHash, appliesTo, embedding) pairs for active laws that have embeddings. Used by conflict search. |

### Domain Types

Plain Go types — no protobuf at this layer:

```go
type Law struct {
    ID              string
    Goal            string
    Tier            int
    Active          bool
    AppliesTo       []string
    Representations []Representation
    VersionHash     string
    CreatedAt       time.Time
    UpdatedAt       time.Time
}

type Representation struct {
    Type    string  // MIME type
    Content string
}

type LawEmbedding struct {
    LawID       string
    VersionHash string
    AppliesTo   []string
    Embedding   []float32
}
```

### Content Hash Computation

SHA-256 of a canonical byte sequence with deterministic ordering: `goal || tier || sorted(appliesTo) || sorted(representations by type then content)`. Identical law content always produces the same hash regardless of field ordering.

### Tests

In-memory SQLite (`:memory:`). Cover:

- CRUD operations
- Versioning: mutation produces new hash
- Scope queries: kind filtering, global law inclusion
- Representation filtering: MIME type matching without stripping
- Inactive law exclusion from queries
- Retirement preserving version history
- Embedding BLOB round-trip

---

## Phase 2: Embedder Interface

**Location**: `librarian/internal/embed/`

### Interface (`embedder.go`)

```go
// Embedder computes vector embeddings for text.
type Embedder interface {
    Embed(ctx context.Context, text string) ([]float32, error)
}

// CosineSimilarity computes the cosine similarity between two vectors.
func CosineSimilarity(a, b []float32) (float64, error)
```

`CosineSimilarity` is a standalone function — pure math, no external dependency.

### Ollama Implementation (`ollama.go`)

```go
type OllamaEmbedder struct {
    baseURL string
    model   string
    client  *http.Client
}

func NewOllamaEmbedder(baseURL, model string) *OllamaEmbedder
func (o *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error)
```

- Calls `POST /api/embed` on the Ollama HTTP API.
- Model defaults to `qwen3-embedding:4b`, configurable.
- `baseURL` defaults to `http://localhost:11434`.
- Returns the embedding vector from the response.
- Errors on HTTP failure, non-200 status, or malformed response.

### Tests

- `httptest.NewServer` to mock the Ollama API.
- Successful embedding, HTTP errors, malformed response.
- `CosineSimilarity` with known vectors: orthogonal = 0, identical = 1, opposite = -1.

---

## Phase 3: Librarian gRPC Service

**Location**: `librarian/internal/service/`

### Constructor

```go
type LibrarianServer struct {
    flowv1.UnimplementedLibrarianServiceServer
    store    *sqlite.Store
    embedder embed.Embedder  // nil-safe: conflict detection degrades gracefully
    newID    IDGenerator
}

func NewLibrarianServer(store *sqlite.Store, embedder embed.Embedder, idGen IDGenerator) *LibrarianServer
```

### RPC Implementations (8 total, `ReplicateLaws` stubbed)

#### Node-Facing (via Sidecar)

**1. `QueryLaws(QueryLawsRequest) -> QueryLawsResponse`**

- Validate: if `representation_type` is set, `artefact_kind` must also be set.
- Delegate to `store.QueryLaws(ctx, filter)`.
- Convert store `[]Law` to proto `[]Law`.
- Capability check: `READ:law` — extract node identity from gRPC metadata, validate against grants.

**2. `Cite(CiteRequest) -> CiteResponse`**

- Validate: at least one `law_id`.
- Verify each law exists (log warning for missing, don't fail — citation is a signal).
- Return `acknowledged: true`.
- Note: friction emission is the Sidecar's responsibility. The Librarian records the citation; the Sidecar wraps it as `AddFriction`.

**3. `RecordFinding(RecordFindingRequest) -> RecordFindingResponse`**

- Validate: `goal` non-empty, at least one representation.
- Capability check: `WRITE:law/tier1`.
- Create a Tier 1 law via `store.CreateLaw` with `tier=1`.
- Return the law ID immediately (write-availability-first).
- Compute embedding inline and store it.
- Run `findConflicts` for candidate duplicates — log results, no automatic action yet.

#### Service-Facing

**4. `GetLaw(GetLawRequest) -> GetLawResponse`**

- Validate: `law_id` non-empty.
- `store.GetLaw(ctx, id)`. Return `LAW_NOT_FOUND` if missing.

**5. `WriteLaw(WriteLawRequest) -> WriteLawResponse`**

- Validate: law has goal, at least one representation, valid tier.
- If `law.id` exists in store: `store.UpdateLaw` (new version).
- If `law.id` is empty: `store.CreateLawInactive` (hearing-created, pending activation).
- Compute and store embedding.
- Return `law_id` and `version_hash`.

**6. `RetireLaw(RetireLawRequest) -> RetireLawResponse`**

- `store.RetireLaw(ctx, id)`.
- Return `acknowledged: true`.

**7. `ReplicateLaws(ReplicateLawsRequest) -> ReplicateLawsResponse`**

- **Stubbed** — cross-flow support out of scope. Returns `Unimplemented` status.

**8. `ApplyLifecycleAction(ApplyLifecycleActionRequest) -> ApplyLifecycleActionResponse`**

- Validate: `law_id` non-empty, `verdict` is valid.
- Switch on verdict:
  - `PROMOTE`: Increment tier (T1->T2, T2->T3). Activate if inactive. New version.
  - `RETIRE`: `store.RetireLaw(ctx, id)`.
  - `DEMOTE`: Decrement tier (T2->T1). New version.
- Return `acknowledged: true`.

### Capability Enforcement

The Librarian receives node identity via gRPC metadata (injected by Sidecar). The Sidecar forwards the capability list as a metadata header (`x-flow-capabilities`). The Librarian parses and checks it. This keeps the Librarian stateless with respect to node configuration.

### Tests

In-memory store + nil embedder (embedding operations degrade gracefully). Cover:

- All 8 RPCs
- Capability denial: missing `READ:law`, missing `WRITE:law/tier1`
- Query filtering modes
- Lifecycle actions: promote T1->T2, demote T2->T1, retire
- `WriteLaw` creates inactive law
- `ApplyLifecycleAction` activates pending law

---

## Phase 4: Conflict Detection

**Location**: Integrated into `librarian/internal/service/`

Used by `RecordFinding` (background duplicate detection) and ready for `ReplicateLaws` when cross-flow is implemented.

### Algorithm (`findConflicts` internal method)

1. Get the incoming law's embedding and `appliesTo` scope.
2. Call `store.GetAllActiveEmbeddings(ctx)` to load the candidate set.
3. **Scope filter**: For each candidate:
   - If incoming law is global (empty `appliesTo`): check against all candidates.
   - If candidate is global: always include.
   - Otherwise: include only if `appliesTo` sets overlap.
4. **Similarity filter**: Compute `CosineSimilarity(incoming, candidate)`. Keep candidates above the configurable similarity threshold.
5. Return the candidate list (law IDs, similarity scores).

Stage 2 (LLM contradiction evaluation) is not implemented in this plan. Conflict detection returns candidate contradictions only. For `RecordFinding`, candidates are logged but no automatic action is taken.

### Configuration

Similarity threshold: `LIBRARIAN_SIMILARITY_THRESHOLD` environment variable, default `0.85`.

---

## Phase 5: Sidecar LibrarianProxy

**Location**: `sidecar/internal/proxy/librarian.go`

Follows the exact pattern of `ArchivistProxy`.

### Structure

```go
type LibrarianProxy struct {
    flowv1.UnimplementedLibrarianServiceServer
    client        flowv1.LibrarianServiceClient
    monitorClient flowv1.FlowMonitorServiceClient  // for Cite friction emission
    conn          *grpc.ClientConn
    monitorConn   *grpc.ClientConn
}
```

### Constructor

`NewLibrarianProxy(librarianAddr, monitorAddr string) (*LibrarianProxy, error)`

### RPC Forwarding

| RPC | Sidecar Behaviour |
|-----|-------------------|
| `QueryLaws` | `propagateMetadata` + forward. Passthrough. |
| `Cite` | `propagateMetadata` + forward to Librarian. **Then** emit `AddFriction` to Flow Monitor with fixed citation magnitude, injecting `node_id`, `workitem_id`, `flow_id` from metadata, and `law_ids` from the request. |
| `RecordFinding` | `propagateMetadata` + forward. Passthrough. |
| `GetLaw` | `propagateMetadata` + forward. Passthrough. |
| `WriteLaw` | `propagateMetadata` + forward. Passthrough. |
| `RetireLaw` | `propagateMetadata` + forward. Passthrough. |
| `ReplicateLaws` | `propagateMetadata` + forward. Passthrough. |
| `ApplyLifecycleAction` | `propagateMetadata` + forward. Passthrough. |

The critical Sidecar-specific logic is in `Cite`: the Sidecar wraps the citation as an `AddFriction` call with fixed citation magnitude. The proxy:

1. Forwards `Cite` to the Librarian (which records the citation).
2. Calls `monitorClient.AddFriction` with extracted identity context and fixed magnitude (configurable via `CITATION_FRICTION_MAGNITUDE`, default `1`).

### Registration in `sidecar/cmd/main.go`

- New env var: `LIBRARIAN_ADDRESS` (same pattern as `ARCHIVIST_ADDRESS`).
- New env var: `MONITOR_ADDRESS` (needed for the Cite->AddFriction path).
- If `LIBRARIAN_ADDRESS` is set: create `LibrarianProxy`, register it. Otherwise: skip (nodes without Librarian access cannot query laws).

### Tests

Mock Librarian and Monitor gRPC servers. Cover:

- `Cite` produces both a forwarded `Cite` call and an `AddFriction` call with correct magnitude and identity
- Metadata propagation on all RPCs

---

## Phase 6: SDK Librarian Client

**Location**: `sdk/go/client.go`

### Changes to `Client` struct

1. Add `Librarian flowv1.LibrarianServiceClient` field.
2. Initialize in `NewClient`: `c.Librarian = flowv1.NewLibrarianServiceClient(conn)`.

### Convenience Methods

```go
// QueryLaws returns all laws matching the filter.
// Pass empty strings for all laws.
// Pass kind for scoped+global.
// Pass kind+repType for further filtering.
func (c *Client) QueryLaws(ctx context.Context, kind, representationType string) ([]*flowv1.Law, error)

// Cite records usage of one or more laws.
func (c *Client) Cite(ctx context.Context, lawIDs ...string) error

// RecordFinding creates a Tier 1 Finding.
func (c *Client) RecordFinding(ctx context.Context, goal string, appliesTo []string, representations []*flowv1.Representation) (string, error)
```

Follow the existing convenience method pattern: build the proto request, inject workitem context, call the stub, wrap errors.

### Tests

Follow the pattern in `client_test.go`. Test metadata injection. Test convenience methods with a mock server.

---

## Phase 7: Wiring & Deployment

### `librarian/cmd/main.go`

Follows the Monitor pattern:

1. Read env vars:
   - `LIBRARIAN_PORT` (default `50056`)
   - `LIBRARIAN_DB_PATH` (default `/data/librarian.db`)
   - `OLLAMA_URL` (default `http://localhost:11434`)
   - `OLLAMA_MODEL` (default `qwen3-embedding:4b`)
   - `LIBRARIAN_SIMILARITY_THRESHOLD` (default `0.85`)
2. Init SQLite store.
3. Init Ollama embedder (nil-safe if `OLLAMA_URL` is empty or unreachable).
4. Create `LibrarianServer`.
5. Register, enable reflection, graceful shutdown.

### `librarian/deployment.yaml`

Kubernetes Deployment + Service + PVC (1Gi, `/data`). Port 50056. Env vars from ConfigMap.

---

## Execution Order

| Step | Component | Files | Dependencies |
|------|-----------|-------|-------------|
| 1 | Store | `librarian/go.mod`, `librarian/internal/store/sqlite/store.go`, `store_test.go` | None |
| 2 | Embedder | `librarian/internal/embed/embedder.go`, `ollama.go`, `ollama_test.go` | None |
| 3 | Service | `librarian/internal/service/librarian_server.go`, `librarian_server_test.go` | Steps 1-2 |
| 4 | Wiring | `librarian/cmd/main.go`, `librarian/deployment.yaml` | Step 3 |
| 5 | Sidecar proxy | `sidecar/internal/proxy/librarian.go`, `librarian_test.go`, update `sidecar/cmd/main.go` | Step 4 |
| 6 | SDK client | Update `sdk/go/client.go`, `client_test.go` | Step 5 |

Steps 1 and 2 are independent and can be built in parallel. Step 3 depends on both. Steps 5 and 6 can be done in parallel after step 4.

---

## Explicitly Deferred

| Item | Reason |
|------|--------|
| `ReplicateLaws` (cross-flow) | Out of scope |
| Hearing trigger polling (friction thresholds, review TTL) | Deferred to Assay implementation |
| LLM contradiction evaluation (Stage 2 of conflict protocol) | Returns candidates only; LLM evaluation deferred |
| Async duplicate Finding detection background loop | Log candidates; automatic merge/retire deferred |
| Capability grant resolution from Operator | Pass capabilities via metadata; full resolution deferred |

---

## Spec Compliance Checklist

| Spec Requirement | Covered |
|------------------|---------|
| Law is a single object with goal + representations | Yes (store schema) |
| Whole-law versioning via content hash | Yes (`law_versions` table) |
| `appliesTo` scoping, empty = global | Yes (store + query logic) |
| Tier field (1-5) | Yes |
| Three query modes | Yes (`QueryLaws`) |
| Filters gate inclusion, never strip representations | Yes |
| `Cite` emits fixed-magnitude friction | Yes (Sidecar proxy) |
| `RecordFinding` is write-availability-first | Yes (returns immediately) |
| `WriteLaw` for Tier 2+ | Yes |
| Inactive/pending state for hearing-created laws | Yes (`active` column) |
| `ApplyLifecycleAction` activate/promote/retire/demote | Yes |
| Retire preserves history | Yes (versions remain, `laws` row deleted) |
| Semantic index for conflict detection | Yes (embeddings in `law_versions`) |
| Scope-aware conflict checking | Yes (`findConflicts` scoping) |
| Capability enforcement (`READ:law`, `WRITE:law/tierN`) | Yes (Librarian-side check) |
| Sidecar mediates all node-facing RPCs | Yes (LibrarianProxy) |
| SDK convenience methods | Yes |
| Fail closed on governance paths | Yes (errors, not degraded results) |
