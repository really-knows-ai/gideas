# Assay Node Implementation

## Overview

The `assay` node is a generic judicial resolution mechanism for Foundry Flow that autonomously resolves deadlocked feedback disputes via jury deliberation and mints binding Tier 2 Rulings.

This is a **core built-in node**, not specific to any particular artefact type. It automatically discovers all artefacts in the workitem's exit contract and adjudicates the **first disputed feedback item** it finds across any artefact.

## Architecture

The node follows the standard Foundry Flow node pattern using `flow.Start()` with a push-based handler model, similar to `haiku-appraise` and `null-node`.

The artefact to adjudicate is **automatically discovered** by examining all artefacts in the workitem's exit contract and selecting the first one with a deadlocked feedback item.

### Key Components

1. **Handler Function**: Main entry point that processes workitem assignments
2. **Four-Phase Execution Model**:
   - Phase 1: Triage
   - Phase 2: Empanel
   - Phase 3: Deliberate
   - Phase 4: Execute

## Execution Flow

### Phase 1: Triage

- Gets the flow topology to discover all artefacts in the exit contract
- Searches each artefact for feedback items in `FEEDBACK_STATE_DEADLOCKED` state
- Selects the **first** deadlocked feedback item found (that artefact becomes the case)
- Fails fast if no deadlocked items exist across any artefact (nothing to adjudicate)

```go
topology, _ := client.GetFlowTopology(ctx)
exitContract := topology.GetExitContract()

// Search all artefacts for first deadlocked feedback
for artefactKind := range exitContract {
    feedback, _ := client.GetFeedback(ctx, artefactKind)
    for _, fb := range feedback {
        if fb.GetState() == flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED {
            // Found our case!
            disputedItem = fb
            targetArtefact = artefactKind
            break
        }
    }
}
```

### Phase 2: Empanel

Determines jury composition based on feedback severity:

| Severity | Jury Size | Consensus Threshold |
|----------|-----------|---------------------|
| LOW/MEDIUM | 3 jurors | >50% (Simple Majority) |
| HIGH | 5 jurors | ≥66% (Super Majority) |
| CRITICAL | 7 jurors | 100% (Unanimity) |

```go
jurySize := determineJurySize(item.GetSeverity())
threshold := determineConsensusThreshold(item.GetSeverity())
```

### Phase 3: Deliberate

- Runs up to `maxRounds` deliberation rounds (default: 3)
- Each round executes parallel LLM inference calls (one per juror)
- Uses FoundryAgent with JSON schema validation
- Tallies votes and checks for consensus
- Returns early if consensus reached

#### Deliberation Schema

```json
{
  "verdict": "resolve" | "reject" | "conflict",
  "reasoning": "explanation of vote",
  "suggested_statement": "optional proposed ruling"
}
```

**Verdicts**:
- `resolve`: Feedback should be resolved (fixed or accepted)
- `reject`: Feedback should be rejected (not applicable)
- `conflict`: Irreconcilable conflict requiring human intervention

### Phase 4: Execute

Based on jury verdict:

1. **resolve/reject**: Mint Tier 2 Ruling
   - Generate ruling statement from feedback discussion
   - Attempt codification (separate subjective/deterministic)
   - Mint law(s) via Librarian's `WriteLaw`
   - Resolve feedback item

2. **conflict**: Escalate to HITL
   - Route to "escalate" output
   - Human intervention required

## Codification Support

The node attempts to separate governance statements into:

1. **Subjective components** (text/markdown)
   - Qualitative rules requiring human/LLM judgment
   - Example: "Poetry must be beautiful and elegant"

2. **Deterministic components** (application/smt-lib)
   - Objective constraints expressible as formal logic
   - Example: `(assert (not (str.contains artefact-content "sausage")))`

### Codification Schema

```json
{
  "has_deterministic": true/false,
  "subjective": "subjective portion as markdown",
  "deterministic": "SMT-LIB constraints (optional)"
}
```

### Law Group Minting

When codification produces both subjective and deterministic components:

1. Mint subjective law (text/markdown representation)
2. Mint deterministic law (application/smt-lib representation)
3. Both laws share the same goal and apply to the same artefacts
4. Laws are conceptually linked (Spirit + Letter)

## Special Cases

### Retirement Hearings

Placeholder for reviewing expired Tier 2 Rulings:

```go
if isRetirementHearing(wctx) {
    return handleRetirementHearing(ctx, client, wctx)
}
```

Currently not fully implemented - would use heuristics based on:
- Days since last citation
- Citation frequency
- Usage patterns

### Hung Jury

If no consensus after `maxRounds`:

```go
return escalateToHITL(ctx, client)
```

Routes to "escalate" output for human intervention.

## Configuration

Environment variables:

- `OLLAMA_BASE_URL`: Ollama API endpoint (default: http://localhost:11434)
- `ASSAY_MODEL`: Model name (default: kimi-k2.5:cloud)
- `ASSAY_MAX_ROUNDS`: Maximum deliberation rounds (default: 3)

The node **automatically discovers** which artefact to adjudicate by searching all artefacts in the workitem's exit contract for the first deadlocked feedback item.

## Routing

The node defines two outputs:

1. **"resolved"**: Routes back to sender (default success path)
2. **"escalate"**: Routes to HITL node (hung jury)

## Testing

The test suite includes:

1. **Schema Validation Tests**
   - Deliberation schema (resolve/reject/conflict verdicts)
   - Codification schema (subjective/deterministic separation)

2. **Business Logic Tests**
   - Jury sizing based on severity
   - Consensus threshold calculation
   - Deadlocked feedback filtering
   - Ruling statement generation

3. **Utility Tests**
   - Environment variable handling
   - Helper functions

All 14 tests pass successfully.

## Dependencies

- `github.com/gideas/flow/gen/flow/v1` - Generated protobuf types
- `github.com/gideas/flow/sdk/go` - Foundry Flow Go SDK
- `github.com/gideas/flow/nodes/internal/ollama` - Ollama LLM client

## Integration

The node integrates with:

1. **Archivist**: Retrieve artefacts and feedback, resolve feedback
2. **Librarian**: Mint Tier 2 Rulings via `WriteLaw`
3. **Operator**: Submit routing instructions
4. **Monitor**: Record telemetry (via SDK)

## Future Enhancements

1. **Retirement Hearings**: Full implementation with citation analysis
2. **Law Grouping**: Explicit group ID field in proto (currently simulated)
3. **Reconsideration Rounds**: Allow jurors to see previous votes
4. **Custom Consensus Strategies**: Pluggable consensus mechanisms
5. **Batch Adjudication**: Process multiple disputes in single session

## References

- Specifications: `/legacy/flow_spec/09_assay_node.md`
- Execution Details: `/legacy/flow_spec/09a_assay_execution.md`
- Similar Nodes: `haiku-appraise`, `null-node`
