package ladybug

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestFullTextSearch_Valid(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.CreateEntity(context.Background(), "Document", "",
		map[string]string{"title": "Hello World", "body": "This is a test document"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	results, err := s.FullTextSearch(context.Background(), "World", "Document", "")
	if err != nil {
		t.Fatalf("FullTextSearch: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least one FTS result")
	}
}

func TestFullTextSearch_CrossType(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.CreateEntity(context.Background(), "Document", "",
		map[string]string{"title": "UniqueTerm", "body": "content"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity Document: %v", err)
	}
	_, err = s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "UniqueTerm"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity Component: %v", err)
	}

	// Search across all types (entityType="").
	results, err := s.FullTextSearch(context.Background(), "UniqueTerm", "", "")
	if err != nil {
		t.Fatalf("FullTextSearch: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least one result from cross-type FTS")
	}
}

// TestFullTextSearch_NoResultCap pins that FullTextSearch returns every
// matching document — no silent per-type cap. SPEC R2 defines
// FullTextSearch(query, entityType?) with no result limit and the error table
// defines no cap; LadybugDB's QUERY_FTS_INDEX TOP argument is optional and
// defaults to retrieving all documents, so the store must not inject one. A
// search matching more than 100 documents must return all of them, not the
// capped subset.
func TestFullTextSearch_NoResultCap(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	const want = 120 // > the old hard-coded TOP := 100.
	for i := range want {
		if _, err := s.CreateEntity(ctx, "Document", "",
			map[string]string{"title": fmt.Sprintf("needle doc %d", i), "body": "content"}, nil, ""); err != nil {
			t.Fatalf("CreateEntity %d: %v", i, err)
		}
	}

	results, err := s.FullTextSearch(ctx, "needle", "Document", "")
	if err != nil {
		t.Fatalf("FullTextSearch: %v", err)
	}
	if len(results) != want {
		t.Errorf("expected all %d matching documents, got %d", want, len(results))
	}
}

func TestFullTextSearch_EmptyQuery(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.FullTextSearch(context.Background(), "", "Document", "")
	if err == nil {
		t.Fatal("expected error for empty FTS query")
	}
	if !errors.Is(err, store.ErrEmptyQuery) {
		t.Errorf("expected ErrEmptyQuery, got %v", err)
	}
}

func TestFullTextSearch_UnknownType(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.FullTextSearch(context.Background(), "anything", "NoSuchType", "")
	if err == nil {
		t.Fatal("expected error for unknown entity type")
	}
	if !errors.Is(err, store.ErrUnknownEntityType) {
		t.Errorf("expected ErrUnknownEntityType, got %v", err)
	}
}

// FullTextSearch silently skips an entity type whose FTS index is absent
// (query.go FullTextSearch, ponytail at the Prepare-failure `continue`): the
// search returns a result set with nil error and no partial-result notice, so
// an index-less type contributes nothing. This pins the skip branch — dropping
// a table's FTS index and then searching it must NOT error and must return
// nothing rather than fabricating results.
func TestFullTextSearch_MissingIndexSilentlySkipped(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()
	applyTestSchema(t, s)

	if _, err := s.CreateEntity(ctx, "Document", "",
		map[string]string{"title": "needle"}, nil, ""); err != nil {
		t.Fatalf("CreateEntity Document: %v", err)
	}
	// Confirm the type is currently FTS-searchable.
	if matches, err := s.FullTextSearch(ctx, "needle", "Document", ""); err != nil || len(matches) == 0 {
		t.Fatalf("expected Document FTS searchable before drop, matches=%d err=%v", len(matches), err)
	}

	// Drop the FTS index; the table itself remains.
	db := s.(*ladybugDB)
	res, err := db.conn.Query("CALL DROP_FTS_INDEX('Document', 'Document_fts');")
	if err != nil {
		t.Fatalf("drop FTS index: %v", err)
	}
	res.Close()
	if ok, err := ftsIndexExists(db.conn, "Document"); err != nil {
		t.Fatalf("check FTS index: %v", err)
	} else if ok {
		t.Fatal("expected Document FTS index dropped")
	}

	// Querying the index-less type must silently succeed with an empty result,
	// exercising the Prepare-fail `continue` (skip) branch.
	results, err := s.FullTextSearch(ctx, "needle", "Document", "")
	if err != nil {
		t.Fatalf("expected silent skip (nil error) for absent FTS index, got %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty result set for absent FTS index, got %d", len(results))
	}
}

// SPEC:345-346: before the first ApplySchema (or on a graph with no
// string-property types), a type-omitted (entityType == "") FullTextSearch is a
// non-type-referencing method and must succeed on an empty/fresh graph — the
// store's wildcard branch (query.go:297-301) must return an empty result set
// with a nil error, mirroring SearchNeighbors' empty-graph behavior.
func TestFullTextSearch_WildcardEmptyGraph_Succeeds(t *testing.T) {
	t.Run("no schema applied", func(t *testing.T) {
		s, err := openInMemory()
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, s)
		ctx := context.Background()

		results, err := s.FullTextSearch(ctx, "anything", "", "")
		if err != nil {
			t.Fatalf("wildcard FullTextSearch before ApplySchema should succeed, got %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected no results on an empty graph, got %d", len(results))
		}
	})

	t.Run("schema with no string-property types", func(t *testing.T) {
		s, err := openInMemory()
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, s)
		ctx := context.Background()

		// A property-less entity type creates a table with only the id column:
		// no string properties → no FTS index → the type is legitimately
		// unsearchable and is silently skipped, leaving an empty result set with
		// a nil error.
		if err := s.ApplySchema(ctx, &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{{Name: "Empty"}},
		}); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}

		results, err := s.FullTextSearch(ctx, "anything", "", "")
		if err != nil {
			t.Fatalf("wildcard FullTextSearch on a schema without string-property types should succeed, got %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected no results, got %d", len(results))
		}
	})
}
