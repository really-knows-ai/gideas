package ladybug

import (
	"context"
	"errors"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	uuid "github.com/google/uuid"
)

func TestCreateEdge_Valid(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}

	edge, err := s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt.Id,
		map[string]string{"strength": strengthValue}, "")
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}
	if edge.Type != "DependsOn" {
		t.Errorf("Type = %q, want %q", edge.Type, "DependsOn")
	}
	if edge.FromEntityID != src.Id {
		t.Errorf("FromEntityID = %q, want %q", edge.FromEntityID, src.Id)
	}
	if edge.ToEntityID != tgt.Id {
		t.Errorf("ToEntityID = %q, want %q", edge.ToEntityID, tgt.Id)
	}
	if edge.Properties["strength"] != strengthValue {
		t.Errorf("strength = %q, want %q", edge.Properties["strength"], strengthValue)
	}
}

func TestCreateEdge_MissingRequiredProperty(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	reqSchema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DependsOn"}},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DependsOn", Properties: []*flowv1.Property{
				{Name: "weight", Type: "string", Required: true},
			}},
		},
	}
	if err := s.ApplySchema(context.Background(), reqSchema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt.Id, nil, "")
	if err == nil {
		t.Fatal("expected error for missing required edge property")
	}
	if !errors.Is(err, store.ErrMissingRequiredProperty) {
		t.Errorf("expected ErrMissingRequiredProperty, got %v", err)
	}
}

// TestCreateEdge_StructuralErrorBeforeEntityExistence asserts the SPEC RPC
// check-order (CreateEdge: structural → entity existence): a request that
// carries BOTH a missing source entity AND a structurally invalid edge property
// surfaces the structural error (unknown/missing required property →
// INVALID_ARGUMENT) rather than the existence NOT_FOUND masking it.
func TestCreateEdge_StructuralErrorBeforeEntityExistence(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	reqSchema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DependsOn"}},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DependsOn", Properties: []*flowv1.Property{
				{Name: "weight", Type: "string", Required: true},
			}},
		},
	}
	if err := s.ApplySchema(context.Background(), reqSchema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	missing := uuid.New().String()
	existingTarget, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "tgt"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity target: %v", err)
	}

	// Missing required property + missing source → ErrMissingRequiredProperty
	// (structural) takes precedence over ErrSourceOrTargetNotFound.
	_, err = s.CreateEdge(context.Background(), "DependsOn", missing, existingTarget.Id, nil, "")
	if !errors.Is(err, store.ErrMissingRequiredProperty) {
		t.Errorf("expected ErrMissingRequiredProperty to take precedence over missing source, got %v", err)
	}

	// Unknown property + missing source → ErrUnknownProperty (structural) takes
	// precedence over ErrSourceOrTargetNotFound.
	_, err = s.CreateEdge(context.Background(), "DependsOn", missing, existingTarget.Id,
		map[string]string{"weight": "x", "bogus": "y"}, "")
	if !errors.Is(err, store.ErrUnknownProperty) {
		t.Errorf("expected ErrUnknownProperty to take precedence over missing source, got %v", err)
	}

	// Well-formed property values + missing source → ErrSourceOrTargetNotFound
	// (entity existence, the next check, still fires when structure is valid).
	_, err = s.CreateEdge(context.Background(), "DependsOn", missing, existingTarget.Id,
		map[string]string{"weight": "heavy"}, "")
	if !errors.Is(err, store.ErrSourceOrTargetNotFound) {
		t.Errorf("expected ErrSourceOrTargetNotFound for structurally-valid missing source, got %v", err)
	}
}

func TestCreateEdge_SourceNotFound(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	tgt, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", uuid.New().String(), tgt.Id, nil, "")
	if err == nil {
		t.Fatal("expected error for non-existent source")
	}
	if !errors.Is(err, store.ErrSourceOrTargetNotFound) {
		t.Errorf("expected ErrSourceOrTargetNotFound, got %v", err)
	}
}

func TestCreateEdge_RuleViolation(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	doc, err := s.CreateEntity(context.Background(), "Document", "",
		map[string]string{"title": "doc"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity doc: %v", err)
	}

	// Component rules only allow → Component, not → Document.
	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, doc.Id, nil, "")
	if err == nil {
		t.Fatal("expected edge rule violation")
	}
	if !errors.Is(err, store.ErrEdgeRuleViolation) {
		t.Errorf("expected ErrEdgeRuleViolation, got %v", err)
	}
}

func TestCreateEdge_TargetNotFound(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, uuid.New().String(), nil, "")
	if err == nil {
		t.Fatal("expected error for non-existent target")
	}
	if !errors.Is(err, store.ErrSourceOrTargetNotFound) {
		t.Errorf("expected ErrSourceOrTargetNotFound, got %v", err)
	}
}

// A genuine DB failure from the source/target existence probe must propagate as
// an operational error, not be masked as ErrSourceOrTargetNotFound (a transient
// DB failure must never surface to the client as "source entity not found" /
// NOT_FOUND). Regression: CreateEdge wrapped every findEntityByID error —
// including Prepare/Execute failures — in ErrSourceOrTargetNotFound.
func TestCreateEdge_PropagatesProbeOperationalError(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	db := s.(*ladybugDB)
	db.mu.Lock()
	// A phantom entity-type def with no backing table: findEntityByID's Prepare
	// fails with an operational error. Replacing the defs (rather than adding to
	// them) makes the failure deterministic regardless of map iteration order —
	// the probe can never succeed against a real type.
	db.entityTypeDefs = map[string]*store.EntityTypeDef{
		"NonExistentTable": {Name: "NonExistentTable"},
	}
	db.mu.Unlock()

	_, err = s.CreateEdge(ctx, "DependsOn", uuid.NewString(), uuid.NewString(), nil, "")
	if err == nil {
		t.Fatal("expected an operational error from the phantom-type probe")
	}
	if errors.Is(err, store.ErrSourceOrTargetNotFound) {
		t.Fatalf("expected the probe's operational error to propagate, not ErrSourceOrTargetNotFound: %v", err)
	}
}

func TestCreateEdge_NoRulesDeclared(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// Document declares no rules, so no edge creation is permitted from it.
	src, err := s.CreateEntity(context.Background(), "Document", "",
		map[string]string{"title": "src"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt, err := s.CreateEntity(context.Background(), "Document", "",
		map[string]string{"title": "tgt"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt.Id, nil, "")
	if err == nil {
		t.Fatal("expected edge rule violation for type with no rules")
	}
	if !errors.Is(err, store.ErrEdgeRuleViolation) {
		t.Errorf("expected ErrEdgeRuleViolation, got %v", err)
	}
}

func TestCreateEdge_InvalidUUID(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", "not-a-uuid", src.Id, nil, "")
	if err == nil {
		t.Fatal("expected error for invalid fromUUID")
	}
	if !errors.Is(err, store.ErrInvalidIDFormat) {
		t.Errorf("expected ErrInvalidIDFormat, got %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, "not-a-uuid", nil, "")
	if err == nil {
		t.Fatal("expected error for invalid toUUID")
	}
	if !errors.Is(err, store.ErrInvalidIDFormat) {
		t.Errorf("expected ErrInvalidIDFormat, got %v", err)
	}
}

func TestCreateEdge_UnknownProperty(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt.Id,
		map[string]string{"bogus": "x"}, "")
	if err == nil {
		t.Fatal("expected error for unknown edge property")
	}
	if !errors.Is(err, store.ErrUnknownProperty) {
		t.Errorf("expected ErrUnknownProperty, got %v", err)
	}
}

func TestCreateEdge_UnknownEdgeType(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DoesNotExist", src.Id, tgt.Id, nil, "")
	if err == nil {
		t.Fatal("expected error for unknown edge type")
	}
	if !errors.Is(err, store.ErrUnknownEdgeType) {
		t.Errorf("expected ErrUnknownEdgeType, got %v", err)
	}
}
