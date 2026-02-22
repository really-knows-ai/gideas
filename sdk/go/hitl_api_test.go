package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// Tests — HITL REST API
// ---------------------------------------------------------------------------

const testWorkitemID = "wi-1" //nolint:goconst // test constant

// newTestQueueManager creates an in-memory QueueManager for API tests.
func newTestQueueManager(t *testing.T) *queueManagerImpl {
	t.Helper()
	store, err := newQueueStore(":memory:", "api-test-shard")
	if err != nil {
		t.Fatalf("newQueueStore failed: %v", err)
	}
	mesh := newQueueMesh(store, "api-test-shard", &staticResolver{}, "50053", nil)
	qm := &queueManagerImpl{
		store:   store,
		mesh:    mesh,
		shardID: "api-test-shard",
	}
	t.Cleanup(func() { _ = store.close() })
	return qm
}

func TestHITLAPI_ListQueue(t *testing.T) {
	qm := newTestQueueManager(t)
	ctx := context.Background()
	_ = qm.Enqueue(ctx, testWorkitemID)
	_ = qm.Enqueue(ctx, "wi-2")

	router := newHITLRouter(qm)
	req := httptest.NewRequest(http.MethodGet, "/queue", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var items []QueueItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
}

func TestHITLAPI_ListQueue_StatusFilter(t *testing.T) {
	qm := newTestQueueManager(t)
	ctx := context.Background()
	_ = qm.Enqueue(ctx, testWorkitemID)
	_ = qm.Enqueue(ctx, "wi-2")
	_, _ = qm.Claim(ctx, testWorkitemID)

	router := newHITLRouter(qm)
	req := httptest.NewRequest(http.MethodGet, "/queue?status=waiting", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var items []QueueItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 waiting item, got %d", len(items))
	}
	if items[0].WorkitemID != "wi-2" {
		t.Fatalf("expected wi-2, got %s", items[0].WorkitemID)
	}
}

func TestHITLAPI_ListQueue_Pagination(t *testing.T) {
	qm := newTestQueueManager(t)
	ctx := context.Background()
	_ = qm.Enqueue(ctx, testWorkitemID)
	_ = qm.Enqueue(ctx, "wi-2")
	_ = qm.Enqueue(ctx, "wi-3")

	router := newHITLRouter(qm)
	req := httptest.NewRequest(http.MethodGet, "/queue?limit=2&offset=1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var items []QueueItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items with limit=2&offset=1, got %d", len(items))
	}
}

func TestHITLAPI_GetItem(t *testing.T) {
	qm := newTestQueueManager(t)
	ctx := context.Background()
	_ = qm.Enqueue(ctx, testWorkitemID)

	router := newHITLRouter(qm)
	req := httptest.NewRequest(http.MethodGet, "/queue/"+testWorkitemID, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var item QueueItem
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if item.WorkitemID != testWorkitemID {
		t.Fatalf("expected %s, got %s", testWorkitemID, item.WorkitemID)
	}
}

func TestHITLAPI_GetItem_NotFound(t *testing.T) {
	qm := newTestQueueManager(t)
	router := newHITLRouter(qm)
	req := httptest.NewRequest(http.MethodGet, "/queue/nonexistent", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	var errResp apiError
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode error: %v", err)
	}
	if errResp.Error.Code != "QUEUE_ITEM_NOT_FOUND" {
		t.Fatalf("expected QUEUE_ITEM_NOT_FOUND, got %s", errResp.Error.Code)
	}
}

func TestHITLAPI_Claim(t *testing.T) {
	qm := newTestQueueManager(t)
	ctx := context.Background()
	_ = qm.Enqueue(ctx, testWorkitemID)

	router := newHITLRouter(qm)
	req := httptest.NewRequest(http.MethodPost, "/queue/"+testWorkitemID+"/claim", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var item QueueItem
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if item.Status != QueueStatusClaimed {
		t.Fatalf("expected claimed, got %s", item.Status)
	}
}

func TestHITLAPI_Claim_AlreadyClaimed(t *testing.T) {
	qm := newTestQueueManager(t)
	ctx := context.Background()
	_ = qm.Enqueue(ctx, testWorkitemID)
	_, _ = qm.Claim(ctx, testWorkitemID)

	router := newHITLRouter(qm)
	req := httptest.NewRequest(http.MethodPost, "/queue/"+testWorkitemID+"/claim", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}

	var errResp apiError
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode error: %v", err)
	}
	if errResp.Error.Code != "QUEUE_ITEM_ALREADY_CLAIMED" {
		t.Fatalf("expected QUEUE_ITEM_ALREADY_CLAIMED, got %s", errResp.Error.Code)
	}
}

func TestHITLAPI_Decide(t *testing.T) {
	qm := newTestQueueManager(t)
	ctx := context.Background()
	_ = qm.Enqueue(ctx, testWorkitemID)
	_, _ = qm.Claim(ctx, testWorkitemID)

	router := newHITLRouter(qm)
	req := httptest.NewRequest(http.MethodPost, "/queue/"+testWorkitemID+"/decide", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if !resp["acknowledged"] {
		t.Fatal("expected acknowledged=true")
	}

	// Verify item is deleted.
	_, err := qm.GetItem(ctx, testWorkitemID)
	if err == nil {
		t.Fatal("expected item to be deleted after decide")
	}
}

func TestHITLAPI_Decide_NotClaimed(t *testing.T) {
	qm := newTestQueueManager(t)
	ctx := context.Background()
	_ = qm.Enqueue(ctx, testWorkitemID)

	router := newHITLRouter(qm)
	req := httptest.NewRequest(http.MethodPost, "/queue/"+testWorkitemID+"/decide", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHITLAPI_Release(t *testing.T) {
	qm := newTestQueueManager(t)
	ctx := context.Background()
	_ = qm.Enqueue(ctx, testWorkitemID)
	_, _ = qm.Claim(ctx, testWorkitemID)

	router := newHITLRouter(qm)
	req := httptest.NewRequest(http.MethodPost, "/queue/"+testWorkitemID+"/release", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var item QueueItem
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if item.Status != QueueStatusWaiting {
		t.Fatalf("expected waiting, got %s", item.Status)
	}
}

func TestHITLAPI_Release_NotClaimed(t *testing.T) {
	qm := newTestQueueManager(t)
	ctx := context.Background()
	_ = qm.Enqueue(ctx, testWorkitemID)

	router := newHITLRouter(qm)
	req := httptest.NewRequest(http.MethodPost, "/queue/"+testWorkitemID+"/release", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHITLAPI_Decide_WithChoice(t *testing.T) {
	const choiceID = "wi-choice-api"
	qm := newTestQueueManager(t)
	ctx := context.Background()
	_ = qm.Enqueue(ctx, choiceID)
	_, _ = qm.Claim(ctx, choiceID)

	// Start a waiter so we can verify the choice flows through.
	type result struct {
		choice string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		choice, err := qm.WaitForDecision(ctx, choiceID)
		done <- result{choice: choice, err: err}
	}()

	body, _ := json.Marshal(map[string]string{"choice": "approve"})
	router := newHITLRouter(qm)
	req := httptest.NewRequest(http.MethodPost, "/queue/"+choiceID+"/decide",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("WaitForDecision returned error: %v", r.err)
	}
	if r.choice != "approve" {
		t.Fatalf("expected choice=approve, got %q", r.choice)
	}
}
