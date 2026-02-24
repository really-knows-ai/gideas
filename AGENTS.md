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
├── platform/             # The Runtime Infrastructure
│   ├── operator/         # The Control Plane (Kubebuilder Controller)
│   ├── sidecar/          # The Data Plane (Runtime Host & Proxy)
│   ├── eventbus/         # Event Bus Service
│   ├── frictionledger/   # Friction Ledger Service
│   ├── monitor/          # Flow Monitor Service
│   ├── librarian/        # Librarian Service
│   ├── archivist/        # Archivist Service
│   ├── clerk/            # Clerk Service
│   └── pkg/eventbus/     # Shared event bus client library
├── sdk/                  # Node Development Kits
│   └── go/               # Go SDK Core (Provider abstraction, FoundryAgent)
├── nodes/                # Standard Node Implementations
│   ├── forge/            # Content Generation Node (concrete ForgeAgent)
│   ├── sort/             # Governance Triage Node
│   └── null-node/        # Verification Node (Phase 1)
├── gen/                  # Generated Protocol Buffer Code (The Contract)
├── proto/                # Protocol Buffer Definitions
├── charts/               # Helm Charts for deployment
└── tools/                # Dev/debug utilities (e.g., haiku-watch)

## Reading Order

1. **Concepts** (`specs/01-concepts`) — What Foundry Flow is and why it exists.
2. **Architecture** (`specs/01-concepts/01-architecture.md`) — The Six-Plane Model.
3. **The Contract** (`proto/`) — The wire protocol that binds the components.
4. **Implementation** — The code in `platform/operator`, `platform/sidecar`, and `sdk`.

## Quality Gates

All changes to this repository **must** pass the following before being committed:

1. **Tests** — Run `go test ./...` (or the relevant subset) and ensure all tests pass. New functionality requires new or updated tests.
2. **Lint** — Run `make check-fix` and resolve every issue it reports. Do not commit with lint failures.

These two steps are non-negotiable. A change without tests or with lint violations is incomplete.

## Foundational Axioms

1. **Assume Unreliability** — All agents are fallible. Trust intent, verify execution.
2. **Make Work Auditable** — Every action becomes an immutable, traceable record.
3. **Make the Cost Visible** — Friction is a first-class, quantifiable signal.
4. **Quality is Fixed, Cost is Variable** — The standard is non-negotiable; the system measures the cost of achieving it.

## Compatibility Policy

This is a greenfield project. There are no backward compatibility obligations. Breaking API changes are acceptable and preferred over accumulating backward-compat debt. Do not deprecate -- remove.
