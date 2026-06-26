package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/handlers"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Mock contracts for integration testing
// ---------------------------------------------------------------------------

type mockEval struct{}

func (m *mockEval) Run(_ context.Context, _ *flowv1.FeedbackItem, _, _, _ string) (*flow.EvalResult, error) {
	return &flow.EvalResult{Verdict: "accept", Reason: "mock acceptance"}, nil
}

type mockFinding struct{}

func (m *mockFinding) Run(_ context.Context, _ []*flowv1.FeedbackItem) (*flow.FindingsResult, error) {
	return &flow.FindingsResult{}, nil
}

const (
	eventTypeAttestation = "appraisal.attestation"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func defaultHandlerConfig() handlers.AppraisalConfig {
	return handlers.AppraisalConfig{
		InputArtefacts:   []string{"petition"},
		ReviewArtefact:   "haiku",
		GovernedArtefact: "haiku",
		ReviewerNode:     "appraiser",
		Appraisers: []handlers.AppraiserPersonalityConfig{
			{ID: "skeptic", Personality: "You are strict but fair."},
		},
	}
}

func defaultLaws() []*flowv1.Law {
	return []*flowv1.Law{
		{Id: "L001", Group: "default", Goal: "Be concise", Tier: 1},
		{Id: "L002", Group: "default", Goal: "Be accurate", Tier: 2},
	}
}

func groupDefaultBundle() *flowv1.LawGroup {
	return &flowv1.LawGroup{Name: "default", Mode: "bundle", Passes: 1}
}

func groupSecurityLawByLaw() *flowv1.LawGroup {
	return &flowv1.LawGroup{Name: "security", Mode: "law-by-law", Passes: 1}
}

func groupSecurityBundle() *flowv1.LawGroup {
	return &flowv1.LawGroup{Name: "security", Mode: "bundle", Passes: 1}
}

func reviewOutputJSON(items ...string) string {
	feedback := make([]map[string]any, len(items))
	for i, msg := range items {
		feedback[i] = map[string]any{
			"message":    msg,
			"cited_laws": []string{},
		}
	}
	out := map[string]any{"feedback": feedback}
	b, _ := json.Marshal(out)
	return string(b)
}

// spyForHandler configures the spy for a handler test.
func spyForHandler(t *testing.T, spy *appraisalSpy) *flow.Client {
	t.Helper()
	// Override artefact contents for review-output retrieval.
	spy.ArtefactContents["review-output"] = reviewOutputJSON()
	return newSpyClientWithEventBus(t, spy)
}

// With 2 laws in "default" bundle mode, 2 appraisers, 1 pass:
// ComputeUnits: 1 unit (bundle)
// ComputeDispatchMatrix: 1 unit × 2 appraisers × 1 pass = 2 dispatches
// Expect: 2 children created.
func TestAppraisalHandler_BundleModeChildCount(t *testing.T) {
	spy := newAppraisalSpy()
	spy.Laws = defaultLaws()
	spy.LawGroups["default"] = groupDefaultBundle()
	spy.ChildStatuses = childStatusesCompleted(2)
	spy.ArtefactContents["review-output"] = reviewOutputJSON()
	client := spyForHandler(t, spy)

	cfg := defaultHandlerConfig()
	cfg.Appraisers = []handlers.AppraiserPersonalityConfig{
		{ID: "skeptic", Personality: "Strict"},
		{ID: "auditor", Personality: "Detailed"},
	}

	if err := handlers.HandleAppraisal(context.Background(), client, &mockEval{}, &mockFinding{}, cfg); err != nil {
		t.Fatalf("HandleAppraisal() error: %v", err)
	}

	if len(spy.CreatedChildren) != 2 {
		t.Fatalf("expected 2 children, got %d", len(spy.CreatedChildren))
	}
}

// With 3 laws in "security" law-by-law mode, 2 appraisers, 1 pass:
// ComputeUnits: 3 units (one per law)
// ComputeDispatchMatrix: 3 units × 2 appraisers × 1 pass = 6 dispatches
// Expect: 6 children created.
func TestAppraisalHandler_LawByLawChildCount(t *testing.T) {
	spy := newAppraisalSpy()
	spy.Laws = []*flowv1.Law{
		{Id: "L001", Group: "security", Goal: "No secrets in code", Tier: 3},
		{Id: "L002", Group: "security", Goal: "No hardcoded passwords", Tier: 3},
		{Id: "L003", Group: "security", Goal: "Use env vars", Tier: 2},
	}
	spy.LawGroups["security"] = groupSecurityLawByLaw()
	spy.ChildStatuses = childStatusesCompleted(6)
	spy.ArtefactContents["review-output"] = reviewOutputJSON()
	client := spyForHandler(t, spy)

	cfg := defaultHandlerConfig()
	cfg.Appraisers = []handlers.AppraiserPersonalityConfig{
		{ID: "skeptic", Personality: "Strict"},
		{ID: "auditor", Personality: "Detailed"},
	}
	// No "default" group in LawGroups — uses fallback defaults (bundle, 1 pass)
	// But all laws are in "security" group, so no dispatches for "default".

	if err := handlers.HandleAppraisal(context.Background(), client, &mockEval{}, &mockFinding{}, cfg); err != nil {
		t.Fatalf("HandleAppraisal() error: %v", err)
	}

	if len(spy.CreatedChildren) != 6 {
		t.Fatalf("expected 6 children, got %d", len(spy.CreatedChildren))
	}
}

// With 2 groups (bundle), 1 appraiser, 1 pass:
// ComputeUnits: 1 unit per group = 2 units
// ComputeDispatchMatrix: 2 units × 1 appraiser × 1 pass = 2 dispatches
// Expect: 2 children created.
func TestAppraisalHandler_MultiGroupChildCount(t *testing.T) {
	spy := newAppraisalSpy()
	spy.Laws = []*flowv1.Law{
		{Id: "L001", Group: "security", Goal: "Be secure", Tier: 1},
		{Id: "L002", Group: "style", Goal: "Be clean", Tier: 1},
	}
	spy.LawGroups["security"] = groupSecurityBundle()
	spy.LawGroups["style"] = &flowv1.LawGroup{Name: "style", Mode: "bundle", Passes: 1}
	spy.ChildStatuses = childStatusesCompleted(2)
	spy.ArtefactContents["review-output"] = reviewOutputJSON()
	client := spyForHandler(t, spy)

	cfg := defaultHandlerConfig()
	// Only 1 appraiser.
	cfg.Appraisers = []handlers.AppraiserPersonalityConfig{
		{ID: "skeptic", Personality: "Strict"},
	}

	if err := handlers.HandleAppraisal(context.Background(), client, &mockEval{}, &mockFinding{}, cfg); err != nil {
		t.Fatalf("HandleAppraisal() error: %v", err)
	}

	if len(spy.CreatedChildren) != 2 {
		t.Fatalf("expected 2 children, got %d", len(spy.CreatedChildren))
	}
}

// All children complete → stamps applied, coverage + attestation events emitted.
func TestAppraisalHandler_AllCompleteStampsAndEvents(t *testing.T) {
	spy := newAppraisalSpy()
	spy.Laws = defaultLaws()
	spy.LawGroups["default"] = groupDefaultBundle()
	spy.ChildStatuses = childStatusesCompleted(1) // 1 appraiser × 1 pass × 1 unit
	spy.ArtefactContents["review-output"] = reviewOutputJSON()
	client := spyForHandler(t, spy)

	cfg := defaultHandlerConfig()
	cfg.Appraisers = []handlers.AppraiserPersonalityConfig{
		{ID: "skeptic", Personality: "Strict"},
	}

	if err := handlers.HandleAppraisal(context.Background(), client, &mockEval{}, &mockFinding{}, cfg); err != nil {
		t.Fatalf("HandleAppraisal() error: %v", err)
	}

	// Should have at least one appraise stamp.
	found := slices.Contains(spy.StampedArtefacts, "appraise-default")
	if !found {
		t.Fatalf("expected stamp 'appraise-default', got stamps: %v", spy.StampedArtefacts)
	}

	// Should have two events: coverage and attestation.
	if len(spy.PublishedEvents) < 2 {
		t.Fatalf("expected at least 2 published events, got %d", len(spy.PublishedEvents))
	}

	eventTypes := make(map[string]bool)
	for _, e := range spy.PublishedEvents {
		eventTypes[e.GetEventType()] = true
	}
	if !eventTypes["appraisal.coverage"] {
		t.Error("expected appraisal.coverage event")
	}
	if !eventTypes[eventTypeAttestation] {
		t.Error("expected appraisal.attestation event")
	}

	// Verify attestation payload.
	var attestPayload map[string]any
	for _, e := range spy.PublishedEvents {
		if e.GetEventType() == eventTypeAttestation {
			if err := json.Unmarshal(e.GetPayload(), &attestPayload); err != nil {
				t.Fatalf("unmarshal attestation payload: %v", err)
			}
			break
		}
	}
	if attestPayload == nil {
		t.Fatal("attestation payload not found")
	}
	if status, ok := attestPayload["status"]; !ok || status != "pass" {
		t.Fatalf("expected attestation status 'pass', got %v", status)
	}
}

// All children fail → no stamps, attestation status "incomplete".
func TestAppraisalHandler_AllChildrenFail(t *testing.T) {
	spy := newAppraisalSpy()
	spy.Laws = defaultLaws()
	spy.LawGroups["default"] = groupDefaultBundle()
	spy.ChildStatuses = childStatusesFailed(1)
	spy.ArtefactContents["review-output"] = reviewOutputJSON()
	client := spyForHandler(t, spy)

	cfg := defaultHandlerConfig()
	cfg.Appraisers = []handlers.AppraiserPersonalityConfig{
		{ID: "skeptic", Personality: "Strict"},
	}

	if err := handlers.HandleAppraisal(context.Background(), client, &mockEval{}, &mockFinding{}, cfg); err != nil {
		t.Fatalf("HandleAppraisal() error: %v", err)
	}

	// No stamps should be applied when all children fail.
	if len(spy.StampedArtefacts) > 0 {
		t.Fatalf("expected no stamps, got %v", spy.StampedArtefacts)
	}

	// Attestation should have status "incomplete".
	var attestPayload map[string]any
	for _, e := range spy.PublishedEvents {
		if e.GetEventType() == eventTypeAttestation {
			if err := json.Unmarshal(e.GetPayload(), &attestPayload); err != nil {
				t.Fatalf("unmarshal attestation payload: %v", err)
			}
			break
		}
	}
	if attestPayload == nil {
		t.Fatal("attestation payload not found")
	}
	if status, ok := attestPayload["status"]; !ok || status != "incomplete" {
		t.Fatalf("expected attestation status 'incomplete', got %v", status)
	}
}

// Partial failure: one group completes, another fails.
// Only the successful group gets stamped.
func TestAppraisalHandler_PartialFailure(t *testing.T) {
	spy := newAppraisalSpy()
	spy.Laws = []*flowv1.Law{
		{Id: "L001", Group: "security", Goal: "Be secure", Tier: 1},
		{Id: "L002", Group: "style", Goal: "Be clean", Tier: 1},
	}
	spy.LawGroups["security"] = groupSecurityBundle()
	spy.LawGroups["style"] = &flowv1.LawGroup{Name: "style", Mode: "bundle", Passes: 1}
	// 2 dispatches: [0]=security, [1]=style
	// security child fails, style child completes.
	spy.ChildStatuses = []*flowv1.ChildWorkitemStatus{
		{WorkitemId: "child-0", Phase: "Failed"},
		{WorkitemId: "child-1", Phase: "Completed"},
	}
	// Only child-1 has review-output.
	spy.ArtefactContents["review-output"] = reviewOutputJSON("some feedback")
	client := spyForHandler(t, spy)

	cfg := defaultHandlerConfig()
	cfg.Appraisers = []handlers.AppraiserPersonalityConfig{
		{ID: "skeptic", Personality: "Strict"},
	}

	if err := handlers.HandleAppraisal(context.Background(), client, &mockEval{}, &mockFinding{}, cfg); err != nil {
		t.Fatalf("HandleAppraisal() error: %v", err)
	}

	// Only "style" group should have a stamp (security failed).
	hasSecurityStamp := false
	hasStyleStamp := false
	for _, s := range spy.StampedArtefacts {
		switch s {
		case "appraise-security":
			hasSecurityStamp = true
		case "appraise-style":
			hasStyleStamp = true
		}
	}
	if hasSecurityStamp {
		t.Error("expected NO stamp for failed 'security' group")
	}
	if !hasStyleStamp {
		t.Error("expected stamp for 'style' group")
	}
}

// Empty appraiser list → zero children, clean completion.
func TestAppraisalHandler_EmptyAppraisers(t *testing.T) {
	spy := newAppraisalSpy()
	spy.Laws = defaultLaws()
	spy.LawGroups["default"] = groupDefaultBundle()
	client := newSpyClientWithEventBus(t, spy)

	cfg := defaultHandlerConfig()
	cfg.Appraisers = nil // no appraisers

	if err := handlers.HandleAppraisal(context.Background(), client, &mockEval{}, &mockFinding{}, cfg); err != nil {
		t.Fatalf("HandleAppraisal() error: %v", err)
	}

	if len(spy.CreatedChildren) != 0 {
		t.Fatalf("expected 0 children, got %d", len(spy.CreatedChildren))
	}
	// No stamps, no events.
	if len(spy.StampedArtefacts) != 0 {
		t.Fatalf("expected no stamps, got %v", spy.StampedArtefacts)
	}
}

// PublishAuditEvent failure is tolerated: handler succeeds and stamps still apply.
func TestAppraisal_PublishAuditEventFailure(t *testing.T) {
	spy := newAppraisalSpy()
	spy.PublishFail = true
	spy.Laws = defaultLaws()
	spy.LawGroups["default"] = groupDefaultBundle()
	spy.ChildStatuses = childStatusesCompleted(1)
	spy.ArtefactContents["review-output"] = reviewOutputJSON()
	client := spyForHandler(t, spy)

	cfg := defaultHandlerConfig()
	cfg.Appraisers = []handlers.AppraiserPersonalityConfig{
		{ID: "skeptic", Personality: "Strict"},
	}

	if err := handlers.HandleAppraisal(context.Background(), client, &mockEval{}, &mockFinding{}, cfg); err != nil {
		t.Fatalf("HandleAppraisal() error: %v", err)
	}

	// Stamps must still be applied despite Publish failure.
	if !slices.Contains(spy.StampedArtefacts, "appraise-default") {
		t.Fatalf("expected stamp 'appraise-default', got stamps: %v", spy.StampedArtefacts)
	}
}

// Coverage event payload correctness.
func TestAppraisalHandler_CoveragePayload(t *testing.T) {
	spy := newAppraisalSpy()
	spy.Laws = defaultLaws()
	spy.LawGroups["default"] = groupDefaultBundle()
	spy.ChildStatuses = childStatusesCompleted(1)
	client := spyForHandler(t, spy)
	// Set review-output AFTER spyForHandler (which resets it).
	spy.ArtefactContents["review-output"] = reviewOutputJSON("issue 1", "issue 2")

	cfg := defaultHandlerConfig()
	cfg.Appraisers = []handlers.AppraiserPersonalityConfig{
		{ID: "skeptic", Personality: "Strict"},
	}

	if err := handlers.HandleAppraisal(context.Background(), client, &mockEval{}, &mockFinding{}, cfg); err != nil {
		t.Fatalf("HandleAppraisal() error: %v", err)
	}

	// Find coverage event.
	var coveragePayload map[string]any
	for _, e := range spy.PublishedEvents {
		if e.GetEventType() == "appraisal.coverage" {
			if err := json.Unmarshal(e.GetPayload(), &coveragePayload); err != nil {
				t.Fatalf("unmarshal coverage payload: %v", err)
			}
			break
		}
	}
	if coveragePayload == nil {
		t.Fatal("coverage payload not found")
	}

	if stage, ok := coveragePayload["stage"]; !ok || stage != "appraisal" {
		t.Fatalf("expected stage 'appraisal', got %v", stage)
	}
	units, ok := coveragePayload["units"].([]any)
	if !ok {
		t.Fatal("expected units array")
	}
	if len(units) != 1 {
		t.Fatalf("expected 1 unit, got %d", len(units))
	}
	unit, ok := units[0].(map[string]any)
	if !ok {
		t.Fatal("expected unit to be a map")
	}
	if v, ok := unit["violations"]; !ok || int(v.(float64)) != 2 {
		t.Fatalf("expected violations=2, got %v", v)
	}
}

// Attestation event with violations.
func TestAppraisalHandler_AttestationWithViolations(t *testing.T) {
	spy := newAppraisalSpy()
	spy.Laws = defaultLaws()
	spy.LawGroups["default"] = groupDefaultBundle()
	spy.ChildStatuses = childStatusesCompleted(1)
	client := spyForHandler(t, spy)
	spy.ArtefactContents["review-output"] = reviewOutputJSON("violation 1", "violation 2", "violation 3")

	cfg := defaultHandlerConfig()
	cfg.Appraisers = []handlers.AppraiserPersonalityConfig{
		{ID: "skeptic", Personality: "Strict"},
	}

	if err := handlers.HandleAppraisal(context.Background(), client, &mockEval{}, &mockFinding{}, cfg); err != nil {
		t.Fatalf("HandleAppraisal() error: %v", err)
	}

	var attestPayload map[string]any
	for _, e := range spy.PublishedEvents {
		if e.GetEventType() == eventTypeAttestation {
			if err := json.Unmarshal(e.GetPayload(), &attestPayload); err != nil {
				t.Fatalf("unmarshal attestation payload: %v", err)
			}
			break
		}
	}
	if attestPayload == nil {
		t.Fatal("attestation payload not found")
	}

	if status, ok := attestPayload["status"]; !ok || status != "fail" {
		t.Fatalf("expected attestation status 'fail', got %v", status)
	}
	if vt, ok := attestPayload["violations_total"]; !ok || int(vt.(float64)) != 3 {
		t.Fatalf("expected violations_total=3, got %v", vt)
	}

	verdicts, ok := attestPayload["appraiser_verdicts"].([]any)
	if !ok || len(verdicts) == 0 {
		t.Fatal("expected non-empty appraiser_verdicts")
	}
}

// Law-by-law partial failure: only successful laws get per-law stamps.
func TestAppraisalHandler_LawByLawPartialFailure(t *testing.T) {
	spy := newAppraisalSpy()
	spy.Laws = []*flowv1.Law{
		{Id: "L001", Group: "security", Goal: "No secrets", Tier: 3},
		{Id: "L002", Group: "security", Goal: "No passwords", Tier: 3},
	}
	spy.LawGroups["security"] = groupSecurityLawByLaw()
	// 2 dispatches: [0]=L001 unit, [1]=L002 unit
	// L001 child fails, L002 child completes.
	spy.ChildStatuses = []*flowv1.ChildWorkitemStatus{
		{WorkitemId: "child-0", Phase: "Failed"},
		{WorkitemId: "child-1", Phase: "Completed"},
	}
	client := spyForHandler(t, spy)
	spy.ArtefactContents["review-output"] = reviewOutputJSON()

	cfg := defaultHandlerConfig()
	cfg.Appraisers = []handlers.AppraiserPersonalityConfig{
		{ID: "skeptic", Personality: "Strict"},
	}

	if err := handlers.HandleAppraisal(context.Background(), client, &mockEval{}, &mockFinding{}, cfg); err != nil {
		t.Fatalf("HandleAppraisal() error: %v", err)
	}

	// L001 failed → no stamp for it. L002 completed → stamp for it.
	// Group also failed (L001 failure) → no group stamp.
	hasGroupStamp := false
	hasL001Stamp := false
	hasL002Stamp := false
	for _, s := range spy.StampedArtefacts {
		switch s {
		case "appraise-security":
			hasGroupStamp = true
		case "appraise-security-L001":
			hasL001Stamp = true
		case "appraise-security-L002":
			hasL002Stamp = true
		}
	}
	if hasGroupStamp {
		t.Error("expected no group stamp when one law failed")
	}
	if hasL001Stamp {
		t.Error("expected no stamp for failed law L001")
	}
	if !hasL002Stamp {
		t.Error("expected stamp for completed law L002")
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func childStatusesCompleted(n int) []*flowv1.ChildWorkitemStatus {
	statuses := make([]*flowv1.ChildWorkitemStatus, n)
	for i := range n {
		statuses[i] = &flowv1.ChildWorkitemStatus{
			WorkitemId: fmt.Sprintf("child-%d", i),
			Phase:      "Completed",
		}
	}
	return statuses
}

func childStatusesFailed(n int) []*flowv1.ChildWorkitemStatus {
	statuses := make([]*flowv1.ChildWorkitemStatus, n)
	for i := range n {
		statuses[i] = &flowv1.ChildWorkitemStatus{
			WorkitemId: fmt.Sprintf("child-%d", i),
			Phase:      "Failed",
		}
	}
	return statuses
}
