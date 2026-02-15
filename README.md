# Foundry Flow

Foundry Flow is a governed workflow runtime on Kubernetes that orchestrates work through adversarial cycles of creation, validation, review, and refinement. A Flow is a sovereign runtime in a single Kubernetes namespace — all state, storage, governance, and execution live within the boundary. Artefacts produced by a Flow carry cryptographic proof of every governance checkpoint they passed, and structured feedback drives iterative refinement until exit contracts are satisfied. Friction — a first-class, quantifiable signal — makes the real-time cost of governance visible at every layer.

## Concepts

The [Conceptual Overview](01-concepts/00-overview.md) introduces the core vocabulary: Flows, Workitems, nodes, artefacts, passports, stamps, feedback, and friction. [Architecture](01-concepts/01-architecture.md) defines the six-plane structure and responsibility boundaries that keep concerns separated. [The Foundry Cycle](01-concepts/02-foundry-cycle.md) describes the reference arrangement — the standard adversarial pattern of Forge, Quench, Appraise, Sort, and Refine. [Data Model](01-concepts/03-data-model.md) covers the four core data types in detail: Workitems, artefacts, feedback, and laws. [Governance](01-concepts/04-governance.md) establishes the legal metaphor — law tiers, precedent, judicial review, and friction as a governance signal.

## Flow Runtime

The Flow runtime defines the platform: how the control plane drives work, how services manage artefact and law lifecycle, and how Flows collaborate across boundaries. Audience: operators and administrators.

| Document | Description |
|----------|-------------|
| [Overview](02-flow/00-overview.md) | Runtime composition, execution loop, and platform invariants |
| [Operator](02-flow/01-operator.md) | Control-plane authority — reconciliation, assignment, routing, exit enforcement |
| [Workitems](02-flow/02-workitem.md) | Workitem lifecycle states, ownership boundaries, and contract interactions |
| [Nodes and External Integrations](02-flow/03-nodes-external.md) | Node execution boundaries, capability model, and Assay as standard component |
| [System Services](02-flow/04-system-services.md) | Librarian, Archivist, Flow Monitor, Flow Support Services, hearing protocol |
| [Configuration](02-flow/05-configuration.md) | CRD authority model, topology, contracts, capability grants, operational knobs |
| [Cross-Flow Collaboration](02-flow/06-cross-flow.md) | Export and import lifecycle, trust topologies, treaties, law integration |
| [Operations](02-flow/07-operations.md) | Monitoring, triage, recovery, upgrade, and operational verification |

## Node Runtime

Nodes execute assignment-scoped work inside a Flow while the Operator retains control-plane authority. Audience: platform engineers and node implementors.

| Document | Description |
|----------|-------------|
| [Overview](03-node/00-overview.md) | Node execution boundary and assignment lifecycle |
| [Sidecar](03-node/01-sidecar.md) | Trust boundary — identity mediation, service brokering, capability enforcement |
| [Configuration](03-node/02-configuration.md) | Configuration precedence, capability resolution, timeout and concurrency |
| [Patterns](03-node/03-patterns.md) | Implementation guidance — idempotency, retries, HITL, agents, anti-patterns |

## SDK

The SDK is the programming interface between node handler code and the Flow runtime. All operations pass through the Sidecar. Audience: node developers.

| Document | Description |
|----------|-------------|
| [Overview](04-sdk/00-overview.md) | SDK role, execution scope, and FlowSupportService base class |
| [Core](04-sdk/01-sdk-core.md) | Handler lifecycle, routing instructions, completion semantics, error taxonomy |
| [Artefacts](04-sdk/02-sdk-artefacts.md) | Read, write, versioning, and stamp operations for governed artefacts |
| [Legal](04-sdk/03-sdk-legal.md) | Law retrieval, citation, and finding creation |
| [Feedback](04-sdk/04-sdk-feedback.md) | Feedback lifecycle, friction emission, deadlock, and contempt guard |
| [Workitems](04-sdk/05-sdk-workitems.md) | Workitem read access, local creation, and routing submission |
| [Telemetry](04-sdk/06-sdk-telemetry.md) | Friction emission, metrics, traces, and custom events |

## Reference

Quick lookup for implementors across all roles.

| Document | Description |
|----------|-------------|
| [CRDs](05-reference/crds.md) | All custom resources under `flow.gideas.io/v1` with full field schemas |
| [gRPC API](05-reference/grpc-api.md) | All runtime service APIs with method signatures |
| [Error Catalogue](05-reference/error-catalog.md) | Structured error codes, categories, and caller response guidance |
| [Glossary](05-reference/glossary.md) | Canonical term definitions organised by domain |
