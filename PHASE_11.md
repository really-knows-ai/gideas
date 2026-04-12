# Phase 11 - Embassy Foundations (CRDs, Proto, Operator, SDK)

Status: complete and reviewed.

This phase lays down the Embassy-side runtime contract and configuration model.

## 11.1 Flow and Treaty Configuration (Embassy)

- `platform/operator/api/v1/foundryflow_types.go`
  - Replace `ImportNode string` with `CrossFlow.ImportTypes map[string]ImportTypeSpec`.
  - Add `ImportTypeSpec` with `node` and `requireForeignStamps`.
  - Treat `crossFlow.importTypes` as the flow-authored/custom extension set only.
  - Reject any attempt to define built-in system import types (for example `law-petition`) in YAML.
- `platform/operator/api/v1/treaty_types.go`
  - Keep or add `AllowedImportTypes []string` for non-federation exchange.
- Regenerate DeepCopy/manifests and update config/samples.
- Validation rules:
  - every mapped `node` must exist and be entry-bound,
  - built-in system import types must not be rebound or user-defined,
  - Treaty `allowedImportTypes` values must reference valid effective import
    types on the receiving Flow (built-in system or flow-authored).

## 11.2 Embassy Wire Protocol

- Create `proto/flow/v1/embassy.proto` for the Embassy manifest/package
  protocol.
- Define:
  - signed manifest/header message,
  - per-artefact inventory and foreign stamp records,
  - accept/reject response,
  - streamed package upload/download.
- Remove or replace the current operator-centric `ExportWorkitem` /
  `ImportWorkitem` shape in `proto/flow/v1/operator.proto` if it no longer
  matches the Embassy design.
- Regenerate `gen/flow/v1/`.

## 11.3 Operator and SDK Support (Embassy)

- Operator:
  - auto-provision an Embassy node/service for every Flow,
  - validate `crossFlow.importTypes`,
  - project platform-owned import-type/system config to Embassy,
  - stop treating `importNode` as the sole import admission mechanism.
- SDK:
  - extend entry-node support for Embassy staging/materialisation helpers,
  - add Embassy-specific client/server helpers for manifest verification and
    package transfer,
  - preserve Sidecar as the local API path for Workitem/Artefact operations
    after import.

## 11.4 Trust Enforcement Parity

- Enforce the same Embassy protocol for federation-member exchange and Treaty
  exchange.
- Federation-member crossings use federation root trust and
  role/relationship policy.
- Treaty crossings additionally enforce:
  - `allowedSubjects`,
  - `maxBundleSize`,
  - `allowedImportTypes`.
- Effective import-type resolution for both topologies must use the merged set
  of built-in system import types and flow-authored `crossFlow.importTypes`.
