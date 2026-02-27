package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"text/template"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Mock Model (same pattern as juror/forge tests)
// ---------------------------------------------------------------------------

type mockModel struct {
	output         *flow.InferOutput
	err            error
	capturedSystem string
	capturedQuery  []byte
}

func (m *mockModel) Infer(_ context.Context, systemPrompt string, query []byte) (*flow.InferOutput, error) {
	m.capturedSystem = systemPrompt
	m.capturedQuery = query
	if m.err != nil {
		return nil, m.err
	}
	return m.output, nil
}

// ---------------------------------------------------------------------------
// Happy Path: Create action (with codification)
// ---------------------------------------------------------------------------

func TestClerk_HappyPath_Create(t *testing.T) {
	spy := newClerkSpy()

	deliberation := &deliberationResult{
		Outcome: "favour_refiner",
		Justifications: []jurorJustification{
			{JurorID: "juror-1", Outcome: "favour_refiner", Reasoning: "Strong argument"},
			{JurorID: "juror-2", Outcome: "favour_refiner", Reasoning: "Agreed"},
		},
		RoundsUsed: 1,
		Hung:       false,
	}
	vctx := &verdictContext{
		Trigger:        "deadlock-resolution",
		SourceWorkitem: "wi-123",
		Goal:           "Enforce consistent naming",
		AppliesTo:      []string{"haiku"},
		Tier:           2,
		Action:         "create",
	}
	seedClerkArtefacts(spy, deliberation, vctx)

	// Seed codification result for the auto-created child.
	seedCodificationResults(spy, "child-1", petitionRep{
		Type:    "application/smt",
		Content: "(assert (= naming consistent))",
	})

	client := setupClerkTest(t, spy)

	// Build agent with mock model.
	proseJSON := `{"prose_justification":"The jury found in favour of consistent naming enforcement."}`
	mm := &mockModel{
		output: &flow.InferOutput{Output: []byte(proseJSON)},
	}

	schema, err := buildProseSchema()
	if err != nil {
		t.Fatalf("buildProseSchema: %v", err)
	}
	queryTmpl, err := template.New("clerk-query").Parse(queryTemplateText)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	agent, err := flow.NewAgent(client,
		flow.WithSchema(schema),
		flow.WithModel(mm),
		flow.WithSystemPrompt("test"),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	// Run prose drafting.
	prose, err := runDraftProse(context.Background(), agent, deliberation, vctx, "")
	if err != nil {
		t.Fatalf("runDraftProse: %v", err)
	}

	if prose != "The jury found in favour of consistent naming enforcement." {
		t.Errorf("prose = %q, unexpected", prose)
	}

	// Now test the full handleClerk with fan-out to one codification node.
	// Reset spy for clean tracking.
	spy2 := newClerkSpy()
	seedClerkArtefacts(spy2, deliberation, vctx)
	seedCodificationResults(spy2, "child-1", petitionRep{
		Type:    "application/smt",
		Content: "(assert (= naming consistent))",
	})

	client2 := setupClerkTest(t, spy2)
	cfg := &clerkConfig{
		CodificationNodes: []string{"codify-smt"},
	}

	// We can't inject the mock model into handleClerk directly because it
	// creates its own agent. Instead, test the assembly path by calling
	// assemblePetition directly.
	reps := []petitionRep{
		{Type: "application/smt", Content: "(assert (= naming consistent))"},
	}
	err = assemblePetition(
		context.Background(), client2, cfg,
		deliberation, vctx,
		"The jury found in favour of consistent naming enforcement.",
		reps,
	)
	if err != nil {
		t.Fatalf("assemblePetition: %v", err)
	}

	// Verify petition was stored.
	stored, ok := spy2.StoredArtefacts[artefactPetition]
	if !ok {
		t.Fatal("petition artefact was not stored")
	}

	var p petition
	if err := json.Unmarshal(stored, &p); err != nil {
		t.Fatalf("unmarshal petition: %v", err)
	}

	if p.Petition.Context.Trigger != "deadlock-resolution" {
		t.Errorf("trigger = %q, want %q", p.Petition.Context.Trigger, "deadlock-resolution")
	}
	if p.Petition.Context.Verdict != "favour_refiner" {
		t.Errorf("verdict = %q, want %q", p.Petition.Context.Verdict, "favour_refiner")
	}
	if len(p.Petition.Changes) != 1 {
		t.Fatalf("changes count = %d, want 1", len(p.Petition.Changes))
	}
	change := p.Petition.Changes[0]
	if change.Action != "create" {
		t.Errorf("action = %q, want %q", change.Action, "create")
	}
	if change.Goal != "Enforce consistent naming" {
		t.Errorf("goal = %q, want %q", change.Goal, "Enforce consistent naming")
	}
	if change.Tier != 2 {
		t.Errorf("tier = %d, want 2", change.Tier)
	}

	// Should have markdown + smt representations.
	if len(change.Representations) != 2 {
		t.Fatalf("representations count = %d, want 2", len(change.Representations))
	}
	if change.Representations[0].Type != "text/markdown" {
		t.Errorf("rep[0] type = %q, want text/markdown", change.Representations[0].Type)
	}
	if change.Representations[1].Type != "application/smt" {
		t.Errorf("rep[1] type = %q, want application/smt", change.Representations[1].Type)
	}

	// Verify routed to default output.
	assertClerkRoutedTo(t, spy2, "default")
}

// ---------------------------------------------------------------------------
// Happy Path: Retire action (no codification)
// ---------------------------------------------------------------------------

func TestClerk_HappyPath_Retire(t *testing.T) {
	spy := newClerkSpy()

	deliberation := &deliberationResult{
		Outcome: "retire",
		Justifications: []jurorJustification{
			{JurorID: "juror-1", Outcome: "retire", Reasoning: "Law is obsolete"},
		},
		RoundsUsed: 1,
	}
	vctx := &verdictContext{
		Trigger: "friction-hearing",
		Goal:    "Retire obsolete formatting rule",
		LawID:   "law-42",
		Action:  "retire",
	}
	seedClerkArtefacts(spy, deliberation, vctx)

	client := setupClerkTest(t, spy)
	cfg := &clerkConfig{CodificationNodes: []string{"codify-smt"}} // Should be skipped for retire.

	err := handleRetire(
		context.Background(), client, cfg,
		deliberation, vctx,
		"The law is obsolete and should be retired.",
	)
	if err != nil {
		t.Fatalf("handleRetire: %v", err)
	}

	// Verify petition was stored.
	stored, ok := spy.StoredArtefacts[artefactPetition]
	if !ok {
		t.Fatal("petition artefact was not stored")
	}

	var p petition
	if err := json.Unmarshal(stored, &p); err != nil {
		t.Fatalf("unmarshal petition: %v", err)
	}

	if len(p.Petition.Changes) != 1 {
		t.Fatalf("changes count = %d, want 1", len(p.Petition.Changes))
	}
	if p.Petition.Changes[0].Action != "retire" {
		t.Errorf("action = %q, want %q", p.Petition.Changes[0].Action, "retire")
	}
	if p.Petition.Changes[0].LawID != "law-42" {
		t.Errorf("law_id = %q, want %q", p.Petition.Changes[0].LawID, "law-42")
	}
	if len(p.Petition.Changes[0].Representations) != 0 {
		t.Errorf("retire should have no representations, got %d", len(p.Petition.Changes[0].Representations))
	}

	assertClerkRoutedTo(t, spy, "default")
}

// ---------------------------------------------------------------------------
// Happy Path: Demote action
// ---------------------------------------------------------------------------

func TestClerk_HappyPath_Demote(t *testing.T) {
	spy := newClerkSpy()

	deliberation := &deliberationResult{
		Outcome: "demote",
		Justifications: []jurorJustification{
			{JurorID: "juror-1", Outcome: "demote", Reasoning: "Not enough evidence for tier 2"},
		},
		RoundsUsed: 2,
	}
	vctx := &verdictContext{
		Trigger:   "friction-hearing",
		Goal:      "Demote overreaching naming rule",
		AppliesTo: []string{"haiku"},
		Tier:      1, // Demote to tier 1.
		LawID:     "law-99",
		Action:    "demote",
	}
	seedClerkArtefacts(spy, deliberation, vctx)

	client := setupClerkTest(t, spy)
	cfg := &clerkConfig{}

	err := assemblePetition(
		context.Background(), client, cfg,
		deliberation, vctx,
		"The law should be demoted to tier 1.",
		nil, // No codification for demote.
	)
	if err != nil {
		t.Fatalf("assemblePetition: %v", err)
	}

	stored, ok := spy.StoredArtefacts[artefactPetition]
	if !ok {
		t.Fatal("petition artefact was not stored")
	}

	var p petition
	if err := json.Unmarshal(stored, &p); err != nil {
		t.Fatalf("unmarshal petition: %v", err)
	}

	change := p.Petition.Changes[0]
	if change.Action != "demote" {
		t.Errorf("action = %q, want demote", change.Action)
	}
	if change.LawID != "law-99" {
		t.Errorf("law_id = %q, want law-99", change.LawID)
	}
	if change.Tier != 1 {
		t.Errorf("tier = %d, want 1", change.Tier)
	}
}

// ---------------------------------------------------------------------------
// Happy Path: No codification nodes configured
// ---------------------------------------------------------------------------

func TestClerk_NoCodificationNodes(t *testing.T) {
	spy := newClerkSpy()

	deliberation := &deliberationResult{
		Outcome: "create",
		Justifications: []jurorJustification{
			{JurorID: "juror-1", Outcome: "create", Reasoning: "Good idea"},
		},
		RoundsUsed: 1,
	}
	vctx := &verdictContext{
		Trigger: "deadlock-resolution",
		Goal:    "Simple rule",
		Action:  "create",
		Tier:    1,
	}
	seedClerkArtefacts(spy, deliberation, vctx)

	client := setupClerkTest(t, spy)
	cfg := &clerkConfig{} // No codification nodes.

	err := assemblePetition(
		context.Background(), client, cfg,
		deliberation, vctx,
		"Simple prose justification.",
		nil,
	)
	if err != nil {
		t.Fatalf("assemblePetition: %v", err)
	}

	stored := spy.StoredArtefacts[artefactPetition]
	var p petition
	if err := json.Unmarshal(stored, &p); err != nil {
		t.Fatalf("unmarshal petition: %v", err)
	}

	// Should only have markdown representation.
	if len(p.Petition.Changes[0].Representations) != 1 {
		t.Fatalf("representations count = %d, want 1", len(p.Petition.Changes[0].Representations))
	}
	if p.Petition.Changes[0].Representations[0].Type != "text/markdown" {
		t.Errorf("rep type = %q, want text/markdown", p.Petition.Changes[0].Representations[0].Type)
	}
}

// ---------------------------------------------------------------------------
// Revision path: existing feedback
// ---------------------------------------------------------------------------

func TestClerk_RevisionPath_WithFeedback(t *testing.T) {
	spy := newClerkSpy()

	deliberation := &deliberationResult{
		Outcome: "create",
		Justifications: []jurorJustification{
			{JurorID: "juror-1", Outcome: "create", Reasoning: "Good rule"},
		},
		RoundsUsed: 1,
	}
	vctx := &verdictContext{
		Trigger: "deadlock-resolution",
		Goal:    "Naming convention",
		Action:  "create",
		Tier:    2,
	}
	seedClerkArtefacts(spy, deliberation, vctx)

	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:       "fb-1",
			Severity: flowv1.Severity_SEVERITY_HIGH,
			Message:  "Petition is too vague. Be more specific.",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_NEW,
		},
	}

	client := setupClerkTest(t, spy)

	// Test that feedback is formatted correctly.
	items := spy.FeedbackItems
	formatted := formatFeedback(items)
	if formatted == "" {
		t.Fatal("feedback should not be empty")
	}
	if !containsSubstring(formatted, "too vague") {
		t.Error("formatted feedback should contain the feedback message")
	}

	// Test prose drafting with feedback.
	proseJSON := `{"prose_justification":"Revised: enforce consistent camelCase naming for all variables."}`
	mm := &mockModel{
		output: &flow.InferOutput{Output: []byte(proseJSON)},
	}

	schema, err := buildProseSchema()
	if err != nil {
		t.Fatalf("buildProseSchema: %v", err)
	}
	queryTmpl, err := template.New("clerk-query").Parse(queryTemplateText)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	agent, err := flow.NewAgent(client,
		flow.WithSchema(schema),
		flow.WithModel(mm),
		flow.WithSystemPrompt("test"),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	prose, err := runDraftProse(context.Background(), agent, deliberation, vctx, formatted)
	if err != nil {
		t.Fatalf("runDraftProse: %v", err)
	}
	if prose != "Revised: enforce consistent camelCase naming for all variables." {
		t.Errorf("prose = %q, unexpected", prose)
	}

	// Verify the query included the feedback.
	queryStr := string(mm.capturedQuery)
	if !containsSubstring(queryStr, "Previous Tribunal Feedback") {
		t.Error("query should include feedback section")
	}
	if !containsSubstring(queryStr, "too vague") {
		t.Error("query should include feedback content")
	}
}

// ---------------------------------------------------------------------------
// Error: deliberation-result missing
// ---------------------------------------------------------------------------

func TestClerk_Error_DeliberationResultMissing(t *testing.T) {
	spy := newClerkSpy()
	// No artefacts seeded.

	client := setupClerkTest(t, spy)
	cfg := &clerkConfig{}

	err := handleClerk(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when deliberation-result missing")
	}
	if !containsSubstring(err.Error(), "deliberation-result") {
		t.Errorf("error should mention deliberation-result: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Error: verdict-context missing
// ---------------------------------------------------------------------------

func TestClerk_Error_VerdictContextMissing(t *testing.T) {
	spy := newClerkSpy()
	deliberation := &deliberationResult{Outcome: "create", RoundsUsed: 1}
	deliberationJSON, _ := json.Marshal(deliberation)
	spy.Artefacts[artefactDeliberationResult] = deliberationJSON
	// No verdict-context artefact.

	client := setupClerkTest(t, spy)
	cfg := &clerkConfig{}

	err := handleClerk(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when verdict-context missing")
	}
	if !containsSubstring(err.Error(), "verdict-context") {
		t.Errorf("error should mention verdict-context: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Error: agent inference fails
// ---------------------------------------------------------------------------

func TestClerk_Error_AgentInferFails(t *testing.T) {
	spy := newClerkSpy()
	deliberation := &deliberationResult{
		Outcome:    "create",
		RoundsUsed: 1,
		Justifications: []jurorJustification{
			{JurorID: "j1", Outcome: "create", Reasoning: "r"},
		},
	}
	vctx := &verdictContext{
		Trigger: "deadlock-resolution",
		Goal:    "test",
		Action:  "create",
	}
	seedClerkArtefacts(spy, deliberation, vctx)

	client := setupClerkTest(t, spy)

	mm := &mockModel{err: fmt.Errorf("inference exploded")}

	schema, err := buildProseSchema()
	if err != nil {
		t.Fatalf("buildProseSchema: %v", err)
	}
	queryTmpl, err := template.New("clerk-query").Parse(queryTemplateText)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	agent, err := flow.NewAgent(client,
		flow.WithSchema(schema),
		flow.WithModel(mm),
		flow.WithSystemPrompt("test"),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	_, runErr := runDraftProse(context.Background(), agent, deliberation, vctx, "")
	if runErr == nil {
		t.Fatal("expected error when inference fails")
	}
	if !containsSubstring(runErr.Error(), "agent run") {
		t.Errorf("error should mention agent run: %v", runErr)
	}
}

// ---------------------------------------------------------------------------
// Error: store petition fails
// ---------------------------------------------------------------------------

func TestClerk_Error_StorePetitionFails(t *testing.T) {
	spy := newClerkSpy()
	deliberation := &deliberationResult{Outcome: "retire", RoundsUsed: 1}
	vctx := &verdictContext{Action: "retire", LawID: "law-1"}
	seedClerkArtefacts(spy, deliberation, vctx)

	spy.StoreArtefactErr = status.Errorf(codes.Internal, "archivist down")

	client := setupClerkTest(t, spy)
	cfg := &clerkConfig{}

	err := handleRetire(
		context.Background(), client, cfg,
		deliberation, vctx,
		"Retire this law.",
	)
	if err == nil {
		t.Fatal("expected error when store petition fails")
	}
	if !containsSubstring(err.Error(), "store petition") {
		t.Errorf("error should mention store petition: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Error: route to output fails
// ---------------------------------------------------------------------------

func TestClerk_Error_RouteToOutputFails(t *testing.T) {
	spy := newClerkSpy()
	deliberation := &deliberationResult{Outcome: "retire", RoundsUsed: 1}
	vctx := &verdictContext{Action: "retire", LawID: "law-1"}
	seedClerkArtefacts(spy, deliberation, vctx)

	spy.RouteToOutputErr = status.Errorf(codes.Internal, "operator down")

	client := setupClerkTest(t, spy)
	cfg := &clerkConfig{}

	err := handleRetire(
		context.Background(), client, cfg,
		deliberation, vctx,
		"Retire this law.",
	)
	if err == nil {
		t.Fatal("expected error when route fails")
	}
	if !containsSubstring(err.Error(), "route to output") {
		t.Errorf("error should mention route to output: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Error: codification fan-out create child fails
// ---------------------------------------------------------------------------

func TestClerk_Error_FanOutCreateChildFails(t *testing.T) {
	spy := newClerkSpy()
	spy.CreateChildErr = status.Errorf(codes.Internal, "operator down")

	client := setupClerkTest(t, spy)
	cfg := &clerkConfig{CodificationNodes: []string{"codify-smt"}}

	vctx := &verdictContext{Goal: "test", Action: "create"}
	_, err := fanOutCodification(context.Background(), client, cfg, vctx)
	if err == nil {
		t.Fatal("expected error when create child fails")
	}
	if !containsSubstring(err.Error(), "fan-out") {
		t.Errorf("error should mention fan-out: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Codification fan-out: happy path
// ---------------------------------------------------------------------------

func TestClerk_FanOutCodification_HappyPath(t *testing.T) {
	spy := newClerkSpy()

	// Seed codification results for auto-created children.
	seedCodificationResults(spy, "child-1", petitionRep{
		Type:    "application/smt",
		Content: "(assert true)",
	})
	seedCodificationResults(spy, "child-2", petitionRep{
		Type:    "application/rego",
		Content: "package test\ndefault allow = false",
	})

	client := setupClerkTest(t, spy)
	cfg := &clerkConfig{CodificationNodes: []string{"codify-smt", "codify-rego"}}

	vctx := &verdictContext{
		Goal:      "Test goal",
		AppliesTo: []string{"haiku"},
		Tier:      2,
		Action:    "create",
	}

	reps, err := fanOutCodification(context.Background(), client, cfg, vctx)
	if err != nil {
		t.Fatalf("fanOutCodification: %v", err)
	}

	// Verify two children were created and routed.
	if len(spy.CreatedChildren) != 2 {
		t.Fatalf("created children = %d, want 2", len(spy.CreatedChildren))
	}
	if len(spy.RoutedChildren) != 2 {
		t.Fatalf("routed children = %d, want 2", len(spy.RoutedChildren))
	}
	if spy.RoutedChildren[0].TargetNode != "codify-smt" {
		t.Errorf("child 1 target = %q, want codify-smt", spy.RoutedChildren[0].TargetNode)
	}
	if spy.RoutedChildren[1].TargetNode != "codify-rego" {
		t.Errorf("child 2 target = %q, want codify-rego", spy.RoutedChildren[1].TargetNode)
	}

	// Verify codification results were collected.
	if len(reps) != 2 {
		t.Fatalf("representations = %d, want 2", len(reps))
	}
	if reps[0].Type != "application/smt" {
		t.Errorf("rep[0] type = %q, want application/smt", reps[0].Type)
	}
	if reps[1].Type != "application/rego" {
		t.Errorf("rep[1] type = %q, want application/rego", reps[1].Type)
	}

	// Verify codification goal artefact was stored on children.
	for _, childID := range spy.CreatedChildren {
		key := childID + ":" + artefactCodificationGoal
		content, ok := spy.ChildStoredArtefacts[key]
		if !ok {
			t.Errorf("codification-goal not stored on child %s", childID)
			continue
		}
		var goal map[string]any
		if err := json.Unmarshal(content, &goal); err != nil {
			t.Errorf("invalid codification-goal JSON on child %s: %v", childID, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Markdown prose drafting (ported from deleted clerk server)
// ---------------------------------------------------------------------------

func TestDraftMarkdownProse(t *testing.T) {
	deliberation := &deliberationResult{
		Outcome: "favour_refiner",
		Justifications: []jurorJustification{
			{JurorID: "juror-1", Outcome: "favour_refiner", Reasoning: "Strong citation"},
			{JurorID: "juror-2", Outcome: "favour_reviewer", Reasoning: "Weak rebuttal"},
		},
		RoundsUsed: 2,
	}

	prose := draftMarkdownProse("Enforce naming conventions", deliberation)

	if !containsSubstring(prose, "# Law") {
		t.Error("prose should contain # Law header")
	}
	if !containsSubstring(prose, "Enforce naming conventions") {
		t.Error("prose should contain goal")
	}
	if !containsSubstring(prose, "favour_refiner") {
		t.Error("prose should contain outcome")
	}
	if !containsSubstring(prose, "juror-1") {
		t.Error("prose should contain juror IDs")
	}
	if !containsSubstring(prose, "Strong citation") {
		t.Error("prose should contain juror reasoning")
	}
}

// ---------------------------------------------------------------------------
// Config defaults
// ---------------------------------------------------------------------------

func TestClerkConfig_Defaults(t *testing.T) {
	cfg := &clerkConfig{}
	if cfg.codificationPrefix() != "codification" {
		t.Errorf("codificationPrefix() = %q, want %q", cfg.codificationPrefix(), "codification")
	}
	if cfg.defaultOutput() != "default" {
		t.Errorf("defaultOutput() = %q, want %q", cfg.defaultOutput(), "default")
	}
}

func TestClerkConfig_CustomValues(t *testing.T) {
	cfg := &clerkConfig{
		CodificationArtefactPrefix: "custom-codify",
		DefaultOutput:              "tribunal-review",
	}
	if cfg.codificationPrefix() != "custom-codify" {
		t.Errorf("codificationPrefix() = %q, want %q", cfg.codificationPrefix(), "custom-codify")
	}
	if cfg.defaultOutput() != "tribunal-review" {
		t.Errorf("defaultOutput() = %q, want %q", cfg.defaultOutput(), "tribunal-review")
	}
}

// ---------------------------------------------------------------------------
// Build prose schema
// ---------------------------------------------------------------------------

func TestBuildProseSchema(t *testing.T) {
	schemaBytes, err := buildProseSchema()
	if err != nil {
		t.Fatalf("buildProseSchema: %v", err)
	}

	var schema map[string]any
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema missing properties")
	}
	if _, ok := props["prose_justification"]; !ok {
		t.Fatal("schema missing prose_justification property")
	}

	required, ok := schema["required"].([]any)
	if !ok || len(required) != 1 {
		t.Fatal("schema should require exactly one field")
	}
}

// ---------------------------------------------------------------------------
// Format feedback
// ---------------------------------------------------------------------------

func TestFormatFeedback(t *testing.T) {
	items := []*flowv1.FeedbackItem{
		{
			Severity: flowv1.Severity_SEVERITY_HIGH,
			Message:  "Too vague",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_NEW,
		},
		{
			Severity: flowv1.Severity_SEVERITY_MEDIUM,
			Message:  "Consider restructuring",
			State:    flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
		},
	}

	result := formatFeedback(items)
	if !containsSubstring(result, "Too vague") {
		t.Error("should contain first feedback message")
	}
	if !containsSubstring(result, "Consider restructuring") {
		t.Error("should contain second feedback message")
	}
}

func TestFormatFeedback_Empty(t *testing.T) {
	result := formatFeedback(nil)
	if result != "" {
		t.Errorf("empty feedback should produce empty string, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// Petition context builder
// ---------------------------------------------------------------------------

func TestBuildPetitionContext(t *testing.T) {
	deliberation := &deliberationResult{
		Outcome: "create",
		Justifications: []jurorJustification{
			{JurorID: "j1", Outcome: "create", Reasoning: "The reason"},
		},
	}
	vctx := &verdictContext{
		Trigger:        "friction-hearing",
		SourceWorkitem: "wi-456",
	}

	pc := buildPetitionContext(deliberation, vctx)
	if pc.Trigger != "friction-hearing" {
		t.Errorf("trigger = %q, want friction-hearing", pc.Trigger)
	}
	if pc.SourceWorkitem != "wi-456" {
		t.Errorf("source_workitem = %q, want wi-456", pc.SourceWorkitem)
	}
	if pc.Verdict != "create" {
		t.Errorf("verdict = %q, want create", pc.Verdict)
	}
	if pc.Justification != "The reason" {
		t.Errorf("justification = %q, want 'The reason'", pc.Justification)
	}
}

func TestBuildPetitionContext_NoJustifications(t *testing.T) {
	deliberation := &deliberationResult{Outcome: "retire"}
	vctx := &verdictContext{Trigger: "ttl-hearing"}

	pc := buildPetitionContext(deliberation, vctx)
	if pc.Justification != "" {
		t.Errorf("justification should be empty, got %q", pc.Justification)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func assertClerkRoutedTo(t *testing.T, spy *clerkSpy, expected string) {
	t.Helper()
	if len(spy.RoutedOutputs) != 1 {
		t.Fatalf("expected 1 routed output, got %d: %v", len(spy.RoutedOutputs), spy.RoutedOutputs)
	}
	if spy.RoutedOutputs[0] != expected {
		t.Errorf("routed to %q, want %q", spy.RoutedOutputs[0], expected)
	}
}

// containsSubstring checks if s contains substr.
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && contains(s, substr))
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
