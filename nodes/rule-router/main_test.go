package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// setupRuleRouterTest spins up an ephemeral gRPC server backed by the spy,
// creates a real SDK client connected to it, and returns both for the test.
func setupRuleRouterTest(t *testing.T, spy *ruleRouterSpy) *flow.Client {
	t.Helper()

	lis, err := newLocalListener()
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := newSpyGRPCServer(spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	t.Setenv(flow.EnvWorkitemID, "test-workitem")
	client, err := flow.NewClient(
		flow.WithSidecarAddress(lis.Addr().String()),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// TestRuleRouter_FirstRuleMatch verifies that the first matching rule
// determines the routing output.
func TestRuleRouter_FirstRuleMatch(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "always", When: "true", Output: "target-a"},
			{Name: "never-reached", When: "true", Output: "target-b"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "target-a" {
		t.Fatalf("expected route to target-a, got %v", spy.RoutedOutputs)
	}

	// Verify telemetry events were emitted.
	if len(spy.TelemetryEvents) < 2 {
		t.Fatalf("expected at least 2 telemetry events (started + matched), got %d", len(spy.TelemetryEvents))
	}
	if spy.TelemetryEvents[0].EventType != "foundry.rule_router.started" {
		t.Errorf("first telemetry event type = %q, want foundry.rule_router.started", spy.TelemetryEvents[0].EventType)
	}
	if spy.TelemetryEvents[1].EventType != "foundry.rule_router.matched" {
		t.Errorf("second telemetry event type = %q, want foundry.rule_router.matched", spy.TelemetryEvents[1].EventType)
	}

	// Verify heartbeat was called.
	if spy.HeartbeatCount == 0 {
		t.Error("expected at least one heartbeat call")
	}
}

// TestRuleRouter_LaterRuleMatch verifies that earlier non-matching rules
// are skipped and a later matching rule is used.
func TestRuleRouter_LaterRuleMatch(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "skip", When: "false", Output: "target-a"},
			{Name: "match", When: "true", Output: "target-b"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "target-b" {
		t.Fatalf("expected route to target-b, got %v", spy.RoutedOutputs)
	}
}

// TestRuleRouter_NoMatchWithDefault verifies that when no rule matches,
// the default output is used.
func TestRuleRouter_NoMatchWithDefault(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "skip", When: "false", Output: "target-a"},
		},
		Default: "fallback",
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "fallback" {
		t.Fatalf("expected route to fallback, got %v", spy.RoutedOutputs)
	}
}

// TestRuleRouter_NoMatchNoDefault verifies that when no rule matches and
// there is no default, an error is returned.
func TestRuleRouter_NoMatchNoDefault(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "skip", When: "false", Output: "target-a"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err == nil {
		t.Fatal("expected error when no rule matches and no default")
	}
}

// TestRuleRouter_MetadataRouting verifies that rules can access the
// metadata CEL variable.
func TestRuleRouter_MetadataRouting(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "tier-check", When: `metadata["tier"] == "gold"`, Output: "gold-path"},
			{Name: "fallback", When: "true", Output: "default-path"},
		},
	}

	meta := map[string]string{"tier": "gold"}
	err := handleRuleRouter(context.Background(), client, cfg, meta)
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "gold-path" {
		t.Fatalf("expected route to gold-path, got %v", spy.RoutedOutputs)
	}
}

// TestRuleRouter_DefaultOnlyNoRules verifies that a config with only a
// default (no rules) routes to the default.
func TestRuleRouter_DefaultOnlyNoRules(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Default: "only-output",
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "only-output" {
		t.Fatalf("expected route to only-output, got %v", spy.RoutedOutputs)
	}
}

// TestRuleRouter_LazyLoad_MetadataOnly verifies that when rules only
// reference metadata, no RPCs for artefacts/feedback/stamps/children
// are made.
func TestRuleRouter_LazyLoad_MetadataOnly(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "meta", When: `metadata["key"] == "val"`, Output: "out"},
		},
	}

	meta := map[string]string{"key": "val"}
	err := handleRuleRouter(context.Background(), client, cfg, meta)
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if spy.ListArtefactsCalled {
		t.Error("ListArtefacts should not have been called for metadata-only rules")
	}
	if spy.GetFeedbackCalled {
		t.Error("GetFeedback should not have been called for metadata-only rules")
	}
	if spy.QueryArtefactStateCalled {
		t.Error("QueryArtefactState should not have been called for metadata-only rules")
	}
	if spy.GetChildrenCalled {
		t.Error("GetChildren should not have been called for metadata-only rules")
	}
}

// TestRuleRouter_InvalidCEL verifies that an invalid CEL expression
// causes a compile-time error.
func TestRuleRouter_InvalidCEL(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "bad", When: "this is not valid CEL !!!", Output: "out"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err == nil {
		t.Fatal("expected error for invalid CEL expression")
	}
}

// TestRuleRouter_NonBoolCEL verifies that a CEL expression returning
// a non-bool type causes a compile-time error.
func TestRuleRouter_NonBoolCEL(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "int-expr", When: `1 + 2`, Output: "out"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err == nil {
		t.Fatal("expected error for non-bool CEL expression")
	}
}

// TestRuleRouter_EmptyRulesWithDefault verifies that an empty rules list
// with a default output routes to the default.
func TestRuleRouter_EmptyRulesWithDefault(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules:   []ruleEntry{},
		Default: "default-out",
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "default-out" {
		t.Fatalf("expected route to default-out, got %v", spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Artefact-based CEL routing
// ---------------------------------------------------------------------------

// TestRuleRouter_ArtefactRouting verifies that rules referencing the
// "artefacts" CEL variable trigger a ListArtefacts RPC and can route
// based on artefact IDs.
func TestRuleRouter_ArtefactRouting(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.ArtefactRefs = []*flowv1.ArtefactRef{
		{Id: "art-001", GovernedArtefact: "haiku"},
		{Id: "art-002", GovernedArtefact: "doc"},
	}
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "has-two", When: `artefacts.size() == 2`, Output: "dual-path"},
			{Name: "fallback", When: "true", Output: "other"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "dual-path" {
		t.Fatalf("expected route to dual-path, got %v", spy.RoutedOutputs)
	}
	if !spy.ListArtefactsCalled {
		t.Error("ListArtefacts should have been called for artefacts rule")
	}
}

// TestRuleRouter_ArtefactContains verifies that the artefacts variable
// can be searched for a specific ID.
func TestRuleRouter_ArtefactContains(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.ArtefactRefs = []*flowv1.ArtefactRef{
		{Id: "art-special", GovernedArtefact: "petition"},
	}
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "has-special", When: `artefacts.exists(a, a == "art-special")`, Output: "special"},
			{Name: "fallback", When: "true", Output: "other"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "special" {
		t.Fatalf("expected route to special, got %v", spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Feedback-based CEL routing
// ---------------------------------------------------------------------------

// TestRuleRouter_FeedbackRouting_UnresolvedCount verifies that rules
// referencing the "feedback" CEL variable trigger both ListArtefacts
// and GetFeedback RPCs and can route on unresolved_count.
func TestRuleRouter_FeedbackRouting_UnresolvedCount(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.ArtefactRefs = []*flowv1.ArtefactRef{
		{Id: "art-1", GovernedArtefact: "haiku"},
	}
	spy.FeedbackByArtefact = map[string][]*flowv1.FeedbackItem{
		"art-1": {
			{Id: "fb-1", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
			{Id: "fb-2", State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED},
			{Id: "fb-3", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
		},
	}
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "has-unresolved", When: `feedback.unresolved_count > 0`, Output: "needs-work"},
			{Name: "fallback", When: "true", Output: "done"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "needs-work" {
		t.Fatalf("expected route to needs-work, got %v", spy.RoutedOutputs)
	}
	if !spy.ListArtefactsCalled {
		t.Error("ListArtefacts should have been called (feedback depends on artefacts)")
	}
	if !spy.GetFeedbackCalled {
		t.Error("GetFeedback should have been called for feedback rule")
	}
}

// TestRuleRouter_FeedbackRouting_HasDeadlocked verifies routing based on
// feedback.has_deadlocked.
func TestRuleRouter_FeedbackRouting_HasDeadlocked(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.ArtefactRefs = []*flowv1.ArtefactRef{
		{Id: "art-1", GovernedArtefact: "haiku"},
	}
	spy.FeedbackByArtefact = map[string][]*flowv1.FeedbackItem{
		"art-1": {
			{Id: "fb-1", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
			{Id: "fb-2", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
		},
	}
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "deadlocked", When: `feedback.has_deadlocked`, Output: "arbiter"},
			{Name: "fallback", When: "true", Output: "done"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "arbiter" {
		t.Fatalf("expected route to arbiter, got %v", spy.RoutedOutputs)
	}
}

// TestRuleRouter_FeedbackAggregation_MultipleArtefacts verifies that
// feedback is aggregated across all artefacts.
func TestRuleRouter_FeedbackAggregation_MultipleArtefacts(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.ArtefactRefs = []*flowv1.ArtefactRef{
		{Id: "art-1", GovernedArtefact: "haiku"},
		{Id: "art-2", GovernedArtefact: "doc"},
	}
	spy.FeedbackByArtefact = map[string][]*flowv1.FeedbackItem{
		"art-1": {
			{Id: "fb-1", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
		},
		"art-2": {
			{Id: "fb-2", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
			{Id: "fb-3", State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX},
		},
	}
	client := setupRuleRouterTest(t, spy)

	// unresolved_count should be 2 (NEW + NEW), total_count should be 3.
	// WONT_FIX is not unresolved (not NEW, ACTIONED, or DEADLOCKED).
	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "exact-unresolved", When: `feedback.unresolved_count == 2 && feedback.total_count == 3`, Output: "match"},
			{Name: "fallback", When: "true", Output: "other"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "match" {
		t.Fatalf("expected route to match, got %v", spy.RoutedOutputs)
	}
}

// TestRuleRouter_FeedbackRouting_NoUnresolved verifies that when all
// feedback is resolved, unresolved_count is 0.
func TestRuleRouter_FeedbackRouting_NoUnresolved(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.ArtefactRefs = []*flowv1.ArtefactRef{
		{Id: "art-1", GovernedArtefact: "haiku"},
	}
	spy.FeedbackByArtefact = map[string][]*flowv1.FeedbackItem{
		"art-1": {
			{Id: "fb-1", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
		},
	}
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "clean", When: `feedback.unresolved_count == 0`, Output: "clean-path"},
			{Name: "fallback", When: "true", Output: "other"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "clean-path" {
		t.Fatalf("expected route to clean-path, got %v", spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Stamps-based CEL routing
// ---------------------------------------------------------------------------

// TestRuleRouter_StampsRouting verifies that rules referencing the
// "stamps" CEL variable trigger ListArtefacts and QueryArtefactState RPCs
// and can route based on stamp data.
func TestRuleRouter_StampsRouting(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.ArtefactRefs = []*flowv1.ArtefactRef{
		{Id: "art-1", GovernedArtefact: "haiku"},
	}
	spy.ArtefactStates = []*flowv1.ArtefactState{
		{ArtefactId: "art-1", GovernedArtefact: "haiku", StampNames: []string{"linter", "review"}},
	}
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{
				Name:   "has-review-stamp",
				When:   `"art-1" in stamps && stamps["art-1"].exists(s, s == "review")`,
				Output: "reviewed",
			},
			{Name: "fallback", When: "true", Output: "other"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "reviewed" {
		t.Fatalf("expected route to reviewed, got %v", spy.RoutedOutputs)
	}
	if !spy.ListArtefactsCalled {
		t.Error("ListArtefacts should have been called (stamps depends on artefacts)")
	}
	if !spy.QueryArtefactStateCalled {
		t.Error("QueryArtefactState should have been called for stamps rule")
	}
}

// ---------------------------------------------------------------------------
// Children-based CEL routing
// ---------------------------------------------------------------------------

// TestRuleRouter_ChildrenRouting verifies that rules referencing the
// "children" CEL variable trigger a GetChildren RPC and can route based
// on child statuses including completion_reason.
func TestRuleRouter_ChildrenRouting(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.ChildStatuses = []*flowv1.ChildWorkitemStatus{
		{
			WorkitemId:       "child-1",
			Phase:            "Completed",
			CompletionReason: flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED,
		},
		{
			WorkitemId:       "child-2",
			Phase:            "Completed",
			CompletionReason: flowv1.CompletionReason_COMPLETION_REASON_CANCELLED,
		},
	}
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{
				Name:   "any-cancelled",
				When:   `children.exists(c, c.completion_reason == "COMPLETION_REASON_CANCELLED")`,
				Output: "cancelled-path",
			},
			{Name: "fallback", When: "true", Output: "success-path"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "cancelled-path" {
		t.Fatalf("expected route to cancelled-path, got %v", spy.RoutedOutputs)
	}
	if !spy.GetChildrenCalled {
		t.Error("GetChildren should have been called for children rule")
	}
}

// TestRuleRouter_ChildrenAllCompleted verifies that the children variable
// can be used to check if all children are in Completed phase.
func TestRuleRouter_ChildrenAllCompleted(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.ChildStatuses = []*flowv1.ChildWorkitemStatus{
		{WorkitemId: "child-1", Phase: "Completed"},
		{WorkitemId: "child-2", Phase: "Completed"},
	}
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "all-done", When: `children.size() > 0 && children.all(c, c.phase == "Completed")`, Output: "all-done"},
			{Name: "fallback", When: "true", Output: "waiting"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "all-done" {
		t.Fatalf("expected route to all-done, got %v", spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Lazy-load isolation tests
// ---------------------------------------------------------------------------

// TestRuleRouter_LazyLoad_ArtefactsOnly verifies that when rules only
// reference artefacts, no RPCs for feedback/stamps/children are made.
func TestRuleRouter_LazyLoad_ArtefactsOnly(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.ArtefactRefs = []*flowv1.ArtefactRef{
		{Id: "art-1", GovernedArtefact: "haiku"},
	}
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "art-check", When: `artefacts.size() > 0`, Output: "has-arts"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if !spy.ListArtefactsCalled {
		t.Error("ListArtefacts should have been called")
	}
	if spy.GetFeedbackCalled {
		t.Error("GetFeedback should not have been called for artefacts-only rules")
	}
	if spy.QueryArtefactStateCalled {
		t.Error("QueryArtefactState should not have been called for artefacts-only rules")
	}
	if spy.GetChildrenCalled {
		t.Error("GetChildren should not have been called for artefacts-only rules")
	}
}

// TestRuleRouter_LazyLoad_ChildrenOnly verifies that when rules only
// reference children, no RPCs for artefacts/feedback/stamps are made.
func TestRuleRouter_LazyLoad_ChildrenOnly(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.ChildStatuses = []*flowv1.ChildWorkitemStatus{
		{WorkitemId: "child-1", Phase: "Running"},
	}
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "has-children", When: `children.size() > 0`, Output: "has-kids"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if !spy.GetChildrenCalled {
		t.Error("GetChildren should have been called")
	}
	if spy.ListArtefactsCalled {
		t.Error("ListArtefacts should not have been called for children-only rules")
	}
	if spy.GetFeedbackCalled {
		t.Error("GetFeedback should not have been called for children-only rules")
	}
	if spy.QueryArtefactStateCalled {
		t.Error("QueryArtefactState should not have been called for children-only rules")
	}
}

// TestRuleRouter_LazyLoad_FeedbackTriggersArtefacts verifies that referencing
// "feedback" implicitly loads artefacts (required for artefact IDs).
func TestRuleRouter_LazyLoad_FeedbackTriggersArtefacts(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.ArtefactRefs = []*flowv1.ArtefactRef{
		{Id: "art-1", GovernedArtefact: "haiku"},
	}
	spy.FeedbackByArtefact = map[string][]*flowv1.FeedbackItem{
		"art-1": {},
	}
	client := setupRuleRouterTest(t, spy)

	// Rule references "feedback" but not "artefacts" directly.
	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "fb-check", When: `feedback.total_count == 0`, Output: "clean"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	if !spy.ListArtefactsCalled {
		t.Error("ListArtefacts should have been called (feedback depends on artefact IDs)")
	}
	if !spy.GetFeedbackCalled {
		t.Error("GetFeedback should have been called")
	}
	if spy.GetChildrenCalled {
		t.Error("GetChildren should not have been called for feedback-only rules")
	}
}

// ---------------------------------------------------------------------------
// Config validation edge cases
// ---------------------------------------------------------------------------

// TestRuleRouter_Validate_EmptyWhen verifies that a rule with an empty
// 'when' expression fails config validation.
func TestRuleRouter_Validate_EmptyWhen(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "bad", When: "", Output: "target"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err == nil {
		t.Fatal("expected error for empty 'when' expression")
	}
}

// TestRuleRouter_Validate_EmptyOutput verifies that a rule with an empty
// 'output' fails config validation.
func TestRuleRouter_Validate_EmptyOutput(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "bad", When: "true", Output: ""},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err == nil {
		t.Fatal("expected error for empty 'output'")
	}
}

// TestRuleRouter_Validate_NoRulesNoDefault verifies that a completely
// empty config (no rules, no default) fails validation.
func TestRuleRouter_Validate_NoRulesNoDefault(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err == nil {
		t.Fatal("expected error for empty config")
	}
}

// ---------------------------------------------------------------------------
// Error injection tests
// ---------------------------------------------------------------------------

// TestRuleRouter_Error_ListArtefactsFails verifies that a ListArtefacts
// failure propagates correctly when artefacts are referenced.
func TestRuleRouter_Error_ListArtefactsFails(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.ListArtefactsErr = fmt.Errorf("archivist unavailable")
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "art-check", When: `artefacts.size() > 0`, Output: "out"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err == nil {
		t.Fatal("expected error from ListArtefacts failure")
	}
}

// TestRuleRouter_Error_GetFeedbackFails verifies that a GetFeedback
// failure propagates correctly when feedback is referenced.
func TestRuleRouter_Error_GetFeedbackFails(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.ArtefactRefs = []*flowv1.ArtefactRef{
		{Id: "art-1", GovernedArtefact: "haiku"},
	}
	spy.GetFeedbackErr = fmt.Errorf("feedback service down")
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "fb-check", When: `feedback.unresolved_count == 0`, Output: "out"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err == nil {
		t.Fatal("expected error from GetFeedback failure")
	}
}

// TestRuleRouter_Error_QueryArtefactStateFails verifies that a
// QueryArtefactState failure propagates correctly when stamps are referenced.
func TestRuleRouter_Error_QueryArtefactStateFails(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.ArtefactRefs = []*flowv1.ArtefactRef{
		{Id: "art-1", GovernedArtefact: "haiku"},
	}
	spy.QueryArtefactStateErr = fmt.Errorf("state query failed")
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "stamp-check", When: `"art-1" in stamps`, Output: "out"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err == nil {
		t.Fatal("expected error from QueryArtefactState failure")
	}
}

// TestRuleRouter_Error_GetChildrenFails verifies that a GetChildren
// failure propagates correctly when children are referenced.
func TestRuleRouter_Error_GetChildrenFails(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.GetChildrenErr = fmt.Errorf("operator unavailable")
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "child-check", When: `children.size() > 0`, Output: "out"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err == nil {
		t.Fatal("expected error from GetChildren failure")
	}
}

// TestRuleRouter_Error_RouteToOutputFails verifies that a routing failure
// propagates from RouteToOutput.
func TestRuleRouter_Error_RouteToOutputFails(t *testing.T) {
	spy := newRuleRouterSpy()
	spy.RouteToOutputErr = fmt.Errorf("routing failed")
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "always", When: "true", Output: "target"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err == nil {
		t.Fatal("expected error from RouteToOutput failure")
	}
}

// ---------------------------------------------------------------------------
// Telemetry payload assertions
// ---------------------------------------------------------------------------

// TestRuleRouter_Telemetry_MatchedPayload verifies that the "matched"
// telemetry event contains the correct rule name and output.
func TestRuleRouter_Telemetry_MatchedPayload(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "my-rule", When: "true", Output: "my-output"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	// Should have "started" and "matched" telemetry events.
	if len(spy.TelemetryEvents) < 2 {
		t.Fatalf("expected at least 2 telemetry events, got %d", len(spy.TelemetryEvents))
	}

	// Verify "started" payload.
	var startedPayload map[string]any
	if err := json.Unmarshal(spy.TelemetryEvents[0].Payload, &startedPayload); err != nil {
		t.Fatalf("failed to unmarshal started payload: %v", err)
	}
	if startedPayload["rule_count"] != float64(1) {
		t.Errorf("started.rule_count = %v, want 1", startedPayload["rule_count"])
	}
	if startedPayload["has_default"] != false {
		t.Errorf("started.has_default = %v, want false", startedPayload["has_default"])
	}

	// Verify "matched" payload.
	var matchedPayload map[string]any
	if err := json.Unmarshal(spy.TelemetryEvents[1].Payload, &matchedPayload); err != nil {
		t.Fatalf("failed to unmarshal matched payload: %v", err)
	}
	if matchedPayload["rule_name"] != "my-rule" {
		t.Errorf("matched.rule_name = %v, want my-rule", matchedPayload["rule_name"])
	}
	if matchedPayload["output"] != "my-output" {
		t.Errorf("matched.output = %v, want my-output", matchedPayload["output"])
	}
	if matchedPayload["rule_index"] != float64(0) {
		t.Errorf("matched.rule_index = %v, want 0", matchedPayload["rule_index"])
	}
}

// TestRuleRouter_Telemetry_NoMatchWithDefaultPayload verifies that the
// "no_match" telemetry event for a default route contains the output.
func TestRuleRouter_Telemetry_NoMatchWithDefaultPayload(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "skip", When: "false", Output: "target"},
		},
		Default: "fallback-out",
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err != nil {
		t.Fatalf("handleRuleRouter() error: %v", err)
	}

	// Should have "started" and "no_match" events.
	if len(spy.TelemetryEvents) < 2 {
		t.Fatalf("expected at least 2 telemetry events, got %d", len(spy.TelemetryEvents))
	}

	noMatch := spy.TelemetryEvents[1]
	if noMatch.EventType != "foundry.rule_router.no_match" {
		t.Fatalf("second event type = %q, want foundry.rule_router.no_match", noMatch.EventType)
	}

	var payload map[string]any
	if err := json.Unmarshal(noMatch.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal no_match payload: %v", err)
	}
	if payload["output"] != "fallback-out" {
		t.Errorf("no_match.output = %v, want fallback-out", payload["output"])
	}
	if payload["default"] != true {
		t.Errorf("no_match.default = %v, want true", payload["default"])
	}
}

// TestRuleRouter_Telemetry_NoMatchNoDefaultPayload verifies the "no_match"
// telemetry event when there is no default output.
func TestRuleRouter_Telemetry_NoMatchNoDefaultPayload(t *testing.T) {
	spy := newRuleRouterSpy()
	client := setupRuleRouterTest(t, spy)

	cfg := &ruleRouterConfig{
		Rules: []ruleEntry{
			{Name: "skip", When: "false", Output: "target"},
		},
	}

	err := handleRuleRouter(context.Background(), client, cfg, map[string]string{})
	if err == nil {
		t.Fatal("expected error when no rule matches and no default")
	}

	// Should still have "started" and "no_match" telemetry events.
	if len(spy.TelemetryEvents) < 2 {
		t.Fatalf("expected at least 2 telemetry events, got %d", len(spy.TelemetryEvents))
	}

	noMatch := spy.TelemetryEvents[1]
	if noMatch.EventType != "foundry.rule_router.no_match" {
		t.Fatalf("second event type = %q, want foundry.rule_router.no_match", noMatch.EventType)
	}

	var payload map[string]any
	if err := json.Unmarshal(noMatch.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal no_match payload: %v", err)
	}
	if payload["default"] != false {
		t.Errorf("no_match.default = %v, want false", payload["default"])
	}
}

// ---------------------------------------------------------------------------
// validateConfig unit tests
// ---------------------------------------------------------------------------

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *ruleRouterConfig
		wantErr bool
	}{
		{
			name:    "valid: rules only",
			cfg:     &ruleRouterConfig{Rules: []ruleEntry{{Name: "r", When: "true", Output: "o"}}},
			wantErr: false,
		},
		{
			name:    "valid: default only",
			cfg:     &ruleRouterConfig{Default: "out"},
			wantErr: false,
		},
		{
			name:    "valid: rules and default",
			cfg:     &ruleRouterConfig{Rules: []ruleEntry{{Name: "r", When: "true", Output: "o"}}, Default: "d"},
			wantErr: false,
		},
		{
			name:    "invalid: empty",
			cfg:     &ruleRouterConfig{},
			wantErr: true,
		},
		{
			name:    "invalid: empty when",
			cfg:     &ruleRouterConfig{Rules: []ruleEntry{{Name: "r", When: "", Output: "o"}}},
			wantErr: true,
		},
		{
			name:    "invalid: empty output",
			cfg:     &ruleRouterConfig{Rules: []ruleEntry{{Name: "r", When: "true", Output: ""}}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// needsVar unit tests
// ---------------------------------------------------------------------------

func TestNeedsVar(t *testing.T) {
	rules := []ruleEntry{
		{When: `metadata["tier"] == "gold"`},
		{When: `artefacts.size() > 0`},
	}

	if !needsVar(rules, "metadata") {
		t.Error("expected metadata to be detected")
	}
	if !needsVar(rules, "artefacts") {
		t.Error("expected artefacts to be detected")
	}
	if needsVar(rules, "feedback") {
		t.Error("expected feedback to NOT be detected")
	}
	if needsVar(rules, "children") {
		t.Error("expected children to NOT be detected")
	}
	if needsVar(rules, "stamps") {
		t.Error("expected stamps to NOT be detected")
	}
}
