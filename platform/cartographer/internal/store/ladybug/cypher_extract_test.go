package ladybug

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
)

// TestExtractEntityTypes pins the store's server-authoritative statement
// analysis seam directly — the layer that produces the extraction must carry
// the tests (R3 test-discipline). Error classification must match
// ExecuteCypher's exactly so the SPEC check order "empty query → Cypher
// syntax → read-only enforcement → capability" (SPEC:958) holds.
func TestExtractEntityTypes(t *testing.T) {
	ctx := context.Background()
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	extractTestSchema(t, s)

	t.Run("empty query returns ErrEmptyQuery", func(t *testing.T) {
		_, err := s.ExtractEntityTypes(ctx, "")
		if !errors.Is(err, store.ErrEmptyQuery) {
			t.Errorf("expected ErrEmptyQuery, got %v", err)
		}
	})

	t.Run("invalid syntax returns ErrInvalidCypher", func(t *testing.T) {
		_, err := s.ExtractEntityTypes(ctx, "this is not valid cypher {{")
		if !errors.Is(err, store.ErrInvalidCypher) {
			t.Errorf("expected ErrInvalidCypher, got %v", err)
		}
	})

	// A statement that fails Prepare with a mutation keyword quoted inside a
	// string literal must keep ErrInvalidCypher, matching ExecuteCypher's
	// error classification — the syntax gate precedes read-only enforcement
	// (SPEC R3 / SPEC:260, SPEC:1015).
	t.Run("invalid syntax with string-literal mutation keyword returns ErrInvalidCypher", func(t *testing.T) {
		_, err := s.ExtractEntityTypes(ctx, "MATCH (n:Component) RETURN n 'delete'")
		if errors.Is(err, store.ErrMutationCypher) {
			t.Errorf("a malformed read-only statement quoting a mutation keyword "+
				"must not be classified as mutation, got %v", err)
		}
		if !errors.Is(err, store.ErrInvalidCypher) {
			t.Errorf("expected ErrInvalidCypher, got %v", err)
		}
	})

	// Each mutation/DDL clause the SPEC R7 §5 and the error table enumerate
	// must be rejected before any capability decision. Clauses the v0.17.0
	// grammar parses (CREATE, SET, DELETE, MERGE, DROP) are classified
	// non-read-only by IsReadOnly and surface ErrMutationCypher — never
	// ErrInvalidCypher, so read-only enforcement precedes capability. Clauses
	// the grammar cannot prepare (REMOVE, FOREACH, index/constraint DDL) fail
	// at the syntax gate and surface ErrInvalidCypher, which precedes
	// read-only enforcement (SPEC:1015) — identical to ExecuteCypher.
	mutations := []struct {
		name   string
		cypher string
	}{
		{"create", "CREATE (n:Component {id: 'bad-uuid'})"},
		{"set", "MATCH (n:Component) SET n.name = 'x'"},
		{"delete", "MATCH (n:Component) DELETE n"},
		{"merge", "MERGE (n:Component {id: 'bad-uuid'})"},
		{"drop", "DROP TABLE Component"},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ExtractEntityTypes(ctx, tc.cypher)
			if !errors.Is(err, store.ErrMutationCypher) {
				t.Errorf("expected ErrMutationCypher for %q, got %v", tc.cypher, err)
			}
		})
	}

	// Grammar-gap mutations fail at Prepare, so the syntax gate surfaces
	// ErrInvalidCypher (SPEC R3 / SPEC:260; row "Invalid Cypher syntax",
	// SPEC:979; R7 §5 note SPEC:493-497).
	prepareFailMutations := []struct {
		name   string
		cypher string
	}{
		{"remove-syntax-gate", "MATCH (n:Component) REMOVE n.name"},
		{"foreach-syntax-gate", "FOREACH (x IN ['aaa'] | CREATE (n:Component {id: x}))"},
	}
	for _, tc := range prepareFailMutations {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ExtractEntityTypes(ctx, tc.cypher)
			if !errors.Is(err, store.ErrInvalidCypher) {
				t.Errorf("expected ErrInvalidCypher for %q, got %v", tc.cypher, err)
			}
		})
	}

	t.Run("valid read-only single type", func(t *testing.T) {
		labels, err := s.ExtractEntityTypes(ctx, "MATCH (n:Component) RETURN n")
		if err != nil {
			t.Fatalf("ExtractEntityTypes: %v", err)
		}
		if !slices.Equal(labels, []string{"Component"}) {
			t.Errorf("expected [Component], got %v", labels)
		}
	})

	t.Run("valid read-only multi type", func(t *testing.T) {
		labels, err := s.ExtractEntityTypes(ctx,
			"MATCH (a:Component)-[:DEPENDS_ON]->(b:Service) RETURN b")
		if err != nil {
			t.Fatalf("ExtractEntityTypes: %v", err)
		}
		if !slices.Equal(labels, []string{"Component", "Service"}) {
			t.Errorf("expected [Component Service], got %v", labels)
		}
	})

	t.Run("unlabelled match yields empty slice not error", func(t *testing.T) {
		labels, err := s.ExtractEntityTypes(ctx, "MATCH (n) RETURN n")
		if err != nil {
			t.Fatalf("ExtractEntityTypes: %v", err)
		}
		if labels != nil {
			t.Errorf("expected nil labels, got %v", labels)
		}
	})

	// A bare (non-parenthesised) label predicate in a WHERE clause —
	// `WHERE m:Service` — is rejected by the LadybugDB v0.17.0 grammar at
	// Prepare (the parenthesised pattern-predicate form `WHERE (m:Service)` is
	// the supported shape). The seam therefore never extracts labels for this
	// shape: the syntax gate surfaces ErrInvalidCypher before any capability
	// decision (SPEC:1015 check order), so no per-type-grant bypass is
	// reachable through it today. The analyzer's own handling of bare
	// `identifier:Label` predicates is pinned by TestExtractEntityTypeLabels
	// (bare-where-predicate), so a future grammar that accepts the shape cannot
	// silently extract only the parenthesised labels.
	t.Run("bare WHERE label predicate is rejected by the grammar", func(t *testing.T) {
		_, err := s.ExtractEntityTypes(ctx,
			"MATCH (n:Component)-[r]->(m) WHERE m:Service RETURN m")
		if !errors.Is(err, store.ErrInvalidCypher) {
			t.Errorf("expected ErrInvalidCypher (grammar rejects bare label predicates), got %v", err)
		}
	})

	// A statement mixing a classifiable label with an unclassifiable one must
	// fail closed (nil labels → READ:graph/entity/* wildcard fallback), never
	// return the partial subset: a caller holding only the extracted type must
	// not be able to execute a query that also touches the missed type
	// (SPEC R3 every-referenced-type rule, SPEC:260 wildcard bound). The
	// backtick-quoted label references the existing Component table so the
	// binder accepts the statement, but `` `Component` `` is not a bare Cypher
	// identifier, so the analyzer abandons the extraction.
	t.Run("unclassifiable label fails closed to wildcard", func(t *testing.T) {
		labels, err := s.ExtractEntityTypes(ctx, "MATCH (a:Component), (b:`Component`) RETURN a")
		if err != nil {
			t.Fatalf("ExtractEntityTypes: %v", err)
		}
		if labels != nil {
			t.Errorf("expected nil labels (wildcard fallback), got %v", labels)
		}
	})
}

// TestExtractEntityTypeLabels pins the pure-Go label analyzer directly — the
// pattern shapes (named/anonymous/multi-label nodes, inline property maps,
// relationship patterns, comment/string-literal stripping) that the
// server-side extraction depends on.
func TestExtractEntityTypeLabels(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cypher   string
		expected []string
	}{
		{"single", "MATCH (c:Component) RETURN c", []string{"Component"}},
		{"multi-type", "MATCH (a:Component)-[:DEPENDS_ON]->(b:Service) RETURN a, b",
			[]string{"Component", "Service"}},
		{"anonymous-node", "MATCH (a:Component) WHERE (a)--(:Service) RETURN a",
			[]string{"Component", "Service"}},
		{"multi-label", "MATCH (c:Component:Service) RETURN c", []string{"Component", "Service"}},
		{"property-map", "MATCH (c:Component {name: 'x'}) RETURN c", []string{"Component"}},
		{"property-map-compact", "MATCH (c:Component{name:'x'}) RETURN c", []string{"Component"}},
		{"nested-property-map", "MATCH (c:Component {meta: {a: 1}}) RETURN c", []string{"Component"}},
		{"line-comment-stripped",
			"MATCH (c:Component) RETURN c // (b:Service)", []string{"Component"}},
		{"block-comment-stripped",
			"MATCH (c:Component) RETURN c /* (b:Service) */", []string{"Component"}},
		{"string-literal-colon-stripped",
			"MATCH (c:Component {name: 'x:Service'}) RETURN c", []string{"Component"}},
		{"string-literal-node-shape-stripped",
			"MATCH (c:Component) RETURN '(:Service)' AS s", []string{"Component"}},
		{"duplicate-labels-deduped",
			"MATCH (a:Component)-->(b:Component) RETURN a, b", []string{"Component"}},
		{"multiple-match-clauses",
			"MATCH (c:Component) MATCH (s:Service) RETURN c, s", []string{"Component", "Service"}},
		// Bare (non-parenthesised) label predicates in WHERE clauses reference
		// the same labels as node patterns and must be extracted, so a query
		// like `MATCH (n:Component)-[r]->(m) WHERE m:Service RETURN m` is not
		// authorised for Service rows by a caller holding only the Component
		// per-type grant (SPEC R3). Regression for the pre-fix bypass where
		// only [Component] was extracted.
		{"bare-where-predicate",
			"MATCH (n:Component)-[r]->(m) WHERE m:Service RETURN m",
			[]string{"Component", "Service"}},
		{"bare-where-predicate-only",
			"MATCH (n) WHERE n:Service RETURN n", []string{"Service"}},
		{"bare-where-predicate-whitespace-after-colon",
			"MATCH (n) WHERE n: Service RETURN n", []string{"Service"}},
		// A `WHERE x:Label` predicate inside a list comprehension references
		// the same labels as node patterns and must be extracted, never
		// silently skipped (SPEC R3 every-referenced-type rule). Regression for
		// the pre-fix partial-subset extraction that returned only the labels
		// outside the bracket group.
		{"list-comprehension-predicate",
			"MATCH (n:Component) RETURN [x IN collect(n) WHERE x:Service | x]",
			[]string{"Component", "Service"}},
		{"list-comprehension-predicate-only",
			"RETURN [x IN collect(n) WHERE x:Service | x]", []string{"Service"}},
		{"list-comprehension-no-predicate",
			"MATCH (n:Component) RETURN [x IN collect(n) | x.name]", []string{"Component"}},
		{"list-literal-ignored",
			"MATCH (n:Component) RETURN [1, 2, 3]", []string{"Component"}},
		{"nested-list-comprehension-predicate",
			"RETURN [x IN [y IN coll WHERE y:Service | y] | x]", []string{"Service"}},
		// An unclassifiable label predicate inside a comprehension fails
		// closed to the READ:graph/entity/* wildcard fallback, never a partial
		// subset.
		{"list-comprehension-unclassifiable-fails-closed",
			"MATCH (n:Component) RETURN [x IN collect(n) WHERE x:$label | x]", nil},
		// Relationship specifiers — directed and undirected — carry edge types,
		// not entity-type labels, and must not be extracted as entity labels.
		{"undirected-relationship-type-ignored",
			"MATCH (a:Component)-[r:DEPENDS_ON]-(b:Service) RETURN a",
			[]string{"Component", "Service"}},
		// A bare label predicate whose label form the analyzer cannot classify
		// fails closed to the READ:graph/entity/* wildcard fallback, never a
		// partial subset.
		{"bare-where-param-label-fails-closed",
			"MATCH (n:Component) WHERE n:$label RETURN n", nil},
		{"bare-where-backtick-label-fails-closed",
			"MATCH (n:Component) WHERE n:`Label With Space` RETURN n", nil},
		{"unlabelled-nodes-nil", "MATCH (n) RETURN n", nil},
		{"no-match-nil", "RETURN 1", nil},
		// SPEC:260 fail-closed rule: a pattern the analyzer cannot classify
		// (backtick-quoted, parameterised labels) abandons the extraction and
		// returns nil so the caller falls back to READ:graph/entity/* — never a
		// partial subset that could widen access beyond the extracted types.
		{"backtick-label-fails-closed", "MATCH (c:`Label With Space`) RETURN c", nil},
		{"parameterised-label-fails-closed", "MATCH (c:$label) RETURN c", nil},
		{"partial-extraction-fails-closed",
			"MATCH (a:Component)-[:R]->(b:`Label With Space`) RETURN a", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			labels := extractEntityTypeLabels(tc.cypher)
			if !slices.Equal(labels, tc.expected) {
				t.Errorf("expected %v, got %v", tc.expected, labels)
			}
		})
	}
}
