# Spec Review

Deep review of all spec directories against AGENTS.md foundational axioms, writing principles, and cross-document consistency.

Reviewed directories: `01-concepts/`, `02-flow/`, `03-node/`, `04-sdk/`, `05-reference/`
Also reviewed: `README.md`

---

## Summary

| Severity | Count | Issues | Status |
|----------|-------|--------|--------|
| **Critical** | 1 | #1 | All resolved |
| **Significant** | 8 | #2-#7, #9, #14 | All resolved |
| **Minor** | 3 | #10-#12 | All resolved |
| **Informational** | 2 | #8, #13 | No fix needed |

All 14 issues identified and resolved:

1. README SDK table missing Agent document
2. Meta-commentary in gRPC API
3. "Three modes" planning-voice phrasing across three documents
4. `RecordFinding` `representations` parameter optionality inconsistency
5. Law Tier table inconsistency between overview and data model
6. Workitem state diagram trigger label divergence
7. Inconsistent FoundryNode CRD cross-reference targets (missing section anchors)
8. *(Informational)* `wont_fix` naming — confirmed consistent, no fix needed
9. `Cite` method capability enforcement path unclear — introduced `WRITE:friction`
10. `README.md` link paths missing `specs/` prefix
11. Ambiguous "Operators" terminology — replaced with "Flow Administrators"
12. Glossary `appliesTo` entry missing Data Model cross-reference
13. *(Informational)* `RecordFinding` return description — confirmed consistent, no fix needed
14. Codification Service discovery path clarification

### Terminology Consistency

All checked terms (Workitem, artefact, organisation, behaviour, Flow Operator, wont_fix, Governance Flow, Foundry Cycle, Sidecar, FoundryAgent, FlowSupportService, CodificationService, `law-reference`) are used consistently throughout the spec.

### Cross-Link Coverage

Cross-linking is thorough across core concepts, SDK documents, and the glossary. All identified gaps were resolved during this review.
