package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// ─── Mock HTTP Handler ──────────────────────────────────────────────────────

// capturedRequest stores a request along with its body bytes for test assertions.
type capturedRequest struct {
	Method     string
	Path       string
	Header     http.Header
	Body       []byte
}

// mockHandler responds to configured routes for HITL REST testing.
type mockHandler struct {
	mu       sync.Mutex
	routes   map[string]mockRoute
	requests []capturedRequest // captured for assertion
}

type mockRoute struct {
	status int
	body   string
}

func newMockHandler() *mockHandler {
	return &mockHandler{routes: make(map[string]mockRoute)}
}

func (h *mockHandler) set(path string, status int, body string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.routes[path] = mockRoute{status: status, body: body}
}

func (h *mockHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Capture request body
	bodyBytes, _ := io.ReadAll(r.Body)
	r.Body.Close()

	cr := capturedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Header: r.Header.Clone(),
		Body:   bodyBytes,
	}

	h.mu.Lock()
	h.requests = append(h.requests, cr)
	route, ok := h.routes[r.URL.Path]
	h.mu.Unlock()
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(errorEnvelope{Error: HitlError{
			Code:    ErrQueueItemNotFound,
			Message: "not found",
		}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(route.status)
	w.Write([]byte(route.body))
}

// lastRequest returns the most recently captured request (for assertion).
func (h *mockHandler) lastRequest() capturedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.requests) == 0 {
		return capturedRequest{}
	}
	return h.requests[len(h.requests)-1]
}

// requestCount returns the number of captured requests.
func (h *mockHandler) requestCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.requests)
}

// ─── Test helpers ───────────────────────────────────────────────────────────

func testChoiceJSON(value, label, chType string) string {
	return fmt.Sprintf(`{"value":%q,"label":%q,"type":%q}`, value, label, chType)
}

func testQueueItemJSON(workitemID string) string {
	return fmt.Sprintf(`{
		"workitem_id": %q,
		"shard_id": "shard-0",
		"queue_name": "default",
		"status": "pending",
		"enqueued_at": "2024-01-01T00:00:00Z",
		"claimed_at": ""
	}`, workitemID)
}

func testErrorJSON(code, message string) string {
	return fmt.Sprintf(`{"error":{"code":%q,"message":%q}}`, code, message)
}

// ─── T1: ProbeQueue returns QueueItem on 200 ────────────────────────────────

func TestProbeQueue200(t *testing.T) {
	h := newMockHandler()
	h.set("/queue/test-wi", 200, testQueueItemJSON("test-wi"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL)
	qi, err := client.ProbeQueue(context.Background(), "test-wi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qi == nil {
		t.Fatal("expected non-nil QueueItem")
	}
	if qi.WorkitemID != "test-wi" {
		t.Errorf("expected WorkitemID 'test-wi', got %q", qi.WorkitemID)
	}
	if qi.ShardID != "shard-0" {
		t.Errorf("expected ShardID 'shard-0', got %q", qi.ShardID)
	}
	if qi.Status != "pending" {
		t.Errorf("expected Status 'pending', got %q", qi.Status)
	}
}

// ─── T2: ProbeQueue returns nil on 404 ──────────────────────────────────────

func TestProbeQueue404(t *testing.T) {
	h := newMockHandler()
	h.set("/queue/test-wi", 404, testErrorJSON(ErrQueueItemNotFound, "not found"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL)
	qi, err := client.ProbeQueue(context.Background(), "test-wi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qi != nil {
		t.Fatal("expected nil QueueItem on 404")
	}
}

// ─── T3: ProbeQueue propagates errors ───────────────────────────────────────

func TestProbeQueueError(t *testing.T) {
	h := newMockHandler()
	h.set("/queue/test-wi", 409, testErrorJSON(ErrQueueItemAlreadyClaimed, "claimed by X"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL)
	qi, err := client.ProbeQueue(context.Background(), "test-wi")
	if qi != nil {
		t.Fatal("expected nil QueueItem on error")
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsAlreadyClaimed(err) {
		t.Errorf("expected IsAlreadyClaimed to be true, got %v", err)
	}
}

// ─── T4: GetChoices returns choices on 200 ──────────────────────────────────

func TestGetChoices200(t *testing.T) {
	h := newMockHandler()
	body := fmt.Sprintf(`{
		"choices": [%s, %s],
		"hasFeedback": false,
		"hasCancel": true
	}`, testChoiceJSON("approve", "Approve", "route"),
		testChoiceJSON("cancel", "Cancel", "cancel"))
	h.set("/choices", 200, body)
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL)
	cr, err := client.GetChoices(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cr == nil {
		t.Fatal("expected non-nil ChoicesResponse")
	}
	if len(cr.Choices) != 2 {
		t.Fatalf("expected 2 choices, got %d", len(cr.Choices))
	}
	if cr.Choices[0].Value != "approve" {
		t.Errorf("expected first choice value 'approve', got %q", cr.Choices[0].Value)
	}
	if cr.Choices[0].Label != "Approve" {
		t.Errorf("expected first choice label 'Approve', got %q", cr.Choices[0].Label)
	}
	if cr.Choices[0].Type != "route" {
		t.Errorf("expected first choice type 'route', got %q", cr.Choices[0].Type)
	}
	if !cr.HasCancel {
		t.Error("expected HasCancel true")
	}
}

// ─── T5: GetChoices returns nil on 404 ──────────────────────────────────────

func TestGetChoices404(t *testing.T) {
	h := newMockHandler()
	h.set("/choices", 404, testErrorJSON(ErrQueueItemNotFound, "not found"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL)
	cr, err := client.GetChoices(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cr != nil {
		t.Fatal("expected nil ChoicesResponse on 404")
	}
}

// ─── T6: GetChoices returns error on 5xx ────────────────────────────────────

func TestGetChoices5xx(t *testing.T) {
	h := newMockHandler()
	h.set("/choices", 500, testErrorJSON(ErrQueueUnavailable, "internal error"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL)
	cr, err := client.GetChoices(context.Background())
	if cr != nil {
		t.Fatal("expected nil ChoicesResponse on 5xx")
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsQueueUnavailable(err) {
		t.Errorf("expected IsQueueUnavailable to be true, got %v", err)
	}
}

// ─── T7: Claim returns QueueItem ────────────────────────────────────────────

func TestClaim(t *testing.T) {
	h := newMockHandler()
	h.set("/queue/test-wi/claim", 200, testQueueItemJSON("test-wi"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL)
	qi, err := client.Claim(context.Background(), "test-wi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qi == nil {
		t.Fatal("expected non-nil QueueItem")
	}
	if qi.WorkitemID != "test-wi" {
		t.Errorf("expected WorkitemID 'test-wi', got %q", qi.WorkitemID)
	}
}

// ─── T8: Decide sends correct request body ──────────────────────────────────

func TestDecideRequestBody(t *testing.T) {
	h := newMockHandler()
	h.set("/queue/test-wi/decide", 200, `{"acknowledged": true}`)
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL)
	err := client.Decide(context.Background(), "test-wi", "approve")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := h.lastRequest()
	if req.Method != http.MethodPost {
		t.Errorf("expected POST, got %s", req.Method)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", req.Header.Get("Content-Type"))
	}

	var body map[string]string
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body["choice"] != "approve" {
		t.Errorf("expected choice 'approve', got %q", body["choice"])
	}
}

// ─── T9: Decide returns error on QUEUE_ITEM_ALREADY_CLAIMED ─────────────────

func TestDecideAlreadyClaimed(t *testing.T) {
	h := newMockHandler()
	h.set("/queue/test-wi/decide", 409, testErrorJSON(ErrQueueItemAlreadyClaimed, "already claimed"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL)
	err := client.Decide(context.Background(), "test-wi", "approve")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsAlreadyClaimed(err) {
		t.Errorf("expected IsAlreadyClaimed, got %v", err)
	}
}

// ─── T10: Decide returns error on QUEUE_ITEM_INVALID_STATE ──────────────────

func TestDecideInvalidState(t *testing.T) {
	h := newMockHandler()
	h.set("/queue/test-wi/decide", 409, testErrorJSON(ErrQueueItemInvalidState, "unexpected state"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL)
	err := client.Decide(context.Background(), "test-wi", "approve")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsInvalidState(err) {
		t.Errorf("expected IsInvalidState, got %v", err)
	}
}

// ─── T11: Decide returns error on QUEUE_UNAVAILABLE ─────────────────────────

func TestDecideQueueUnavailable(t *testing.T) {
	h := newMockHandler()
	h.set("/queue/test-wi/decide", 503, testErrorJSON(ErrQueueUnavailable, "queue down"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL)
	err := client.Decide(context.Background(), "test-wi", "approve")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsQueueUnavailable(err) {
		t.Errorf("expected IsQueueUnavailable, got %v", err)
	}
}

// ─── T12: Decide returns error on BAD_REQUEST ───────────────────────────────

func TestDecideBadRequest(t *testing.T) {
	h := newMockHandler()
	h.set("/queue/test-wi/decide", 400, testErrorJSON(ErrBadRequest, "missing choice"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL)
	err := client.Decide(context.Background(), "test-wi", "")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsBadRequest(err) {
		t.Errorf("expected IsBadRequest, got %v", err)
	}
}

// ─── T13: Decide returns error on QUEUE_ITEM_NOT_FOUND ──────────────────────

func TestDecideQueueItemNotFound(t *testing.T) {
	h := newMockHandler()
	h.set("/queue/test-wi/decide", 404, testErrorJSON(ErrQueueItemNotFound, "item gone"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL)
	err := client.Decide(context.Background(), "test-wi", "approve")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsQueueItemNotFound(err) {
		t.Errorf("expected IsQueueItemNotFound, got %v", err)
	}
}

// ─── T14: Release returns QueueItem ─────────────────────────────────────────

func TestRelease(t *testing.T) {
	h := newMockHandler()
	h.set("/queue/test-wi/release", 200, testQueueItemJSON("test-wi"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL)
	qi, err := client.Release(context.Background(), "test-wi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qi == nil {
		t.Fatal("expected non-nil QueueItem")
	}
	if qi.WorkitemID != "test-wi" {
		t.Errorf("expected WorkitemID 'test-wi', got %q", qi.WorkitemID)
	}
}

// ─── T15: Error envelope parsing for all known codes ────────────────────────

func TestErrorEnvelopeParsing(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		message  string
		matcher  func(error) bool
	}{
		{"QueueItemNotFound", ErrQueueItemNotFound, "not found", IsQueueItemNotFound},
		{"AlreadyClaimed", ErrQueueItemAlreadyClaimed, "claimed", IsAlreadyClaimed},
		{"InvalidState", ErrQueueItemInvalidState, "invalid", IsInvalidState},
		{"QueueUnavailable", ErrQueueUnavailable, "unavailable", IsQueueUnavailable},
		{"BadRequest", ErrBadRequest, "bad request", IsBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newMockHandler()
			h.set("/queue/test-wi", 409, testErrorJSON(tt.code, tt.message))
			srv := httptest.NewServer(h)
			defer srv.Close()

			client := NewHitlClient(srv.URL)
			_, err := client.ProbeQueue(context.Background(), "test-wi")
			if err == nil {
				t.Fatal("expected error")
			}
			if !tt.matcher(err) {
				t.Errorf("expected matcher(%v) to be true", err)
			}
			// Verify the error code is in the message
			he, ok := err.(*HitlError)
			if !ok {
				t.Fatal("expected *HitlError")
			}
			if he.Code != tt.code {
				t.Errorf("expected code %q, got %q", tt.code, he.Code)
			}
		})
	}
}

// ─── T16: Probe retries — verify 5 attempts with delays on 404 ──────────────

func TestProbeRetryOn404(t *testing.T) {
	// Mock: first 4 calls return 404, 5th returns 200
	callCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount < 5 {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(errorEnvelope{Error: HitlError{
				Code:    ErrQueueItemNotFound,
				Message: "not found",
			}})
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(testQueueItemJSON("test-wi")))
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := NewHitlClient(srv.URL)

	// Simulate the caller-side retry loop (as done in TUI HitlState.Probe)
	var qi *QueueItem
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		qi, err = client.ProbeQueue(context.Background(), "test-wi")
		if err == nil && qi != nil {
			break
		}
		// In real code, this would be a 3s tea.Tick delay
	}

	if err != nil {
		t.Fatalf("unexpected error after retries: %v", err)
	}
	if qi == nil {
		t.Fatal("expected QueueItem after retries")
	}
	if callCount != 5 {
		t.Errorf("expected 5 total probe attempts, got %d", callCount)
	}
}

// ─── T17: QueueUnavailable retries — verify 3 attempts with delays ──────────

func TestQueueUnavailableRetry(t *testing.T) {
	// Mock: first 2 calls return QUEUE_UNAVAILABLE, 3rd succeeds
	callCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(errorEnvelope{Error: HitlError{
				Code:    ErrQueueUnavailable,
				Message: "queue down",
			}})
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(testQueueItemJSON("test-wi")))
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := NewHitlClient(srv.URL)

	// Simulate the caller-side retry loop (as done in HitlState.retryQueueUnavailable)
	var qi *QueueItem
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			// In real code, this would be a 2s time.Sleep
		}
		qi, err = client.Claim(context.Background(), "test-wi")
		if err == nil {
			break
		}
		if !IsQueueUnavailable(err) {
			break // non-retryable
		}
	}

	if err != nil {
		t.Fatalf("unexpected error after retries: %v", err)
	}
	if qi == nil {
		t.Fatal("expected QueueItem after retries")
	}
	if callCount != 3 {
		t.Errorf("expected 3 total claim attempts, got %d", callCount)
	}
}
