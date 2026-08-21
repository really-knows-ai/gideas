package queuemgr_test

import (
	"context"
	"net"
	"sync"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/foundry/flow/sdk/go/internal/queuemgr"
)

// Fixed RFC3339 timestamps the fake stamps on items. The thin client must
// parse these into time.Time, so tests can compare deterministically without
// touching a real clock.
const (
	fakeEnqueuedAt = "2026-01-02T15:04:05Z"
	fakeClaimedAt  = "2026-01-02T15:05:00Z"
)

// Shared status/choice literals for the external test package. Deduplicating
// the repeated strings satisfies goconst while keeping the assertions
// meaningful (e.g. TestQueueStatus_ConstantValues still pins the literal value).
const (
	queueStatusWaiting = "waiting"
	queueStatusClaimed = "claimed"
	choiceApprove      = "approve"
)

// fakeQueueService is an in-memory, in-process implementation of the
// queue-service's SDK-facing surface (flowv1.QueueGatewayServiceServer). It is
// a true unit-test fake: no real I/O, no database, no EventBus. Every RPC
// returns the PHASE_01 pinned gRPC status codes so the client's translation to
// the sdk sentinels can be asserted.
//
// Items are keyed by workitem_id (by construction there is a single queue).
// Enqueue additionally records the choices it received so tests can assert the
// client actually sent the configured WithChoices payload.
type fakeQueueService struct {
	flowv1.UnimplementedQueueGatewayServiceServer

	mu         sync.Mutex
	items      map[string]*flowv1.QueueItem
	decided    map[string]string
	enqChoices map[string][]string

	// claimUnavailable makes Claim return gRPC codes.Unavailable so tests can
	// exercise the thin client's ErrShardUnavailable reverse-mapping. Set it
	// before any Claim RPC.
	claimUnavailable bool
}

func newFakeQueueService() *fakeQueueService {
	return &fakeQueueService{
		items:      map[string]*flowv1.QueueItem{},
		decided:    map[string]string{},
		enqChoices: map[string][]string{},
	}
}

// enqueueChoices returns a copy of the choices recorded for a workitem_id.
func (f *fakeQueueService) enqueueChoices(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.enqChoices[id]...)
}

// item returns a deep-ish copy of the stored item for id (nil if absent), so
// tests can inspect what the fake served without aliasing internal state.
func (f *fakeQueueService) item(id string) *flowv1.QueueItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[id]
	if !ok {
		return nil
	}
	return proto.Clone(it).(*flowv1.QueueItem)
}

// decidedChoice returns the recorded decision choice for id ("" if none).
func (f *fakeQueueService) decidedChoice(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.decided[id]
}

func (f *fakeQueueService) Enqueue(ctx context.Context, req *flowv1.EnqueueRequest) (*flowv1.EnqueueResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[req.GetWorkitemId()] = &flowv1.QueueItem{
		WorkitemId:   req.GetWorkitemId(),
		QueueName:    req.GetQueueName(),
		Status:       queueStatusWaiting,
		EnqueuedAt:   fakeEnqueuedAt,
		ShardId:      "shard-0",
		GenerationId: "gen-1",
		Choices:      append([]string(nil), req.GetChoices()...),
	}
	f.enqChoices[req.GetWorkitemId()] = append([]string(nil), req.GetChoices()...)
	return &flowv1.EnqueueResponse{Acknowledged: true}, nil
}

func (f *fakeQueueService) GetGlobalQueue(
	ctx context.Context,
	req *flowv1.GetGlobalQueueRequest,
) (*flowv1.GetGlobalQueueResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*flowv1.QueueItem{}
	for _, it := range f.items {
		if req.GetStatus() != "" && it.GetStatus() != req.GetStatus() {
			continue
		}
		cp := proto.Clone(it).(*flowv1.QueueItem)
		out = append(out, cp)
	}
	return &flowv1.GetGlobalQueueResponse{Items: out}, nil
}

func (f *fakeQueueService) GetItem(ctx context.Context, req *flowv1.GetItemRequest) (*flowv1.GetItemResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[req.GetWorkitemId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "queue item not found")
	}
	cp := proto.Clone(it).(*flowv1.QueueItem)
	return &flowv1.GetItemResponse{Item: cp}, nil
}

func (f *fakeQueueService) Claim(ctx context.Context, req *flowv1.ClaimRequest) (*flowv1.ClaimResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimUnavailable {
		return nil, status.Error(codes.Unavailable, "queue shard unavailable")
	}
	it, ok := f.items[req.GetWorkitemId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "queue item not found")
	}
	if it.GetStatus() == queueStatusClaimed {
		return nil, status.Error(codes.AlreadyExists, "queue item already claimed")
	}
	it.Status = queueStatusClaimed
	it.ClaimedAt = fakeClaimedAt
	cp := proto.Clone(it).(*flowv1.QueueItem)
	return &flowv1.ClaimResponse{Item: cp}, nil
}

func (f *fakeQueueService) Release(ctx context.Context, req *flowv1.ReleaseRequest) (*flowv1.ReleaseResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[req.GetWorkitemId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "queue item not found")
	}
	if it.GetStatus() != queueStatusClaimed {
		return nil, status.Error(codes.FailedPrecondition, "queue item invalid state transition")
	}
	it.Status = queueStatusWaiting
	it.ClaimedAt = ""
	cp := proto.Clone(it).(*flowv1.QueueItem)
	return &flowv1.ReleaseResponse{Item: cp}, nil
}

func (f *fakeQueueService) Decide(ctx context.Context, req *flowv1.DecideRequest) (*flowv1.DecideResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[req.GetWorkitemId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "queue item not found")
	}
	if it.GetStatus() != queueStatusClaimed {
		return nil, status.Error(codes.FailedPrecondition, "queue item invalid state transition")
	}
	delete(f.items, req.GetWorkitemId())
	f.decided[req.GetWorkitemId()] = req.GetChoice()
	return &flowv1.DecideResponse{Acknowledged: true}, nil
}

// newBufconnManager wires a fakeQueueService behind a bufconn listener and
// returns a *queuemgr.Manager whose dialer connects to that in-process
// listener. The manager is constructed with the injected dialer seam
// (WithDialer) so the thin client never touches real network or the
// FLOW_QUEUE_SERVICE_ADDR environment variable.
//
// opts are appended before WithDialer so the test-supplied options (queue
// name, choices) apply; the dialer always wins.
func newBufconnManager(t *testing.T, opts ...queuemgr.Option) (*queuemgr.Manager, *fakeQueueService) {
	t.Helper()
	fake := newFakeQueueService()

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	flowv1.RegisterQueueGatewayServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	dialer := func(ctx context.Context, _ string) (grpc.ClientConnInterface, error) {
		conn, err := grpc.NewClient(
			"passthrough:///bufconn-queue",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn, nil
	}

	m, err := queuemgr.NewManager(append(opts, queuemgr.WithDialer(dialer))...)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m, fake
}
