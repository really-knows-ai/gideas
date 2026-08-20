package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowv1api "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// setupRestRegistry seeds a CR with living shards wired to bufconn mirror
// shards via r.peerDialer and returns the REST handler + the mirror shards
// keyed by addr. The mirror fakes implement the broadcast+dedupe path
// (PHASE_03) so list/get/claim/decide/release are meaningfully exercised.
type restHarness struct {
	handler *http.ServeMux
	reg     *Registry
	shards  map[string]*fakeMirrorShard
}

func newRestHarness(t *testing.T, shardAddrs []string) *restHarness {
	now := time.Now().UTC()
	entries := make([]flowv1api.QueueShardStatus, 0, len(shardAddrs))
	h := &restHarness{shards: map[string]*fakeMirrorShard{}}
	for _, addr := range shardAddrs {
		f := newFakeMirrorShard(t, addr+"-id")
		h.shards[addr] = f
		entries = append(entries, shard(addr+"-id", addr, phaseActive, now))
	}
	seed := queueCR("hitl-approval", entries...)
	c := newFakeClient(t, seed)
	r := NewRegistry(c, time.Minute, time.Second)
	r.Namespace = testNamespace
	r.peerDialer = func(ctx context.Context, addr string) (*grpc.ClientConn, error) {
		f, ok := h.shards[addr]
		if !ok {
			return nil, errShardUnavailable
		}
		return f.dialer(ctx, addr)
	}
	h.reg = r
	h.handler = NewRestServer(r).Handler()
	return h
}

func doReq(h *restHarness, method, path string, body string) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, req)
	return w
}

func TestREST_GetQueues_ListsRegistered(t *testing.T) {
	h := newRestHarness(t, []string{"say:a"})
	w := doReq(h, http.MethodGet, "/queues", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var names []string
	if err := json.Unmarshal(w.Body.Bytes(), &names); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(names) != 1 || names[0] != testQueueName {
		t.Fatalf("queues = %v, want [hitl-approval]", names)
	}
}

func TestREST_GetQueue_ListItems(t *testing.T) {
	// Two living mirror shards each hold a copy of the SAME item
	// (mirror-everywhere). The route dedupes by workitem_id (R-3.6): the shared
	// copy appears once, not once per shard.
	h := newRestHarness(t, []string{"say:a", "say:b"})
	dup := &flowv1.QueueItem{WorkitemId: "dup-id", Status: "waiting", GenerationId: "0000000000000001"}
	h.shards["say:a"].setItem(&flowv1.QueueItem{WorkitemId: "wi-a", Status: "waiting", GenerationId: "0000000000000001"})
	h.shards["say:a"].setItem(dup)
	h.shards["say:b"].setItem(&flowv1.QueueItem{WorkitemId: "wi-b", Status: "waiting", GenerationId: "0000000000000001"})
	h.shards["say:b"].setItem(dup)

	w := doReq(h, http.MethodGet, "/queues/hitl-approval", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var items []*flowv1.QueueItem
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Deduped: 3 distinct workitems (dup-id once, wi-a, wi-b).
	if len(items) != 3 {
		t.Fatalf("expected 3 deduped items, got %d: %s", len(items), w.Body.String())
	}
	dupCount := 0
	for _, it := range items {
		if it.GetWorkitemId() == "dup-id" {
			dupCount++
		}
	}
	if dupCount != 1 {
		t.Fatalf("dup-id appeared %d times, want 1 (deduped)", dupCount)
	}
}

func TestREST_GetItem_ProxiesToLivingShard(t *testing.T) {
	h := newRestHarness(t, []string{"say:a"})
	h.shards["say:a"].setItem(&flowv1.QueueItem{WorkitemId: "wi-1", Status: "waiting", GenerationId: "0000000000000001"})

	w := doReq(h, http.MethodGet, "/queues/hitl-approval/wi-1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var item *flowv1.QueueItem
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if item.GetWorkitemId() != testWorkitemID {
		t.Fatalf("item = %v", item)
	}
}

func TestREST_ClaimDecideRelease_ProxyPath(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "claim", method: http.MethodPost, path: "/queues/hitl-approval/wi-1/claim"},
		{name: "decide", method: http.MethodPost, path: "/queues/hitl-approval/wi-1/decide", body: `{"choice":"approve"}`},
		{name: "release", method: http.MethodPost, path: "/queues/hitl-approval/wi-1/release"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRestHarness(t, []string{"say:a"})
			h.shards["say:a"].setItem(&flowv1.QueueItem{WorkitemId: "wi-1", Status: "waiting", GenerationId: "0000000000000001"})
			w := doReq(h, tc.method, tc.path, tc.body)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			// decide: pin the JSON {"choice":...} → DecideItem body-parsing seam.
			if tc.name == "decide" {
				got := h.shards["say:a"].decidedCalls()
				if len(got) != 1 {
					t.Fatalf("decide reached the shard %d times, want 1", len(got))
				}
				if got[0].GetChoice() != testChoiceApprove {
					t.Fatalf("decide choice = %q, want %q", got[0].GetChoice(), "approve")
				}
			}
		})
	}
}

func TestREST_NeverProxiesToStaleShard(t *testing.T) {
	// A shard present in the CR but stale (heartbeat outside TTL, not yet
	// swept) ⇒ 503 QUEUE_UNAVAILABLE; its address is never dialed.
	now := time.Now().UTC()
	staleFake := newFakePeerShard(t)
	c := newFakeClient(t, queueCR("hitl-approval",
		shard("stale-id", "stale-addr", phaseActive, now.Add(-10*time.Minute)),
	))
	r := NewRegistry(c, time.Minute, time.Second)
	r.Namespace = testNamespace
	var dialed []string
	var mu sync.Mutex
	r.peerDialer = func(ctx context.Context, addr string) (*grpc.ClientConn, error) {
		mu.Lock()
		dialed = append(dialed, addr)
		mu.Unlock()
		return staleFake.dialer(ctx, addr)
	}
	handler := NewRestServer(r).Handler()

	req := httptest.NewRequest(http.MethodGet, "/queues/hitl-approval/wi-1", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "QUEUE_UNAVAILABLE") {
		t.Fatalf("body lacks QUEUE_UNAVAILABLE: %s", w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 0 {
		t.Fatalf("stale shard address was dialed: %v", dialed)
	}
}

func TestREST_NeverProxiesToPhaseEvictedShard(t *testing.T) {
	// A shard present with phase=evicted (not yet dropped by the sweep) is
	// never dialed ⇒ 503 QUEUE_UNAVAILABLE.
	now := time.Now().UTC()
	evictedFake := newFakePeerShard(t)
	c := newFakeClient(t, queueCR("hitl-approval",
		shard("evicted-id", "evicted-addr", phaseEvicted, now),
	))
	r := NewRegistry(c, time.Minute, time.Second)
	r.Namespace = testNamespace
	var dialed []string
	var mu sync.Mutex
	r.peerDialer = func(ctx context.Context, addr string) (*grpc.ClientConn, error) {
		mu.Lock()
		dialed = append(dialed, addr)
		mu.Unlock()
		return evictedFake.dialer(ctx, addr)
	}
	handler := NewRestServer(r).Handler()

	req := httptest.NewRequest(http.MethodGet, "/queues/hitl-approval/wi-1", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 0 {
		t.Fatalf("evicted shard address was dialed: %v", dialed)
	}
}

func TestREST_AllShardsDead_Returns503(t *testing.T) {
	// All living shards unreachable ⇒ 503.
	c := newFakeClient(t, queueCR("hitl-approval",
		shard("s1", "dead-addr", phaseActive, time.Now().UTC()),
	))
	r := NewRegistry(c, time.Minute, time.Second)
	r.Namespace = testNamespace
	r.peerDialer = func(ctx context.Context, addr string) (*grpc.ClientConn, error) {
		return nil, errShardUnavailable
	}
	handler := NewRestServer(r).Handler()

	req := httptest.NewRequest(http.MethodGet, "/queues/hitl-approval/wi-1", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
}

func TestREST_UnknownItem_AllShardsLiving_Returns404(t *testing.T) {
	// All CR shards living, none owns the item ⇒ 404 (decision rule 404 arm).
	h := newRestHarness(t, []string{"say:a"}) // living, but no wi-1
	w := doReq(h, http.MethodGet, "/queues/hitl-approval/wi-1", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "QUEUE_ITEM_NOT_FOUND") {
		t.Fatalf("body lacks QUEUE_ITEM_NOT_FOUND: %s", w.Body.String())
	}
}

func TestREST_UnknownItem_WithEvictedShard_Returns404(t *testing.T) {
	// Mirror-everywhere: reads scatter-gather over LIVING shards only. The sole
	// living shard responds empty, so the item is genuinely absent → 404 (not
	// 503) — an evicted shard is simply not part of the write/read set and must
	// not be dialed.
	now := time.Now().UTC()
	evictedFake := newFakePeerShard(t)
	liveFake := newFakePeerShard(t)
	c := newFakeClient(t, queueCR("hitl-approval",
		shard("evicted-id", "evicted-addr", phaseEvicted, now),
		shard("live-id", "live-addr", phaseActive, now),
	))
	r := NewRegistry(c, time.Minute, time.Second)
	r.Namespace = testNamespace
	var dialed []string
	var mu sync.Mutex
	r.peerDialer = func(ctx context.Context, addr string) (*grpc.ClientConn, error) {
		mu.Lock()
		dialed = append(dialed, addr)
		mu.Unlock()
		switch addr {
		case "evicted-addr":
			return evictedFake.dialer(ctx, addr)
		default:
			return liveFake.dialer(ctx, addr)
		}
	}
	handler := NewRestServer(r).Handler()

	req := httptest.NewRequest(http.MethodGet, "/queues/hitl-approval/wi-1", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	for _, a := range dialed {
		if a == "evicted-addr" {
			t.Fatal("evicted shard's address was dialed")
		}
	}
}

func TestREST_ConflictErrors_SurfaceAs409(t *testing.T) {
	// Claim: peer ClaimItem → gRPC AlreadyExists → 409 QUEUE_ITEM_ALREADY_CLAIMED.
	// Decide: peer DecideItem → gRPC FailedPrecondition → 409 QUEUE_ITEM_INVALID_STATE.
	cases := []struct {
		name     string
		method   string
		path     string
		body     string
		inject   func(f *fakeMirrorShard)
		wantCode string
	}{
		{
			name:   "claim already claimed",
			method: http.MethodPost,
			path:   "/queues/hitl-approval/wi-1/claim",
			inject: func(f *fakeMirrorShard) {
				f.setClaimError(status.Error(codes.AlreadyExists, "already claimed"))
			},
			wantCode: "QUEUE_ITEM_ALREADY_CLAIMED",
		},
		{
			name:   "decide invalid state",
			method: http.MethodPost,
			path:   "/queues/hitl-approval/wi-1/decide",
			body:   `{"choice":"approve"}`,
			inject: func(f *fakeMirrorShard) {
				f.setDecideError(status.Error(codes.FailedPrecondition, "invalid state"))
			},
			wantCode: "QUEUE_ITEM_INVALID_STATE",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRestHarness(t, []string{"say:a"})
			h.shards["say:a"].setItem(&flowv1.QueueItem{WorkitemId: "wi-1", Status: "waiting", GenerationId: "0000000000000001"})
			tc.inject(h.shards["say:a"])
			w := doReq(h, tc.method, tc.path, tc.body)
			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wantCode) {
				t.Fatalf("body lacks %s: %s", tc.wantCode, w.Body.String())
			}
		})
	}
}

func TestREST_PartialShardFailure_Returns503(t *testing.T) {
	// One living shard answers empty, another living shard fails GetLocalQueue
	// mid-fan-out and no owner is found ⇒ 503 QUEUE_UNAVAILABLE (retryable),
	// NOT 404 (SPEC proxy-write error row — the item may sit on the
	// newly-unreachable living shard).
	h := newRestHarness(t, []string{"say:a", "say:b"})
	h.shards["say:b"].setLocalError(status.Error(codes.Unavailable, "shard b unreachable"))
	w := doReq(h, http.MethodGet, "/queues/hitl-approval/wi-1", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "QUEUE_UNAVAILABLE") {
		t.Fatalf("body lacks QUEUE_UNAVAILABLE: %s", w.Body.String())
	}
}

func TestREST_Decide_MalformedBody_Returns400(t *testing.T) {
	h := newRestHarness(t, []string{"say:a"})
	h.shards["say:a"].setItem(&flowv1.QueueItem{WorkitemId: "wi-1", Status: "waiting", GenerationId: "0000000000000001"})
	// ContentLength != 0 with an invalid JSON body ⇒ 400 before any shard is
	// dialed.
	w := doReq(h, http.MethodPost, "/queues/hitl-approval/wi-1/decide", "{not-json")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "BAD_REQUEST") {
		t.Fatalf("body lacks BAD_REQUEST: %s", w.Body.String())
	}
	if got := h.shards["say:a"].decidedCalls(); len(got) != 0 {
		t.Fatalf("decide reached the shard on a malformed body: %v", got)
	}
}
