package service

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestExportGraph_UnsupportedFormat(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")

	handlerInvoked, err := invokeExportGraph(
		srv, &flowv1.ExportGraphRequest{Format: "unsupported"}, &mockExportStream{ctx: ctx},
	)
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestExportGraph_JSON(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "a"}, nil, "")
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "b"}, nil, "")

	stream := &mockExportStream{ctx: capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")}
	handlerInvoked, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "json"}, stream)
	if err != nil {
		t.Fatalf("export JSON failed: %v", err)
	}
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
	if len(stream.data) == 0 {
		t.Fatal("expected non-empty export data")
	}
}

func TestExportGraph_GraphML(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	applyTestSchema(ctx, t, srv.store)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "x"}, nil, "")

	stream := &mockExportStream{ctx: capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")}
	handlerInvoked, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "graphml"}, stream)
	if err != nil {
		t.Fatalf("export GraphML failed: %v", err)
	}
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
	if len(stream.data) == 0 {
		t.Fatal("expected non-empty export data")
	}

	// Validate XML structure.
	type graphmlKey struct {
		ID       string `xml:"id,attr"`
		For      string `xml:"for,attr"`
		AttrName string `xml:"attr.name,attr"`
		AttrType string `xml:"attr.type,attr"`
	}
	type graphmlNode struct {
		ID string `xml:"id,attr"`
	}
	type graphmlEdge struct {
		ID     string `xml:"id,attr"`
		Source string `xml:"source,attr"`
		Target string `xml:"target,attr"`
	}
	type graphmlGraph struct {
		ID          string        `xml:"id,attr"`
		EdgeDefault string        `xml:"edgedefault,attr"`
		Nodes       []graphmlNode `xml:"node"`
		Edges       []graphmlEdge `xml:"edge"`
	}
	type graphmlRoot struct {
		XMLName struct{}     `xml:"graphml"`
		Keys    []graphmlKey `xml:"key"`
		Graph   graphmlGraph `xml:"graph"`
	}

	var root graphmlRoot
	if err := xml.Unmarshal(stream.data, &root); err != nil {
		t.Fatalf("invalid GraphML XML: %v", err)
	}
	if root.Graph.ID != "G" {
		t.Errorf("expected graph id G, got %q", root.Graph.ID)
	}
	if root.Graph.EdgeDefault != "directed" {
		t.Errorf("expected edgedefault directed, got %q", root.Graph.EdgeDefault)
	}
	if len(root.Graph.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(root.Graph.Nodes))
	}
	if root.Graph.Nodes[0].ID != ent.Id {
		t.Errorf("expected node id %q, got %q", ent.Id, root.Graph.Nodes[0].ID)
	}
	foundNameKey := false
	for _, k := range root.Keys {
		if k.ID == "name" && k.For == "node" && k.AttrName == "name" && k.AttrType == "string" {
			foundNameKey = true
		}
	}
	if !foundNameKey {
		t.Error("expected <key> declaration for property 'name' on nodes")
	}
}

// TestSerializeGraph_GraphMLDeterministic pins the serializer determinism
// contract: GraphML <data> elements inside each node/edge are emitted in sorted
// property-key order (the same order as the <key> declarations), so repeated
// serialisation of the same graph is byte-identical. Map-iteration order is
// randomised by the runtime, so this test fails without the sort.
func TestSerializeGraph_GraphMLDeterministic(t *testing.T) {
	entities := []store.Entity{
		{Id: "e1", Type: "Component", Properties: map[string]string{"z": "1", "a": "2", "m": "3"}},
	}
	edges := []store.Edge{
		{
			Id:           "ed1",
			Type:         "DEPENDS_ON",
			FromEntityID: "e1",
			ToEntityID:   "e2",
			Properties:   map[string]string{"w": "5", "k": "x", "q": "y"},
		},
	}
	want := `<?xml version="1.0" encoding="UTF-8"?>
<graphml xmlns="http://graphml.graphdrawing.org/xmlns">
  <key id="a" for="node" attr.name="a" attr.type="string"/>
  <key id="m" for="node" attr.name="m" attr.type="string"/>
  <key id="z" for="node" attr.name="z" attr.type="string"/>
  <key id="k" for="edge" attr.name="k" attr.type="string"/>
  <key id="q" for="edge" attr.name="q" attr.type="string"/>
  <key id="w" for="edge" attr.name="w" attr.type="string"/>
  <graph id="G" edgedefault="directed">
    <node id="e1"><data key="a">2</data><data key="m">3</data><data key="z">1</data></node>
    <edge id="ed1" source="e1" target="e2"><data key="k">x</data><data key="q">y</data><data key="w">5</data></edge>
  </graph>
</graphml>
`
	first, err := serializeGraph(ExportFormatGraphML, entities, edges)
	if err != nil {
		t.Fatalf("serializeGraph: %v", err)
	}
	if got := string(first); got != want {
		t.Fatalf("GraphML output mismatch\nwant: %s\ngot:  %s", want, got)
	}
	for i := range 9 {
		got, err := serializeGraph(ExportFormatGraphML, entities, edges)
		if err != nil {
			t.Fatalf("serializeGraph iteration %d: %v", i, err)
		}
		if string(got) != string(first) {
			t.Fatalf("GraphML serialisation is non-deterministic (iteration %d differs):\nfirst: %s\niter:  %s", i, first, got)
		}
	}
}

func TestExportGraph_EmptyGraph(t *testing.T) {
	srv, _ := newTestServer(t)

	// No data in the graph — export should succeed with an empty result.
	stream := &mockExportStream{
		ctx: capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar"),
	}
	handlerInvoked, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "json"}, stream)
	if err != nil {
		t.Fatalf("export empty graph failed: %v", err)
	}
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
}

// TestExportGraph_JSONShape pins the SPEC R11 (SPEC:619) JSON export shape: the
// graph always serialises with top-level "nodes" and "edges" arrays — an empty
// graph is {"nodes":[],"edges":[]}, not {} — and every node/edge entry carries a
// top-level "properties" key, an empty object when the element has no properties.
func TestExportGraph_JSONShape(t *testing.T) {
	t.Run("empty graph serialises with empty arrays", func(t *testing.T) {
		srv, _ := newTestServer(t)
		stream := &mockExportStream{ctx: capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")}
		handlerInvoked, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "json"}, stream)
		if err != nil {
			t.Fatalf("export empty graph failed: %v", err)
		}
		if !handlerInvoked {
			t.Fatal("stream interceptor did not invoke ExportGraph")
		}
		if got, want := string(stream.data), `{"nodes":[],"edges":[]}`; got != want {
			t.Fatalf("empty-graph JSON = %s, want %s", got, want)
		}
	})

	t.Run("entries always carry a properties key", func(t *testing.T) {
		srv, st := newTestServer(t)
		ctx := context.Background()
		if err := st.ApplySchema(ctx, &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{
				{
					Name: "Loose",
					// No declared properties, so a property-less entity has a
					// genuinely empty properties map (a declared-but-unset
					// property would be materialised by the store instead).
					Rules: []*flowv1.ConnectionRule{{
						CanConnectTo: []string{"Loose"}, Using: []string{"DEPENDS_ON"},
					}},
				},
			},
			EdgeTypes: []*flowv1.EdgeType{{Name: "DEPENDS_ON"}},
		}); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		// Create an entity with no properties and an edge with no properties.
		ent, err := st.CreateEntity(ctx, "Loose", "", nil, nil, "")
		if err != nil {
			t.Fatalf("CreateEntity: %v", err)
		}
		other, err := st.CreateEntity(ctx, "Loose", "", nil, nil, "")
		if err != nil {
			t.Fatalf("CreateEntity other: %v", err)
		}
		if _, err := st.CreateEdge(ctx, "DEPENDS_ON", ent.Id, other.Id, nil, ""); err != nil {
			t.Fatalf("CreateEdge: %v", err)
		}

		stream := &mockExportStream{ctx: capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")}
		if _, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "json"}, stream); err != nil {
			t.Fatalf("export failed: %v", err)
		}
		var out struct {
			Nodes []map[string]any `json:"nodes"`
			Edges []map[string]any `json:"edges"`
		}
		if err := json.Unmarshal(stream.data, &out); err != nil {
			t.Fatalf("invalid export JSON: %v", err)
		}
		if len(out.Nodes) != 2 {
			t.Fatalf("expected 2 nodes, got %d", len(out.Nodes))
		}
		for _, node := range out.Nodes {
			props, ok := node["properties"].(map[string]any)
			if !ok {
				t.Fatalf("node entry missing object properties key: %+v", node)
			}
			if len(props) != 0 {
				t.Fatalf("expected empty properties object for property-less node, got %v", props)
			}
		}
		if len(out.Edges) != 1 {
			t.Fatalf("expected 1 edge, got %d", len(out.Edges))
		}
		props, ok := out.Edges[0]["properties"].(map[string]any)
		if !ok {
			t.Fatalf("edge entry missing object properties key: %+v", out.Edges[0])
		}
		if len(props) != 0 {
			t.Fatalf("expected empty properties object for property-less edge, got %v", props)
		}
	})
}

func TestExportGraph_MidStreamFailure(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Add some data so export has content.
	applySchemaCtx := context.Background()
	applyTestSchema(applySchemaCtx, t, srv.store)
	_, _ = srv.store.CreateEntity(applySchemaCtx, "Component", "", map[string]string{"name": "a"}, nil, "")

	stream := &mockExportStream{
		ctx:     capabilityContext("READ:graph/entity/*", scPriv, "sidecar"),
		sendErr: fmt.Errorf("stream send failure"),
	}
	handlerInvoked, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "json"}, stream)
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
	if err == nil {
		t.Fatal("expected error for mid-stream failure, got nil")
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", status.Code(err))
	}
}

func TestExportGraph_BufferAllocationFailure(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Wrap store to panic on ListMainEntityTypes, simulating an OOM.
	srv.store = &panicStore{Store: st}

	stream := &mockExportStream{ctx: capabilityContext("READ:graph/entity/*", scPriv, "sidecar")}
	handlerInvoked, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "json"}, stream)
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
	if err == nil {
		t.Fatal("expected error for buffer allocation failure, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
	}
}

func TestExportGraph_MissingReadCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only WRITE capabilities, no READ:graph/entity/*.
	ctx := capabilityContext("WRITE:graph/entity/*,WRITE:graph/tx", scPriv, "sidecar")
	stream := &mockExportStream{ctx: ctx}

	handlerInvoked, err := invokeExportGraph(
		srv, &flowv1.ExportGraphRequest{Format: "json"}, stream,
	)
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
	if stream.verifiedCaps == nil || len(stream.verifiedCaps.Caps) != 2 ||
		stream.verifiedCaps.Caps[0] != "WRITE:graph/entity/*" || stream.verifiedCaps.Caps[1] != "WRITE:graph/tx" {
		t.Fatalf("handler did not receive interceptor-verified WRITE capabilities: %+v", stream.verifiedCaps)
	}
	if status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != "capability denied: READ:graph/entity/*" {
		t.Fatalf("expected missing-READ PermissionDenied, got %v", err)
	}
}

// TestExportGraph_PerTypeReadCapabilityDenied pins the SPEC R3 negative branch
// (SPEC:241: only READ:graph/entity/* "Authorises the above plus
// ExportGraph(format)"): a caller holding only a per-type READ grant (e.g.
// READ:graph/entity/Component) must be denied ExportGraph.
// TestExportGraph_MissingReadCapability uses a WRITE-only holder; if the
// wildcard gate regressed to accept per-type grants, only this test fails.
func TestExportGraph_PerTypeReadCapabilityDenied(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only a per-type READ capability, no READ:graph/entity/*.
	ctx := capabilityContext("READ:graph/entity/Component", scPriv, "sidecar")
	stream := &mockExportStream{ctx: ctx}

	handlerInvoked, err := invokeExportGraph(
		srv, &flowv1.ExportGraphRequest{Format: "json"}, stream,
	)
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
	if status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != "capability denied: READ:graph/entity/*" {
		t.Fatalf("expected per-type-only PermissionDenied for ExportGraph, got %v", err)
	}
}

func TestExportGraph_DeterministicResourceExhausted(t *testing.T) {
	base, _ := openTestStore(t)
	t.Cleanup(func() { _ = base.Close() })
	gs, _ := gitstore.New(t.TempDir())
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(base, gs, opPub, initTestKey(), nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()
	srv.store = &exhaustedStore{Store: base}

	stream := &mockExportStream{ctx: capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")}
	handlerInvoked, err := invokeExportGraph(srv, &flowv1.ExportGraphRequest{Format: "json"}, stream)
	if !handlerInvoked {
		t.Fatal("stream interceptor did not invoke ExportGraph")
	}
	if err == nil {
		t.Fatal("expected error during enumeration, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v (%v)", status.Code(err), err)
	}
	if len(stream.data) != 0 {
		t.Fatalf("expected no data streamed on failure, got %d bytes", len(stream.data))
	}
}
