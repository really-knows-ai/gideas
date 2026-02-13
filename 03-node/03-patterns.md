# Node Implementation Patterns

## Goal

- Provide implementation patterns that are correct under retries, reassignment, and strict runtime governance.
- Translate Flow and Sidecar invariants into practical handler-level guidance.
- Highlight anti-patterns that appear functional but violate control-plane or governance boundaries.

## Pattern Selection Principles

- Prefer deterministic behaviour at routing boundaries and side-effect boundaries.
- Preserve auditability: every material decision and mutation path should be reconstructable.
- Keep patterns capability-aware and assignment-scoped.
- Optimise for failure clarity over hidden recovery logic.

## Idempotent Assignment Handlers

- Rebuild execution context from Workitem + Archivist each assignment; do not depend on pod-local memory.
- Use stable artefact IDs for updates and treat unchanged content writes as no-op outcomes.
- Ensure duplicate invocation of the same handler does not create divergent control outcomes.
- Return a single routing instruction derived from explicit state checks.

## Retry-Safe External Side Effects

- Use idempotency keys anchored to Workitem and operation identity.
- Separate local state mutation intent from external commit acknowledgement.
- Map external response classes to deterministic route outcomes.
- Design retries so replay does not duplicate irreversible external effects.

## Governance-Safe Feedback and Refine Loops

- Treat non-`resolved` feedback as unresolved for gate logic.
- Handle deadlock as a distinct branch that escalates to Assay rather than normal refine routing.
- Implement Sort gate decision order exactly: unresolved non-deadlocked -> Refine, deadlocked -> Assay, missing stamps -> configured provider, all clear -> apply `approval` and complete.
- Preserve judicial finality after `linkedRuling` is set; avoid transitions that trigger contempt violations.
- Keep law and feedback interactions explicit and auditable.

## Human-in-the-Loop Pattern

- Model human decision points as explicit runtime states, not hidden thread-local waits.
- Preserve assignment ownership and timeout semantics while awaiting human input.
- Resume with deterministic routing based on recorded human outcome.
- Keep human intervention evidence attached to artefacts/feedback, not freeform context bags.

## External Integration Pattern

- Integrate external APIs behind clear request/response contracts and bounded retry policy.
- Preserve traceability across boundaries with explicit operation references and service audit events.
- Use export/import for cross-flow sovereignty transfer; do not simulate it with local routing shortcuts.
- Fail closed on ambiguous external outcomes that could compromise governance integrity.

## Anti-Patterns

- Hidden mutable cross-assignment state that changes semantic outcomes.
- Direct CRD field mutation assumptions in node code.
- Direct service calls that bypass Sidecar mediation and authentication.
- Hardcoding Sort stamp-provider targets instead of reading configuration.
- Treating stamp names as privileged platform keywords.

## Pattern Invariants Checklist

- Handler remains correct under replay, retry, and reassignment.
- External side effects are idempotent or safely deduplicated.
- Feedback-state handling preserves deadlock escalation and ruling finality.
- Routing decisions are deterministic, resolvable, and single-outcome.
- All node-originated operations remain Sidecar-mediated and service-authorised.
