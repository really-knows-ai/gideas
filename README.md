# Foundry Flow

Foundry Flow is a governed workflow runtime on Kubernetes that orchestrates work through adversarial cycles of creation, validation, review, and refinement. A Flow is a sovereign runtime in a single Kubernetes namespace — all state, storage, governance, and execution live within the boundary. Artefacts produced by a Flow carry cryptographic proof of every governance checkpoint they passed, and structured feedback drives iterative refinement until exit contracts are satisfied. Friction — a first-class, quantifiable signal — makes the real-time cost of governance visible at every layer.

## Concepts

Foundry Flow operates through sovereign [Flows](specs/01-concepts/00-overview.md) that manage the lifecycle of [Workitems](specs/01-concepts/03-data-model.md) and their associated [Artefacts](specs/01-concepts/03-data-model.md). The system is built on [architectural planes](specs/01-concepts/01-architecture.md) that separate concerns between control, data, and governance. Work progresses through the [Foundry Cycle](specs/01-concepts/02-foundry-cycle.md) — an adversarial arrangement of nodes (Forge, Quench, Appraise, Sort, Refine) that use [Feedback](specs/01-concepts/03-data-model.md) and [Friction](specs/01-concepts/04-governance.md) to drive quality. All execution is governed by a multi-tiered [Legal Model](specs/01-concepts/04-governance.md) that uses laws, precedent, and judicial review to ensure compliance and auditability.

## Flow Runtime

The Flow runtime defines the platform: how the control plane drives work, how services manage artefact and law lifecycle, and how Flows collaborate across boundaries. Audience: operators and administrators.

| Document | Description |
|----------|-------------|
| [Overview](specs/02-flow/00-overview.md) | Runtime composition, execution loop, and platform invariants |
| [Operator](specs/02-flow/01-operator.md) | Control-plane authority — reconciliation, assignment, routing, exit enforcement |
| [Workitems](specs/02-flow/02-workitem.md) | Workitem lifecycle states, ownership boundaries, and contract interactions |
| [Nodes and External Integrations](specs/02-flow/03-nodes-external.md) | Node execution boundaries, capability model, and Assay as standard component |
| [System Services](specs/02-flow/04-system-services.md) | Librarian, Archivist, Flow Monitor, Flow Support Services, hearing protocol |
| [Configuration](specs/02-flow/05-configuration.md) | CRD authority model, topology, contracts, capability grants, operational knobs |
| [Cross-Flow Collaboration](specs/02-flow/06-cross-flow.md) | Export and import lifecycle, trust topologies, treaties, law integration |
| [Operations](specs/02-flow/07-operations.md) | Monitoring, triage, recovery, upgrade, and operational verification |

## Node Runtime

Nodes execute assignment-scoped work inside a Flow while the Operator retains control-plane authority. Audience: platform engineers and node implementors.

| Document | Description |
|----------|-------------|
| [Overview](specs/03-node/00-overview.md) | Node execution boundary and assignment lifecycle |
| [Sidecar](specs/03-node/01-sidecar.md) | Trust boundary — identity mediation, service brokering, capability enforcement |
| [Configuration](specs/03-node/02-configuration.md) | Configuration precedence, capability resolution, timeout and concurrency |
| [Patterns](specs/03-node/03-patterns.md) | Implementation guidance — idempotency, retries, HITL, agents, anti-patterns |

## SDK

The SDK is the programming interface between node handler code and the Flow runtime. All operations pass through the Sidecar. Audience: node developers.

| Document | Description |
|----------|-------------|
| [Overview](specs/04-sdk/00-overview.md) | SDK role, execution scope, and FlowSupportService base class |
| [Core](specs/04-sdk/01-sdk-core.md) | Handler lifecycle, routing instructions, completion semantics, error taxonomy |
| [Artefacts](specs/04-sdk/02-sdk-artefacts.md) | Read, write, versioning, and stamp operations for governed artefacts |
| [Legal](specs/04-sdk/03-sdk-legal.md) | Law retrieval, citation, and finding creation |
| [Feedback](specs/04-sdk/04-sdk-feedback.md) | Feedback lifecycle, friction emission, deadlock, and contempt guard |
| [Workitems](specs/04-sdk/05-sdk-workitems.md) | Workitem read access, local creation, and routing submission |
| [Telemetry](specs/04-sdk/06-sdk-telemetry.md) | Friction emission, metrics, traces, and custom events |
| [Agent](specs/04-sdk/07-sdk-agent.md) | Managed inference wrapper: FoundryAgent heartbeat, output validation, cost accounting |

## Reference

Quick lookup for implementors across all roles.

| Document | Description |
|----------|-------------|
| [CRDs](specs/05-reference/crds.md) | All custom resources under `flow.gideas.io/v1` with full field schemas |
| [gRPC API](specs/05-reference/grpc-api.md) | All runtime service APIs with method signatures |
| [Error Catalogue](specs/05-reference/error-catalogue.md) | Structured error codes, categories, and caller response guidance |
| [Glossary](specs/05-reference/glossary.md) | Canonical term definitions organised by domain |
