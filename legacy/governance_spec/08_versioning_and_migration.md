# Versioning and Migration Strategy

> Status: Draft Implementation Contract

This document defines API versioning, migration paths, and deprecation policy for Foundry Flow.

## Canonical API Version
- Current canonical CRD `apiVersion`: `flow.gideas.io/v1`
- Future `v2` introduces federation primitives and mTLS-first authentication.
- All examples in the spec default to `v1` unless explicitly marked.

## Compatibility Policy
- Minor changes are backward compatible within a major version.
- Breaking changes require a new `apiVersion` and a migration path.
- Deprecation window: one minor release cycle after `vNext` GA.

## Version Differences (Summary)
- `v1`: Atomic flows, ServiceAccount auth, Treaties via Helm values.
- `v2`: Federated flows, Governor-issued certificates, Treaties as CRDs, expanded RBAC.

## Migration Guidance
1. Inventory CRDs and ensure they declare `flow.gideas.io/v1`.
2. Adopt new fields behind `featureGates` when preparing for `v2`.
3. Use dual-write strategies during migration windows where applicable.
4. Validate sidecar and system service compatibility via contract tests.

## CRD Upgrade Procedure
- Provide conversion webhooks for `v1`→`v2` when schemas differ.
- Maintain OpenAPI schemas with default values to support safe rollouts.

## Proto/API Versioning
- gRPC packages include version suffixes (e.g., `foundry.sidecar.v1`).
- New RPCs are added as optional; removing or changing semantics requires a new package.

## Deprecation Policy
- Mark deprecated fields with documentation banners and ignore them in controllers.
- Remove deprecated fields only after the deprecation window.

## References
- See node_spec/sdk/proto packages for `*.v1` namespaces.
- See governance_spec/01_certificate_authority.md for `v2` mTLS policy.
