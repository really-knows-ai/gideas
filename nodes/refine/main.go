// Refine is the revision node of the Haiku Foundry Cycle.
//
// It reads the "petition" (creative brief), the current "haiku", applicable
// governance laws, and any unresolved feedback, then uses an LLM
// (gpt-oss:120b-cloud via Ollama) to decide how to handle each item and
// produce a revised haiku.
//
// Refine operates in two phases:
//
//  1. Per-Item Triage — For each NEW or REJECTED feedback item, a separate
//     FoundryAgent inference call decides whether to action (fix) or refuse
//     (won't fix) the item. These run in parallel, each with managed heartbeat
//     and cost telemetry. Refusals require a structured justification (law
//     citation or novel argument). If a REJECTED item has a linked ruling
//     (contempt guard), it is force-actioned without LLM inference.
//
//  2. Revision — A single FoundryAgent inference call takes the petition,
//     current haiku, applicable laws, and the actioned items from Phase 1
//     to produce a revised haiku addressing all committed fixes.
//
// If Phase 1 produces no actioned items (all feedback refused), Phase 2 is
// skipped — the existing haiku is stored unchanged and routed back to Sort.
//
// Always routes back to Sort for governance triage of the new version.
//
// Configuration is loaded from a ConfigMap-mounted YAML file:
//
//	inputArtefact:    "petition"
//	outputArtefact:   "haiku"
//	governedArtefact: "haiku"
//	outputField:      "haiku"
package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"text/template"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

// refineConfig holds the node's configuration, loaded from a
// ConfigMap-mounted YAML file via nodeconfig.Load.
type refineConfig struct {
	InputArtefact    string `yaml:"inputArtefact"`    // artefact ID for the creative brief (e.g. "petition")
	OutputArtefact   string `yaml:"outputArtefact"`   // artefact ID to revise and store back (e.g. "haiku")
	GovernedArtefact string `yaml:"governedArtefact"` // GovernedArtefact CR name (e.g. "haiku")
	OutputField      string `yaml:"outputField"`      // JSON key in revision output (e.g. "haiku")
}

const (
	decisionAction = "action"
	decisionRefuse = "refuse"

	justTypeCitation      = "citation"
	justTypeNovelArgument = "novel_argument"

	contemptMessage = "Complying with judicial ruling"
)

// actionedItem records a feedback item that Phase 1 decided to fix.
type actionedItem struct {
	FeedbackID string
	Message    string // the original feedback message
	FixDesc    string // what the LLM promised to fix
}

// ---------------------------------------------------------------------------
// Agent Construction Helper
// ---------------------------------------------------------------------------

// buildAgent is the shared construction pattern for the triage and revision
// agents. It renders the system prompt template, parses the query template,
// and creates a flow.Agent with schema, model (GptOss120bOllama), and prompts.
//
// The model is created internally — model choice is a code-time decision
// coupled to the prompts, not deploy-time config.
func buildAgent(
	client *flow.Client,
	name string,
	sysTmplStr string,
	sysData any,
	queryTmplStr string,
	schema []byte,
) (*flow.Agent, error) {
	// 1. Render system prompt with config params.
	sysTmpl, err := template.New("system").Parse(sysTmplStr)
	if err != nil {
		return nil, fmt.Errorf("%s: parse system template: %w", name, err)
	}

	var sysBuf bytes.Buffer
	if err := sysTmpl.Execute(&sysBuf, sysData); err != nil {
		return nil, fmt.Errorf("%s: render system prompt: %w", name, err)
	}

	// 2. Parse query template.
	queryTmpl, err := template.New("query").Parse(queryTmplStr)
	if err != nil {
		return nil, fmt.Errorf("%s: parse query template: %w", name, err)
	}

	// 3. Create flow.Agent with schema, model, prompts.
	agent, err := flow.NewAgent(client,
		flow.WithSchema(schema),
		flow.WithModel(flow.NewGptOss120bOllama()),
		flow.WithSystemPrompt(sysBuf.String()),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		return nil, fmt.Errorf("%s: create agent: %w", name, err)
	}

	return agent, nil
}

// ---------------------------------------------------------------------------
// Entrypoint
// ---------------------------------------------------------------------------

func main() {
	slog.Info("refine: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("refine: server failed", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("refine: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("refine: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	// Load configuration from ConfigMap-mounted YAML.
	cfg, err := nodeconfig.Load[refineConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("refine: load config: %w", err)
	}

	// Create agents (model is created internally by buildAgent).
	triageAgent, err := NewTriageAgent(client, cfg)
	if err != nil {
		return fmt.Errorf("refine: create triage agent: %w", err)
	}

	revisionAgent, err := NewRevisionAgent(client, cfg)
	if err != nil {
		return fmt.Errorf("refine: create revision agent: %w", err)
	}

	return handleRefine(ctx, client, triageAgent, revisionAgent, cfg)
}

// ---------------------------------------------------------------------------
// Core Orchestration
// ---------------------------------------------------------------------------

// handleRefine performs the 2-phase refine logic: triage feedback, then
// optionally revise the content.
func handleRefine(
	ctx context.Context,
	client *flow.Client,
	triage *TriageAgent,
	revision *RevisionAgent,
	cfg *refineConfig,
) error {
	// ---------------------------------------------------------------
	// Pre-inference: read artefacts, query laws, get existing feedback
	// ---------------------------------------------------------------

	inputResp, err := client.GetArtefact(ctx, cfg.InputArtefact)
	if err != nil {
		return fmt.Errorf("refine: read %s: %w", cfg.InputArtefact, err)
	}
	inputContent := string(inputResp.GetContent())

	outputResp, err := client.GetArtefact(ctx, cfg.OutputArtefact)
	if err != nil {
		return fmt.Errorf("refine: read %s: %w", cfg.OutputArtefact, err)
	}
	reviewContent := string(outputResp.GetContent())

	slog.Info("refine: context",
		"input_artefact", cfg.InputArtefact,
		"output_artefact", cfg.OutputArtefact,
	)

	laws, _ := client.QueryLaws(ctx, cfg.GovernedArtefact, "")

	feedbackItems, err := client.GetFeedback(ctx, cfg.GovernedArtefact)
	if err != nil {
		return fmt.Errorf("refine: get feedback: %w", err)
	}

	// ---------------------------------------------------------------
	// Phase 1: Per-item triage (parallel)
	// ---------------------------------------------------------------

	actionedItems, err := triageFeedback(ctx, triage, client,
		feedbackItems, inputContent, reviewContent, laws)
	if err != nil {
		return fmt.Errorf("refine: triage feedback: %w", err)
	}

	// ---------------------------------------------------------------
	// Phase 2: Revision — produce revised content addressing actioned items
	// ---------------------------------------------------------------

	var revised string
	if len(actionedItems) > 0 {
		revised, err = revision.Run(ctx, inputContent, reviewContent, laws, actionedItems)
		if err != nil {
			return fmt.Errorf("refine: revision run: %w", err)
		}
		slog.Info("refine: revised content", "content", revised)
	} else {
		// All feedback refused — store the existing content unchanged.
		revised = reviewContent
		slog.Info("refine: no actioned items — content unchanged")
	}

	// ---------------------------------------------------------------
	// Post-inference: store revised content and route back to Sort
	// ---------------------------------------------------------------

	storeResp, err := client.StoreArtefact(ctx, cfg.OutputArtefact, cfg.GovernedArtefact, []byte(revised))
	if err != nil {
		return fmt.Errorf("refine: store revised %s: %w", cfg.OutputArtefact, err)
	}
	slog.Info("refine: stored revised content",
		"artefact", cfg.OutputArtefact,
		"version_hash", storeResp.GetVersionHash(),
		"is_new_version", storeResp.GetIsNewVersion(),
	)

	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("refine: route to sort: %w", err)
	}

	slog.Info("refine: routed to sort",
		"workitem_id", os.Getenv(flow.EnvWorkitemID))
	return nil
}

// ---------------------------------------------------------------------------
// Phase 1: Parallel Per-Item Triage
// ---------------------------------------------------------------------------

// triageFeedback runs parallel LLM triage for NEW and REJECTED feedback items.
// Each item gets a focused inference call that decides action or refuse.
// Returns the list of items that were actioned (for Phase 2 context).
func triageFeedback(
	ctx context.Context,
	triage *TriageAgent,
	client *flow.Client,
	feedback []*flowv1.FeedbackItem,
	inputContent, reviewContent string,
	laws []*flowv1.Law,
) ([]actionedItem, error) {
	type triageTask struct {
		item          *flowv1.FeedbackItem
		forceActioned bool // contempt guard — skip LLM
	}

	var tasks []triageTask
	for _, fb := range feedback {
		state := fb.GetState()
		if state != flowv1.FeedbackState_FEEDBACK_STATE_NEW &&
			state != flowv1.FeedbackState_FEEDBACK_STATE_REJECTED {
			continue
		}

		// Contempt guard: linked ruling on a REJECTED item forces action.
		if fb.GetLinkedRuling() != "" && state == flowv1.FeedbackState_FEEDBACK_STATE_REJECTED {
			tasks = append(tasks, triageTask{
				item:          fb,
				forceActioned: true,
			})
			continue
		}

		tasks = append(tasks, triageTask{
			item: fb,
		})
	}

	if len(tasks) == 0 {
		slog.Info("refine: no feedback items to triage")
		return nil, nil
	}

	slog.Info("refine: triaging feedback items", "count", len(tasks))

	type triageResult struct {
		task triageTask
		out  triageOutput
		err  error
	}

	results := make([]triageResult, len(tasks))
	var wg sync.WaitGroup
	for i, task := range tasks {
		if task.forceActioned {
			results[i] = triageResult{
				task: task,
				out: triageOutput{
					Decision: decisionAction,
					Message:  contemptMessage,
				},
			}
			continue
		}

		wg.Add(1)
		go func(idx int, t triageTask) {
			defer wg.Done()
			out, err := triage.Run(ctx, t.item, inputContent, reviewContent, laws)
			if err != nil {
				results[idx] = triageResult{task: t, err: err}
				return
			}
			results[idx] = triageResult{task: t, out: *out}
		}(i, task)
	}
	wg.Wait()

	// Apply decisions sequentially (gRPC calls to Archivist).
	var actioned []actionedItem
	for _, r := range results {
		if r.err != nil {
			return nil, fmt.Errorf("refine: triage feedback %s: %w",
				r.task.item.GetId(), r.err)
		}

		fbID := r.task.item.GetId()

		switch r.out.Decision {
		case decisionAction:
			slog.Info("refine: actioning feedback",
				"feedback_id", fbID, "message", r.out.Message)
			if err := client.ResolveFeedback(ctx, fbID, r.out.Message); err != nil {
				return nil, fmt.Errorf("refine: resolve feedback %s: %w", fbID, err)
			}
			actioned = append(actioned, actionedItem{
				FeedbackID: fbID,
				Message:    r.task.item.GetMessage(),
				FixDesc:    r.out.Message,
			})

		case decisionRefuse:
			justification, err := buildJustification(r.out)
			if err != nil {
				return nil, fmt.Errorf("refine: build justification for %s: %w", fbID, err)
			}
			slog.Info("refine: refusing feedback",
				"feedback_id", fbID,
				"justification_type", r.out.JustificationType,
				"message", r.out.Message)
			if err := client.RefuseFeedback(ctx, fbID, justification); err != nil {
				return nil, fmt.Errorf("refine: refuse feedback %s: %w", fbID, err)
			}

		default:
			return nil, fmt.Errorf("refine: unexpected decision %q for feedback %s",
				r.out.Decision, fbID)
		}
	}

	return actioned, nil
}

// buildJustification converts the LLM's triage output into a proto
// Justification for the RefuseFeedback call.
func buildJustification(out triageOutput) (*flowv1.Justification, error) {
	switch out.JustificationType {
	case justTypeCitation:
		if len(out.CitationIDs) == 0 {
			return nil, fmt.Errorf("citation justification requires at least one citation_id")
		}
		return &flowv1.Justification{
			Kind: &flowv1.Justification_Citation{
				Citation: &flowv1.Citation{CitationIds: out.CitationIDs},
			},
		}, nil

	case justTypeNovelArgument:
		if out.Argument == "" {
			return nil, fmt.Errorf("novel_argument justification requires a non-empty argument")
		}
		return &flowv1.Justification{
			Kind: &flowv1.Justification_NovelArgument{
				NovelArgument: &flowv1.NovelArgument{Argument: out.Argument},
			},
		}, nil

	default:
		return nil, fmt.Errorf("refuse decision requires justification_type (citation or novel_argument), got %q",
			out.JustificationType)
	}
}
