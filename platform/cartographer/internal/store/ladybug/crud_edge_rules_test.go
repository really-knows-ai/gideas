package ladybug

import (
	"context"
	"errors"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// SPEC R1 membership-OR rule composition (crud.go validateEdgeRulesFor): an
// edge is permitted when ANY rule entry authorizes it — a second rule can
// authorize a connection the first denies — and within a rule, canConnectTo and
// using are ANDed. Pins the two previously-untested branches: the
// OR-across-entries authorization, and the deny-by-using-mismatch (target type
// present in a rule's canConnectTo but the edge type absent from that rule's
// using).
func TestCreateEdge_RuleComposition(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Service",
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
					{CanConnectTo: []string{"Document"}, Using: []string{"LINKS_TO"}},
				},
			},
			{Name: "Component"},
			{Name: "Document"},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON"},
			{Name: "LINKS_TO"},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	svc, err := s.CreateEntity(ctx, "Service", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Service: %v", err)
	}
	comp, err := s.CreateEntity(ctx, "Component", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Component: %v", err)
	}
	doc, err := s.CreateEntity(ctx, "Document", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Document: %v", err)
	}

	// Rule 1 authorizes Service → Component via DEPENDS_ON.
	if _, err := s.CreateEdge(ctx, "DEPENDS_ON", svc.Id, comp.Id, nil, "main"); err != nil {
		t.Fatalf("rule 1 should authorize Service→Component via DEPENDS_ON: %v", err)
	}

	// OR across rule entries: rule 2 authorizes Service → Document via LINKS_TO
	// even though rule 1 (which only names Component) denies it.
	if _, err := s.CreateEdge(ctx, "LINKS_TO", svc.Id, doc.Id, nil, "main"); err != nil {
		t.Fatalf("rule 2 should authorize Service→Document via LINKS_TO: %v", err)
	}

	// Deny by using-mismatch: Document appears in rule 2's canConnectTo, but
	// DEPENDS_ON is absent from rule 2's using (and Document is absent from
	// rule 1's canConnectTo), so the connection must be denied.
	_, err = s.CreateEdge(ctx, "DEPENDS_ON", svc.Id, doc.Id, nil, "main")
	if !errors.Is(err, store.ErrEdgeRuleViolation) {
		t.Fatalf("expected ErrEdgeRuleViolation for using-mismatch, got %v", err)
	}
}

// SPEC R1:133 — "Only the source entity type's rules are evaluated — the
// target entity type's rules play no role in edge authorization." Source
// permits Source→Target via LINKS; Target's own rules authorize only
// Target→Source via a different edge type (REVERSES). If the target's rules
// were consulted for the Source→Target LINKS edge, the connection would be
// denied — it must succeed, proving the target's rules are never evaluated.
// (Target's rules must use a different edge type than Source's so the LINKS
// rel table has a single FROM label — LadybugDB rejects a rel table whose
// endpoint clauses bind multiple node labels.)
func TestCreateEdge_TargetRulesNotEvaluated(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Source",
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Target"}, Using: []string{"LINKS"}},
				},
			},
			{
				Name: "Target",
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Source"}, Using: []string{"REVERSES"}},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "LINKS"},
			{Name: "REVERSES"},
		},
	}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	src, err := s.CreateEntity(ctx, "Source", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Source: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "Target", "", nil, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity Target: %v", err)
	}

	// The source's rules authorize the connection; the target's rules (which
	// never name LINKS) must not be consulted for an edge into Target.
	if _, err := s.CreateEdge(ctx, "LINKS", src.Id, tgt.Id, nil, "main"); err != nil {
		t.Fatalf("edge authorized by source rules must succeed regardless of target rules, got %v", err)
	}

	// Directionality proof: the target's rules DO govern edges originating
	// from Target — a LINKS edge from Target is denied even though LINKS is a
	// declared edge type — while the same rules play no role for edges into
	// Target (asserted above).
	if _, err := s.CreateEdge(ctx, "LINKS", tgt.Id, src.Id, nil, "main"); !errors.Is(err, store.ErrEdgeRuleViolation) {
		t.Fatalf("expected ErrEdgeRuleViolation for LINKS from Target (its own rules govern its outgoing edges), got %v", err)
	}
}
