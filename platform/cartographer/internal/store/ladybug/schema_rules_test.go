package ladybug

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// TestApplySchema_RuleModification_PreexistingEdgeRemainsValid pins the SPEC R6
// forward-only rules branch (SPEC:413-415): CreateEdge validates against the
// current rules, but existing edges created under previous rules remain valid
// and are not retroactively re-validated. The rule modification must preserve
// the edge type's FROM/TO pair set (a pair change is destructive), so the edit
// grants the source type a second, brand-new edge type — a non-destructive rule
// change. The pre-existing edge must stay readable and listed while new edge
// creation validates against the modified rules.
func TestApplySchema_RuleModification_PreexistingEdgeRemainsValid(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Initial schema: Service may connect to Component only via DEPENDS_ON.
	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Service", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
			}},
			{Name: "Component"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "DEPENDS_ON"}},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}

	svc, err := s.CreateEntity(ctx, "Service", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Service: %v", err)
	}
	comp, err := s.CreateEntity(ctx, "Component", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Component: %v", err)
	}
	edge, err := s.CreateEdge(ctx, "DEPENDS_ON", svc.Id, comp.Id, nil, "main")
	if err != nil {
		t.Fatalf("CreateEdge under the initial rules: %v", err)
	}

	// Non-destructive rule modification: Service's rule gains a second edge type
	// LINKS_TO (a new edge type, so DEPENDS_ON's FROM/TO pair set is unchanged
	// and the apply stays additive).
	schema2 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Service", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON", "LINKS_TO"}},
			}},
			{Name: "Component"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "DEPENDS_ON"}, {Name: "LINKS_TO"}},
	}
	if err := s.ApplySchema(ctx, schema2); err != nil {
		t.Fatalf("rule-modifying ApplySchema: %v", err)
	}

	// The pre-existing edge created under the previous rules stays valid — it is
	// readable and listed, not retroactively re-validated against the new rules.
	got, err := s.GetEdge(ctx, edge.Id, "main")
	if err != nil {
		t.Fatalf("GetEdge after the rule modification must not fail: %v", err)
	}
	if got.Type != edge.Type {
		t.Fatalf("expected a %s edge, got %+v", edge.Type, got)
	}
	listed, err := s.ListEdgesOfType(ctx, "DEPENDS_ON", "main")
	if err != nil {
		t.Fatalf("ListEdgesOfType after the rule modification: %v", err)
	}
	if len(listed) != 1 || listed[0].Id != edge.Id {
		t.Fatalf("expected the pre-existing edge to remain listed, got %+v", listed)
	}

	// New edge creation validates against the CURRENT rules: the newly added
	// LINKS_TO edge type is now permitted...
	if _, err := s.CreateEdge(ctx, "LINKS_TO", svc.Id, comp.Id, nil, "main"); err != nil {
		t.Fatalf("CreateEdge via the newly permitted edge type must succeed: %v", err)
	}
	// ...while a direction no rule ever declared stays forbidden.
	_, err = s.CreateEdge(ctx, "DEPENDS_ON", comp.Id, svc.Id, nil, "main")
	if !errors.Is(err, store.ErrEdgeRuleViolation) {
		t.Fatalf("expected ErrEdgeRuleViolation for an unpermitted reverse edge, got %v", err)
	}
}

// TestApplySchema_AddNewFromToPairOnExistingEdgeType_Rejected verifies the
// deliberate, documented divergence between SPEC R1/R2 (which treats a rule
// modification as non-destructive) and the storage engine: adding a rule that
// introduces a NEW FROM/TO pair on an existing edge type changes the rel
// table's endpoint clauses, which Ladybug fixes at CREATE time and cannot
// ALTER. Such a change must therefore be rejected as a destructive schema
// change, not silently applied.
func TestApplySchema_AddNewFromToPairOnExistingEdgeType_Rejected(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Initial schema: X connects to Y via edge R only.
	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "X", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Y"}, Using: []string{"R"}},
			}},
			{Name: "Y"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "R"}},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}

	// Extend the schema with a second rule that adds a NEW FROM/TO pair
	// (X→Z) on the EXISTING edge type R. SPEC R1 membership-OR makes this a
	// valid schema; the rel table cannot express the added pair, so the store
	// must reject it as a destructive change rather than silently accepting it.
	schema2 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "X", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Y"}, Using: []string{"R"}},
				{CanConnectTo: []string{"Z"}, Using: []string{"R"}},
			}},
			{Name: "Y"},
			{Name: "Z"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "R"}},
	}
	err = s.ApplySchema(ctx, schema2)
	if err == nil {
		t.Fatal("expected destructive schema change for a new FROM/TO pair on an existing edge type")
	}
	if !errors.Is(err, store.ErrDestructiveSchemaChange) {
		t.Fatalf("expected ErrDestructiveSchemaChange, got %v", err)
	}
}

// TestApplySchema_MixedAdditiveAndDestructive_AllOrNothing pins the ordering
// requirement that every destructive check runs in the pre-DDL catalog diff: a
// schema that BOTH adds a new entity type (additive) AND changes an existing
// edge type's FROM/TO endpoint set (destructive) must fail all-or-nothing
// before any DDL executes. The pre-fix code detected the endpoint change only
// inside alterRelTable — after the entity-type DDL loop had already created the
// new entity's table — so a rejected ApplySchema left partial DDL applied with
// schema.json unpublished, wedging the next file-backed Open in
// validateMetadataAgainstCatalog ("database entity type W is absent from schema
// metadata"). The schema also carries an unchanged edgeless edge type whose rel
// table persists the `_untyped` placeholder pair, pinning that the diff's
// endpoint comparison normalizes empty requested pairs to the placeholder (an
// edgeless edge type must never false-positive the new check).
func TestApplySchema_MixedAdditiveAndDestructive_AllOrNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	// Initial schema: X connects to Y via edge R only, plus an edgeless edge
	// type S (no rules reference it, so its rel table carries the `_untyped`
	// placeholder pair).
	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "X", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Y"}, Using: []string{"R"}},
			}},
			{Name: "Y"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "R"}, {Name: "S"}},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}
	db := s.(*ladybugDB)

	// Mixed schema: (a) ADDITIVE — new entity types Z and W; (b) DESTRUCTIVE —
	// a new X→Z rule on the existing edge R adds a FROM/TO pair the rel table
	// cannot express. S stays edgeless and unchanged. S is listed before R so a
	// broken `_untyped` normalization would surface as an error naming S
	// instead of R.
	mixed := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "X", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Y"}, Using: []string{"R"}},
				{CanConnectTo: []string{"Z"}, Using: []string{"R"}},
			}},
			{Name: "Y"},
			{Name: "Z"},
			{Name: "W"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "S"}, {Name: "R"}},
	}
	err = s.ApplySchema(ctx, mixed)
	if err == nil {
		t.Fatal("expected destructive schema change for the mixed additive+destructive schema")
	}
	if !errors.Is(err, store.ErrDestructiveSchemaChange) {
		t.Fatalf("expected ErrDestructiveSchemaChange, got %v", err)
	}
	// The rejection must name the destructive edge R — S's edgeless placeholder
	// normalization must have passed first.
	if !strings.Contains(err.Error(), `edge "R"`) {
		t.Fatalf("expected the error to name edge R, got %v", err)
	}

	// All-or-nothing: the additive part must not have been applied. The W and Z
	// tables must not exist in the physical catalog (the pre-fix code created
	// them in the entity-type DDL loop before alterRelTable rejected R).
	if kind := tableKindOnConn(t, db.conn, "W"); kind != "" {
		t.Fatalf("additive entity type W partially applied (catalog kind %q) before the destructive rejection", kind)
	}
	if kind := tableKindOnConn(t, db.conn, "Z"); kind != "" {
		t.Fatalf("additive entity type Z partially applied (catalog kind %q) before the destructive rejection", kind)
	}

	// The store is not wedged: schema.json was never rewritten, so a
	// close/reopen must succeed (the pre-fix code left the catalog ahead of the
	// metadata and the Open failed in validateMetadataAgainstCatalog).
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after rejected mixed schema must succeed, got %v", err)
	}
	defer closeStore(t, reopened)

	// The additive part alone applies cleanly afterwards.
	additive := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "X", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Y"}, Using: []string{"R"}},
			}},
			{Name: "Y"},
			{Name: "Z"},
			{Name: "W"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "R"}, {Name: "S"}},
	}
	if err := reopened.ApplySchema(ctx, additive); err != nil {
		t.Fatalf("applying only the additive part after the rejection must succeed, got %v", err)
	}
	if !reopened.TableExists("W") {
		t.Fatal("W table should exist after applying the additive part")
	}
}

// TestApplySchema_RedundantRulesDedupPairsSurviveReopen verifies that
// overlapping/redundant rules (valid per SPEC R1 membership-OR semantics,
// which merge the canConnectTo and using lists across rule entries) do NOT
// brick the store. The pair-derivation paths must dedup consistently so the
// metadata-derived pair set matches the rel table's endpoint clauses on reopen.
func TestApplySchema_RedundantRulesDedupPairsSurviveReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Two identical overlapping rules both yield a (T→X) pair via DEPENDS_ON:
	// the extraction produces exactly the same FROM/TO pair twice.
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "T",
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"X"}, Using: []string{"DEPENDS_ON"}},
					{CanConnectTo: []string{"X"}, Using: []string{"DEPENDS_ON"}},
				},
			},
			{Name: "X"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "DEPENDS_ON"}},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	// Create a matching entity and edge so a reopen that silently corrupts the
	// catalog comparison has observable data to lose.
	src, err := s.CreateEntity(ctx, "T", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity T: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "X", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity X: %v", err)
	}
	edge, err := s.CreateEdge(ctx, "DEPENDS_ON", src.Id, tgt.Id, nil, "main")
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}

	// Reopen — the pre-fix code derived duplicate pairs on the reopen path and
	// failed the catalog comparison (equalFromToPairs), bricking the open.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen with redundant rules: %v", err)
	}
	defer closeStore(t, reopened)
	if _, err := reopened.GetEdge(ctx, edge.Id, "main"); err != nil {
		t.Fatalf("reopened edge missing: %v", err)
	}
}
