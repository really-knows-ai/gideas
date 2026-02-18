# Plan: Remove `spec.kind` from GovernedArtefact

## Problem

The `GovernedArtefact` CRD has two fields that identify the governed artefact type:

- `metadata.name` — the standard Kubernetes resource name (e.g. `"haiku"`)
- `spec.kind` — a custom domain field (e.g. `"text/haiku"`)

These are redundant. The spec's own examples use plain strings like `"haiku"` and `"petition-draft"` for `kind`, which would make it identical to `metadata.name`. Worse, the current manifests use MIME-style values (`"text/haiku"`) for `spec.kind` which contradicts the spec examples and creates a confusing split identity — consumers don't know which value to use where.

In practice, `Spec.Kind` is **never read by any Go code**. The operator reconciler is an empty stub. Contract keys, capability strings, law scoping, and all other consumers use plain string values that are meant to identify the GovernedArtefact type — but nothing currently cross-references or validates these against `spec.kind`.

The fix: delete `spec.kind`. The `metadata.name` is the sole identifier for a governed artefact type. The proto `kind` field (which associates artefact instances with their governed type) gets renamed to `governed_artefact` for clarity, and its values become GovernedArtefact names (e.g. `"haiku"` not `"text/haiku"`).

## Phases

### Phase 1 — CRD & Operator

1.1. `operator/api/v1/governedartefact_types.go`
  - Remove the `Kind` field from `GovernedArtefactSpec`
  - Update struct comments

1.2. `operator/internal/controller/governedartefact_controller_test.go`
  - Remove `Spec.Kind: "test-kind"` from test fixture

1.3. `operator/config/crd/bases/flow.gideas.io_governedartefacts.yaml`
  - Regenerate (or manually remove `kind` property from spec schema, remove from `required`)

1.4. `operator/api/v1/zz_generated.deepcopy.go`
  - Regenerate (auto-generated, updates when struct changes)

1.5. `operator/api/v1/foundryflow_types.go`
  - Update comments on `Contract` type: "artefact kind" → "governed artefact name"

### Phase 2 — Proto field renames

The proto `kind` field still serves a purpose (associating an artefact instance with its governed type at the wire level) but should be renamed for clarity.

2.1. `proto/flow/v1/common.proto`
  - `ArtefactRef.kind` → `ArtefactRef.governed_artefact` (field 2)
  - `ArtefactState.kind` → `ArtefactState.governed_artefact` (field 2)

2.2. `proto/flow/v1/archivist.proto`
  - `QueryArtefactStateRequest.artefact_kinds` → `governed_artefacts` (field 2)
  - `GetArtefactResponse.kind` → `governed_artefact` (field 3)
  - `StoreArtefactRequest.kind` → `governed_artefact` (field 3)
  - `ListArtefacts` RPC comment: "id, kind" → "id, governed artefact"

2.3. `proto/flow/v1/librarian.proto`
  - `LawFilter.artefact_kind` → `governed_artefact` (field 1)
  - Update comments on lines 17-18

2.4. `proto/flow/v1/operator.proto`
  - Update comment on exit_contract: "artefact kind" → "governed artefact name"
  - Update comment on StampRequirements: same

2.5. Regenerate Go code from proto

### Phase 3 — SDK

3.1. `sdk/go/client.go`
  - `StoreArtefact` param: `kind` → `governedArtefact`
  - `QueryLaws` param: `kind` → `governedArtefact`
  - Update generated field references (`Kind:` → `GovernedArtefact:`)
  - Update comments

3.2. `sdk/go/client_test.go`
  - Update test values

### Phase 4 — Archivist

4.1. `archivist/internal/service/archivist_server.go`
  - Update `.Kind` → `.GovernedArtefact` (generated Go field name)
  - Update log keys

4.2. `archivist/internal/store/sqlite/store.go`
  - Update Go struct field names
  - Keep SQL column names as-is (avoids migration; they're an internal detail)

4.3. `archivist/internal/store/sqlite/store_test.go`
  - Update test references

### Phase 5 — Sidecar

5.1. `sidecar/internal/proxy/archivist.go`
  - Update `Kind:` → `GovernedArtefact:`

5.2. `sidecar/internal/mock/handlers.go` + `handlers_test.go`
  - Update field references

5.3. `sidecar/internal/proxy/archivist_test.go`
  - Update test values

### Phase 6 — Librarian

6.1. `librarian/internal/store/sqlite/store.go`
  - `ArtefactKind` field in `QueryFilter` → `GovernedArtefact`
  - Keep SQL column names as-is

6.2. `librarian/internal/service/librarian_server.go`
  - Update `.GetArtefactKind()` → `.GetGovernedArtefact()`
  - Update log keys and error messages

6.3. Tests in both packages

### Phase 7 — Node code

7.1. `nodes/null-node/main.go`
  - Update `StoreArtefact` call (second arg is now a GovernedArtefact name)

7.2. `nodes/haiku-forge/main.go`
  - `StoreArtefact(ctx, "haiku", "text/haiku", ...)` → `StoreArtefact(ctx, "haiku", "haiku", ...)`
  - `QueryLaws(ctx, "text/haiku", "")` → `QueryLaws(ctx, "haiku", "")`

7.3. `nodes/haiku-refine/main.go`, `nodes/haiku-appraise/main.go`
  - Same pattern: `"text/haiku"` → `"haiku"` wherever passed as the governed artefact

7.4. `nodes/haiku-sort/main.go`
  - Already generic — uses exit contract map key dynamically. No production code changes.

7.5. `nodes/haiku-sort/testutil_test.go`, `nodes/haiku-sort/main_test.go`
  - Verify test capability strings (already use `"haiku"` not `"text/haiku"`)

### Phase 8 — Manifests

8.1. `nodes/haiku-manifests/flow.yaml`
  - Remove `spec.kind` from both GovernedArtefact CRs
  - Update contract keys: `"text/haiku"` → `"haiku"`, `"text/petition"` → `"petition"`
  - Update capability strings: `"READ:artefact/text/haiku"` → `"READ:artefact/haiku"`, etc.
  - Update comments

8.2. `nodes/haiku-manifests/workitem.yaml`
  - Update comment referencing `"text/petition"`

### Phase 9 — Demo tools

9.1. `tools/demo/add-law`
  - `"text/haiku"` → `"haiku"` throughout

9.2. `tools/demo/new-haiku`
  - `"text/petition"` → `"petition"`

### Phase 10 — Specs

Update all spec files that reference `spec.kind` or "artefact kind" in the GovernedArtefact context. Key files:

- `specs/05-reference/crds.md` — Remove `kind` from GovernedArtefact spec table, update all references
- `specs/05-reference/grpc-api.md` — Update API field names (`kind` → `governed_artefact`)
- `specs/04-sdk/02-sdk-artefacts.md` — Update `StoreArtefact` signature, identity table, invariants
- `specs/01-concepts/03-data-model.md` — Update core conceptual definition, YAML examples, contract semantics
- `specs/05-reference/error-catalogue.md` — Update `ARTEFACT_KIND_CONFLICT` error
- `specs/05-reference/glossary.md` — Update glossary entries
- `specs/02-flow/05-configuration.md` — Update contract examples, capability syntax
- `specs/02-flow/01-operator.md` — Update contract validation semantics
- `specs/04-sdk/03-sdk-legal.md` — Update law query modes
- `specs/02-flow/03-nodes-external.md` — Update capability grants
- `specs/02-flow/04-system-services.md` — Update law scoping
- `specs/03-node/02-configuration.md` — Update capability syntax
- `specs/02-flow/02-workitem.md` — Update contract keying
- `specs/01-concepts/02-foundry-cycle.md` — Update law filtering references
- `specs/01-concepts/00-overview.md` — Update overview
- `specs/02-flow/00-overview.md` — Update overview
- `specs/02-flow/06-cross-flow.md` — Update export eligibility
- `specs/04-sdk/01-sdk-core.md` — Update export scoping

### Phase 11 — Quality gates

- Run `go test ./...` across operator, archivist, librarian, sidecar, sdk, nodes
- Run `make check-fix` for linting
- Regenerate auto-generated code (CRD manifests, proto, deepcopy)

## Out of scope

- **Legacy files** (`legacy/`) — Historical. No updates.
- **Node directory renames** (`haiku-sort` → `sort`, etc.) — Separate task (node genericisation).
- **Archivist/Librarian SQL column renames** — Keep internal column names to avoid migrations. Only update Go struct fields.
