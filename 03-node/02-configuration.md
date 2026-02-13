# Node Configuration Semantics

## Goal

- Define how [FoundryNode](../05-reference/crds.md) settings shape node runtime behaviour inside [FoundryFlow](../05-reference/crds.md) constraints.
- Keep node-level configuration aligned with Operator guard enforcement and service-side authorisation through Sidecar mediation.
- Document semantics only; leave field-level schema detail to the reference section.

## Configuration Surfaces and Precedence

- Flow-wide invariants and policy limits are defined in FoundryFlow.
- Node-local behaviour and permission envelope are defined in FoundryNode.
- Runtime resolution is performed by Operator reconciliation and service-side validation on Sidecar-mediated requests.
- Node configuration cannot override Flow-level invariants.

## Routing Outputs and Target Resolution

- `route_to_output` resolves output names from the assigned node's configured outputs.
- `route_to` resolves direct node targets from Flow topology.
- Unresolvable outputs or targets are rejected as invalid runtime instructions.
- Configuration must keep routes coherent so every declared target is resolvable.

## Capability Grants and Enforcement Implications

- Capability strings define what actions a node may request through SDK surfaces.
- Runtime services enforce grants using node identity presented through Sidecar.
- Stamp grants are exact by artefact kind and stamp name.
- Missing grants produce deterministic runtime denial for the attempted operation.
- Malformed or non-enforceable grant definitions are rejected at configuration admission.

## Entry and Exit Bindings

- `entry` binds a node to a named entry contract for Workitem admission paths.
- `exit` binds a node to a named exit contract and grants `complete()` eligibility.
- Only exit-bound nodes may complete; non-exit completion attempts are rejected.
- Contract selection is fixed by binding and not chosen dynamically by node code.
- Import and hearing paths must respect configured entry/exit bindings (`importNode`, Assay hearing bindings).

## Timeout and Execution Budget Semantics

- Node timeout budget bounds assignment execution windows.
- Timeout handling integrates with Operator failure policy and does not transfer lifecycle ownership to nodes.
- Timeout behaviour composes with retry/backoff policy and thrash guard enforcement.
- Long-running node patterns must preserve activity semantics within configured budgets.

## Validation and Rejection Scenarios

- Unknown contract bindings, missing routing targets, or invalid `importNode` references are rejected.
- Invalid capability formats or unsafe combinations are rejected or denied at runtime by policy.
- Configuration that would violate entry/exit invariants is rejected rather than partially applied.
- Rejection paths produce structured errors suitable for operations and audit.

## Rollout, Drift, and Runtime Behaviour

- Operator reconciliation is convergent and continuously enforces declared configuration.
- Configuration rollout must preserve processability for in-flight and queued Workitems.
- Drift between declared and observed runtime state is surfaced through reconciliation and telemetry signals.
- Behavioural changes are applied through configuration evolution, not ad hoc runtime mutation.

## Configuration Invariants

- Node behaviour remains subordinate to Flow-level invariants.
- Exit status is explicit through `exit` binding, not inferred from topology shape.
- Entry admission and exit completion remain contract-bound.
- Stamp authority remains capability-scoped and write-once per artefact version.
- Import intake always starts at configured `importNode` when admission succeeds.
- No configuration path reintroduces `WorkitemType`, `spec.type`, or context bag semantics.
