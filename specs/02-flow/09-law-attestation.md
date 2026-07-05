# Law Attestation Semantics

## Overview

Law attestation is the mechanism by which the platform enforces that every applicable law has been evaluated before a governed artefact can exit the Flow. Laws with `appliesTo` matching an artefact kind become implicit completion requirements — the platform computes the required stamp set from the Librarian at completion time, without any exit contract changes.

## How `appliesTo` Creates Implicit Exit Contract Requirements

When a [Law](../01-concepts/03-data-model.md#laws) CRD has `appliesTo: ["haiku"]`, that law becomes an implicit completion requirement for any Flow producing the `haiku` GovernedArtefact. No changes to the exit contract are needed — the requirement is automatic.

At completion time, the operator's `handleComplete` calls `VerifyLawAttestations` against the governed artefact kind:

1. Query the Librarian for laws whose `appliesTo` includes the governed artefact kind (plus global laws with empty `appliesTo`).
2. For each applicable law, determine its group via `ListLawGroups`.
3. Compute the required attestation stamps based on the group mode (bundle vs law-by-law).
4. Verify that all required stamps exist on the artefact's passport.

If any attestation stamp is missing, `Complete()` is rejected with a guard error. This is the platform-level invariant that makes law enforcement automatic and zero-config.

## Bundle vs Law-by-Law Stamp Granularity

Group mode determines how attestation stamps are produced:

### Bundle Mode (`mode: bundle`)

All laws in the group are evaluated as a single unit. A single `lawgrp-<group>` stamp covers the entire group. No per-law or per-representation stamps are produced.

```
Required stamps for artefact: [lawgrp-content]
```

A bundle mode group is attested when the `lawgrp-<group>` stamp exists. The platform does not check individual law stamps because bundle mode attests that the group as a whole was evaluated, not that every law was individually verified.

### Law-by-Law Mode (`mode: law-by-law`)

Each law in the group is evaluated independently. Each passing law's representation type produces a `law-<lawID>-<type>` stamp. Once all laws in the group have passed, the group produces an aggregate `lawgrp-<group>` stamp.

```
Required stamps for artefact (single law "no-weather", rep type text/markdown):
  - law-no-weather-text-markdown
  - lawgrp-default
```

The aggregate `lawgrp-<group>` stamp gives `VerifyLawAttestations` a fast path (check group stamp exists) and a granular path (check per-law stamps). Sort can use either: the group stamp to confirm all laws are attested, or individual stamps to determine which specific law is still missing.

```
Required stamps for artefact (multi-rep law "syllable", law-by-law group "content"):
  - law-syllable-text-markdown
  - law-syllable-application-rego
  - lawgrp-content
```

## Representation-Level Attestation

A law with multiple representations (e.g., `text/markdown` + `application/rego`) requires an individual stamp per representation. The stamp name encodes the MIME type with `/` replaced by `-`:

| MIME type | Stamp suffix |
|-----------|-------------|
| `text/markdown` | `text-markdown` |
| `application/rego` | `application-rego` |
| `text/plain` | `text-plain` |

For a law with ID `syllable` and representations `text/markdown` + `application/rego`:

```
law-syllable-text-markdown
law-syllable-application-rego
```

A law is attested when all its `law-<lawID>-<type>` stamps exist. There is no aggregate `law-<lawID>` stamp — the platform derives coverage from the individual representation stamps.

## Vocabulary Wildcards vs Sort Completion Gating

The `law-*` wildcard in the GovernedArtefact stamp vocabulary is an **Archivist write-time acceptance mechanism** — it tells the Archivist to accept any stamp matching `law-*` when a node writes a stamp via `StampArtefact`. It is NOT a Sort completion gate.

Sort (and the operator's `handleComplete`) gates completion on the **actual attestation stamps derived from applicable laws**, not on the vocabulary wildcard. The vocabulary wildcard determines what can be written; the attestation computation determines what must exist.

```yaml
# GovernedArtefact vocabulary (what the Archivist accepts):
stamps:
  - appraisal
  - law-*        # Accepts any law-* stamp, but does not require any
  - approval

# Exit contract (what Sort checks — law stamps not listed):
exitContracts:
  standard-exit:
    haiku:
      - appraisal
      - approval
```

The attestation stamps are computed dynamically from `QueryLaws` + `ListLawGroups` — they are never listed in the exit contract. Adding a new law is zero-config for the exit contract.

## Canonical Computation Invariant

Both the operator's `validateLawAttestations` (in the scheduler's `handleComplete`) and the SDK's `VerifyLawAttestations` compute the required stamp set from the same two Librarian RPCs:

1. **`QueryLaws`** filtered by `governed_artefact` — returns applicable laws with their representations, each law's group, and all metadata.
2. **`ListLawGroups`** — returns group modes (bundle vs law-by-law).

The stamp set is a pure function of these inputs:

- For each applicable law, for each representation type: emit `law-<lawID>-<type>`.
- For each group in bundle mode: emit `lawgrp-<group>`.
- For each group in law-by-law mode with all laws passing: emit `lawgrp-<group>`.

Both callers compose these RPCs identically so they cannot diverge. No new Librarian RPC is needed.

```
LawQuerier.QueryLaws(ctx, "haiku")
  → [{ID: "no-weather", Group: "content", Representations: ["text/markdown"]},
     {ID: "no-atmosphere", Group: "content", Representations: ["text/markdown"]}]

LawQuerier.ListLawGroups(ctx)
  → [{Name: "content", Mode: "law-by-law"}]

Computed required stamps:
  - law-no-weather-text-markdown
  - law-no-atmosphere-text-markdown
  - lawgrp-content
```

This invariant guarantees that if `VerifyLawAttestations` returns an empty missing-stamp list during Sort routing, the operator's `validateLawAttestations` will also pass during `Complete()`. There is no divergence path.

## Attestation Lifecycle Summary

```
1. Appraisal evaluates laws for artefact
2. For each passing law (law-by-law mode):
     law.Attest(artefact, repType)       → stamps law-<id>-<type>
3. When all laws in group pass:
     group.Attest(artefact)              → stamps lawgrp-<group>
4. Appraisal stamps "appraisal"
5. Sort calls VerifyLawAttestations(kind) → returns missing stamps
6. If any missing: route to provider node
7. If all present: stamp "approval" → Complete()
8. Operator handleComplete calls VerifyLawAttestations → confirms
9. Artefact exits the Flow
```
