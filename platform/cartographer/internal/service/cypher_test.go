package service

import (
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestExecuteCypher_EmptyQuery(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: ""})
	if err == nil {
		t.Fatal("expected error for empty query, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// TestExecuteCypher_Params pins the SPEC R2 params contract: params must
// decode from a JSON object, and the wire type is google.protobuf.Struct per
// the SPEC error-table row "ExecuteCypher params not a JSON object". A Struct
// payload flows through the handler to the store; a scalar or list params
// value is structurally inexpressible in a parsed request — protobuf unmarshal
// rejects it before handler code runs (the same annotation the store makes for
// non-string property values, store.go:55-62). The protojson round-trip below
// pins that rejection: it fails if the wire type ever regresses to
// google.protobuf.Value, whose kind-oneof would accept a scalar or list.
func TestExecuteCypher_Params(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()
	applyTestSchema(ctx, t, srv.store)

	// Positive: a Struct params payload is passed through to the store.
	ent, err := srv.store.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "param-test"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	resp, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
		Cypher: "MATCH (n:Component {id: $id}) RETURN n.name AS name",
		Params: &structpb.Struct{Fields: map[string]*structpb.Value{
			"id": structpb.NewStringValue(ent.Id),
		}},
	})
	if err != nil {
		t.Fatalf("ExecuteCypher with struct params failed: %v", err)
	}
	if len(resp.Rows) != 1 || len(resp.Rows[0].Values) != 1 || resp.Rows[0].Values[0] != "param-test" {
		t.Fatalf("unexpected rows for params query: %+v", resp.Rows)
	}

	// Negative: a scalar or list params value cannot decode into the Struct
	// wire field (SPEC error-table row "ExecuteCypher params not a JSON
	// object") — protojson rejects it at the wire boundary.
	for _, payload := range []string{
		`{"cypher": "MATCH (n) RETURN n", "params": [1, 2, 3]}`,
		`{"cypher": "MATCH (n) RETURN n", "params": "scalar"}`,
	} {
		if err := protojson.Unmarshal([]byte(payload), &flowv1.ExecuteCypherRequest{}); err == nil {
			t.Fatalf("expected scalar/list params to be rejected at the wire boundary, got nil for %s", payload)
		}
	}
}

func TestExecuteCypher_ValidQuery(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "test"}, nil, "")

	resp, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (n:Component) RETURN n"})
	if err != nil {
		t.Fatalf("ExecuteCypher failed: %v", err)
	}
	if len(resp.Rows) == 0 {
		t.Fatal("expected at least one row")
	}
}

// TestRowsToRowsOneTuplePerRow asserts the SPEC R2 flat-tuple Row contract:
// each Row is one flat tuple of string values in the order the caller supplied
// (LadybugDB return order) — no sorted column schema, no cross-row alignment
// or null-filling, and non-string values stringified. A null column (an absent
// property in a RETURN) becomes the empty string, since the v1 wire carries no
// null marker.
func TestRowsToRowsOneTuplePerRow(t *testing.T) {
	rows := []store.CypherRow{
		{Values: []any{"id-1", "x", int64(2)}},
		{Values: []any{"id-2"}},
	}
	got := rowsToRows(rows)
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}
	want := [][]string{
		{"id-1", "x", "2"},
		{"id-2"},
	}
	for i, row := range got {
		if len(row.Values) != len(want[i]) {
			t.Fatalf("row %d: expected %d values, got %d", i, len(want[i]), len(row.Values))
		}
		for j, w := range want[i] {
			if row.Values[j] != w {
				t.Fatalf("row %d value %d: expected %q, got %q", i, j, w, row.Values[j])
			}
		}
	}
}

// TestCypherValueStringNullIsEmpty asserts a null column value stringifies to
// the empty string on the string-only v1 row wire.
func TestCypherValueStringNullIsEmpty(t *testing.T) {
	if got, want := cypherValueString(nil), ""; got != want {
		t.Fatalf("cypherValueString(nil) = %q, want %q", got, want)
	}
	if got, want := cypherValueString("hello"), "hello"; got != want {
		t.Fatalf("cypherValueString(\"hello\") = %q, want %q", got, want)
	}
}

func TestExecuteCypher_MutationRejected(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()
	applyTestSchema(ctx, t, st)

	// Each mutation/DDL clause the SPEC R7 §5 and the error table enumerate
	// (CREATE, SET, DELETE, MERGE, REMOVE, DROP, DDL index/constraint, and
	// FOREACH-as-mutation) must be rejected by ExecuteCypher so no mutation
	// ever executes through the read-only RPC.
	//
	// LadybugDB v0.17.0's parser recognises CREATE/SET/DELETE/MERGE/DROP and
	// classifies each as non-read-only, surfacing ErrMutationCypher which the
	// service maps to PERMISSION_DENIED (row "ExecuteCypher with mutation
	// statement", SPEC:976). Its grammar does not parse top-level FOREACH,
	// `MATCH ... REMOVE ...`, or index/constraint DDL; those fail at the
	// syntax gate and surface INVALID_ARGUMENT ("Invalid Cypher syntax",
	// SPEC:979) — SPEC R3 mandates INVALID_ARGUMENT for every statement that
	// fails to parse (SPEC:260) and the R7 §5 grammar-gap note pins it "never
	// as PERMISSION_DENIED" (SPEC:493-497). The syntax gate precedes read-only
	// enforcement in the ExecuteCypher check order (SPEC:1015).
	cases := []struct {
		name           string
		cypher         string
		wantStatusCode codes.Code
	}{
		{"create", "CREATE (n:Component {id: '11111111-1111-1111-1111-111111111111', name: 'x'})", codes.PermissionDenied},
		{"set", "MATCH (n:Component) SET n.name = 'x'", codes.PermissionDenied},
		{"delete", "MATCH (n:Component) DELETE n", codes.PermissionDenied},
		{"merge", "MERGE (n:Component {id: '11111111-1111-1111-1111-111111111111'})", codes.PermissionDenied},
		{"drop", "DROP TABLE Component", codes.PermissionDenied},
		{"remove", "MATCH (n:Component) REMOVE n.name", codes.InvalidArgument},
		{"ddl-index", "CREATE INDEX Component_name IF NOT EXISTS FOR (n:Component) ON (n.name)", codes.InvalidArgument},
		{"ddl-constraint", "CREATE CONSTRAINT IF NOT EXISTS FOR (n:Component) REQUIRE n.id IS UNIQUE",
			codes.InvalidArgument},
		{"foreach-as-mutation", "FOREACH (x IN ['aaa'] | CREATE (n:Component {id: x}))", codes.InvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: tc.cypher})
			if err == nil {
				t.Fatalf("expected error for mutation %q, got nil", tc.cypher)
			}
			if got := status.Code(err); got != tc.wantStatusCode {
				t.Errorf("mutation %q: expected %v, got %v (%v)", tc.cypher, tc.wantStatusCode, got, err)
			}
		})
	}
}

func TestExecuteCypher_InvalidSyntax(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
		Cypher: "NOT VALID CYPHER SYNTAX @@@",
	})
	if err == nil {
		t.Fatal("expected error for invalid Cypher syntax, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestExecuteCypher_MissingReadCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only WRITE capabilities, no READ.
	ctx := capabilityContext("WRITE:graph/entity/*,WRITE:graph/tx", scPriv, "sidecar")

	// Schema is applied so the unlabelled query parses server-side; the
	// no-labels statement then falls back to the READ:graph/entity/* check,
	// which a write-only caller lacks (SPEC R3 server-authoritative path).
	applyTestSchema(ctx, t, srv.store)

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (n) RETURN n"})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing READ capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// TestExecuteCypher_NoLabelsFallsBackToWildcard asserts the SPEC R3 wildcard
// fallback on the server-authoritative path: a statement that parses as a
// read-only cross-type read but yields no labels (an unlabelled MATCH) is
// checked against READ:graph/entity/* — which a write-only caller lacks.
func TestExecuteCypher_NoLabelsFallsBackToWildcard(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx() // WRITE-only capabilities, no READ.
	applyTestSchema(ctx, t, srv.store)

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: "MATCH (n) RETURN n"})
	if err == nil {
		t.Fatal("expected PermissionDenied for write-only caller, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// TestExecuteCypher_MultiTypeSubsetRejected asserts SPEC R3 (SPEC:249): the
// Cartographer derives the referenced entity-type labels from its own
// server-side parse of the statement, and a caller holding read capability
// for only a subset of the referenced types is rejected with PERMISSION_DENIED
// — the specific-type check must not fall back to the wildcard.
func TestExecuteCypher_MultiTypeSubsetRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("READ:graph/entity/Component", "READ:graph/tx")
	applyTestSchema(ctx, t, srv.store)

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
		Cypher: "MATCH (a:Component)-[:DEPENDS_ON]->(b:Service) RETURN b",
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for subset capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v (%v)", status.Code(err), err)
	}
}

// TestExecuteCypher_SingleTypeSpecificCapabilityPasses asserts SPEC R3: a
// caller holding READ:graph/entity/Component passes the server-side per-type
// check for a single-type query referencing exactly Component, and the query
// executes successfully (the per-type branch is not a blanket rejection).
func TestExecuteCypher_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("READ:graph/entity/Component", "READ:graph/tx")
	applyTestSchema(ctx, t, srv.store)
	_, _ = srv.store.CreateEntity(testCtx(), "Component", "", map[string]string{"name": "x"}, nil, "")

	resp, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
		Cypher: "MATCH (n:Component) RETURN n",
	})
	if err != nil {
		t.Fatalf("ExecuteCypher failed: %v", err)
	}
	if len(resp.Rows) == 0 {
		t.Fatal("expected at least one row")
	}
}

// TestExecuteCypher_WildcardHolderPassesMultiType asserts SPEC R3 (SPEC:249):
// a caller holding READ:graph/entity/* passes regardless of the label set the
// server extracts from its own parse, and the query executes successfully.
func TestExecuteCypher_WildcardHolderPassesMultiType(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("READ:graph/entity/*", "READ:graph/tx")
	applyTestSchema(ctx, t, srv.store)
	comp, _ := srv.store.CreateEntity(testCtx(), "Component", "", map[string]string{"name": "x"}, nil, "")
	svc, _ := srv.store.CreateEntity(testCtx(), "Service", "", map[string]string{"name": "s"}, nil, "")
	_, _ = srv.store.CreateEdge(testCtx(), "DEPENDS_ON", svc.Id, comp.Id, nil, "")

	resp, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
		Cypher: "MATCH (a:Service)-[:DEPENDS_ON]->(b:Component) RETURN b",
	})
	if err != nil {
		t.Fatalf("ExecuteCypher failed: %v", err)
	}
	if len(resp.Rows) == 0 {
		t.Fatal("expected at least one row")
	}
}

// TestExecuteCypher_MutationRejectedBeforeCapability pins the SPEC ExecuteCypher
// check order (SPEC:1018: empty query → Cypher syntax → read-only enforcement →
// capability) for a combined fault: a caller lacking READ capability sending a
// mutation statement must receive the read-only enforcement's PERMISSION_DENIED
// ("mutation or DDL Cypher statements are not allowed"), NOT the capability
// gate's denial — the read-only enforcement precedes the capability check.
// Every mutation test uses a full-capability context and every capability test
// uses a read-only statement, so only a combined-fault test can detect a
// reorder that surfaced the capability denial ahead of the read-only rejection.
func TestExecuteCypher_MutationRejectedBeforeCapability(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := noReadCtx() // WRITE-only, no READ
	applyTestSchema(ctx, t, st)

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
		Cypher: "CREATE (n:Component {id: '11111111-1111-1111-1111-111111111111', name: 'x'})",
	})
	if err == nil {
		t.Fatal("expected error for mutation statement from a no-READ caller, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
	if msg := status.Convert(err).Message(); !strings.Contains(msg, "mutation or DDL") {
		t.Fatalf("expected the read-only enforcement's rejection (mutation/DDL), got %q", msg)
	}
}

// TestExecuteCypher_EmptyQueryBeforeCapability pins the SPEC ExecuteCypher
// check order (SPEC:1018: empty query → Cypher syntax → read-only enforcement →
// capability) for the empty-query gate: a caller lacking READ capability
// sending an EMPTY query must receive the empty-query gate's INVALID_ARGUMENT
// ("Empty ExecuteCypher query"), NOT the capability gate's PERMISSION_DENIED —
// the empty-query gate precedes the capability check. The plain
// TestExecuteCypher_EmptyQuery runs with a full-capability context, so only a
// combined fault can detect a reorder that hoisted the capability gate ahead.
func TestExecuteCypher_EmptyQueryBeforeCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx() // WRITE-only, no READ

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{Cypher: ""})
	if err == nil {
		t.Fatal("expected error for empty query from a no-READ caller, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument (empty-query gate first), got %v", status.Code(err))
	}
}

// TestExecuteCypher_InvalidSyntaxBeforeCapability pins the SPEC ExecuteCypher
// check order (SPEC:1018) for the syntax gate: a caller lacking READ capability
// sending a syntactically invalid query must receive the parse gate's
// INVALID_ARGUMENT ("Invalid Cypher syntax"), NOT the capability gate's
// PERMISSION_DENIED — the syntax gate precedes the capability check. The plain
// TestExecuteCypher_InvalidSyntax runs with a full-capability context, so only
// a combined fault can detect a reorder that hoisted the capability gate ahead.
func TestExecuteCypher_InvalidSyntaxBeforeCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := noReadCtx() // WRITE-only, no READ
	applyTestSchema(ctx, t, srv.store)

	_, err := srv.ExecuteCypher(ctx, &flowv1.ExecuteCypherRequest{
		Cypher: "NOT VALID CYPHER SYNTAX @@@",
	})
	if err == nil {
		t.Fatal("expected error for invalid Cypher syntax from a no-READ caller, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument (syntax gate first), got %v", status.Code(err))
	}
}
