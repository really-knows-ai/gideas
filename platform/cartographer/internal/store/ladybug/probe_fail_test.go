package ladybug

import (
	"context"
	"fmt"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestProbeExecuteFailures(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	if err := s.ApplySchema(ctx, &flowv1.Schema{EntityTypes: []*flowv1.EntityType{
		{Name: "Vec", EnableVectorIndex: true, Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		{Name: "Doc", Properties: []*flowv1.Property{{Name: "title", Type: "string"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateEntity(ctx, "Vec", "", map[string]string{"name": "x"}, []float32{1, 2, 3}, ""); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if _, err := s.CreateEntity(ctx, "Doc", "", map[string]string{"title": "hello world"}, nil, ""); err != nil {
		t.Fatalf("create Doc: %v", err)
	}

	ldb := s.(*ladybugDB)
	conn, _, unlock, err := ldb.lockForRead("")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	// FTS execute: prepare then execute a valid FTS query
	const ftsQuery = "CALL QUERY_FTS_INDEX('Doc', 'Doc_fts', 'hello', TOP := 100) RETURN node, score ORDER BY score DESC;"
	stmt, perr := conn.Prepare(ftsQuery)
	fmt.Printf("FTS Prepare err=%v\n", perr)
	if perr == nil {
		res, eerr := conn.Execute(stmt, map[string]any{"q": "hello"})
		fmt.Printf("FTS Execute err=%v resHasNext? %v\n", eerr, res != nil)
		if res != nil {
			res.Close()
		}
		stmt.Close()
	}

	// Vector index: drop it, then Prepare+Execute
	_, _ = ldb.conn.Query("DROP_VECTOR_INDEX('Vec', 'Vec_vec');")
	const vecQuery = "CALL QUERY_VECTOR_INDEX('Vec', 'Vec_vec', [$emb], 10) RETURN node, distance ORDER BY distance;"
	stmt2, perr2 := conn.Prepare(vecQuery)
	fmt.Printf("VEC-after-drop Prepare err=%v\n", perr2)
	if perr2 == nil {
		r2, eerr2 := conn.Execute(stmt2, map[string]any{"emb": []any{float32(1), float32(2), float32(3)}})
		fmt.Printf("VEC-after-drop Execute err=%v\n", eerr2)
		if r2 != nil {
			r2.Close()
		}
		stmt2.Close()
	}
}
