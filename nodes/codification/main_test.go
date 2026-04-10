package main

import (
	"context"
	"encoding/json"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Test 1: Happy path (mixed changes — create + retire)
// ---------------------------------------------------------------------------

func TestCodification_HappyPath_MixedChanges(t *testing.T) {
	spy := newCodificationSpy()
	seedPetition(spy,
		petitionChange{Action: "create", Goal: "Enforce naming", AppliesTo: []string{"haiku"}, Tier: 2},
		petitionChange{Action: "retire", LawID: "law-42"},
	)
	seedCodificationResult(spy, "child-1", "application/smt-lib", "(assert (= naming consistent))")

	client := setupCodificationTest(t, spy)
	cfg := defaultTestConfig()

	err := handleCodification(context.Background(), client, cfg)
	if err != nil {
		t.Fatalf("handleCodification: %v", err)
	}

	pet := storedPetition(t, spy)

	// Verify 2 changes preserved.
	if len(pet.Petition.Changes) != 2 {
		t.Fatalf("changes count = %d, want 2", len(pet.Petition.Changes))
	}

	// Create change should have representations.
	create := pet.Petition.Changes[0]
	if create.Action != "create" {
		t.Errorf("change[0] action = %q, want create", create.Action)
	}
	if len(create.Representations) != 1 {
		t.Fatalf("create representations = %d, want 1", len(create.Representations))
	}
	if create.Representations[0].Type != "application/smt-lib" {
		t.Errorf("rep type = %q, want application/smt-lib", create.Representations[0].Type)
	}
	if create.Representations[0].Content != "(assert (= naming consistent))" {
		t.Errorf("rep content = %q, unexpected", create.Representations[0].Content)
	}

	// Retire change should have no representations.
	retire := pet.Petition.Changes[1]
	if retire.Action != "retire" {
		t.Errorf("change[1] action = %q, want retire", retire.Action)
	}
	if len(retire.Representations) != 0 {
		t.Errorf("retire representations = %d, want 0", len(retire.Representations))
	}

	// Verify exactly 1 child was created (only the create change).
	if len(spy.CreatedChildren) != 1 {
		t.Fatalf("created children = %d, want 1", len(spy.CreatedChildren))
	}

	// Verify routed to default.
	assertRoutedTo(t, spy, "default")
}

// ---------------------------------------------------------------------------
// Test 2: Multi-change, multi-codifier
// ---------------------------------------------------------------------------

func TestCodification_MultiChange_MultiCodifier(t *testing.T) {
	spy := newCodificationSpy()
	seedPetition(spy,
		petitionChange{Action: "create", Goal: "Rule A", AppliesTo: []string{"haiku"}, Tier: 1},
		petitionChange{Action: "update", Goal: "Rule B", AppliesTo: []string{"sonnet"}, Tier: 2, LawID: "law-10"},
	)

	// 2 qualifying changes × 2 codifiers = 4 children.
	// Order: change0→codify-smt (child-1), change0→codify-rego (child-2),
	//        change1→codify-smt (child-3), change1→codify-rego (child-4).
	seedCodificationResult(spy, "child-1", "application/smt-lib", "smt-rule-a")
	seedCodificationResult(spy, "child-2", "application/rego", "rego-rule-a")
	seedCodificationResult(spy, "child-3", "application/smt-lib", "smt-rule-b")
	seedCodificationResult(spy, "child-4", "application/rego", "rego-rule-b")

	client := setupCodificationTest(t, spy)
	cfg := &codificationConfig{
		CodificationNodes: []string{"codify-smt", "codify-rego"},
	}

	err := handleCodification(context.Background(), client, cfg)
	if err != nil {
		t.Fatalf("handleCodification: %v", err)
	}

	// Verify 4 children created and routed correctly.
	if len(spy.CreatedChildren) != 4 {
		t.Fatalf("created children = %d, want 4", len(spy.CreatedChildren))
	}
	if len(spy.RoutedChildren) != 4 {
		t.Fatalf("routed children = %d, want 4", len(spy.RoutedChildren))
	}

	// Verify routing order: smt, rego, smt, rego.
	expectedTargets := []string{"codify-smt", "codify-rego", "codify-smt", "codify-rego"}
	for i, expected := range expectedTargets {
		if spy.RoutedChildren[i].TargetNode != expected {
			t.Errorf("child %d target = %q, want %q", i, spy.RoutedChildren[i].TargetNode, expected)
		}
	}

	pet := storedPetition(t, spy)

	// Change 0 (create) should have 2 representations (smt + rego).
	if len(pet.Petition.Changes[0].Representations) != 2 {
		t.Fatalf("change[0] reps = %d, want 2", len(pet.Petition.Changes[0].Representations))
	}
	if pet.Petition.Changes[0].Representations[0].Content != "smt-rule-a" {
		t.Errorf("change[0] rep[0] content = %q, want smt-rule-a", pet.Petition.Changes[0].Representations[0].Content)
	}
	if pet.Petition.Changes[0].Representations[1].Content != "rego-rule-a" {
		t.Errorf("change[0] rep[1] content = %q, want rego-rule-a", pet.Petition.Changes[0].Representations[1].Content)
	}

	// Change 1 (update) should also have 2 representations.
	if len(pet.Petition.Changes[1].Representations) != 2 {
		t.Fatalf("change[1] reps = %d, want 2", len(pet.Petition.Changes[1].Representations))
	}
	if pet.Petition.Changes[1].Representations[0].Content != "smt-rule-b" {
		t.Errorf("change[1] rep[0] content = %q, want smt-rule-b", pet.Petition.Changes[1].Representations[0].Content)
	}
	if pet.Petition.Changes[1].Representations[1].Content != "rego-rule-b" {
		t.Errorf("change[1] rep[1] content = %q, want rego-rule-b", pet.Petition.Changes[1].Representations[1].Content)
	}

	assertRoutedTo(t, spy, "default")
}

// ---------------------------------------------------------------------------
// Test 3: All retire — no fan-out
// ---------------------------------------------------------------------------

func TestCodification_AllRetire_NoFanOut(t *testing.T) {
	spy := newCodificationSpy()
	seedPetition(spy,
		petitionChange{Action: "retire", LawID: "law-1"},
		petitionChange{Action: "retire", LawID: "law-2"},
	)

	client := setupCodificationTest(t, spy)
	cfg := defaultTestConfig()

	err := handleCodification(context.Background(), client, cfg)
	if err != nil {
		t.Fatalf("handleCodification: %v", err)
	}

	// No children should be created.
	if len(spy.CreatedChildren) != 0 {
		t.Errorf("created children = %d, want 0", len(spy.CreatedChildren))
	}

	// Petition should be stored unchanged.
	pet := storedPetition(t, spy)
	if len(pet.Petition.Changes) != 2 {
		t.Fatalf("changes count = %d, want 2", len(pet.Petition.Changes))
	}
	for i, c := range pet.Petition.Changes {
		if c.Action != "retire" {
			t.Errorf("change[%d] action = %q, want retire", i, c.Action)
		}
		if len(c.Representations) != 0 {
			t.Errorf("change[%d] representations = %d, want 0", i, len(c.Representations))
		}
	}

	assertRoutedTo(t, spy, "default")
}

// ---------------------------------------------------------------------------
// Test 4: Single change — backward compatibility
// ---------------------------------------------------------------------------

func TestCodification_SingleChange(t *testing.T) {
	spy := newCodificationSpy()
	seedPetition(spy,
		petitionChange{Action: "create", Goal: "Single rule", AppliesTo: []string{"haiku"}, Tier: 1},
	)
	seedCodificationResult(spy, "child-1", "application/smt-lib", "(assert true)")

	client := setupCodificationTest(t, spy)
	cfg := defaultTestConfig()

	err := handleCodification(context.Background(), client, cfg)
	if err != nil {
		t.Fatalf("handleCodification: %v", err)
	}

	if len(spy.CreatedChildren) != 1 {
		t.Fatalf("created children = %d, want 1", len(spy.CreatedChildren))
	}

	pet := storedPetition(t, spy)
	if len(pet.Petition.Changes) != 1 {
		t.Fatalf("changes count = %d, want 1", len(pet.Petition.Changes))
	}
	if len(pet.Petition.Changes[0].Representations) != 1 {
		t.Fatalf("representations = %d, want 1", len(pet.Petition.Changes[0].Representations))
	}
	if pet.Petition.Changes[0].Representations[0].Content != "(assert true)" {
		t.Errorf("rep content = %q, want %q", pet.Petition.Changes[0].Representations[0].Content, "(assert true)")
	}

	assertRoutedTo(t, spy, "default")
}

// ---------------------------------------------------------------------------
// Test 5: Missing petition artefact — error
// ---------------------------------------------------------------------------

func TestCodification_Error_PetitionMissing(t *testing.T) {
	spy := newCodificationSpy()
	// Deliberately don't seed the petition artefact.

	client := setupCodificationTest(t, spy)
	cfg := defaultTestConfig()

	err := handleCodification(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when petition artefact missing")
	}
	if !containsSubstring(err.Error(), "get petition") {
		t.Errorf("error should mention get petition: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test 6: Child failure — error propagation
// ---------------------------------------------------------------------------

func TestCodification_Error_ChildFailure(t *testing.T) {
	spy := newCodificationSpy()
	seedPetition(spy,
		petitionChange{Action: "create", Goal: "Rule A", AppliesTo: []string{"haiku"}, Tier: 1},
	)

	// Override GetChildren to return a failed child.
	spy.Children = []*flowv1.ChildWorkitemStatus{
		{
			WorkitemId: "child-1",
			Phase:      "Failed",
		},
	}

	client := setupCodificationTest(t, spy)
	cfg := defaultTestConfig()

	err := handleCodification(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when child fails")
	}
	if !containsSubstring(err.Error(), "collect artefacts") {
		t.Errorf("error should mention collect artefacts: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test 7: Empty codification nodes list — no fan-out
// ---------------------------------------------------------------------------

func TestCodification_EmptyCodificationNodes(t *testing.T) {
	spy := newCodificationSpy()
	seedPetition(spy,
		petitionChange{Action: "create", Goal: "Rule A", AppliesTo: []string{"haiku"}, Tier: 1},
	)

	client := setupCodificationTest(t, spy)
	cfg := &codificationConfig{} // No codification nodes.

	err := handleCodification(context.Background(), client, cfg)
	if err != nil {
		t.Fatalf("handleCodification: %v", err)
	}

	// No children should be created.
	if len(spy.CreatedChildren) != 0 {
		t.Errorf("created children = %d, want 0", len(spy.CreatedChildren))
	}

	// Petition should be stored as-is.
	pet := storedPetition(t, spy)
	if len(pet.Petition.Changes) != 1 {
		t.Fatalf("changes count = %d, want 1", len(pet.Petition.Changes))
	}
	if len(pet.Petition.Changes[0].Representations) != 0 {
		t.Errorf("representations = %d, want 0", len(pet.Petition.Changes[0].Representations))
	}

	assertRoutedTo(t, spy, "default")
}

// ---------------------------------------------------------------------------
// Test 8: Round-trip preservation
// ---------------------------------------------------------------------------

func TestCodification_RoundTripPreservation(t *testing.T) {
	spy := newCodificationSpy()

	// Build a petition with rich context and multiple field types.
	seedPetition(spy,
		petitionChange{
			Action:        "create",
			Goal:          "Enforce naming conventions",
			AppliesTo:     []string{"haiku", "sonnet"},
			Tier:          3,
			FromTier:      0,
			ToTier:        0,
			Justification: "per jury verdict",
		},
		petitionChange{
			Action:        "demote",
			Goal:          "Relax formatting rule",
			AppliesTo:     []string{"haiku"},
			Tier:          1,
			LawID:         "law-77",
			FromTier:      2,
			ToTier:        1,
			Justification: "too strict for content",
		},
	)

	// Seed results for 2 qualifying changes × 1 codifier = 2 children.
	seedCodificationResult(spy, "child-1", "application/smt-lib", "smt-create")
	seedCodificationResult(spy, "child-2", "application/smt-lib", "smt-demote")

	client := setupCodificationTest(t, spy)
	cfg := defaultTestConfig()

	err := handleCodification(context.Background(), client, cfg)
	if err != nil {
		t.Fatalf("handleCodification: %v", err)
	}

	pet := storedPetition(t, spy)

	// Verify context survived.
	if pet.Petition.Context.Trigger != "deadlock-resolution" {
		t.Errorf("trigger = %q, want deadlock-resolution", pet.Petition.Context.Trigger)
	}
	if pet.Petition.Context.VerdictDecision != "favour_refiner" {
		t.Errorf("verdict_decision = %q, want favour_refiner", pet.Petition.Context.VerdictDecision)
	}
	if pet.Petition.Context.Justification != "Strong argument for change" {
		t.Errorf("justification = %q, want 'Strong argument for change'", pet.Petition.Context.Justification)
	}
	if pet.Petition.ProseJustification != "Test prose justification" {
		t.Errorf("prose_justification = %q, want 'Test prose justification'", pet.Petition.ProseJustification)
	}

	// Verify change fields survived.
	c0 := pet.Petition.Changes[0]
	if c0.Goal != "Enforce naming conventions" {
		t.Errorf("change[0] goal = %q, unexpected", c0.Goal)
	}
	if len(c0.AppliesTo) != 2 || c0.AppliesTo[0] != "haiku" || c0.AppliesTo[1] != "sonnet" {
		t.Errorf("change[0] applies_to = %v, want [haiku, sonnet]", c0.AppliesTo)
	}
	if c0.Tier != 3 {
		t.Errorf("change[0] tier = %d, want 3", c0.Tier)
	}
	if c0.Justification != "per jury verdict" {
		t.Errorf("change[0] justification = %q, unexpected", c0.Justification)
	}

	c1 := pet.Petition.Changes[1]
	if c1.LawID != "law-77" {
		t.Errorf("change[1] law_id = %q, want law-77", c1.LawID)
	}
	if c1.FromTier != 2 {
		t.Errorf("change[1] from_tier = %d, want 2", c1.FromTier)
	}
	if c1.ToTier != 1 {
		t.Errorf("change[1] to_tier = %d, want 1", c1.ToTier)
	}

	// Representations were added to both qualifying changes.
	if len(c0.Representations) != 1 {
		t.Fatalf("change[0] reps = %d, want 1", len(c0.Representations))
	}
	if len(c1.Representations) != 1 {
		t.Fatalf("change[1] reps = %d, want 1", len(c1.Representations))
	}
}

// ---------------------------------------------------------------------------
// Additional: Invalid petition JSON
// ---------------------------------------------------------------------------

func TestCodification_Error_InvalidPetitionJSON(t *testing.T) {
	spy := newCodificationSpy()
	spy.Artefacts[defaultPetitionArtefact] = []byte("not-json{{{")

	client := setupCodificationTest(t, spy)
	cfg := defaultTestConfig()

	err := handleCodification(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when petition is invalid JSON")
	}
	if !containsSubstring(err.Error(), "parse petition") {
		t.Errorf("error should mention parse petition: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Additional: Store petition fails
// ---------------------------------------------------------------------------

func TestCodification_Error_StorePetitionFails(t *testing.T) {
	spy := newCodificationSpy()
	seedPetition(spy,
		petitionChange{Action: "retire", LawID: "law-1"},
	)
	spy.StoreArtefactErr = status.Errorf(codes.Internal, "archivist down")

	client := setupCodificationTest(t, spy)
	cfg := defaultTestConfig()

	err := handleCodification(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when store petition fails")
	}
	if !containsSubstring(err.Error(), "store petition") {
		t.Errorf("error should mention store petition: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Additional: Route to output fails
// ---------------------------------------------------------------------------

func TestCodification_Error_RouteToOutputFails(t *testing.T) {
	spy := newCodificationSpy()
	seedPetition(spy,
		petitionChange{Action: "retire", LawID: "law-1"},
	)
	spy.RouteToOutputErr = status.Errorf(codes.Internal, "operator down")

	client := setupCodificationTest(t, spy)
	cfg := defaultTestConfig()

	err := handleCodification(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when route to output fails")
	}
	if !containsSubstring(err.Error(), "route to output") {
		t.Errorf("error should mention route to output: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Additional: Fan-out create child fails
// ---------------------------------------------------------------------------

func TestCodification_Error_FanOutCreateChildFails(t *testing.T) {
	spy := newCodificationSpy()
	seedPetition(spy,
		petitionChange{Action: "create", Goal: "Rule A", AppliesTo: []string{"haiku"}, Tier: 1},
	)
	spy.CreateChildErr = status.Errorf(codes.Internal, "operator down")

	client := setupCodificationTest(t, spy)
	cfg := defaultTestConfig()

	err := handleCodification(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when create child fails")
	}
	if !containsSubstring(err.Error(), "fan-out") {
		t.Errorf("error should mention fan-out: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Additional: AwaitChildren fails
// ---------------------------------------------------------------------------

func TestCodification_Error_AwaitChildrenFails(t *testing.T) {
	spy := newCodificationSpy()
	seedPetition(spy,
		petitionChange{Action: "create", Goal: "Rule A", AppliesTo: []string{"haiku"}, Tier: 1},
	)
	spy.GetChildrenErr = status.Errorf(codes.Internal, "operator down")

	client := setupCodificationTest(t, spy)
	cfg := defaultTestConfig()

	err := handleCodification(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error from AwaitChildren failure")
	}
	if !containsSubstring(err.Error(), "await children") {
		t.Errorf("error should mention await children: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Additional: Child returns no result artefact (warning path, not error)
// ---------------------------------------------------------------------------

func TestCodification_ChildMissingResult_SkipsGracefully(t *testing.T) {
	spy := newCodificationSpy()
	seedPetition(spy,
		petitionChange{Action: "create", Goal: "Rule A", AppliesTo: []string{"haiku"}, Tier: 1},
	)
	// Don't seed any child artefacts — child returned no codification-result.

	client := setupCodificationTest(t, spy)
	cfg := defaultTestConfig()

	err := handleCodification(context.Background(), client, cfg)
	if err != nil {
		t.Fatalf("handleCodification: %v", err)
	}

	// Petition should be stored but the change should have no representations
	// (the missing result is logged as a warning, not an error).
	pet := storedPetition(t, spy)
	if len(pet.Petition.Changes[0].Representations) != 0 {
		t.Errorf("representations = %d, want 0 (child had no result)", len(pet.Petition.Changes[0].Representations))
	}

	assertRoutedTo(t, spy, "default")
}

// ---------------------------------------------------------------------------
// Config accessor tests
// ---------------------------------------------------------------------------

func TestCodificationConfig_Defaults(t *testing.T) {
	cfg := &codificationConfig{}
	if cfg.petitionArtefact() != "petition" {
		t.Errorf("petitionArtefact() = %q, want petition", cfg.petitionArtefact())
	}
	if cfg.defaultOutputName() != "default" {
		t.Errorf("defaultOutputName() = %q, want default", cfg.defaultOutputName())
	}
}

func TestCodificationConfig_CustomValues(t *testing.T) {
	cfg := &codificationConfig{
		PetitionArtefact:  "custom-petition",
		CodificationNodes: []string{"codify-a", "codify-b"},
		DefaultOutput:     "tribunal-review",
	}
	if cfg.petitionArtefact() != "custom-petition" {
		t.Errorf("petitionArtefact() = %q, want custom-petition", cfg.petitionArtefact())
	}
	if cfg.defaultOutputName() != "tribunal-review" {
		t.Errorf("defaultOutputName() = %q, want tribunal-review", cfg.defaultOutputName())
	}
}

// ---------------------------------------------------------------------------
// needsCodification unit tests
// ---------------------------------------------------------------------------

func TestNeedsCodification(t *testing.T) {
	cases := []struct {
		action string
		want   bool
	}{
		{actionCreate, true},
		{actionUpdate, true},
		{actionDemote, true},
		{"retire", false},
		{"unknown", false},
		{"", false},
	}
	for _, tc := range cases {
		c := petitionChange{Action: tc.action}
		if got := c.needsCodification(); got != tc.want {
			t.Errorf("needsCodification(%q) = %v, want %v", tc.action, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// buildFanOutTasks unit tests
// ---------------------------------------------------------------------------

func TestBuildFanOutTasks(t *testing.T) {
	qualifying := []indexedChange{
		{originalIndex: 0, change: &petitionChange{Action: "create", Goal: "R1", AppliesTo: []string{"haiku"}, Tier: 1}},
		{originalIndex: 2, change: &petitionChange{Action: "update", Goal: "R2", AppliesTo: []string{"sonnet"}, Tier: 2}},
	}
	codifiers := []string{"codify-smt", "codify-rego"}

	tasks, err := buildFanOutTasks(qualifying, codifiers)
	if err != nil {
		t.Fatalf("buildFanOutTasks: %v", err)
	}

	// 2 changes × 2 codifiers = 4 tasks.
	if len(tasks) != 4 {
		t.Fatalf("tasks = %d, want 4", len(tasks))
	}

	// Verify targets: change0→[smt, rego], change1→[smt, rego].
	expectedTargets := []string{"codify-smt", "codify-rego", "codify-smt", "codify-rego"}
	for i, expected := range expectedTargets {
		if tasks[i].TargetNode != expected {
			t.Errorf("task[%d] target = %q, want %q", i, tasks[i].TargetNode, expected)
		}
	}

	// Verify each task has exactly 1 artefact (codification-goal).
	for i, task := range tasks {
		if len(task.Artefacts) != 1 {
			t.Errorf("task[%d] artefacts = %d, want 1", i, len(task.Artefacts))
			continue
		}
		if task.Artefacts[0].ID != artefactCodificationGoal {
			t.Errorf("task[%d] artefact ID = %q, want %q", i, task.Artefacts[0].ID, artefactCodificationGoal)
		}
		if task.Artefacts[0].GovernedArtefact != governedCodificationGoal {
			t.Errorf("task[%d] governed = %q, want %q", i, task.Artefacts[0].GovernedArtefact, governedCodificationGoal)
		}

		// Verify goal content.
		var goal codificationGoal
		if err := json.Unmarshal(task.Artefacts[0].Content, &goal); err != nil {
			t.Errorf("task[%d] goal JSON invalid: %v", i, err)
			continue
		}
		// Tasks 0,1 map to change 0; tasks 2,3 map to change 1.
		if i < 2 {
			if goal.Goal != "R1" {
				t.Errorf("task[%d] goal = %q, want R1", i, goal.Goal)
			}
		} else {
			if goal.Goal != "R2" {
				t.Errorf("task[%d] goal = %q, want R2", i, goal.Goal)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Verify codification-goal stored on children
// ---------------------------------------------------------------------------

func TestCodification_GoalArtefactStoredOnChildren(t *testing.T) {
	spy := newCodificationSpy()
	seedPetition(spy,
		petitionChange{Action: "create", Goal: "Enforce naming", AppliesTo: []string{"haiku"}, Tier: 2},
	)
	seedCodificationResult(spy, "child-1", "application/smt-lib", "(assert true)")

	client := setupCodificationTest(t, spy)
	cfg := defaultTestConfig()

	err := handleCodification(context.Background(), client, cfg)
	if err != nil {
		t.Fatalf("handleCodification: %v", err)
	}

	// Verify codification-goal was stored on the child.
	key := "child-1:" + artefactCodificationGoal
	content, ok := spy.ChildStoredArtefacts[key]
	if !ok {
		t.Fatal("codification-goal not stored on child-1")
	}

	var goal codificationGoal
	if err := json.Unmarshal(content, &goal); err != nil {
		t.Fatalf("invalid codification-goal JSON: %v", err)
	}
	if goal.Goal != "Enforce naming" {
		t.Errorf("goal = %q, want 'Enforce naming'", goal.Goal)
	}
	if goal.Tier != 2 {
		t.Errorf("tier = %d, want 2", goal.Tier)
	}
	if goal.Action != "create" {
		t.Errorf("action = %q, want create", goal.Action)
	}
}

// ---------------------------------------------------------------------------
// Demote change gets codification
// ---------------------------------------------------------------------------

func TestCodification_DemoteChange_GetsCodification(t *testing.T) {
	spy := newCodificationSpy()
	seedPetition(spy, petitionChange{
		Action: "demote", Goal: "Relax rule", AppliesTo: []string{"haiku"},
		Tier: 1, LawID: "law-5", FromTier: 2, ToTier: 1,
	})
	seedCodificationResult(spy, "child-1", "application/smt-lib", "smt-demote")

	client := setupCodificationTest(t, spy)
	cfg := defaultTestConfig()

	err := handleCodification(context.Background(), client, cfg)
	if err != nil {
		t.Fatalf("handleCodification: %v", err)
	}

	pet := storedPetition(t, spy)
	if len(pet.Petition.Changes[0].Representations) != 1 {
		t.Fatalf("demote reps = %d, want 1", len(pet.Petition.Changes[0].Representations))
	}
	if pet.Petition.Changes[0].Representations[0].Content != "smt-demote" {
		t.Errorf("demote rep content = %q, want smt-demote", pet.Petition.Changes[0].Representations[0].Content)
	}
}

// ---------------------------------------------------------------------------
// Custom petition artefact name
// ---------------------------------------------------------------------------

func TestCodification_CustomPetitionArtefact(t *testing.T) {
	spy := newCodificationSpy()

	// Use a custom artefact name.
	pet := petition{
		Petition: petitionBody{
			Context: petitionContext{Trigger: "test"},
			Changes: []petitionChange{
				{Action: "retire", LawID: "law-1"},
			},
		},
	}
	data, _ := json.Marshal(pet)
	spy.Artefacts["custom-petition"] = data

	client := setupCodificationTest(t, spy)
	cfg := &codificationConfig{
		PetitionArtefact: "custom-petition",
		DefaultOutput:    "custom-output",
	}

	err := handleCodification(context.Background(), client, cfg)
	if err != nil {
		t.Fatalf("handleCodification: %v", err)
	}

	// Verify stored under custom artefact name.
	if _, ok := spy.StoredArtefacts["custom-petition"]; !ok {
		t.Fatal("petition not stored under custom artefact name")
	}

	assertRoutedTo(t, spy, "custom-output")
}
