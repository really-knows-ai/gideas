package ladybug

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/google/uuid"
)

func TestExecuteCypher_ReadOnly(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "cypher-test"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	rows, err := s.ExecuteCypher(context.Background(),
		"MATCH (n:Component {id: $id}) RETURN n.name AS name",
		map[string]any{"id": e.Id}, "")
	if err != nil {
		t.Fatalf("ExecuteCypher: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if len(rows[0].Values) != 1 {
		t.Fatalf("expected 1 value, got %d", len(rows[0].Values))
	}
	if got := rows[0].Values[0]; got != "cypher-test" {
		t.Errorf("name = %v, want cypher-test", got)
	}
}

func TestExecuteCypher_MutationRejected(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.ExecuteCypher(context.Background(),
		"CREATE (n:Component {id: 'bad-uuid'})", nil, "")
	if err == nil {
		t.Fatal("expected mutation to be rejected")
	}
	if !errors.Is(err, store.ErrMutationCypher) {
		t.Errorf("expected ErrMutationCypher, got %v", err)
	}
}

// TestExecuteCypher_MutationClausesClassified asserts the SPEC
// syntax-before-read-only check order (SPEC:1015) for the mutation/DDL clause
// set that R7 §5 and the error table enumerate (CREATE, SET, DELETE, MERGE,
// REMOVE, DROP, DDL index/constraint, FOREACH-as-mutation, and CALL with
// mutating procedures):
//
//   - Clauses the LadybugDB v0.17.0 grammar parses (CREATE, SET, DELETE,
//     MERGE, DROP) are classified non-read-only by the IsReadOnly guard and
//     surface ErrMutationCypher (mapped to PERMISSION_DENIED, error-table row
//     "ExecuteCypher with mutation statement", SPEC:976) — never executed as
//     read-only.
//   - Clauses the grammar cannot parse (top-level FOREACH, `MATCH ... REMOVE
//     ...`, index/constraint DDL) fail at Prepare *before* the IsReadOnly guard
//     runs. Per SPEC R3 "a statement that fails to parse is rejected with
//     INVALID_ARGUMENT — this comes from Prepare, unchanged" (SPEC:260), the
//     "Invalid Cypher syntax" row (SPEC:979), and the R7 §5 grammar-gap note
//     (SPEC:493-497, grammar-unparseable clauses surface INVALID_ARGUMENT
//     "never as PERMISSION_DENIED"), they surface ErrInvalidCypher
//     (INVALID_ARGUMENT) — the syntax gate precedes read-only enforcement, so a
//     statement that fails to parse is INVALID_ARGUMENT regardless of mutation
//     keywords in its text.
//
// A mutating CALL follows the same grammar-parse rule (SPEC R7 §5 "CALL with
// mutating procedures"): `CALL delete.mutations() RETURN *;` carries an
// enumerated mutation keyword in its dotted procedure name, but the dotted
// name is as grammar-unparseable as `load.csv` on v0.17.0, so it fails at
// Prepare and surfaces ErrInvalidCypher — as do the index-DDL procedures
// (CREATE/DROP_VECTOR_INDEX, CREATE/DROP_FTS_INDEX). A mutation/DDL statement
// the grammar cannot parse is never executed as read-only, but the syntax
// error surfaces before read-only enforcement ever runs; this is pinned in the
// prepare-fail loop and the "mutating CALLs grammar cannot parse" subtest
// below.
func TestExecuteCypher_MutationClausesClassified(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	mutationCases := []struct {
		name   string
		cypher string
	}{
		{"create", "CREATE (n:Component {id: 'bad-uuid'})"},
		{"set", "MATCH (n:Component) SET n.name = 'x'"},
		{"delete", "MATCH (n:Component) DELETE n"},
		{"merge", "MERGE (n:Component {id: 'bad-uuid'})"},
		{"drop", "DROP TABLE Component"},
	}
	for _, tc := range mutationCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ExecuteCypher(context.Background(), tc.cypher, nil, "")
			if !errors.Is(err, store.ErrMutationCypher) {
				t.Errorf("expected ErrMutationCypher for %q, got %v", tc.cypher, err)
			}
		})
	}

	// Grammar-gap mutations fail at Prepare, so the syntax gate surfaces
	// ErrInvalidCypher (SPEC R3 / SPEC:260; row "Invalid Cypher syntax",
	// SPEC:979; R7 §5 note SPEC:493-497).
	prepareFailCases := []struct {
		name   string
		cypher string
	}{
		{"create-drop-entity", "CREATE (n:Component {id: 'bad-uuid'}) DROP n"},
		{"remove", "MATCH (n:Component) REMOVE n.name"},
		{"ddl-index", "CREATE INDEX Component_name IF NOT EXISTS FOR (n:Component) ON (n.name)"},
		{"ddl-constraint", "CREATE CONSTRAINT IF NOT EXISTS FOR (n:Component) REQUIRE n.id IS UNIQUE"},
		{"foreach-as-mutation", "FOREACH (x IN ['aaa'] | CREATE (n:Component {id: x}))"},
		// A mutating-procedure CALL carrying a bare enumerated mutation keyword
		// in its procedure name (SPEC R7 §5: "CALL with mutating procedures")
		// is as grammar-unparseable as `load.csv` — the dotted name fails at
		// Prepare, so the syntax gate surfaces ErrInvalidCypher (SPEC:493-497):
		// the mutation keyword in its text does not override the
		// syntax-before-read-only order (SPEC:1015).
		{"call-mutating-procedure-keyword", "CALL delete.mutations() RETURN *;"},
	}
	for _, tc := range prepareFailCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ExecuteCypher(context.Background(), tc.cypher, nil, "")
			if !errors.Is(err, store.ErrInvalidCypher) {
				t.Errorf("expected ErrInvalidCypher for %q, got %v", tc.cypher, err)
			}
		})
	}

	// The rest of the "CALL with mutating procedures" clause class (SPEC:486)
	// is grammar-unparseable: the LadybugDB index-DDL procedures
	// (CREATE/DROP_VECTOR_INDEX, CREATE/DROP_FTS_INDEX) and the
	// LOAD-CSV-style procedure `load.csv` fail at Prepare, and their mutation
	// keywords are hidden behind non-word characters (CREATE_VECTOR_INDEX,
	// load.csv). They are still rejected — never executed as read-only — but
	// as ErrInvalidCypher (INVALID_ARGUMENT), never ErrMutationCypher
	// (PERMISSION_DENIED), following the SPEC's LOAD-CSV note (SPEC:493-497): a
	// statement the v0.17.0 grammar cannot parse surfaces the syntax error,
	// never PERMISSION_DENIED.
	t.Run("mutating-call-procedures-grammar-cannot-parse-invalid-argument", func(t *testing.T) {
		cases := []string{
			"CALL CREATE_VECTOR_INDEX('VectorType', 'VectorType_vec', 'embedding', metric := 'cosine');",
			"CALL DROP_VECTOR_INDEX('VectorType', 'VectorType_vec');",
			"CALL CREATE_FTS_INDEX('Document', 'Document_fts', ['title']);",
			"CALL DROP_FTS_INDEX('Document', 'Document_fts');",
			"CALL load.csv('file:///tmp/rows.csv') RETURN row;",
		}
		for _, cypher := range cases {
			_, err := s.ExecuteCypher(context.Background(), cypher, nil, "")
			if errors.Is(err, store.ErrMutationCypher) {
				t.Errorf("a mutating CALL the grammar cannot parse must surface INVALID_ARGUMENT, "+
					"never PERMISSION_DENIED (LOAD-CSV note, SPEC:493-497): %q got %v", cypher, err)
			}
			if !errors.Is(err, store.ErrInvalidCypher) {
				t.Errorf("expected ErrInvalidCypher for %q, got %v", cypher, err)
			}
		}
	})
}

// TestExecuteCypher_ReadOnlyClausesClassified asserts that each read-only
// clause form the SPEC R7 §5 read-only clause set enumerates (SPEC:480-481 —
// WITH, UNWIND, LOAD CSV, CALL with read-only procedures, alongside the
// MATCH/RETURN forms the historical tests pinned) passes the stmt.IsReadOnly()
// guard and executes through the real ExecuteCypher store path. The existing
// read-only success tests cover only MATCH ... RETURN forms; this test pins
// the rest of the SPEC-enumerated clause set.
//
// WITH, UNWIND, and read-only CALL clauses (show_tables, table_info) prepare
// and execute end-to-end on LadybugDB v0.17.0. LOAD CSV is the one clause in
// the SPEC set the v0.17.0 grammar does not parse: `LOAD CSV FROM ...` fails
// at Prepare with a parser exception, so it cannot be executed end-to-end.
// What is pinned for it is the classification — a read-only clause must never
// be rejected as a mutation, so LOAD CSV surfaces ErrInvalidCypher
// (INVALID_ARGUMENT), never ErrMutationCypher (PERMISSION_DENIED).
func TestExecuteCypher_ReadOnlyClausesClassified(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// A Component entity gives WITH/MATCH clauses a row to project.
	if _, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "cypher-test"}, nil, ""); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	t.Run("executable read-only clauses", func(t *testing.T) {
		cases := []struct {
			name     string
			cypher   string
			wantRows int
			// wantValue, when non-empty, is the expected first column of the
			// first row (a deterministic projection).
			wantValue string
		}{
			{"with", "MATCH (n:Component) WITH n.name AS name RETURN name", 1, "cypher-test"},
			{"unwind", "UNWIND [1, 2, 3] AS x RETURN x", 3, ""},
			{"call-show-tables", "CALL show_tables() RETURN *", 0, ""},
			{"call-table-info", "CALL table_info('Component') RETURN *", 0, ""},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				rows, err := s.ExecuteCypher(context.Background(), tc.cypher, nil, "")
				if err != nil {
					t.Fatalf("ExecuteCypher(%q): %v", tc.cypher, err)
				}
				// Catalog calls (show_tables/table_info) have engine-defined row
				// counts; pin only that they execute and return rows. The
				// projection cases pin exact counts and the projected value.
				if tc.wantRows > 0 {
					if len(rows) != tc.wantRows {
						t.Fatalf("expected %d rows, got %d", tc.wantRows, len(rows))
					}
				} else if len(rows) == 0 {
					t.Fatalf("expected at least 1 row, got 0")
				}
				if tc.wantValue != "" {
					if len(rows[0].Values) == 0 {
						t.Fatalf("expected a value in row 0, got %v", rows[0].Values)
					}
					if got := fmt.Sprintf("%v", rows[0].Values[0]); got != tc.wantValue {
						t.Errorf("row 0 value = %q, want %q", got, tc.wantValue)
					}
				}
			})
		}
	})

	t.Run("load-csv-classified-read-only", func(t *testing.T) {
		// The SPEC R7 §5 read-only clause set lists LOAD CSV (SPEC:481), but
		// LadybugDB v0.17.0's grammar does not parse Neo4j's LOAD CSV clause
		// (`LOAD CSV FROM ...` fails at Prepare with "Parser exception"). The
		// store cannot execute it end-to-end, so the pinnable property is the
		// classification: LOAD CSV is a read-only clause and must never be
		// rejected as a mutation. It surfaces ErrInvalidCypher (INVALID_ARGUMENT)
		// via the Prepare failure — the v0.17.0 grammar limitation — not
		// ErrMutationCypher (PERMISSION_DENIED).
		_, err := s.ExecuteCypher(context.Background(),
			"LOAD CSV FROM 'file:///tmp/rows.csv' AS row RETURN row", nil, "")
		if errors.Is(err, store.ErrMutationCypher) {
			t.Fatalf("LOAD CSV is a read-only clause per SPEC R7 §5 and must not be classified as mutation, got %v", err)
		}
		if !errors.Is(err, store.ErrInvalidCypher) {
			t.Fatalf("LOAD CSV must surface ErrInvalidCypher (v0.17.0 grammar cannot parse it), got %v", err)
		}
	})
}

// TestExecuteCypher_StringLiteralKeywordNotMutation pins the SPEC check-order
// consequence that a statement failing at Prepare surfaces ErrInvalidCypher
// (INVALID_ARGUMENT) regardless of mutation keywords in its text: the syntax
// gate precedes read-only enforcement (SPEC:1015), the "Invalid Cypher syntax"
// row is INVALID_ARGUMENT (SPEC:979), and SPEC R3 mandates INVALID_ARGUMENT for
// every statement that fails to parse (SPEC:260). A malformed read-only
// statement that happens to quote a mutation keyword inside a string literal or
// comment (e.g. `MATCH (n:Component) RETURN n 'delete'`) therefore keeps
// INVALID_ARGUMENT, never PERMISSION_DENIED.
func TestExecuteCypher_StringLiteralKeywordNotMutation(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	// The trailing 'delete' is a string literal in a statement the grammar
	// rejects at Prepare. Classifying it as a mutation would flip the SPEC's
	// syntax-before-read-only ordering into PERMISSION_DENIED.
	_, err = s.ExecuteCypher(context.Background(),
		"MATCH (n:Component) RETURN n 'delete'", nil, "")
	if errors.Is(err, store.ErrMutationCypher) {
		t.Fatalf("a malformed read-only statement quoting a mutation keyword must not be classified as mutation, got %v", err)
	}
	if !errors.Is(err, store.ErrInvalidCypher) {
		t.Fatalf("expected ErrInvalidCypher, got %v", err)
	}

	// A mutation keyword inside a comment must also be ignored: the statement
	// is genuinely malformed (unbalanced paren), and the `delete` keyword lives
	// only inside the /* */ comment.
	_, err = s.ExecuteCypher(context.Background(),
		"MATCH (n:Component RETURN n /* delete */", nil, "")
	if errors.Is(err, store.ErrMutationCypher) {
		t.Fatalf("a mutation keyword inside a comment must not be classified as mutation, got %v", err)
	}
	if !errors.Is(err, store.ErrInvalidCypher) {
		t.Fatalf("expected ErrInvalidCypher, got %v", err)
	}
}

// TestExecuteCypher_BareMutationKeywordTrailingReturnNotMutation pins the
// SPEC check-order consequence that a statement failing at Prepare surfaces
// ErrInvalidCypher (INVALID_ARGUMENT) regardless of mutation keywords in its
// text: a syntactically-invalid read-only statement whose text uses a bare
// mutation keyword AFTER a RETURN clause (e.g. `MATCH (n:Component) RETURN n
// DELETE`) is rejected at Prepare, and the SPEC check order "empty query →
// Cypher syntax → read-only enforcement → capability" (SPEC:1015) plus SPEC R3
// (SPEC:260) and the grammar-unparseable note (SPEC:493-497, "never as
// PERMISSION_DENIED") require INVALID_ARGUMENT. The same boundary must hold on
// the ExtractEntityTypes seam, whose error classification is identical to
// ExecuteCypher's (SPEC check order).
func TestExecuteCypher_BareMutationKeywordTrailingReturnNotMutation(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	cyphers := []string{
		"MATCH (n:Component) RETURN n DELETE",
		"MATCH (n) RETURN n DELETE",
	}
	for _, cypher := range cyphers {
		_, err := s.ExecuteCypher(context.Background(), cypher, nil, "")
		if errors.Is(err, store.ErrMutationCypher) {
			t.Fatalf("a bare mutation keyword trailing RETURN is syntax, not a clause: "+
				"expected ErrInvalidCypher for %q, got %v", cypher, err)
		}
		if !errors.Is(err, store.ErrInvalidCypher) {
			t.Fatalf("expected ErrInvalidCypher for %q, got %v", cypher, err)
		}

		_, err = s.ExtractEntityTypes(context.Background(), cypher)
		if errors.Is(err, store.ErrMutationCypher) {
			t.Fatalf("ExtractEntityTypes: a bare mutation keyword trailing RETURN must not "+
				"be classified as mutation for %q, got %v", cypher, err)
		}
		if !errors.Is(err, store.ErrInvalidCypher) {
			t.Fatalf("ExtractEntityTypes: expected ErrInvalidCypher for %q, got %v", cypher, err)
		}
	}
}

func TestExecuteCypher_WithParams(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "param-test", "version": "2"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	rows, err := s.ExecuteCypher(context.Background(),
		"MATCH (n:Component {id: $id}) RETURN n.version AS ver, n.name AS name",
		map[string]any{"id": e.Id}, "")
	if err != nil {
		t.Fatalf("ExecuteCypher: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	// SPEC R2: each row is one flat tuple in the order LadybugDB returns the
	// columns — ver before name, matching the RETURN clause.
	if len(rows[0].Values) != 2 {
		t.Fatalf("expected 2 values, got %d", len(rows[0].Values))
	}
	if rows[0].Values[0] != "2" {
		t.Errorf("ver = %v, want 2", rows[0].Values[0])
	}
	if rows[0].Values[1] != "param-test" {
		t.Errorf("name = %v, want param-test", rows[0].Values[1])
	}
}

func TestFindEntityByID_PropagatesPrepareError(t *testing.T) {
	// Call findEntityByID with a typeDefs map containing only a phantom type
	// that has no corresponding table in the database. LadybugDB returns an
	// error from Prepare, and findEntityByID propagates it as an operational
	// error rather than swallowing it into ErrEntityNotFound.
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	db := s.(*ladybugDB)
	// Only a phantom type — no real types. Prepare fails on the only type,
	// and findEntityByID propagates that failure as an operational error.
	phantomDefs := map[string]*store.EntityTypeDef{
		"NonExistentTable": {Name: "NonExistentTable"},
	}

	id := uuid.NewString()
	_, err = findEntityByID(db.conn, phantomDefs, id)
	if err == nil {
		t.Fatal("expected error from prepare on non-existent table, got nil")
	}
	if errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("expected operational error, not ErrEntityNotFound: %v", err)
	}
}

func TestFindEdgeByID_PropagatesPrepareError(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	db := s.(*ladybugDB)
	phantomDefs := map[string]*store.EdgeTypeDef{
		"NonExistentEdge": {Name: "NonExistentEdge"},
	}

	id := uuid.NewString()
	_, err = findEdgeByID(db.conn, phantomDefs, id)
	if err == nil {
		t.Fatal("expected error from prepare on non-existent edge table, got nil")
	}
	if errors.Is(err, store.ErrEdgeNotFound) {
		t.Fatalf("expected operational error, not ErrEdgeNotFound: %v", err)
	}
}

func TestExecuteCypher_EmptyQuery(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.ExecuteCypher(context.Background(), "", nil, "")
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if !errors.Is(err, store.ErrEmptyQuery) {
		t.Errorf("expected ErrEmptyQuery, got %v", err)
	}
}

func TestExecuteCypher_InvalidCypher(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.ExecuteCypher(context.Background(), "this is not valid cypher {{", nil, "")
	if err == nil {
		t.Fatal("expected error for invalid cypher")
	}
	if !errors.Is(err, store.ErrInvalidCypher) {
		t.Errorf("expected ErrInvalidCypher, got %v", err)
	}
}
