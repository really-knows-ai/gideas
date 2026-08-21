package api

// PHASE_07 — flowctl HITL client retarget to the queue-service.
//
// These tests pin the FULLY-SPECIFIED retarget design:
//   - NewHitlClient gains a queueName parameter; base URL is the queue-service.
//   - All paths are retargeted to /queues/{queueName}/{workitemID} (+ suffix).
//   - GET /choices is GONE; choices are item metadata on QueueItem, surfaced via
//     (*QueueItem).ChoicesWithDefault.
//   - Error envelope/codes/Is* helpers are preserved unchanged.
//
// The tests are currently RED: production hitl.go still points at /queue/{id},
// NewHitlClient takes only baseURL, QueueItem has no Choices, and there is no
// ChoicesWithDefault.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ─── Test constants ─────────────────────────────────────────────────────────

// The queue name is resolved by the caller (TUI) and passed into the client;
// the client embeds it into every path it builds.
const testQueueName = "hitl-approval"

// ─── Mock HTTP handler ──────────────────────────────────────────────────────

type capturedRequest struct {
	Method string
	Path   string
	Body   []byte
}

// mockHandler routes on the retargeted /queues/{queueName}/{workitemID} paths
// and captures each request so tests can assert the exact paths the client
// builds (base URL = queue-service, queue name embedded).
type mockHandler struct {
	mu       sync.Mutex
	routes   map[string]mockRoute
	requests []capturedRequest
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
	bodyBytes, _ := io.ReadAll(r.Body)
	r.Body.Close()

	h.mu.Lock()
	h.requests = append(h.requests, capturedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Body:   bodyBytes,
	})
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

func (h *mockHandler) lastRequest() capturedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.requests) == 0 {
		return capturedRequest{}
	}
	return h.requests[len(h.requests)-1]
}

func (h *mockHandler) requestCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.requests)
}

// ─── Test helpers ───────────────────────────────────────────────────────────

// queueItemJSON renders a queue-service queue item. choices may be empty,
// in which case the field is omitted entirely (exercises the "missing choices
// -> defaults" path).
func queueItemJSON(workitemID string, choices ...string) string {
	if len(choices) == 0 {
		return fmt.Sprintf(`{
			"workitem_id":    %q,
			"shard_id":       "shard-0",
			"queue_name":     %q,
			"status":         "pending",
			"enqueued_at":    "2024-01-01T00:00:00Z",
			"claimed_at":     ""
		}`, workitemID, testQueueName)
	}
	raw, _ := json.Marshal(choices)
	return fmt.Sprintf(`{
		"workitem_id":    %q,
		"shard_id":       "shard-0",
		"queue_name":     %q,
		"status":         "pending",
		"enqueued_at":    "2024-01-01T00:00:00Z",
		"claimed_at":     "",
		"choices":        %s
	}`, workitemID, testQueueName, string(raw))
}

func errorJSON(code, message string) string {
	return fmt.Sprintf(`{"error":{"code":%q,"message":%q}}`, code, message)
}

// newClient builds a client pointing at the httptest queue-service with the
// pinned queue name; every path the client builds is expected under
// /queues/{testQueueName}/...
func newClient(serverURL string) *HitlClient {
	return NewHitlClient(serverURL, testQueueName)
}

// ─── ProbeQueue ─────────────────────────────────────────────────────────────

// T1: ProbeQueue builds GET /queues/{name}/{id} and returns the item on 200,
// with choices decoded from the queue-service item.
func TestProbeQueue(t *testing.T) {
	h := newMockHandler()
	h.set("/queues/"+testQueueName+"/wi-1", 200, queueItemJSON("wi-1", "approve", "reject"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := newClient(srv.URL)
	qi, err := client.ProbeQueue(context.Background(), "wi-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qi == nil {
		t.Fatal("expected non-nil QueueItem")
	}
	if qi.WorkitemID != "wi-1" {
		t.Errorf("expected WorkitemID 'wi-1', got %q", qi.WorkitemID)
	}
	if qi.QueueName != testQueueName {
		t.Errorf("expected QueueName %q, got %q", testQueueName, qi.QueueName)
	}
	// Choices arrive as item metadata from the queue-service.
	if len(qi.Choices) != 2 || qi.Choices[0] != "approve" || qi.Choices[1] != "reject" {
		t.Errorf("expected item choices [approve reject], got %v", qi.Choices)
	}

	// The client must query the retargeted path (queue-service base + queue name).
	req := h.lastRequest()
	if req.Method != http.MethodGet {
		t.Errorf("expected GET, got %s", req.Method)
	}
	want := "/queues/" + testQueueName + "/wi-1"
	if req.Path != want {
		t.Errorf("expected path %q, got %q", want, req.Path)
	}
}

// T2: ProbeQueue returns (nil, nil) silently on 404.
func TestProbeQueueNotFound(t *testing.T) {
	h := newMockHandler()
	h.set("/queues/"+testQueueName+"/wi-1", 404, errorJSON(ErrQueueItemNotFound, "not found"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := newClient(srv.URL)
	qi, err := client.ProbeQueue(context.Background(), "wi-1")
	if err != nil {
		t.Fatalf("expected silent (nil,nil) on 404, got error: %v", err)
	}
	if qi != nil {
		t.Fatal("expected nil QueueItem on 404")
	}
}

// T3: ProbeQueue propagates a structured error envelope on non-200/404.
func TestProbeQueueError(t *testing.T) {
	h := newMockHandler()
	h.set("/queues/"+testQueueName+"/wi-1", 409, errorJSON(ErrQueueItemAlreadyClaimed, "claimed by X"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := newClient(srv.URL)
	qi, err := client.ProbeQueue(context.Background(), "wi-1")
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

// ─── Claim ──────────────────────────────────────────────────────────────────

// T4: Claim builds POST /queues/{name}/{id}/claim and returns the item on 200.
func TestClaim(t *testing.T) {
	h := newMockHandler()
	h.set("/queues/"+testQueueName+"/wi-1/claim", 200, queueItemJSON("wi-1"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := newClient(srv.URL)
	qi, err := client.Claim(context.Background(), "wi-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qi == nil {
		t.Fatal("expected non-nil QueueItem")
	}
	if qi.WorkitemID != "wi-1" {
		t.Errorf("expected WorkitemID 'wi-1', got %q", qi.WorkitemID)
	}

	req := h.lastRequest()
	if req.Method != http.MethodPost {
		t.Errorf("expected POST, got %s", req.Method)
	}
	want := "/queues/" + testQueueName + "/wi-1/claim"
	if req.Path != want {
		t.Errorf("expected path %q, got %q", want, req.Path)
	}
}

// T5: Claim maps a 409 already-claimed envelope to IsAlreadyClaimed.
func TestClaimAlreadyClaimed(t *testing.T) {
	h := newMockHandler()
	h.set("/queues/"+testQueueName+"/wi-1/claim", 409, errorJSON(ErrQueueItemAlreadyClaimed, "already claimed"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := newClient(srv.URL)
	qi, err := client.Claim(context.Background(), "wi-1")
	if qi != nil {
		t.Fatal("expected nil QueueItem on error")
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsAlreadyClaimed(err) {
		t.Errorf("expected IsAlreadyClaimed, got %v", err)
	}
}

// ─── Decide ─────────────────────────────────────────────────────────────────

// T6: Decide builds POST /queues/{name}/{id}/decide with a {"choice":...} body
// and acks on 200.
func TestDecideSuccess(t *testing.T) {
	h := newMockHandler()
	h.set("/queues/"+testQueueName+"/wi-1/decide", 200, `{"acknowledged": true}`)
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := newClient(srv.URL)
	if err := client.Decide(context.Background(), "wi-1", "approve"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := h.lastRequest()
	if req.Method != http.MethodPost {
		t.Errorf("expected POST, got %s", req.Method)
	}
	want := "/queues/" + testQueueName + "/wi-1/decide"
	if req.Path != want {
		t.Errorf("expected path %q, got %q", want, req.Path)
	}

	var body map[string]string
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body["choice"] != "approve" {
		t.Errorf("expected choice 'approve', got %q", body["choice"])
	}
}

// T7: Decide accepts a 202 acknowledgment.
func TestDecideAccepted(t *testing.T) {
	h := newMockHandler()
	h.set("/queues/"+testQueueName+"/wi-1/decide", 202, `{"acknowledged": true}`)
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := newClient(srv.URL)
	if err := client.Decide(context.Background(), "wi-1", "approve"); err != nil {
		t.Fatalf("expected 202 to ack, got error: %v", err)
	}
}

// T8: Decide maps a 409 invalid-state envelope to IsInvalidState.
func TestDecideInvalidState(t *testing.T) {
	h := newMockHandler()
	h.set("/queues/"+testQueueName+"/wi-1/decide", 409, errorJSON(ErrQueueItemInvalidState, "unexpected state"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := newClient(srv.URL)
	err := client.Decide(context.Background(), "wi-1", "approve")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsInvalidState(err) {
		t.Errorf("expected IsInvalidState, got %v", err)
	}
}

// T9: Decide maps a 503 unavailable envelope to IsQueueUnavailable.
func TestDecideQueueUnavailable(t *testing.T) {
	h := newMockHandler()
	h.set("/queues/"+testQueueName+"/wi-1/decide", 503, errorJSON(ErrQueueUnavailable, "queue down"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := newClient(srv.URL)
	err := client.Decide(context.Background(), "wi-1", "approve")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsQueueUnavailable(err) {
		t.Errorf("expected IsQueueUnavailable, got %v", err)
	}
}

// T10: Decide maps a 400 bad-request envelope to IsBadRequest.
func TestDecideBadRequest(t *testing.T) {
	h := newMockHandler()
	h.set("/queues/"+testQueueName+"/wi-1/decide", 400, errorJSON(ErrBadRequest, "missing choice"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := newClient(srv.URL)
	err := client.Decide(context.Background(), "wi-1", "")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsBadRequest(err) {
		t.Errorf("expected IsBadRequest, got %v", err)
	}
}

// T11: Decide maps a 404 envelope to IsQueueItemNotFound.
func TestDecideQueueItemNotFound(t *testing.T) {
	h := newMockHandler()
	h.set("/queues/"+testQueueName+"/wi-1/decide", 404, errorJSON(ErrQueueItemNotFound, "item gone"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := newClient(srv.URL)
	err := client.Decide(context.Background(), "wi-1", "approve")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsQueueItemNotFound(err) {
		t.Errorf("expected IsQueueItemNotFound, got %v", err)
	}
}

// ─── Release ────────────────────────────────────────────────────────────────

// T12: Release builds POST /queues/{name}/{id}/release and returns the item on 200.
func TestRelease(t *testing.T) {
	h := newMockHandler()
	h.set("/queues/"+testQueueName+"/wi-1/release", 200, queueItemJSON("wi-1"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := newClient(srv.URL)
	qi, err := client.Release(context.Background(), "wi-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qi == nil {
		t.Fatal("expected non-nil QueueItem")
	}
	if qi.WorkitemID != "wi-1" {
		t.Errorf("expected WorkitemID 'wi-1', got %q", qi.WorkitemID)
	}

	req := h.lastRequest()
	if req.Method != http.MethodPost {
		t.Errorf("expected POST, got %s", req.Method)
	}
	want := "/queues/" + testQueueName + "/wi-1/release"
	if req.Path != want {
		t.Errorf("expected path %q, got %q", want, req.Path)
	}
}

// T13: Release maps a structured error envelope to *HitlError.
func TestReleaseError(t *testing.T) {
	h := newMockHandler()
	h.set("/queues/"+testQueueName+"/wi-1/release", 409, errorJSON(ErrQueueItemInvalidState, "cannot release"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := newClient(srv.URL)
	qi, err := client.Release(context.Background(), "wi-1")
	if qi != nil {
		t.Fatal("expected nil QueueItem on error")
	}
	if err == nil {
		t.Fatal("expected error")
	}
	he, ok := err.(*HitlError)
	if !ok {
		t.Fatalf("expected *HitlError, got %T", err)
	}
	if he.Code != ErrQueueItemInvalidState {
		t.Errorf("expected code %q, got %q", ErrQueueItemInvalidState, he.Code)
	}
}

// ─── Error envelope / parseError (unchanged surface) ────────────────────────

// T14: The queue-service envelope {"error":{"code":...,"message":...}} maps to
// *HitlError with the matching code; parseError is unchanged from PHASE_01.
func TestErrorEnvelopeParsing(t *testing.T) {
	tests := []struct {
		name    string
		code    string
		message string
		matcher func(error) bool
	}{
		{"QueueItemNotFound", ErrQueueItemNotFound, "not found", IsQueueItemNotFound},
		{"AlreadyClaimed", ErrQueueItemAlreadyClaimed, "claimed", IsAlreadyClaimed},
		{"InvalidState", ErrQueueItemInvalidState, "invalid", IsInvalidState},
		{"QueueUnavailable", ErrQueueUnavailable, "unavailable", IsQueueUnavailable},
		{"BadRequest", ErrBadRequest, "bad request", IsBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseError(strings.NewReader(errorJSON(tt.code, tt.message)))
			if err == nil {
				t.Fatal("expected error")
			}
			if !tt.matcher(err) {
				t.Errorf("expected matcher(%v) to be true", err)
			}
			he, ok := err.(*HitlError)
			if !ok {
				t.Fatalf("expected *HitlError, got %T", err)
			}
			if he.Code != tt.code {
				t.Errorf("expected code %q, got %q", tt.code, he.Code)
			}
			if he.Message != tt.message {
				t.Errorf("expected message %q, got %q", tt.message, he.Message)
			}
		})
	}
}

// T15: A non-JSON body yields a generic error (not a *HitlError).
func TestErrorEnvelopeNonJSON(t *testing.T) {
	err := parseError(strings.NewReader("502 Bad Gateway"))
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*HitlError); ok {
		t.Fatalf("expected a generic error for non-JSON body, got *HitlError: %v", err)
	}
}

// ─── ChoicesWithDefault (GET /choices is GONE) ──────────────────────────────

// T16: Non-empty item choices win over the provided defaults.
func TestChoicesWithDefault_ItemChoicesWin(t *testing.T) {
	item := &QueueItem{Choices: []string{"approve", "reject"}}
	got := item.ChoicesWithDefault([]string{"default-choice"})
	if len(got) != 2 || got[0] != "approve" || got[1] != "reject" {
		t.Errorf("expected item choices to win, got %v", got)
	}
}

// T17: Empty item choices fall back to the provided defaults.
func TestChoicesWithDefault_EmptyFallsBack(t *testing.T) {
	defaults := []string{"approve", "reject"}
	item := &QueueItem{}
	if got := item.ChoicesWithDefault(defaults); !equalStrings(got, defaults) {
		t.Errorf("expected defaults on nil choices, got %v", got)
	}
	item = &QueueItem{Choices: []string{}}
	if got := item.ChoicesWithDefault(defaults); !equalStrings(got, defaults) {
		t.Errorf("expected defaults on empty choices, got %v", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ─── ResolveQueueForItem ────────────────────────────────────────────────────

// T18: ResolveQueueForItem lists GET /queues (bare array of names), probes each
// queue, and returns the matching probe item's queue_name. The client is built
// with an empty queueName to pin that per-queue probes come from the listed
// names, not the receiver's own queueName field.
func TestResolveQueueForItem_Found(t *testing.T) {
	h := newMockHandler()
	h.set("/queues", 200, `["hitl-approval","sort-review"]`)
	h.set("/queues/hitl-approval/wi-001", 404, errorJSON(ErrQueueItemNotFound, "not found"))
	h.set("/queues/sort-review/wi-001", 200, `{"workitem_id":"wi-001","queue_name":"sort-review","status":"queued"}`)
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL, "")
	got, err := client.ResolveQueueForItem(context.Background(), "wi-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "sort-review" {
		t.Errorf("expected queue %q, got %q", "sort-review", got)
	}
}

// T19: No listed queue serves the item (all probes 404) -> QUEUE_ITEM_NOT_FOUND.
func TestResolveQueueForItem_NotFound(t *testing.T) {
	h := newMockHandler()
	h.set("/queues", 200, `["hitl-approval"]`)
	h.set("/queues/hitl-approval/wi-001", 404, errorJSON(ErrQueueItemNotFound, "not found"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL, "")
	got, err := client.ResolveQueueForItem(context.Background(), "wi-001")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsQueueItemNotFound(err) {
		t.Errorf("expected IsQueueItemNotFound, got %v", err)
	}
	if got != "" {
		t.Errorf("expected empty queue name on error, got %q", got)
	}
}

// T20: A non-200 on GET /queues propagates the structured error envelope.
func TestResolveQueueForItem_ListFails(t *testing.T) {
	h := newMockHandler()
	h.set("/queues", 500, errorJSON(ErrQueueUnavailable, "down"))
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL, "")
	_, err := client.ResolveQueueForItem(context.Background(), "wi-001")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsQueueUnavailable(err) {
		t.Errorf("expected IsQueueUnavailable, got %v", err)
	}
}

// T21: A probe error aborts resolution: the error is surfaced as-is and no
// further queue is probed.
func TestResolveQueueForItem_ProbeErrorStops(t *testing.T) {
	h := newMockHandler()
	h.set("/queues", 200, `["hitl-approval","sort-review"]`)
	h.set("/queues/hitl-approval/wi-001", 500, errorJSON(ErrBadRequest, "boom"))
	// Registering the second queue's probe makes the request-count assertion
	// below fail if resolution wrongly continues past the first probe error.
	h.set("/queues/sort-review/wi-001", 200, `{"workitem_id":"wi-001","queue_name":"sort-review","status":"queued"}`)
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := NewHitlClient(srv.URL, "")
	_, err := client.ResolveQueueForItem(context.Background(), "wi-001")
	if err == nil {
		t.Fatal("expected error")
	}
	he, ok := err.(*HitlError)
	if !ok {
		t.Fatalf("expected *HitlError, got %T", err)
	}
	if he.Code != ErrBadRequest {
		t.Errorf("expected code %q, got %q", ErrBadRequest, he.Code)
	}
	// Exactly two requests: the /queues list plus the failing first probe; the
	// second queue must never be probed.
	if got := h.requestCount(); got != 2 {
		t.Errorf("expected 2 requests (list + one probe), got %d", got)
	}
	if req := h.lastRequest(); req.Path != "/queues/hitl-approval/wi-001" {
		t.Errorf("expected last request to be the failing probe, got %q", req.Path)
	}
}
