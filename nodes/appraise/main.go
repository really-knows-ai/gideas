// Appraise is the reviewer node of the Foundry Cycle.
//
// It reads an input artefact (e.g. "petition") and a review artefact (e.g.
// "haiku"), then uses LLM-backed agents to review how well the content
// adheres to governance laws.
//
// Appraise operates in three phases:
//
//  1. Fix/Refusal Evaluation — For each ACTIONED or WONT_FIX feedback item,
//     the EvalAgent runs a focused evaluation to decide accept or reject.
//     These run in parallel, each with managed heartbeat and cost telemetry.
//
//  2. Fresh Review — The ReviewAgent reviews the content against the input
//     brief and governance laws, producing zero or more new feedback items
//     with severity and optional law citations.
//
//  3. Learning Capture — If Phase 1 resolved any feedback items that carried
//     a NovelArgument justification, the FindingAgent distils the learnings
//     into Tier 1 Findings recorded in the Library.
//
// Appraise always stamps the review — meaning "I have appraised this version",
// not "this version is valid". Always routes back to Sort.
//
// Configuration is loaded from a ConfigMap-mounted YAML file:
//
//	inputArtefact:    "petition"
//	reviewArtefact:   "haiku"
//	governedArtefact: "haiku"
//	stampName:        "review"
package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"text/template"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

// appraiseConfig holds the node's configuration, loaded from a
// ConfigMap-mounted YAML file via nodeconfig.Load.
type appraiseConfig struct {
	InputArtefact    string `yaml:"inputArtefact"`    // artefact ID to read as input (e.g. "petition")
	ReviewArtefact   string `yaml:"reviewArtefact"`   // artefact ID to review (e.g. "haiku")
	GovernedArtefact string `yaml:"governedArtefact"` // GovernedArtefact CR name (e.g. "haiku")
	StampName        string `yaml:"stampName"`        // stamp to apply (e.g. "review")
}

const (
	verdictAccept = "accept"
	verdictReject = "reject"
)

// ---------------------------------------------------------------------------
// Severity Mapping
// ---------------------------------------------------------------------------

// severityMap maps lowercase string severity values from the LLM to the
// proto enum. Returns SEVERITY_MEDIUM for unknown values as a safe default.
var severityMap = map[string]flowv1.Severity{
	"low":      flowv1.Severity_SEVERITY_LOW,
	"medium":   flowv1.Severity_SEVERITY_MEDIUM,
	"high":     flowv1.Severity_SEVERITY_HIGH,
	"critical": flowv1.Severity_SEVERITY_CRITICAL,
}

func parseSeverity(s string) flowv1.Severity {
	if sev, ok := severityMap[strings.ToLower(s)]; ok {
		return sev
	}
	return flowv1.Severity_SEVERITY_MEDIUM
}

// hasNovelArgument returns true if the feedback item carries a
// NovelArgument justification.
func hasNovelArgument(fb *flowv1.FeedbackItem) bool {
	j := fb.GetJustification()
	return j != nil && j.GetNovelArgument() != nil &&
		j.GetNovelArgument().GetArgument() != ""
}

// ---------------------------------------------------------------------------
// Agent Construction Helper
// ---------------------------------------------------------------------------

// buildAgent is the shared construction pattern for all three appraise agents.
// It renders the system prompt template, parses the query template, and
// creates a flow.Agent with schema, model (KimiK2Ollama), and prompts.
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
		flow.WithModel(flow.NewKimiK2Ollama()),
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
	slog.Info("appraise: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("appraise: server failed", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("appraise: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("appraise: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	// Load configuration from ConfigMap-mounted YAML.
	cfg, err := nodeconfig.Load[appraiseConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("appraise: load config: %w", err)
	}

	// Create agents. Each agent creates its model (KimiK2Ollama) internally.
	evalAgent, err := NewEvalAgent(client, cfg)
	if err != nil {
		return fmt.Errorf("appraise: create eval agent: %w", err)
	}

	reviewAgent, err := NewReviewAgent(client, cfg)
	if err != nil {
		return fmt.Errorf("appraise: create review agent: %w", err)
	}

	findingAgent, err := NewFindingAgent(client, cfg)
	if err != nil {
		return fmt.Errorf("appraise: create finding agent: %w", err)
	}

	return handleAppraise(ctx, client, evalAgent, reviewAgent, findingAgent, cfg)
}

// ---------------------------------------------------------------------------
// Core Orchestration
// ---------------------------------------------------------------------------

// handleAppraise performs the 3-phase appraise logic: evaluate existing
// feedback, produce fresh review, and optionally capture learnings.
func handleAppraise(
	ctx context.Context,
	client *flow.Client,
	eval *EvalAgent,
	review *ReviewAgent,
	finding *FindingAgent,
	cfg *appraiseConfig,
) error {
	// ---------------------------------------------------------------
	// Pre-inference: read artefacts, query laws, get existing feedback
	// ---------------------------------------------------------------

	inputResp, err := client.GetArtefact(ctx, cfg.InputArtefact)
	if err != nil {
		return fmt.Errorf("appraise: read %s: %w", cfg.InputArtefact, err)
	}
	inputContent := string(inputResp.GetContent())

	reviewResp, err := client.GetArtefact(ctx, cfg.ReviewArtefact)
	if err != nil {
		return fmt.Errorf("appraise: read %s: %w", cfg.ReviewArtefact, err)
	}
	reviewContent := string(reviewResp.GetContent())

	slog.Info("appraise: reviewing",
		"input_artefact", cfg.InputArtefact,
		"review_artefact", cfg.ReviewArtefact,
	)

	laws, _ := client.QueryLaws(ctx, cfg.GovernedArtefact, "")

	existingFeedback, err := client.GetFeedback(ctx, cfg.GovernedArtefact)
	if err != nil {
		return fmt.Errorf("appraise: get feedback: %w", err)
	}

	// ---------------------------------------------------------------
	// Phase 1: Evaluate ACTIONED and WONT_FIX feedback items (parallel)
	// ---------------------------------------------------------------

	novelResolved, err := evaluateFeedback(
		ctx, eval, client,
		existingFeedback, inputContent, reviewContent)
	if err != nil {
		return fmt.Errorf("appraise: evaluate feedback: %w", err)
	}

	// ---------------------------------------------------------------
	// Phase 2: Fresh review — new feedback items
	// ---------------------------------------------------------------

	reviewOut, err := review.Run(
		ctx, inputContent, reviewContent, laws, existingFeedback)
	if err != nil {
		return fmt.Errorf("appraise: review run: %w", err)
	}

	slog.Info("appraise: review complete",
		"feedback_count", len(reviewOut.Feedback))

	// ---------------------------------------------------------------
	// Post-inference: stamp, raise feedback, cite laws
	// ---------------------------------------------------------------

	if _, err := client.StampArtefact(ctx, cfg.GovernedArtefact, cfg.StampName); err != nil {
		return fmt.Errorf("appraise: stamp %s: %w", cfg.StampName, err)
	}
	slog.Info("appraise: stamp applied", "stamp", cfg.StampName)

	for i, item := range reviewOut.Feedback {
		if item.Message == "" {
			continue
		}

		severity := parseSeverity(item.Severity)
		feedbackID, err := client.AddFeedback(
			ctx, cfg.GovernedArtefact, severity, item.Message)
		if err != nil {
			return fmt.Errorf("appraise: add feedback[%d]: %w", i, err)
		}
		slog.Info("appraise: feedback raised",
			"index", i,
			"feedback_id", feedbackID,
			"severity", item.Severity,
			"message", item.Message,
			"cited_laws", item.CitedLaws,
		)

		if len(item.CitedLaws) > 0 {
			if err := client.Cite(ctx, item.CitedLaws...); err != nil {
				slog.Error("appraise: failed to cite laws",
					"error", err, "law_ids", item.CitedLaws)
			} else {
				slog.Info("appraise: cited laws", "law_ids", item.CitedLaws)
			}
		}
	}

	if len(reviewOut.Feedback) == 0 {
		slog.Info("appraise: no feedback — content looks good")
	}

	// ---------------------------------------------------------------
	// Phase 3: Learning capture — mint Tier 1 Findings from resolved
	// novel arguments
	// ---------------------------------------------------------------

	if len(novelResolved) > 0 {
		if err := mintFindings(ctx, finding, client, novelResolved); err != nil {
			return fmt.Errorf("appraise: mint findings: %w", err)
		}
	} else {
		slog.Info("appraise: no novel arguments resolved " +
			"— skipping learning capture")
	}

	// ---------------------------------------------------------------
	// Route onward
	// ---------------------------------------------------------------

	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("appraise: route to output: %w", err)
	}

	slog.Info("appraise: routed to output",
		"workitem_id", os.Getenv(flow.EnvWorkitemID))
	return nil
}

// ---------------------------------------------------------------------------
// Phase 1: Parallel Fix/Refusal Evaluation
// ---------------------------------------------------------------------------

// evaluateFeedback runs parallel EvalAgent calls for ACTIONED and WONT_FIX
// feedback items. Each item gets a focused inference call that decides
// accept or reject.
//
// Returns the subset of feedback items that were resolved (accepted) AND
// carry a NovelArgument justification. These are candidates for Tier 1
// Finding promotion in the learning-capture phase.
func evaluateFeedback(
	ctx context.Context,
	eval *EvalAgent,
	client *flow.Client,
	feedback []*flowv1.FeedbackItem,
	inputContent, reviewContent string,
) ([]*flowv1.FeedbackItem, error) {
	type evalTask struct {
		item *flowv1.FeedbackItem
		kind string
	}

	var tasks []evalTask
	for _, fb := range feedback {
		switch fb.GetState() {
		case flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED:
			tasks = append(tasks, evalTask{item: fb, kind: "actioned"})
		case flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX:
			tasks = append(tasks, evalTask{item: fb, kind: "wont_fix"})
		}
	}

	if len(tasks) == 0 {
		slog.Info("appraise: no feedback items to evaluate")
		return nil, nil
	}

	slog.Info("appraise: evaluating feedback items", "count", len(tasks))

	type evalResultItem struct {
		task evalTask
		out  *evalOutput
		err  error
	}

	results := make([]evalResultItem, len(tasks))
	var wg sync.WaitGroup
	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t evalTask) {
			defer wg.Done()
			out, err := eval.Run(ctx, t.item, inputContent, reviewContent, t.kind)
			results[idx] = evalResultItem{task: t, out: out, err: err}
		}(i, task)
	}
	wg.Wait()

	// Apply verdicts sequentially (gRPC calls to Archivist).
	// Collect resolved items that carry a novel argument justification.
	var novelResolved []*flowv1.FeedbackItem

	for _, r := range results {
		if r.err != nil {
			return nil, fmt.Errorf(
				"appraise: eval feedback %s: %w",
				r.task.item.GetId(), r.err)
		}

		fbID := r.task.item.GetId()
		state := r.task.item.GetState()

		switch {
		case state == flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED &&
			r.out.Verdict == verdictAccept:
			slog.Info("appraise: accepting fix",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := client.AcceptFix(ctx, fbID); err != nil {
				return nil, fmt.Errorf("appraise: accept fix %s: %w", fbID, err)
			}
			if hasNovelArgument(r.task.item) {
				novelResolved = append(novelResolved, r.task.item)
			}

		case state == flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED &&
			r.out.Verdict == verdictReject:
			slog.Info("appraise: rejecting fix",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := client.RejectFix(ctx, fbID, r.out.Reason); err != nil {
				return nil, fmt.Errorf("appraise: reject fix %s: %w", fbID, err)
			}

		case state == flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX &&
			r.out.Verdict == verdictAccept:
			slog.Info("appraise: accepting refusal",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := client.AcceptRefusal(ctx, fbID); err != nil {
				return nil, fmt.Errorf(
					"appraise: accept refusal %s: %w", fbID, err)
			}
			if hasNovelArgument(r.task.item) {
				novelResolved = append(novelResolved, r.task.item)
			}

		case state == flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX &&
			r.out.Verdict == verdictReject:
			slog.Info("appraise: rejecting refusal",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := client.RejectRefusal(ctx, fbID, r.out.Reason); err != nil {
				return nil, fmt.Errorf(
					"appraise: reject refusal %s: %w", fbID, err)
			}
		}
	}

	return novelResolved, nil
}

// ---------------------------------------------------------------------------
// Phase 3: Learning Capture — Mint Tier 1 Findings
// ---------------------------------------------------------------------------

// mintFindings runs the FindingAgent to distill governance learnings from
// resolved feedback items that carried novel arguments. Each finding is
// recorded as a Tier 1 Finding via the Librarian.
func mintFindings(
	ctx context.Context,
	finding *FindingAgent,
	client *flow.Client,
	novelItems []*flowv1.FeedbackItem,
) error {
	slog.Info("appraise: capturing learnings from resolved "+
		"novel arguments", "count", len(novelItems))

	out, err := finding.Run(ctx, novelItems)
	if err != nil {
		return fmt.Errorf("finding inference: %w", err)
	}
	if out == nil {
		return nil
	}

	slog.Info("appraise: LLM produced findings",
		"count", len(out.Findings))

	for i, f := range out.Findings {
		lawID, err := client.RecordFinding(
			ctx, f.Goal, f.AppliesTo,
			[]*flowv1.Representation{
				{Type: "text/markdown", Content: f.Rationale},
			},
		)
		if err != nil {
			return fmt.Errorf("record finding[%d]: %w", i, err)
		}
		slog.Info("appraise: minted Tier 1 Finding",
			"law_id", lawID,
			"goal", f.Goal,
			"applies_to", f.AppliesTo,
		)
	}

	return nil
}
