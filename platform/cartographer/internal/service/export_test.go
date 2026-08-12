package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// TestExportGraph_EmbeddingsAndNullPropertiesExcluded pins the SPEC R11
// format-table rows (SPEC:669-670): "Null properties are omitted from the
// output" and "Embeddings are excluded from JSON output" /
// "Embeddings are excluded from GraphML output". It creates a vector-enabled
// entity whose optional property is left unset (NULL in the store) and whose
// embedding is persisted, plus an edge with an unset optional property, then
// asserts neither the embedding nor the null properties leak into either
// export format.
func TestExportGraph_EmbeddingsAndNullPropertiesExcluded(t *testing.T) {
	srv, st := newTestServer(t)
	seedExportGraphWithEmbeddingAndNullProps(t, st)
	t.Run("json", func(t *testing.T) {
		assertJSONExportOmitsEmbeddingAndNullProps(t, exportGraphData(t, srv, ExportFormatJSON))
	})
	t.Run("graphml", func(t *testing.T) {
		assertGraphMLExportOmitsEmbeddingAndNullProps(t, exportGraphData(t, srv, ExportFormatGraphML))
	})
}

// seedExportGraphWithEmbeddingAndNullProps applies a vector-enabled schema and
// creates a graph whose entity carries a persisted embedding and whose entity
// and edge each declare an optional property that is left unset (NULL). It also
// pins the preconditions the export exclusions rely on: the store round-trips
// the embedding and omits the unset properties on read-back.
func seedExportGraphWithEmbeddingAndNullProps(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string", Required: true},
					{Name: "description", Type: "string"}, // optional — left unset → NULL
				},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"VecType"}, Using: []string{"LINKS"}},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "LINKS", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}}}, // optional — left unset → NULL
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	ent, err := st.CreateEntity(ctx, "VecType", "",
		map[string]string{"name": "doc-1"}, []float32{1.0, 0.0, 0.0}, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	other, err := st.CreateEntity(ctx, "VecType", "", map[string]string{"name": "doc-2"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity other: %v", err)
	}
	edge, err := st.CreateEdge(ctx, "LINKS", ent.Id, other.Id, nil, "")
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}
	got, err := st.GetEntity(ctx, ent.Id, "")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if len(got.Embedding) != 3 {
		t.Fatalf("expected persisted embedding to survive the read path, got %v", got.Embedding)
	}
	if _, present := got.Properties["description"]; present {
		t.Fatalf("expected unset property to be omitted by the store, got %+v", got.Properties)
	}
	gotEdge, err := st.GetEdge(ctx, edge.Id, "")
	if err != nil {
		t.Fatalf("GetEdge: %v", err)
	}
	if _, present := gotEdge.Properties["weight"]; present {
		t.Fatalf("expected unset edge property to be omitted by the store, got %+v", gotEdge.Properties)
	}
}

// exportGraphData runs ExportGraph through the stream interceptor with the
// READ:graph/entity/* capability and returns the serialised payload.
func exportGraphData(t *testing.T, srv *CartographerServer, format string) []byte {
	t.Helper()
	stream := &mockExportStream{ctx: capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")}
	handlerInvoked, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: format}, stream)
	if err != nil {
		t.Fatalf("export %s failed: %v", format, err)
	}
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
	return stream.data
}

// assertJSONExportOmitsEmbeddingAndNullProps asserts the SPEC R11 json format
// row: the serialised output contains no embedding data and no declared-but-
// unset property keys, while the set node/edge properties survive intact.
func assertJSONExportOmitsEmbeddingAndNullProps(t *testing.T, raw []byte) {
	t.Helper()
	if strings.Contains(string(raw), "embedding") {
		t.Fatalf("embedding leaked into JSON export: %s", raw)
	}
	for _, nullProp := range []string{"description", "weight"} {
		if strings.Contains(string(raw), nullProp) {
			t.Fatalf("null property %q leaked into JSON export: %s", nullProp, raw)
		}
	}
	var out struct {
		Nodes []map[string]any `json:"nodes"`
		Edges []map[string]any `json:"edges"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("invalid export JSON: %v", err)
	}
	if len(out.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(out.Nodes))
	}
	var nodeProps map[string]any
	for _, node := range out.Nodes {
		props, ok := node["properties"].(map[string]any)
		if !ok {
			t.Fatalf("node entry missing object properties key: %+v", node)
		}
		if props["name"] == "doc-1" {
			nodeProps = props
		}
	}
	if len(nodeProps) != 1 || nodeProps["name"] != "doc-1" {
		t.Fatalf("expected exactly {name: doc-1} in exported node properties, got %v", nodeProps)
	}
	if len(out.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(out.Edges))
	}
	edgeProps, ok := out.Edges[0]["properties"].(map[string]any)
	if !ok {
		t.Fatalf("edge entry missing object properties key: %+v", out.Edges[0])
	}
	if len(edgeProps) != 0 {
		t.Fatalf("expected empty properties for null-property edge, got %v", edgeProps)
	}
}

// assertGraphMLExportOmitsEmbeddingAndNullProps asserts the SPEC R11 graphml
// format row: the serialised output contains no embedding data and no
// declared-but-unset property keys, while the set node/edge data survives.
func assertGraphMLExportOmitsEmbeddingAndNullProps(t *testing.T, raw []byte) {
	t.Helper()
	if strings.Contains(string(raw), "embedding") {
		t.Fatalf("embedding leaked into GraphML export: %s", raw)
	}
	for _, nullProp := range []string{"description", "weight"} {
		if strings.Contains(string(raw), nullProp) {
			t.Fatalf("null property %q leaked into GraphML export: %s", nullProp, raw)
		}
	}
	if !strings.Contains(string(raw), `<data key="name">doc-1</data>`) {
		t.Fatalf("expected the set property in GraphML export: %s", raw)
	}
	if !strings.Contains(string(raw), `<edge id="`) {
		t.Fatalf("expected the edge in GraphML export: %s", raw)
	}
}
