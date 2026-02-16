# AGENTS.md

## Project

This repository contains the technical specification for **Foundry Flow** — a governed workflow runtime on Kubernetes that orchestrates work through adversarial cycles of creation, validation, review, and refinement.

## Spec Structure

```text
/
├── AGENTS.md
├── README.md                    # Entry point, navigation (write last)
│
├── 01-concepts/                 # Helicopter view — read first
│   ├── 00-overview.md           # High-level introduction to Foundry Flow
│   ├── 01-architecture.md       # Six-plane architecture, design principles
│   ├── 02-foundry-cycle.md      # The Foundry Cycle reference arrangement
│   ├── 03-data-model.md         # Workitems, Artefacts, Laws, Feedback (detail)
│   └── 04-governance.md         # Law tiers, precedent, the legal metaphor (detail)
│
├── 02-flow/                     # The Platform — assumes nodes exist
│   ├── 00-overview.md
│   ├── 01-operator.md
│   ├── 02-workitem.md
│   ├── 03-nodes-external.md
│   ├── 04-system-services.md    # System services + Flow Support Services
│   ├── 05-configuration.md
│   ├── 06-cross-flow.md
│   └── 07-operations.md
│
├── 03-node/                     # Building Nodes — internal runtime architecture
│   ├── 00-overview.md
│   ├── 01-sidecar.md
│   ├── 02-configuration.md
│   └── 03-patterns.md
│
├── 04-sdk/                      # SDK — external developer interface
│   ├── 00-overview.md
│   ├── 01-sdk-core.md
│   ├── 02-sdk-artefacts.md
│   ├── 03-sdk-legal.md
│   ├── 04-sdk-feedback.md
│   ├── 05-sdk-workitems.md
│   └── 06-sdk-telemetry.md
│
├── 05-reference/                # Quick lookup
│   ├── crds.md
│   ├── grpc-api.md
│   ├── error-catalog.md
│   └── glossary.md
│
└── legacy/                      # Source material (read-only reference)
    ├── papers/                  # Foundational theory (5 files)
    ├── flow_spec/               # Legacy Flow runtime spec (~35 files)
    ├── node_spec/               # Legacy Node runtime spec (~18 files)
    ├── governance_spec/         # Legacy governance spec (~11 files)
    ├── crds/                    # Legacy CRD YAML definitions
    ├── PolymorphicLaw.md        # Polymorphic law envelope paper
    └── Tier5.md                 # 5-tier law hierarchy design rationale
```

### Reading Order

1. **Concepts** — What Foundry Flow is and why it exists
2. **Flow** — The platform (audience: operators and admins)
3. **Node** — Building runtime node architecture (audience: platform and node implementors)
4. **SDK** — Programming interfaces for node developers
5. **Reference** — Look things up

### Four foundational axioms

1. **Assume Unreliability** — All agents are fallible. Trust intent, verify execution.
2. **Make Work Auditable** — Every action becomes an immutable, traceable record.
3. **Make the Cost Visible** — Friction is a first-class, quantifiable signal.
4. **Quality is Fixed, Cost is Variable** — The standard is non-negotiable; the system measures the cost of achieving it.