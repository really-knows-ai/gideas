package flow

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Transaction read methods (SPEC R2: all read methods accept transactionId)
// ---------------------------------------------------------------------------

// TestTx_ExecuteCypher pins the Transaction-layer ExecuteCypher injecting the
// transaction ID into the request (SPEC R2) and converting rows.
func TestTx_ExecuteCypher(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		executeCypher: func(ctx context.Context, req *flowv1.ExecuteCypherRequest) (*flowv1.ExecuteCypherResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.ExecuteCypherResponse{
				Rows: []*flowv1.Row{{Values: []string{"c1"}}, {Values: []string{"c2"}}},
			}, nil
		},
	}
	tx := newMockTx(mock)

	rows, err := tx.ExecuteCypher("MATCH (c:Component) RETURN c", nil)
	if err != nil {
		t.Fatalf("ExecuteCypher returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected tx ID %s on ExecuteCypher, got %q", embassyTestTxID, capturedTxID)
	}
	if len(rows) != 2 || rows[1][0] != "c2" {
		t.Errorf("expected 2 rows in wire order, got %v", rows)
	}
}

// TestTx_SearchNeighbors pins the Transaction-layer SearchNeighbors injecting
// the transaction ID (SPEC R2) and seeding the ID-to-type cache from the
// results (SPEC R3).
func TestTx_SearchNeighbors(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		searchNeighbors: func(
			ctx context.Context, req *flowv1.SearchNeighborsRequest,
		) (*flowv1.SearchNeighborsResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.SearchNeighborsResponse{
				Results: []*flowv1.SearchNeighborResult{{EntityId: "e1", EntityType: componentType, Distance: 0.5}},
			}, nil
		},
	}
	tx := newMockTx(mock)

	results, err := tx.SearchNeighbors([]float32{0.1}, "Component", 10)
	if err != nil {
		t.Fatalf("SearchNeighbors returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected tx ID %s on SearchNeighbors, got %q", embassyTestTxID, capturedTxID)
	}
	if len(results) != 1 || results[0].Distance != 0.5 {
		t.Errorf("unexpected search results: %+v", results)
	}
	if typ, ok := tx.idTypeMap.resolve("e1"); !ok || typ != componentType {
		t.Errorf("expected search result to seed the ID-to-type cache with %s, got %q (ok=%v)", componentType, typ, ok)
	}
}

// TestTx_FullTextSearch pins the Transaction-layer FullTextSearch injecting
// the transaction ID (SPEC R2).
func TestTx_FullTextSearch(t *testing.T) {
	var capturedTxID string
	mock := &mockCartographerClient{
		fullTextSearch: func(ctx context.Context, req *flowv1.FullTextSearchRequest) (*flowv1.FullTextSearchResponse, error) {
			capturedTxID = req.GetTransactionId()
			return &flowv1.FullTextSearchResponse{
				Results: []*flowv1.Entity{{EntityId: "e1", EntityType: componentType}},
			}, nil
		},
	}
	tx := newMockTx(mock)

	entities, err := tx.FullTextSearch("auth", "Component")
	if err != nil {
		t.Fatalf("FullTextSearch returned error: %v", err)
	}
	if capturedTxID != embassyTestTxID {
		t.Errorf("expected tx ID %s on FullTextSearch, got %q", embassyTestTxID, capturedTxID)
	}
	if len(entities) != 1 || entities[0].ID != "e1" || entities[0].Type != componentType {
		t.Errorf("unexpected full-text results: %+v", entities)
	}
}
