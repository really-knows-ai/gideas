# Phase XX - Cleanup, Deletion, and Validation (Always Last)

This phase removes superseded cross-flow pieces, deletes `nodes/advocate/`,
removes stale Governance Flow assumptions, and runs the final quality gates for
the Embassy + Federation redesign.

#### XX.1 Remove Superseded Cross-Flow Pieces

- Delete `nodes/advocate/`.
- Remove any remaining stale routing/config that points at Advocate.
- Remove old `importNode` references from code, specs, samples, and generated
  validation.
- Remove or replace any obsolete operator-owned import/export implementation.
- Remove obsolete Governance Flow runtime terminology and any direct higher-tier
  law publication assumptions superseded by the Federation service.

#### XX.2 Quality Gates and Consistency Sweep

- `make test-all` -- all tests pass.
- `make check-fix-all` -- all lint/tidy clean.
- Spec lint (`tools/spec-lint/`) -- all specs clean.
- Verify no orphaned references remain to:
  - Advocate,
  - `importNode`,
  - Governance Flow as a runtime primitive,
  - direct-authority foreign stamp language that bypasses Embassy,
  - the old operator-centric import/export lifecycle,
  - the old higher-tier law propagation model that bypasses Federation service,
  - pending-decision Tier 1 laws (replaced by dispute records).
- Update `AGENTS.md`, samples, charts, and deployment docs for Embassy and the
  Federation service.
