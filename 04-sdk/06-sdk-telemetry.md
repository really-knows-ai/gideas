# SDK Telemetry

## Goal

Define SDK telemetry and friction emission semantics for operational visibility and governance cost attribution.

## Telemetry Surface Overview

Describe metrics, traces, logs, and friction emission APIs available to handlers.

## Friction Emission Contract

Specify friction event shape, attribution requirements, and source tagging expectations.

## Operational Signal Quality

Define expectations for useful, low-noise instrumentation during normal and failure paths.

## Service and Sidecar Signal Relationship

Clarify that Sidecar telemetry is mediation-level observability, while authoritative mutation audit remains service-owned.

## Failure and Backpressure Behaviour

Describe handling when telemetry sinks are degraded without violating work execution semantics.

## Privacy and Data Minimisation Guidance

Set constraints for telemetry payload content to avoid leaking governed artefact data.

## Telemetry SDK Invariants

Capture non-negotiable observability guarantees and boundaries.
