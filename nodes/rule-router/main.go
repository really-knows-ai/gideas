// Rule Router is a generic, config-driven routing node that evaluates an
// ordered list of CEL expressions against the current workitem state.
//
// The first rule whose expression evaluates to true determines the output.
// If no rule matches and a default output is configured, traffic routes
// there. If no rule matches and no default exists, the handler returns an
// error.
//
// One image (rule-router:latest), many CRD instances with different rule
// configs. Examples: clerk-done-router (tier routing), hitl-gate (post-HITL
// routing).
//
// Configuration (YAML via NODE_CONFIG_PATH, default /etc/foundry/node-config.yaml):
//
//	rules:
//	  - name: "tier-1-2"
//	    when: 'metadata["petition_max_tier"] in ["FINDING", "RULING"]'
//	    output: "law-applicator"
//	  - name: "tier-3-5"
//	    when: 'true'
//	    output: "hitl-appraise"
//	default: "hitl-appraise"
//
// CEL environment variables (lazily loaded -- only fetched when referenced):
//
//	metadata            map(string, string)               WorkitemContext.Metadata
//	artefacts           list(string)                      Artefact IDs on the workitem
//	feedback            map with .unresolved_count (int), Aggregated across all artefacts
//	                    .has_deadlocked (bool),
//	                    .total_count (int)
//	stamps              map(string, list(string))         Artefact ID -> stamp names
//	children            list of {phase, completion_reason, Per-child workitem status
//	                    workitem_id}
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/cel-go/cel"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	"github.com/gideas/flow/nodes/internal/nodeutil"
	flow "github.com/gideas/flow/sdk/go"
)

// ruleRouterConfig holds the Rule Router's runtime configuration.
type ruleRouterConfig struct {
	// Rules is an ordered list of CEL rule entries. The first rule whose
	// expression evaluates to true wins.
	Rules []ruleEntry `yaml:"rules"`

	// Default is the fallback output name when no rule matches. If empty
	// and no rule matches, the handler returns an error.
	Default string `yaml:"default"`
}

// ruleEntry is a single routing rule: a CEL expression and a target output.
type ruleEntry struct {
	// Name is a human-readable label for logging and telemetry.
	Name string `yaml:"name"`

	// When is a CEL expression that must evaluate to bool. The first
	// rule whose expression returns true determines the routing output.
	When string `yaml:"when"`

	// Output is the FoundryNode output name to route to when this rule
	// matches.
	Output string `yaml:"output"`
}

func main() {
	slog.Info("rule-router: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("rule-router: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("rule-router: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("rule-router: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[ruleRouterConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("rule-router: load config: %w", err)
	}

	return handleRuleRouter(ctx, client, cfg, wctx.GetMetadata())
}

// handleRuleRouter contains the Rule Router logic, separated from the
// handler boilerplate for testability. The metadata map is passed
// explicitly because the SDK client does not expose WorkitemContext
// metadata.
func handleRuleRouter(
	ctx context.Context,
	client *flow.Client,
	cfg *ruleRouterConfig,
	metadata map[string]string,
) error {
	_, _ = client.Heartbeat(ctx)

	// Validate configuration.
	if err := validateConfig(cfg); err != nil {
		return fmt.Errorf("rule-router: invalid config: %w", err)
	}

	// Compile all CEL rules against the shared environment.
	compiled, err := compileRules(cfg.Rules)
	if err != nil {
		return fmt.Errorf("rule-router: %w", err)
	}

	// Determine which variables are referenced by any rule expression and
	// lazily load only the data that is actually needed.
	activation, err := buildActivation(ctx, client, cfg.Rules, metadata)
	if err != nil {
		return fmt.Errorf("rule-router: load variables: %w", err)
	}

	// Telemetry: started.
	nodeutil.EmitTelemetry(ctx, client, "foundry.rule_router.started", map[string]any{
		"rule_count":  len(compiled),
		"has_default": cfg.Default != "",
	})

	// Evaluate rules in order — first match wins.
	for i, cr := range compiled {
		out, _, err := cr.program.Eval(activation)
		if err != nil {
			return fmt.Errorf("rule-router: rule %d (%q): eval: %w", i, cr.name, err)
		}
		matched, ok := out.Value().(bool)
		if !ok {
			return fmt.Errorf("rule-router: rule %d (%q): expression returned %T, expected bool", i, cr.name, out.Value())
		}
		if !matched {
			continue
		}

		slog.Info("rule-router: rule matched",
			"rule_index", i,
			"rule_name", cr.name,
			"output", cr.output,
		)
		nodeutil.EmitTelemetry(ctx, client, "foundry.rule_router.matched", map[string]any{
			"rule_index": i,
			"rule_name":  cr.name,
			"output":     cr.output,
		})

		if _, err := client.RouteToOutput(ctx, cr.output); err != nil {
			return fmt.Errorf("rule-router: route to %q: %w", cr.output, err)
		}
		return nil
	}

	// No rule matched — try default.
	if cfg.Default != "" {
		slog.Info("rule-router: no rule matched, using default",
			"output", cfg.Default,
		)
		nodeutil.EmitTelemetry(ctx, client, "foundry.rule_router.no_match", map[string]any{
			"output":  cfg.Default,
			"default": true,
		})

		if _, err := client.RouteToOutput(ctx, cfg.Default); err != nil {
			return fmt.Errorf("rule-router: route to default %q: %w", cfg.Default, err)
		}
		return nil
	}

	// No rule matched and no default — error.
	nodeutil.EmitTelemetry(ctx, client, "foundry.rule_router.no_match", map[string]any{
		"default": false,
	})
	return fmt.Errorf("rule-router: no rule matched and no default output configured")
}

// validateConfig checks that the rule router configuration is usable.
// At least one rule or a default output must be present. Each rule must
// have a non-empty When expression and a non-empty Output.
func validateConfig(cfg *ruleRouterConfig) error {
	if len(cfg.Rules) == 0 && cfg.Default == "" {
		return fmt.Errorf("no rules and no default output configured")
	}
	for i, r := range cfg.Rules {
		if r.When == "" {
			return fmt.Errorf("rule %d (%q): empty 'when' expression", i, r.Name)
		}
		if r.Output == "" {
			return fmt.Errorf("rule %d (%q): empty 'output'", i, r.Name)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// CEL engine
// ---------------------------------------------------------------------------

// celEnvVars are the variables declared in the CEL environment. All use
// DynType to avoid custom type adapters (following the operator pattern).
var celEnvVars = []cel.EnvOption{
	cel.Variable("metadata", cel.MapType(cel.StringType, cel.StringType)),
	cel.Variable("artefacts", cel.ListType(cel.DynType)),
	cel.Variable("feedback", cel.DynType),
	cel.Variable("stamps", cel.DynType),
	cel.Variable("children", cel.ListType(cel.DynType)),
}

// compiledRule pairs a parsed/checked CEL program with the original rule
// metadata (name, output).
type compiledRule struct {
	name    string
	output  string
	program cel.Program
}

// compileRules parses and type-checks every rule expression against the
// shared CEL environment. It returns an error on the first compilation
// failure or if any expression does not evaluate to bool.
func compileRules(rules []ruleEntry) ([]compiledRule, error) {
	env, err := cel.NewEnv(celEnvVars...)
	if err != nil {
		return nil, fmt.Errorf("create CEL env: %w", err)
	}

	compiled := make([]compiledRule, len(rules))
	for i, r := range rules {
		ast, issues := env.Compile(r.When)
		if issues != nil && issues.Err() != nil {
			return nil, fmt.Errorf("rule %d (%q): compile: %w", i, r.Name, issues.Err())
		}
		// Accept both bool and dyn output types. DynType variables (e.g.
		// feedback) produce dyn for field access expressions like
		// "feedback.has_deadlocked". The runtime type assertion below
		// (line ~148) catches non-bool results at evaluation time.
		outType := ast.OutputType()
		if outType != cel.BoolType && outType != cel.DynType {
			return nil, fmt.Errorf("rule %d (%q): expression must return bool, got %s", i, r.Name, outType)
		}
		prg, err := env.Program(ast)
		if err != nil {
			return nil, fmt.Errorf("rule %d (%q): program: %w", i, r.Name, err)
		}
		compiled[i] = compiledRule{name: r.Name, output: r.Output, program: prg}
	}
	return compiled, nil
}

// ---------------------------------------------------------------------------
// Lazy variable loading
// ---------------------------------------------------------------------------

// needsVar returns true if any rule expression textually references the
// given variable name. This is a heuristic (string-contains) that
// intentionally over-matches: a false positive only costs an extra RPC,
// while a false negative would break evaluation.
func needsVar(rules []ruleEntry, varName string) bool {
	for _, r := range rules {
		if strings.Contains(r.When, varName) {
			return true
		}
	}
	return false
}

// buildActivation assembles the CEL activation map by lazily loading only
// the variables referenced by the configured rules.
//
// Load order matters: feedback depends on artefacts (needs artefact IDs to
// query per-artefact feedback). The dependency chain is:
//
//	metadata   — always available (passed in)
//	artefacts  — ListArtefacts RPC
//	feedback   — GetFeedback per artefact (requires artefact IDs)
//	stamps     — QueryArtefactState RPC
//	children   — GetChildren RPC (raw proto, includes completion_reason)
func buildActivation(
	ctx context.Context,
	client *flow.Client,
	rules []ruleEntry,
	metadata map[string]string,
) (map[string]any, error) {
	act := map[string]any{
		// metadata is always provided (zero-cost — already in memory).
		"metadata": metadata,
		// Provide zero-value defaults for variables not loaded so that
		// CEL evaluation does not fail with "no such attribute".
		"artefacts": []any{},
		"feedback":  map[string]any{"unresolved_count": 0, "has_deadlocked": false, "total_count": 0},
		"stamps":    map[string]any{},
		"children":  []any{},
	}

	// Artefacts — needed by both "artefacts" and "feedback" variables.
	var artefactIDs []string
	if needsVar(rules, "artefacts") || needsVar(rules, "feedback") || needsVar(rules, "stamps") {
		ids, err := loadArtefacts(ctx, client)
		if err != nil {
			return nil, fmt.Errorf("load artefacts: %w", err)
		}
		artefactIDs = ids
		// Convert to []any for CEL.
		celList := make([]any, len(ids))
		for i, id := range ids {
			celList[i] = id
		}
		act["artefacts"] = celList
	}

	// Feedback — aggregated across all artefacts.
	if needsVar(rules, "feedback") {
		fb, err := loadFeedback(ctx, client, artefactIDs)
		if err != nil {
			return nil, fmt.Errorf("load feedback: %w", err)
		}
		act["feedback"] = fb
	}

	// Stamps — per-artefact stamp names.
	if needsVar(rules, "stamps") {
		st, err := loadStamps(ctx, client, artefactIDs)
		if err != nil {
			return nil, fmt.Errorf("load stamps: %w", err)
		}
		act["stamps"] = st
	}

	// Children — raw proto for completion_reason access.
	if needsVar(rules, "children") {
		ch, err := loadChildren(ctx, client)
		if err != nil {
			return nil, fmt.Errorf("load children: %w", err)
		}
		act["children"] = ch
	}

	return act, nil
}

// loadArtefacts fetches the governed artefact names on the current workitem.
func loadArtefacts(ctx context.Context, client *flow.Client) ([]string, error) {
	resp, err := client.Archivist.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId: client.WorkitemID(),
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(resp.GetArtefactRefs()))
	for i, ref := range resp.GetArtefactRefs() {
		ids[i] = ref.GetId()
	}
	return ids, nil
}

// loadFeedback aggregates feedback across all artefacts into the feedback
// CEL variable shape: {unresolved_count, has_deadlocked, total_count}.
func loadFeedback(ctx context.Context, client *flow.Client, artefactIDs []string) (map[string]any, error) {
	var totalCount, unresolvedCount int
	var hasDeadlocked bool

	for _, artID := range artefactIDs {
		items, err := client.GetFeedback(ctx, artID)
		if err != nil {
			return nil, fmt.Errorf("artefact %s: %w", artID, err)
		}
		for _, item := range items {
			totalCount++
			switch item.GetState() {
			case flowv1.FeedbackState_FEEDBACK_STATE_NEW,
				flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED:
				unresolvedCount++
			case flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED:
				unresolvedCount++
				hasDeadlocked = true
			}
		}
	}
	return map[string]any{
		"unresolved_count": unresolvedCount,
		"has_deadlocked":   hasDeadlocked,
		"total_count":      totalCount,
	}, nil
}

// loadStamps fetches per-artefact stamp names via QueryArtefactState.
// Returns map[artefactID] -> []stampName for the CEL stamps variable.
func loadStamps(ctx context.Context, client *flow.Client, artefactIDs []string) (map[string]any, error) {
	// Derive governed artefact names for the query.
	resp, err := client.Archivist.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId: client.WorkitemID(),
	})
	if err != nil {
		return nil, err
	}

	govNames := make([]string, len(resp.GetArtefactRefs()))
	idByGovName := make(map[string]string, len(resp.GetArtefactRefs()))
	for i, ref := range resp.GetArtefactRefs() {
		govNames[i] = ref.GetGovernedArtefact()
		idByGovName[ref.GetGovernedArtefact()] = ref.GetId()
	}

	stateResp, err := client.Archivist.QueryArtefactState(ctx, &flowv1.QueryArtefactStateRequest{
		WorkitemId:        client.WorkitemID(),
		GovernedArtefacts: govNames,
	})
	if err != nil {
		return nil, err
	}

	stamps := make(map[string]any, len(stateResp.GetArtefactStates()))
	for _, state := range stateResp.GetArtefactStates() {
		// Convert stamp names to []any for CEL.
		names := make([]any, len(state.GetStampNames()))
		for i, n := range state.GetStampNames() {
			names[i] = n
		}
		stamps[state.GetArtefactId()] = names
	}

	// Ensure every artefact has an entry even if no stamps.
	for _, id := range artefactIDs {
		if _, ok := stamps[id]; !ok {
			stamps[id] = []any{}
		}
	}

	return stamps, nil
}

// loadChildren fetches child workitem statuses using the raw Operator RPC
// (not the SDK convenience method) to preserve CompletionReason.
func loadChildren(ctx context.Context, client *flow.Client) ([]any, error) {
	resp, err := client.Operator.GetChildren(ctx, &flowv1.GetChildrenRequest{})
	if err != nil {
		return nil, err
	}
	children := make([]any, len(resp.GetChildren()))
	for i, ch := range resp.GetChildren() {
		children[i] = map[string]any{
			"workitem_id":       ch.GetWorkitemId(),
			"phase":             ch.GetPhase(),
			"completion_reason": ch.GetCompletionReason().String(),
		}
	}
	return children, nil
}
