# Judiciary Implementation Plan

## Scope

### In Scope

1. Proto definitions (Jury, Clerk, Archivist `LinkRuling`)
2. SDK client + Sidecar proxy integration
3. Jury service (standalone gRPC server with 5 juror agent types)
4. Clerk service (standalone gRPC server, `text/markdown` only)
5. Arbiter node (deadlock resolver)
6. Tribunal node (hearing conductor)
7. Advocate node (HITL, Tier 3 ceiling)

### Out of Scope (Deferred)

- `CodificationService` proto / `Encode` RPC (depends on FSS)
- Clerk codification service discovery/dispatch — Clerk drafts `text/markdown` prose directly; when CodificationServices land later, the Clerk gains discovery/dispatch logic without changing the API contract
- Advocate Tier 4-5 appeal to Governance Flow
- Operator auto-provisioning of Judiciary nodes

---

## Phase 1: Proto Definitions + Code Generation

### New File: `proto/flow/v1/jury.proto`

```protobuf
service JuryService {
  rpc Deliberate(DeliberateRequest) returns (DeliberateResponse);
}

message DeliberateRequest {
  string question;                        // What the jury is deliberating
  string evidence;                        // Structured markdown evidence bundle
  repeated string allowed_outcomes;       // Valid vote values
  ConsensusStrategy consensus_strategy;
  int32 max_rounds;
  int32 jury_size;                        // Number of jurors to empanel
}

message DeliberateResponse {
  string outcome;                         // Winning outcome (one of allowed_outcomes)
  repeated JurorJustification justifications;
  int32 rounds_used;
  bool hung;
}

message JurorJustification {
  string juror_id;
  string outcome;
  string reasoning;
}

enum ConsensusStrategy {
  CONSENSUS_STRATEGY_UNSPECIFIED = 0;
  CONSENSUS_STRATEGY_SIMPLE_MAJORITY = 1;   // >50%
  CONSENSUS_STRATEGY_SUPER_MAJORITY = 2;    // >=66%
  CONSENSUS_STRATEGY_UNANIMITY = 3;         // 100%
}
```

### New File: `proto/flow/v1/clerk.proto`

```protobuf
service ClerkService {
  rpc DraftLaw(DraftLawRequest) returns (DraftLawResponse);
}

message DraftLawRequest {
  DeliberateResponse verdict;
  string goal;
  int32 tier;
  repeated string applies_to;
}

message DraftLawResponse {
  string law_id;
  string version_hash;
  repeated Representation representations;
  repeated string codification_failures;  // Always empty initially; ready for CodificationService
}
```

### Modified: `proto/flow/v1/archivist.proto`

Add `LinkRuling` RPC to `ArchivistService`:

```protobuf
rpc LinkRuling(LinkRulingRequest) returns (LinkRulingResponse);

message LinkRulingRequest {
  string workitem_id = 1;
  string feedback_id = 2;
  string law_id = 3;
}

message LinkRulingResponse {
  FeedbackItem updated_item = 1;
}
```

### Run

`buf generate` to regenerate `gen/`.

---

## Phase 2: SDK + Sidecar Integration

### SDK Client (`sdk/go/client.go`)

Add to `Client` struct:

- `Jury flowv1.JuryServiceClient`
- `Clerk flowv1.ClerkServiceClient`

Initialize in `NewClient()` from the same sidecar connection.

New convenience methods:

- `Deliberate(ctx, question, evidence, allowedOutcomes, strategy, maxRounds, jurySize)` -> `(*flowv1.DeliberateResponse, error)`
- `DraftLaw(ctx, verdict, goal, tier, appliesTo)` -> `(*flowv1.DraftLawResponse, error)`
- `LinkRuling(ctx, feedbackID, lawID)` -> `(*flowv1.FeedbackItem, error)`
- `QueryFriction(ctx, filter)` -> `([]*flowv1.FrictionAggregate, error)` (existing proto, not yet in SDK)
- `GetLaw(ctx, lawID)` -> `(*flowv1.Law, error)` (existing proto, not yet in SDK)

### Sidecar Proxies (New Files)

- `sidecar/internal/proxy/jury.go` — `JuryProxy` passthrough
- `sidecar/internal/proxy/clerk.go` — `ClerkProxy` passthrough

### Sidecar Registration (`sidecar/cmd/main.go`)

- Add `JURY_ADDRESS` and `CLERK_ADDRESS` env vars
- Register `JuryProxy` and `ClerkProxy` (conditional, like Librarian)
- Add closers to graceful shutdown

### Archivist Updates

- `sidecar/internal/proxy/archivist.go` — add `LinkRuling` passthrough
- `archivist/internal/service/archivist_server.go` — implement `LinkRuling`:
  - Validate feedback exists
  - Validate feedback is in `DEADLOCKED` state
  - Set `linked_ruling` field
  - Enforce contempt guard: block if `linked_ruling` already set
  - Return updated feedback item
- Tests for `LinkRuling` including contempt guard enforcement

---

## Phase 3: Jury Service

### New Module: `jury/` (added to `go.work`)

```
jury/
├── cmd/
│   └── main.go                    # Port 50057, env JURY_PORT
├── internal/
│   ├── service/
│   │   ├── jury_server.go         # JuryServiceServer implementation
│   │   └── jury_server_test.go
│   ├── deliberation/
│   │   ├── engine.go              # Core deliberation engine
│   │   └── engine_test.go
│   └── jurors/
│       ├── juror.go               # Juror interface + shared schema/query template
│       ├── textualist.go          # Strict rule interpreter
│       ├── pragmatist.go          # Practical impact weigher
│       ├── conservator.go         # Stability/precedent advocate
│       ├── reformer.go            # Evolution/adaptation advocate
│       └── devils_advocate.go     # Adversarial challenger
├── go.mod
├── Dockerfile
└── deployment.yaml
```

### Juror Interface and Shared Contract (`juror.go`)

All 5 juror types implement a common interface:

```go
type Juror interface {
    Run(ctx context.Context, data JurorQueryData) (*JurorOutput, error)
    Name() string
}
```

Shared across all jurors:

- **Query template**: Renders `question`, `evidence`, `allowed_outcomes`, and optionally `peer_arguments` (for round 2+)
- **Output JSON Schema**: Built dynamically from `allowed_outcomes` — `{"outcome": "<enum of allowed>", "reasoning": "string"}`

Each juror type:

- Wraps a `*flow.Agent` (composition pattern, same as `ForgeAgent`/`EvalAgent`)
- Defines its own **system prompt** encoding its judicial philosophy
- Constructed with whatever `*flow.Model` the Jury service gives it (the juror doesn't choose its model)

### 5 Juror Personalities

| Juror | Philosophy | Tendency |
|-------|-----------|----------|
| **Textualist** | Strict interpretation of cited laws and evidence at face value | Favours the side with stronger legal citations |
| **Pragmatist** | Weighs practical impact and cost-effectiveness | Considers friction economics — favours outcomes that reduce future cost |
| **Conservator** | Favours stability and existing precedent | High bar for change; reluctant to promote, low bar to retire newer laws |
| **Reformer** | Favours evolution and improvement | More willing to promote, retire outdated laws, side with novel arguments |
| **Devil's Advocate** | Challenges the majority position, stress-tests reasoning | If evidence seems one-sided, pushes back; forces considered consensus |

### Deliberation Engine (`engine.go`)

1. Receive `DeliberateRequest` (question, evidence, allowed_outcomes, strategy, max_rounds, jury_size)
2. Build output JSON Schema dynamically from `allowed_outcomes`
3. Select `jury_size` jurors from the pool of 5 types (ensuring diversity — no duplicates if jury_size <= 5)
4. Construct each juror's `FoundryAgent` with the shared query template, shared schema, juror-specific system prompt, and its assigned `Model`
5. **Round 1**: Run all jurors in parallel (`errgroup`), collect structured votes (blind voting)
6. Count votes against consensus strategy
7. If consensus reached: synthesize outcome from majority, return `DeliberateResponse`
8. If hung + rounds remain under `max_rounds`: build "peer arguments" markdown from prior round's reasoning (anonymous — no juror IDs), augment query template, re-execute (**round 2+**)
9. If hung after `max_rounds`: return `hung=true`

### Consensus Counting

- `SIMPLE_MAJORITY`: most-voted outcome has >50% of votes
- `SUPER_MAJORITY`: most-voted outcome has >=66% of votes
- `UNANIMITY`: all votes are the same outcome

### Jury Service Deployment Config

```yaml
jurors:
  - name: "textualist"
    systemPrompt: "You are a strict legal textualist..."
  - name: "pragmatist"
    systemPrompt: "You are a pragmatic analyst..."
  - name: "conservator"
    systemPrompt: "You are a judicial conservator..."
  - name: "reformer"
    systemPrompt: "You are a judicial reformer..."
  - name: "devils-advocate"
    systemPrompt: "You are a devil's advocate..."
```

System prompts in the YAML are optional overrides — sensible defaults are baked into each juror's Go source file.

### Tests

- Consensus counting for all 3 strategies (edge cases: ties, exact thresholds)
- Multi-round deliberation (consensus on round 1, consensus on round 2, hung after max_rounds)
- Juror selection from pool (diversity, handling jury_size > pool size)
- Output schema validation (valid outcome, invalid outcome)

### Dependencies

`sdk/go` (for `Agent`, `Provider`, `Model`, `Client`), `gen/`

---

## Phase 4: Clerk Service

### New Module: `clerk/` (added to `go.work`)

```
clerk/
├── cmd/
│   └── main.go                    # Port 50058, env CLERK_PORT
├── internal/
│   └── service/
│       ├── clerk_server.go        # ClerkServiceServer implementation
│       └── clerk_server_test.go
├── go.mod
├── Dockerfile
└── deployment.yaml
```

### `DraftLaw` Handler (`clerk_server.go`)

1. Receive `DraftLawRequest` (verdict, goal, tier, applies_to)
2. Draft prose representation: format verdict justifications as `text/markdown` with:
   - Goal statement
   - Verdict outcome + reasoning synthesis
   - Per-juror reasoning summaries
3. Assemble `Law` object: goal, `[]{type: "text/markdown", content: prose}`, tier, applies_to
4. Call Librarian `WriteLaw` to persist
5. Return `law_id`, `version_hash`, `representations`, empty `codification_failures`

For retire verdicts: call Librarian `RetireLaw` (no prose drafting needed).

For demote verdicts: call Librarian `WriteLaw` with modified tier.

### Dependencies

`gen/`, outbound gRPC to Librarian (configured via `LIBRARIAN_ADDRESS` env).

No SQLite store (stateless). No SDK import (calls Librarian directly via gRPC client).

### Tests

- Prose drafting from verdict justifications
- `WriteLaw` integration (mock Librarian)
- Retire path
- Demote path
- Error handling (Librarian unavailable)

---

## Phase 5: Judiciary Nodes

### 5a. Arbiter Node (`nodes/arbiter/`)

```
nodes/arbiter/
├── main.go
├── main_test.go
└── testutil_test.go
```

#### Handler Logic

1. Get flow topology, find artefact kinds from exit contract
2. For each artefact kind, scan feedback for `DEADLOCKED` items
3. Gather case evidence and assemble evidence markdown bundle:
   - Feedback debate history (all events)
   - Artefact content excerpt (via `GetArtefact`)
   - Cited laws from both sides (via `QueryLaws`)
   - Friction cost summary (via `QueryFriction`)
4. Frame the question: "Should the reviewer's feedback be upheld, or should the refiner's refusal stand?"
5. Call `Deliberate` with `allowed_outcomes: ["favour_refiner", "favour_reviewer"]`
6. If `hung`: route to Advocate (`outputAdvocate`)
7. If verdict reached:
   - Call `DraftLaw` on Clerk to mint Tier 2 Ruling (goal synthesized from dispute)
   - Call `LinkRuling` on each deadlocked feedback item with the new `law_id`
   - The `LinkRuling` call atomically sets `linked_ruling`, enabling the contempt guard
8. Route back to Sort (`outputSort`)

#### Config (`nodeconfig` YAML)

- `consensusStrategy` (default: `SIMPLE_MAJORITY`)
- `maxRounds` (default: 3)
- `jurySize` (default: 5)

#### Outputs

`sort`, `advocate`

#### Tests

- Favour refiner flow (verdict -> Clerk -> LinkRuling -> route to Sort)
- Favour reviewer flow
- Hung jury escalation to Advocate
- Evidence bundle assembly
- Error propagation

### 5b. Tribunal Node (`nodes/tribunal/`)

```
nodes/tribunal/
├── main.go
├── main_test.go
└── testutil_test.go
```

#### Handler Logic

1. Read the `law-reference` artefact to get the law ID under review (via `GetArtefact`)
2. Call `GetLaw` on Librarian for the full law object
3. Call `QueryFriction` on Monitor for the law's accumulated friction data
4. Call `QueryLaws` on Librarian for related laws (context)
5. Assemble evidence markdown bundle (law goal, representations, friction summary, related laws)
6. Frame the question based on law tier:
   - Tier 1: "Should this Finding be promoted to a Ruling, or retired?"
   - Tier 2: "Should this Ruling be promoted to a Local Statute, retired, or demoted to a Finding?"
7. Call `Deliberate` with tier-appropriate `allowed_outcomes`:
   - Tier 1: `["promote", "retire"]`
   - Tier 2: `["promote", "retire", "demote"]`
8. If `hung`: route to Advocate
9. Based on verdict + tier:
   - **Tier 1 + promote**: Call `DraftLaw` on Clerk (tier=2), then `Complete`
   - **Tier 1 + retire**: Call Clerk to retire, then `Complete`
   - **Tier 2 + promote**: Route to Advocate for Tier 3 HITL ratification
   - **Tier 2 + retire**: Call Clerk to retire, then `Complete`
   - **Tier 2 + demote**: Call Clerk to demote (tier=1), then `Complete`

#### Config

Same as Arbiter (`consensusStrategy`, `maxRounds`, `jurySize`).

#### Outputs

`advocate`

#### Tests

- Tier 1 promote / retire flows
- Tier 2 promote (escalate to Advocate) / retire / demote flows
- Hung jury escalation
- Evidence bundle assembly from law + friction data

### 5c. Advocate Node (`nodes/advocate/`) — Full HITL

```
nodes/advocate/
├── main.go
├── main_test.go
└── testutil_test.go
```

#### Handler Logic (HITL Pattern)

1. Create `QueueManager`, pass to `flow.Start` via `WithQueueManager`
2. On assignment, determine escalation type from context:
   - **Hung jury from Arbiter**: deadlock dispute, human picks `favour_refiner` or `favour_reviewer`
   - **Hung jury from Tribunal**: hearing, human picks `promote`, `retire`, or `demote`
   - **Tier 3 proposal from Tribunal**: Tier 2 promote verdict, human ratifies or rejects
3. Build human-readable decision request with full context (verdict justifications, evidence summary, allowed choices)
4. `Enqueue` -> `PauseTimer` -> `WaitForDecision` -> `ResumeTimer`
5. Based on human decision:
   - **Hung jury (Arbiter)**: Call Clerk to mint Tier 2 Ruling per human's verdict, `LinkRuling` on feedback items, route back to Sort
   - **Hung jury (Tribunal)**: Call Clerk per human's verdict (promote/retire/demote), `Complete`
   - **Tier 3 ratification accepted**: Call Clerk to mint Tier 3 Local Statute, `Complete`
   - **Tier 3 ratification rejected**: `Complete` (conflicting statutes remain active, recurring friction)

#### Outputs

`sort` (for Arbiter-originated hung juries routed back after human verdict)

#### Deployment

`StatefulSet` with `Headless Service` (for Federated Queue Mesh), `PVC` for SQLite queue persistence — same pattern as hitl-sort/hitl-appraise.

#### Tests

- Hung jury (Arbiter) -> human favour_refiner -> Clerk + LinkRuling + route to Sort
- Hung jury (Tribunal) -> human promote -> Clerk + Complete
- Tier 3 ratification accept / reject
- Queue integration tests

---

## Phase 6: Build Integration

### Makefile Updates

- Add `test-jury`, `test-clerk` targets
- Add `build-jury`, `build-clerk` targets
- Add `jury/`, `clerk/` to lint and tidy paths
- Add to aggregate `test` and `build` targets

### go.work

Add `./jury` and `./clerk` modules.

---

## Execution Order

| Step | Work | Depends On |
|------|------|-----------|
| 1 | Proto definitions (jury.proto, clerk.proto, archivist.proto LinkRuling) | — |
| 2 | `buf generate` | Step 1 |
| 3 | SDK client (add Jury/Clerk/LinkRuling/QueryFriction/GetLaw) | Step 2 |
| 4 | Sidecar proxies (JuryProxy, ClerkProxy, LinkRuling passthrough) + registration | Step 2 |
| 5 | Archivist `LinkRuling` implementation + tests | Step 2 |
| 6 | Jury service (module, server, 5 jurors, deliberation engine, tests) | Steps 2-4 |
| 7 | Clerk service (module, server, prose drafting, tests) | Steps 2-4 |
| 8 | Arbiter node + tests | Steps 3-7 |
| 9 | Tribunal node + tests | Steps 3-7 |
| 10 | Advocate node (HITL) + tests | Steps 3-7 |
| 11 | Makefile + go.work updates | Steps 6-10 |
| 12 | `make check-fix` + `go test ./...` | All |

Steps 3-5 can run in parallel. Steps 6+7 can run in parallel. Steps 8+9+10 can run in parallel.

---

## Quality Gates

Per AGENTS.md, every phase:

1. `go test ./...` (relevant subset) — all tests pass
2. `make check-fix` — all lint issues resolved

---

## File Summary

| Phase | New Files | Modified Files |
|-------|-----------|----------------|
| 1. Proto | 2 new (jury.proto, clerk.proto) | archivist.proto, regenerated gen/ |
| 2. SDK + Sidecar | 2 new proxy files | client.go, sidecar/cmd/main.go, archivist proxy |
| 3. Archivist LinkRuling | — | archivist_server.go, archivist_server_test.go |
| 4. Jury | ~12 new (cmd, server, engine, 5 jurors, juror interface, go.mod, Dockerfile, deployment) | go.work, Makefile |
| 5. Clerk | ~6 new (cmd, server, tests, go.mod, Dockerfile, deployment) | go.work, Makefile |
| 6. Nodes | ~9 new (3 nodes x main.go + test + testutil) | — |
| **Total** | **~31 new files** | **~10 modified files** |

---

## Key Design Decisions

1. **Jury API is generic**: The Jury receives a `question` (string) and `evidence` (markdown bundle), not domain-specific structured data. The calling node (Arbiter/Tribunal) is responsible for framing the question and assembling evidence. This keeps the Jury a reusable deliberation engine with no Foundry-domain coupling.

2. **No confidence scores**: LLMs are poor at numerical confidence prediction. Confidence is reflected structurally — a hung jury indicates low confidence, unanimous verdict indicates high confidence. Per-juror justification reasoning provides qualitative signal.

3. **Juror diversity is internal to the Jury**: The caller specifies `jury_size` (how many jurors) but not who they are. The Jury service owns juror construction — 5 distinct FoundryAgent implementations with different judicial philosophies and potentially different models/providers.

4. **`allowed_outcomes` enables dynamic schema validation**: The caller declares valid vote values, and the Jury builds the output JSON Schema dynamically. This ensures juror votes are always valid outcomes without hardcoding domain-specific enums in the Jury proto.

5. **`LinkRuling` is a new Archivist RPC**: Atomically sets `linked_ruling` on a feedback item, enabling the contempt guard. This is cleaner and more auditable than extending existing feedback transition methods.

6. **Clerk drafts `text/markdown` only (initially)**: No CodificationService dispatch. The `codification_failures` field in the response is always empty but present in the proto, ready for when CodificationServices land.

7. **Advocate ceiling is Tier 3**: No Tier 4-5 Governance Flow appeals. The Advocate handles hung jury resolution and Tier 3 ratification only. Extension point exists but is not wired.

8. **FoundryAgent encapsulates model/provider**: The Jury's juror config does not expose model or provider — each juror is a concrete FoundryAgent implementation that internally owns its model/provider binding. This is consistent with how all other nodes use FoundryAgent.
