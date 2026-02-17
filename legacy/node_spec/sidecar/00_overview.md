# Sidecar: Overview

> Status: Draft Implementation Contract

The Sidecar bridges node logic to the Foundry control plane. It manages session lifecycle, capability enforcement, and RPC transport to system services.

## Responsibilities
- Initialize session and bind to the node runtime.
- Enforce capabilities (e.g., `INSPECT:artefact/*`, `APPROVE:artefact/*`).
- Proxy artefact, legal, feedback, and telemetry operations.
- Handle `Export()` as a session terminator.

## Interfaces
- Node ↔ Sidecar: `foundry.sidecar.v1` on port 35697.
- Sidecar ↔ System Services: `foundry.system.v1` on port 35698.

See `01_lifecycle.md` for startup and shutdown details.
