package rest

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/queue/internal/peer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Test constants to avoid goconst warnings.
const (
	testStatusClaimed = "claimed"
	testItemID1       = "item-1"
	testQueueName     = "test-queue"
)

// --- Spy gRPC server ---

// spyShard is a test shard that implements QueuePeerServiceServer with
// configurable items and behaviours.
type spyShard struct {
	flowv1.UnimplementedQueuePeerServiceServer
	items      []*flowv1.QueueItem
	claimErr   error
	decideErr  error
	releaseErr error
	delay      time.Duration // artificial delay per call
	shouldHang bool          // if true, never responds (tests timeout)
	mu         sync.Mutex
}

func (s *spyShard) GetLocalQueue(
	ctx context.Context, req *flowv1.GetLocalQueueRequest,
) (*flowv1.GetLocalQueueResponse, error) {
	s.mu.Lock()
	items := s.items
	delay := s.delay
	hang := s.shouldHang
	s.mu.Unlock()

	if hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	return &flowv1.GetLocalQueueResponse{Items: items, Total: int32(len(items))}, nil
}

func (s *spyShard) ClaimItem(ctx context.Context, req *flowv1.ClaimItemRequest) (*flowv1.ClaimItemResponse, error) {
	s.mu.Lock()
	err := s.claimErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for _, item := range s.items {
		if item.GetWorkitemId() == req.GetWorkitemId() {
			if item.GetStatus() == testStatusClaimed {
				return nil, status.Error(codes.AlreadyExists, "item already claimed")
			}
			item.Status = testStatusClaimed
			return &flowv1.ClaimItemResponse{Item: item}, nil
		}
	}
	return nil, status.Error(codes.NotFound, "item not found")
}

func (s *spyShard) DecideItem(ctx context.Context, req *flowv1.DecideItemRequest) (*flowv1.DecideItemResponse, error) {
	s.mu.Lock()
	err := s.decideErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for _, item := range s.items {
		if item.GetWorkitemId() == req.GetWorkitemId() {
			return &flowv1.DecideItemResponse{Acknowledged: true}, nil
		}
	}
	return nil, status.Error(codes.NotFound, "item not found")
}

func (s *spyShard) ReleaseItem(
	ctx context.Context, req *flowv1.ReleaseItemRequest,
) (*flowv1.ReleaseItemResponse, error) {
	s.mu.Lock()
	err := s.releaseErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for _, item := range s.items {
		if item.GetWorkitemId() == req.GetWorkitemId() {
			item.Status = "waiting"
			return &flowv1.ReleaseItemResponse{Item: item}, nil
		}
	}
	return nil, status.Error(codes.NotFound, "item not found")
}

// --- staticResolver implements flow.PeerResolver with fixed addresses ---

type staticResolver struct {
	addrs []string
}

func (r *staticResolver) Resolve(ctx context.Context) ([]string, error) {
	return r.addrs, nil
}

// --- Test helpers ---

func startSpyShard(t *testing.T, srv flowv1.QueuePeerServiceServer) (addr string, stop func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	s := grpc.NewServer()
	flowv1.RegisterQueuePeerServiceServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	return lis.Addr().String(), s.Stop
}

func newTestHandler(shards map[string][]string) *Handler {
	queues := make([]string, 0, len(shards))
	peers := make(map[string]*peer.PeerClient, len(shards))
	for qname, addrs := range shards {
		queues = append(queues, qname)
		peers[qname] = peer.NewPeerClient(&staticResolver{addrs: addrs})
	}
	return NewHandler(queues, peers)
}

func doRequest(t *testing.T, h *Handler, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody io.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reqBody)
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	mux.ServeHTTP(w, req)
	return w
}

// --- Tests ---

func TestListQueues(t *testing.T) {
	h := newTestHandler(map[string][]string{
		"human-arbiter":  {"localhost:10001"},
		"human-approval": {"localhost:10002"},
	})

	w := doRequest(t, h, "GET", "/queues", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var got []string
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 queues, got %d: %v", len(got), got)
	}
}

func TestGetQueueItems_ScatterGather(t *testing.T) {
	shard1 := &spyShard{
		items: []*flowv1.QueueItem{
			{WorkitemId: "item-1", ShardId: "shard-1", Status: "waiting", QueueName: "test-queue"},
		},
	}
	shard2 := &spyShard{
		items: []*flowv1.QueueItem{
			{WorkitemId: "item-2", ShardId: "shard-2", Status: "waiting", QueueName: "test-queue"},
		},
	}

	addr1, stop1 := startSpyShard(t, shard1)
	defer stop1()
	addr2, stop2 := startSpyShard(t, shard2)
	defer stop2()

	h := newTestHandler(map[string][]string{
		"test-queue": {addr1, addr2},
	})

	w := doRequest(t, h, "GET", "/queues/test-queue", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	items, ok := resp["items"].([]any)
	if !ok {
		t.Fatalf("expected items array, got %T", resp["items"])
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	unreachable, ok := resp["unreachable_shards"].([]any)
	if !ok {
		t.Fatalf("expected unreachable_shards array, got %T", resp["unreachable_shards"])
	}
	if len(unreachable) != 0 {
		t.Fatalf("expected 0 unreachable shards, got %d", len(unreachable))
	}
}

func TestGetQueueItem(t *testing.T) {
	shard1 := &spyShard{
		items: []*flowv1.QueueItem{
			{WorkitemId: "item-1", ShardId: "shard-1", Status: "waiting", QueueName: "test-queue"},
		},
	}
	shard2 := &spyShard{
		items: []*flowv1.QueueItem{
			{WorkitemId: "item-2", ShardId: "shard-2", Status: "waiting", QueueName: "test-queue"},
		},
	}

	addr1, stop1 := startSpyShard(t, shard1)
	defer stop1()
	addr2, stop2 := startSpyShard(t, shard2)
	defer stop2()

	h := newTestHandler(map[string][]string{
		"test-queue": {addr1, addr2},
	})

	w := doRequest(t, h, "GET", "/queues/test-queue/item-1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var item flowv1.QueueItem
	if err := json.NewDecoder(w.Body).Decode(&item); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if item.GetWorkitemId() != testItemID1 {
		t.Fatalf("expected item-1, got %s", item.GetWorkitemId())
	}
}

func TestClaimItem_FirstSuccess(t *testing.T) {
	shard1 := &spyShard{items: []*flowv1.QueueItem{}}
	shard2 := &spyShard{
		items: []*flowv1.QueueItem{
			{WorkitemId: "item-1", ShardId: "shard-2", Status: "waiting", QueueName: "test-queue"},
		},
	}

	addr1, stop1 := startSpyShard(t, shard1)
	defer stop1()
	addr2, stop2 := startSpyShard(t, shard2)
	defer stop2()

	h := newTestHandler(map[string][]string{
		"test-queue": {addr1, addr2},
	})

	w := doRequest(t, h, "POST", "/queues/test-queue/item-1/claim", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var item flowv1.QueueItem
	if err := json.NewDecoder(w.Body).Decode(&item); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if item.GetWorkitemId() != "item-1" {
		t.Fatalf("expected item-1, got %s", item.GetWorkitemId())
	}
	if item.GetStatus() != "claimed" {
		t.Fatalf("expected claimed status, got %s", item.GetStatus())
	}
}

func TestDecideItem(t *testing.T) {
	shard1 := &spyShard{
		items: []*flowv1.QueueItem{
			{WorkitemId: "item-1", ShardId: "shard-1", Status: "claimed", QueueName: "test-queue"},
		},
	}

	addr1, stop1 := startSpyShard(t, shard1)
	defer stop1()

	h := newTestHandler(map[string][]string{
		"test-queue": {addr1},
	})

	w := doRequest(t, h, "POST", "/queues/test-queue/item-1/decide", `{"choice":"approve"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp["acknowledged"] != true {
		t.Fatalf("expected acknowledged:true, got %v", resp)
	}
}

func TestReleaseItem(t *testing.T) {
	shard1 := &spyShard{
		items: []*flowv1.QueueItem{
			{WorkitemId: "item-1", ShardId: "shard-1", Status: "claimed", QueueName: "test-queue"},
		},
	}

	addr1, stop1 := startSpyShard(t, shard1)
	defer stop1()

	h := newTestHandler(map[string][]string{
		"test-queue": {addr1},
	})

	w := doRequest(t, h, "POST", "/queues/test-queue/item-1/release", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var item flowv1.QueueItem
	if err := json.NewDecoder(w.Body).Decode(&item); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if item.GetWorkitemId() != "item-1" {
		t.Fatalf("expected item-1, got %s", item.GetWorkitemId())
	}
	if item.GetStatus() != "waiting" {
		t.Fatalf("expected waiting status after release, got %s", item.GetStatus())
	}
}

func TestUnknownQueue_404(t *testing.T) {
	h := newTestHandler(map[string][]string{
		"known-queue": {"localhost:1"},
	})

	w := doRequest(t, h, "GET", "/queues/unknown-queue", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUnknownItem_404(t *testing.T) {
	shard1 := &spyShard{
		items: []*flowv1.QueueItem{
			{WorkitemId: "item-1", ShardId: "shard-1", Status: "waiting"},
		},
	}

	addr1, stop1 := startSpyShard(t, shard1)
	defer stop1()

	h := newTestHandler(map[string][]string{
		"test-queue": {addr1},
	})

	w := doRequest(t, h, "GET", "/queues/test-queue/nonexistent", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestShardTimeout_PartialResults(t *testing.T) {
	shard1 := &spyShard{
		items: []*flowv1.QueueItem{
			{WorkitemId: "item-1", ShardId: "shard-1", Status: "waiting", QueueName: "test-queue"},
		},
	}
	shard2 := &spyShard{
		shouldHang: true,
	}

	addr1, stop1 := startSpyShard(t, shard1)
	defer stop1()
	addr2, stop2 := startSpyShard(t, shard2)
	defer stop2()

	h := newTestHandler(map[string][]string{
		"test-queue": {addr1, addr2},
	})

	w := doRequest(t, h, "GET", "/queues/test-queue", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 partial results, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	items, ok := resp["items"].([]any)
	if !ok {
		t.Fatalf("expected items array, got %T", resp["items"])
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item from reachable shard, got %d", len(items))
	}

	unreachable, ok := resp["unreachable_shards"].([]any)
	if !ok {
		t.Fatalf("expected unreachable_shards array, got %T", resp["unreachable_shards"])
	}
	if len(unreachable) != 1 {
		t.Fatalf("expected 1 unreachable shard, got %d: %v", len(unreachable), unreachable)
	}
}

func TestAllShardsUnreachable_502(t *testing.T) {
	shard1 := &spyShard{shouldHang: true}
	shard2 := &spyShard{shouldHang: true}

	addr1, stop1 := startSpyShard(t, shard1)
	defer stop1()
	addr2, stop2 := startSpyShard(t, shard2)
	defer stop2()

	h := newTestHandler(map[string][]string{
		"test-queue": {addr1, addr2},
	})

	w := doRequest(t, h, "GET", "/queues/test-queue", "")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMutationAllShardsDown_503(t *testing.T) {
	h := newTestHandler(map[string][]string{
		"test-queue": {"localhost:1", "localhost:2"},
	})

	w := doRequest(t, h, "POST", "/queues/test-queue/item-1/claim", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestItemAlreadyClaimed_409(t *testing.T) {
	shard1 := &spyShard{
		items: []*flowv1.QueueItem{
			{WorkitemId: "item-1", ShardId: "shard-1", Status: "claimed", QueueName: "test-queue"},
		},
	}

	addr1, stop1 := startSpyShard(t, shard1)
	defer stop1()

	h := newTestHandler(map[string][]string{
		"test-queue": {addr1},
	})

	w := doRequest(t, h, "POST", "/queues/test-queue/item-1/claim", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestItemNotClaimed_409(t *testing.T) {
	shard1 := &spyShard{
		items: []*flowv1.QueueItem{
			{WorkitemId: "item-1", ShardId: "shard-1", Status: "waiting", QueueName: "test-queue"},
		},
		releaseErr: status.Error(codes.FailedPrecondition, "item not claimed"),
	}

	addr1, stop1 := startSpyShard(t, shard1)
	defer stop1()

	h := newTestHandler(map[string][]string{
		"test-queue": {addr1},
	})

	w := doRequest(t, h, "POST", "/queues/test-queue/item-1/release", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}
