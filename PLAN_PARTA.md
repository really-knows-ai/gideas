# Part A: Agent/Model/Provider Refactor

## Overview

Refactor the SDK's Agent/Model/Provider hierarchy so that models encapsulate their provider, model selection is a code-time decision (not deploy-time config), and the provider layer is fully internal. This cleans up the abstraction before building the perspective fan-out Appraise in Part B.

## Design Principles

1. **The model is code, not config** -- prompts are intrinsically coupled to a specific model. Model choice belongs in source code, not ConfigMaps.
2. **The provider is encapsulated inside the model** -- consumers never see or touch providers. The concrete model type name encodes both model and provider (e.g. `KimiK2Ollama`).
3. **`Model` interface is minimal** -- just `Infer()`. No `ID()` method. The model identity flows through `CostMetadata.Model`, populated by the provider (the authoritative source for runtime cost data).
4. **Test injection via `OverrideModelForTest()`** -- a single exported SDK function. Concrete agents own their model internally; tests swap it out after construction.

## Design Decisions

1. **Model ID is not configurable** -- an agent's prompts are intrinsically linked to the model they were built and tested with. You cannot generally switch out a model and expect the same prompts to work. The model is code, not configuration.

2. **Agents that support multiple models expose that as a constrained choice** -- not all agents support multiple models. When one does, it's the agent's decision how to expose that (enum, separate constructors, etc.). The consumer picks from a validated set.

3. **Concrete agents create their own model internally** -- `ForgeAgent` creates `flow.NewGptOss120bOllama()` in its constructor. The caller never sees the model. This keeps internals safe -- injecting a model from outside is exposing an unsafe surface.

4. **Provider is fully internal** -- the `provider` interface and `ollamaProvider` struct are unexported. The `OLLAMA_BASE_URL` env var still works for infrastructure config (which endpoint to hit), handled internally by the provider.

5. **Model type name encodes model + provider** -- e.g. `GptOss120bOllama`, `KimiK2Ollama`. The same underlying model served by different providers (Ollama vs OpenRouter) would be different types because they have different cost profiles, API behaviours, and prompt formatting requirements.

6. **No `ID()` on Model interface** -- `CostMetadata.Model` (populated by the provider) already carries the model identity at runtime. Adding `ID()` to the interface would be redundant. The concrete type name tells you at code-read time; `CostMetadata.Model` tells you at runtime.

7. **Test injection is an exported escape hatch** -- `flow.OverrideModelForTest(agent, mock)` sets the model on an `Agent`. Named to make misuse in production code obvious. Works cross-package because it's exported, while the `model` field on `Agent` stays unexported.

## Current State

### What exists today

```
Agent (exported struct)
  model *Model (exported struct)
    id       string
    provider Provider (exported interface)
      OllamaProvider (exported struct, sole implementation)
```

- `Provider` is an exported interface with one method: `Infer(ctx, model, systemPrompt, queryPrompt)`
- `Model` is an exported struct binding a model ID string to a `Provider`
- Every LLM node manually wires: `NewOllamaProvider()` -> `NewModel(cfg.Model, provider)` -> `WithModel(model)`
- Model ID comes from each node's ConfigMap YAML (`model: "kimi-k2.5:cloud"`)

### Production models in use

| Model ID | Used By | Purpose |
|---|---|---|
| `gpt-oss:120b-cloud` | Forge, Refine | Content generation and revision |
| `kimi-k2.5:cloud` | Appraise (eval/review/finding), Jury | Evaluation and review |
| `qwen3-embedding:4b` | Librarian | Embedding (separate concern, not part of Agent layer) |

### Problems

- Model selection is a deploy-time ConfigMap concern, but prompts are code-time decisions coupled to specific models
- Every node handler has identical boilerplate: `NewOllamaProvider()` -> `NewModel()` -> `WithModel()`
- Provider is exported but there's only one implementation and consumers shouldn't touch it
- Injecting a model from outside an agent exposes internals in an unsafe way

## Target State

```
Model (exported interface -- just Infer)
  KimiK2Ollama (exported concrete type)
    ollamaProvider (unexported, created internally)
  GptOss120bOllama (exported concrete type)
    ollamaProvider (unexported, created internally)

Agent (exported struct)
  model Model (interface)

OverrideModelForTest(a *Agent, m Model) -- exported escape hatch
```

### New Model Interface

```go
// Model abstracts LLM inference. Concrete implementations encapsulate
// both the model identity and the transport backend (provider).
// Implementations must be safe for concurrent use.
type Model interface {
    Infer(ctx context.Context, systemPrompt string, queryPrompt []byte) (*InferOutput, error)
}
```

### Concrete Model Example

```go
// KimiK2Ollama is the Kimi K2.5 cloud model served via Ollama.
type KimiK2Ollama struct {
    p *ollamaProvider
}

func NewKimiK2Ollama() *KimiK2Ollama {
    return &KimiK2Ollama{p: newOllamaProvider()}
}

func (m *KimiK2Ollama) Infer(ctx context.Context, systemPrompt string, queryPrompt []byte) (*InferOutput, error) {
    return m.p.infer(ctx, "kimi-k2.5:cloud", systemPrompt, queryPrompt)
}
```

### Test Pattern

**Before:**
```go
mp := &mockProvider{output: &flow.InferOutput{Output: []byte(`...`), Cost: &flow.CostMetadata{Model: "test-model"}}}
model := flow.NewModel(cfg.Model, mp)
agent, err := NewForgeAgent(client, model, cfg)
```

**After:**
```go
mock := &mockModel{output: &flow.InferOutput{Output: []byte(`...`), Cost: &flow.CostMetadata{Model: "test-model"}}}
agent, err := NewForgeAgent(client, cfg)
flow.OverrideModelForTest(agent.agent, mock)
```

Where `mockModel` (defined per test package, unexported):
```go
type mockModel struct {
    output         *flow.InferOutput
    err            error
    capturedSystem string
    capturedQuery  []byte
}

func (m *mockModel) Infer(ctx context.Context, systemPrompt string, queryPrompt []byte) (*flow.InferOutput, error) {
    m.capturedSystem = systemPrompt
    m.capturedQuery = queryPrompt
    return m.output, m.err
}
```

The appraise and refine variants add `sync.Mutex` and `outputs []*flow.InferOutput` for multi-call support, matching their current `mockProvider` patterns.

## File-by-File Changes

### SDK (`sdk/go/`)

| File | Action | Details |
|---|---|---|
| `provider.go` | Refactor | Remove `Model` struct, `NewModel()`, exported `Provider` interface. Keep `InferOutput` and `CostMetadata` exported. Add unexported `provider` interface. |
| `provider_ollama.go` | Refactor | `OllamaProvider` -> `ollamaProvider`. `NewOllamaProvider()` -> `newOllamaProvider()`. `WithBaseURL`/`WithTimeout` -> `withBaseURL`/`withTimeout`. Method `Infer` -> `infer`. All unexported. |
| `model.go` | New | `Model` interface definition. `OverrideModelForTest()` function. |
| `model_kimi_k2_ollama.go` | New | `KimiK2Ollama` concrete type with `NewKimiK2Ollama()`. Encapsulates `ollamaProvider` with model ID `"kimi-k2.5:cloud"`. |
| `model_gpt_oss_ollama.go` | New | `GptOss120bOllama` concrete type with `NewGptOss120bOllama()`. Encapsulates `ollamaProvider` with model ID `"gpt-oss:120b-cloud"`. |
| `agent.go` | Update | `WithModel` accepts `Model` (interface, not `*Model`). `Agent.model` typed as `Model`. `agentConfig.model` typed as `Model`. Everything else unchanged -- `Run()` still calls `a.model.Infer()`, telemetry still reads `cost.Model`. |
| `agent_test.go` | Update | Replace `mockProvider` with `mockModel` implementing `Model`. Replace all `NewModel(testModel, mp)` -> `&mockModel{...}`. Update `newTestAgent` helper. ~19 mock instantiations, ~12 `NewModel` call sites. |
| `provider_ollama_test.go` | Update | Test `ollamaProvider` directly (same package, unexported access works). `NewOllamaProvider` -> `newOllamaProvider`, `WithBaseURL` -> `withBaseURL`, etc. ~8 test functions. |

### Forge (`nodes/forge/`)

| File | Action | Details |
|---|---|---|
| `main.go` | Update | Remove `Model string` from `forgeConfig`. Remove `provider := flow.NewOllamaProvider()` and `model := flow.NewModel(cfg.Model, provider)` lines. `NewForgeAgent` no longer receives a model. |
| `agent.go` | Update | `NewForgeAgent` no longer accepts `*flow.Model`. Creates `flow.NewGptOss120bOllama()` internally. Passes to `flow.NewAgent` via `flow.WithModel(...)`. |
| `main_test.go` | Update | Replace `mockProvider` definition with `mockModel`. Replace `flow.NewModel(cfg.Model, mp)` -> construct agent then `flow.OverrideModelForTest(agent.agent, mock)`. Remove `Model` from test config structs. ~3 mock instantiations, 1 `NewModel` call site. |

### Appraise (`nodes/appraise/`)

| File | Action | Details |
|---|---|---|
| `main.go` | Update | Remove `Model string` from `appraiseConfig`. Remove `provider := flow.NewOllamaProvider()` and `model := flow.NewModel(cfg.Model, provider)`. Remove `model` param from `buildAgent()` helper. Agent constructors no longer receive model. |
| `agent_eval.go` | Update | `NewEvalAgent` creates `flow.NewKimiK2Ollama()` internally. Remove `model` parameter. |
| `agent_review.go` | Update | `NewReviewAgent` creates `flow.NewKimiK2Ollama()` internally. Remove `model` parameter. |
| `agent_finding.go` | Update | `NewFindingAgent` creates `flow.NewKimiK2Ollama()` internally. Remove `model` parameter. |
| `testutil_test.go` | Update | Replace `mockProvider` with `mockModel` (including mutex and multi-call `outputs` support). Remove `Model` from `defaultTestConfig()`. |
| `main_test.go` | Update | `newTestEvalAgent`, `newTestReviewAgent`, `newTestFindingAgent` use `flow.OverrideModelForTest()` after construction. ~12 `NewModel` call sites, ~30 mock instantiations. |

### Refine (`nodes/refine/`)

| File | Action | Details |
|---|---|---|
| `main.go` | Update | Remove `Model string` from `refineConfig`. Remove provider/model creation. Remove model param from `buildAgent()`. |
| `agent_triage.go` | Update | Creates `flow.NewGptOss120bOllama()` internally. Remove `model` parameter. |
| `agent_revision.go` | Update | Creates `flow.NewGptOss120bOllama()` internally. Remove `model` parameter. |
| `testutil_test.go` | Update | Replace `mockProvider` with `mockModel` (with mutex and multi-call support). Remove `Model` from `defaultTestConfig()`. |
| `main_test.go` | Update | `newTestTriageAgent`, `newTestRevisionAgent` use `flow.OverrideModelForTest()`. ~16 `NewModel` call sites, ~28 mock instantiations. |

### Jury (`jury/`)

| File | Action | Details |
|---|---|---|
| `cmd/main.go` | Update | Remove `defaultModel` const, `envModel` const, `JURY_MODEL` env var reading, `flow.NewOllamaProvider()`, `flow.NewModel()`. Remove `model` from `service.NewDefaultFactory()` call. |
| `internal/jurors/juror.go` | Update | `NewBaseJuror` no longer accepts `*flow.Model`. Creates `flow.NewKimiK2Ollama()` internally. Passes to `flow.NewAgent` via `flow.WithModel()`. |
| `internal/jurors/textualist.go` | Update | Remove `model` parameter from `NewTextualist`. Remove `model` from `NewBaseJuror` call. |
| `internal/jurors/pragmatist.go` | Update | Same. |
| `internal/jurors/conservator.go` | Update | Same. |
| `internal/jurors/reformer.go` | Update | Same. |
| `internal/jurors/devils_advocate.go` | Update | Same. |
| `internal/service/jury_server.go` | Update | `DefaultFactory` removes `model *flow.Model` field. `NewDefaultFactory` removes `model` param. `createJuror` no longer passes model to constructors. |
| `deployment.yaml` | Update | Remove `JURY_MODEL` env var. |

### ConfigMaps

| File | Action | Details |
|---|---|---|
| `nodes/haiku-manifests/configmaps.yaml` | Update | Remove `model:` line from forge-config, appraise-config, refine-config sections. |

### Specs

| File | Action | Details |
|---|---|---|
| `specs/04-sdk/07-sdk-agent.md` | Update | Add section documenting Model interface, concrete model types, provider encapsulation, and the naming convention (ModelProvider). |

## Execution Order

1. SDK core: `model.go` (new interface + `OverrideModelForTest`)
2. SDK: refactor `provider.go` (unexport `Provider`, remove `Model` struct / `NewModel`)
3. SDK: refactor `provider_ollama.go` (unexport everything)
4. SDK: new concrete models (`model_kimi_k2_ollama.go`, `model_gpt_oss_ollama.go`)
5. SDK: update `agent.go` (`WithModel` takes interface)
6. SDK: update `agent_test.go` and `provider_ollama_test.go`
7. Forge: update agent, main, tests
8. Appraise: update agents, main, tests
9. Refine: update agents, main, tests
10. Jury: update jurors, server, main, deployment
11. ConfigMaps: remove model lines
12. Specs: update `07-sdk-agent.md`
13. Quality gates: `go test ./...`, `make check-fix`, `make lint-operator`

## Resolved Risks

1. **Test accessibility** -- Non-issue. All node test files are `package main` (same package as source), so they can access unexported fields like `agent.agent` on concrete agents to call `flow.OverrideModelForTest()`.

2. **`CostMetadata.Model`** -- Stays as-is, populated by the provider. No `ID()` on Model interface. No redundancy. The provider is the authoritative source for cost data including model identity.

3. **Jury ephemeral instances** -- Non-issue. `ollamaProvider` is a stateless HTTP client wrapper (`baseURL` + `*http.Client`). Creating N instances per deliberation (N = jury size) is negligible overhead.

## Quality Gates

All changes must pass before commit:

1. `go test ./...` -- all tests pass
2. `make check-fix` -- lint clean (2 pre-existing issues in `sdk/go/child_test.go` and `sdk/go/testutil_test.go` are known and accepted)
3. `make lint-operator` -- 0 issues
