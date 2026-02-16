# AGENTS.md

## Project

This repository contains the technical specification and reference implementation for **Foundry Flow** — a governed workflow runtime on Kubernetes.

## Repository Structure

### Documentation (`/specs`)

The authoritative source of truth for the system design.

/specs
├── 01-concepts/        # Helicopter view — read first
├── 02-flow/            # The Platform — assumes nodes exist
├── 03-node/            # Building Nodes — internal runtime architecture
├── 04-sdk/             # SDK — external developer interface
└── 05-reference/       # CRDs, APIs, Errors, Glossary

### Implementation (Source Code)

The "Walking Skeleton" and reference components.

/
├── operator/           # The Control Plane (Kubebuilder Controller)
├── sidecar/            # The Data Plane (Runtime Host & Proxy)
├── sdk/                # Node Development Kits
│   └── go/             # Go SDK Core
├── nodes/              # Standard Node Implementations
│   └── null-node/      # Verification Node (Phase 1)
├── proto/              # Protocol Buffer Definitions (The Contract)
├── charts/             # Helm Charts for deployment
└── tools/              # Maintenance and build scripts (e.g., linting)

## Reading Order

1. **Concepts** (`specs/01-concepts`) — What Foundry Flow is and why it exists.
2. **Architecture** (`specs/01-concepts/01-architecture.md`) — The Six-Plane Model.
3. **The Contract** (`proto/`) — The wire protocol that binds the components.
4. **Implementation** — The code in `operator`, `sidecar`, and `sdk`.

## Foundational Axioms

1. **Assume Unreliability** — All agents are fallible. Trust intent, verify execution.
2. **Make Work Auditable** — Every action becomes an immutable, traceable record.
3. **Make the Cost Visible** — Friction is a first-class, quantifiable signal.
4. **Quality is Fixed, Cost is Variable** — The standard is non-negotiable; the system measures the cost of achieving it.
