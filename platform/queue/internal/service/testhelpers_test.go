package service

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowv1api "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// bufconnDialer returns a peerDialer that connects to the given bufconn
// listener (zero real network). addr is ignored (bufconn); the registry records
// a distinct logical addr per shard and the dialer routes them all to the same
// listener when tests are single-fake, or distinct fakes via a switch in the
// injection point.
func bufconnDialer(lis *bufconn.Listener) peerDialer {
	return func(ctx context.Context, _ string) (*grpc.ClientConn, error) {
		return grpc.NewClient("passthrough:///fake-peer",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}
}

// testNamespace is the namespace every registry test operates in, mirroring
// how production resolves FLOW_NAMESPACE and assigns Registry.Namespace
// post-construction.
const testNamespace = "test"

// Common test constants for shard identity/addr literals that recur across
// tests (goconst).
const (
	testShard0     = "shard-0"
	testShard0Addr = "10.0.0.1:50053"
)

// newTestScheme builds a scheme with client-go + flowv1 types.
func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(flowv1api.AddToScheme(scheme))
	return scheme
}

// newFakeClient builds a controller-runtime fake client with the Queue type's
// status subresource enabled, so Status().Update() round-trips like a real API
// server (production strips status on a main-resource Update — the tests must
// exercise the subresource so a broken implementation stays red).
func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithStatusSubresource(&flowv1api.Queue{}).
		WithObjects(objs...).
		Build()
}

// queueCR is a helper to construct a Queue CR for seeding the fake client.
func queueCR(queueName string, shards ...flowv1api.QueueShardStatus) *flowv1api.Queue {
	q := &flowv1api.Queue{}
	q.Name = queueName
	q.Namespace = testNamespace
	q.Spec.QueueName = queueName
	q.Status.Shards = shards
	return q
}

// shard is a helper to build a QueueShardStatus entry.
func shard(id, addr, phase string, heartbeatAt time.Time) flowv1api.QueueShardStatus {
	s := flowv1api.QueueShardStatus{
		ShardID: id,
		Addr:    addr,
		Phase:   phase,
	}
	if !heartbeatAt.IsZero() {
		s.LastHeartbeatAt = metav1.NewTime(heartbeatAt)
	}
	return s
}

// ---------------------------------------------------------------------------
// bufconn QueuePeerService fakes
// ---------------------------------------------------------------------------

// fakePeerShard is a bufconn-backed QueuePeerService server recording the
// calls it receives (the canned-response fake: returns fixed items / fixed
// results rather than simulating a real store). The faithful mirror-store fake
// for the PHASE_03 write funnel is fakeMirrorShard below.
type fakePeerShard struct {
	flowv1.UnimplementedQueuePeerServiceServer

	mu       sync.Mutex
	items    []*flowv1.QueueItem
	claimed  []string
	released []string
	decided  []*flowv1.DecideItemRequest

	// Error-injection knobs (mirror the SDK's failBeats pattern): when set,
	// the corresponding handler returns the error instead of succeeding.
	claimErr  error
	decideErr error
	localErr  error

	lis    *bufconn.Listener
	srv    *grpc.Server
	dialer peerDialer
}

// newFakePeerShard starts a bufconn QueuePeerService server and returns a
// peerDialer closing over its listener. addr is the logical address the
// registry records in the CR; the dialer ignores it (bufconn).
func newFakePeerShard(t *testing.T) *fakePeerShard {
	t.Helper()
	lis := bufconn.Listen(meshBufSize)
	srv := grpc.NewServer()
	f := &fakePeerShard{lis: lis, srv: srv}
	flowv1.RegisterQueuePeerServiceServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)
	f.dialer = bufconnDialer(lis)
	return f
}

func (f *fakePeerShard) GetLocalQueue(
	_ context.Context, _ *flowv1.GetLocalQueueRequest,
) (*flowv1.GetLocalQueueResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.localErr != nil {
		return nil, f.localErr
	}
	return &flowv1.GetLocalQueueResponse{Items: f.items}, nil
}

func (f *fakePeerShard) ClaimItem(
	_ context.Context, req *flowv1.ClaimItemRequest,
) (*flowv1.ClaimItemResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	f.claimed = append(f.claimed, req.GetWorkitemId())
	// Return a minimal item so the proxy has something to proxy back.
	item := &flowv1.QueueItem{WorkitemId: req.GetWorkitemId(), Status: "claimed"}
	return &flowv1.ClaimItemResponse{Item: item}, nil
}

func (f *fakePeerShard) ReleaseItem(
	_ context.Context, req *flowv1.ReleaseItemRequest,
) (*flowv1.ReleaseItemResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, req.GetWorkitemId())
	item := &flowv1.QueueItem{WorkitemId: req.GetWorkitemId(), Status: "waiting"}
	return &flowv1.ReleaseItemResponse{Item: item}, nil
}

func (f *fakePeerShard) DecideItem(
	_ context.Context, req *flowv1.DecideItemRequest,
) (*flowv1.DecideItemResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.decideErr != nil {
		return nil, f.decideErr
	}
	f.decided = append(f.decided, req)
	return &flowv1.DecideItemResponse{Acknowledged: true}, nil
}

// setItems replaces the fake's served item list.
func (f *fakePeerShard) setItems(items ...*flowv1.QueueItem) {
	f.mu.Lock()
	f.items = items
	f.mu.Unlock()
}

// setClaimsError makes ClaimItem return the given error once set (injection
// knob mirroring the SDK's failBeats pattern).
func (f *fakePeerShard) setClaimsError(err error) {
	f.mu.Lock()
	f.claimErr = err
	f.mu.Unlock()
}

// setDecideError makes DecideItem return the given error once set.
func (f *fakePeerShard) setDecideError(err error) {
	f.mu.Lock()
	f.decideErr = err
	f.mu.Unlock()
}

// setLocalError makes GetLocalQueue return the given error once set.
func (f *fakePeerShard) setLocalError(err error) {
	f.mu.Lock()
	f.localErr = err
	f.mu.Unlock()
}

func (f *fakePeerShard) decidedCalls() []*flowv1.DecideItemRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.decided
}

// failDialer returns errShardUnavailable immediately without dialing any
// network. Used by lease tests that only assert CR state changes and must not
// let any proxy op hit real addresses.
func failDialer(_ context.Context, _ string) (*grpc.ClientConn, error) {
	return nil, errShardUnavailable
}

const meshBufSize = 1024 * 1024

// ---------------------------------------------------------------------------
// fakeMirrorShard — the faithful mirror-store double for the write funnel
// ---------------------------------------------------------------------------

// fakeMirrorShard is a bufconn-backed QueuePeerService server that FAITHFULLY
// implements the mirror-store semantics the PHASE_03 write funnel targets: a
// per-shard in-process store keyed by workitem_id with generation-guarded
// apply, waiting→claimed CAS, claimed→waiting release, claimed-row delete, and
// served_by_shard_id tagging on reads. It stands in for a real shard's local
// store so the funnel is meaningfully unit-testable (single unit = the funnel;
// the fake is a local in-process double — zero real I/O). Constructor takes a
// shardID so tests can name shards and assert the owner tie-break.
type fakeMirrorShard struct {
	flowv1.UnimplementedQueuePeerServiceServer
	shardID string

	mu      sync.Mutex
	store   map[string]*flowv1.QueueItem
	applied []*flowv1.QueueItem
	claimed []string
	decided []*flowv1.DecideItemRequest
	dropped []string

	// Error-injection knobs (gRPC status errors, mirrors the SDK's failBeats
	// pattern). When set the corresponding handler returns the error, letting
	// tests simulate a mid-write shard failure for quorum accounting.
	applyErr  error
	claimErr  error
	decideErr error
	localErr  error

	lis    *bufconn.Listener
	srv    *grpc.Server
	dialer peerDialer
}

func newFakeMirrorShard(t *testing.T, shardID string) *fakeMirrorShard {
	t.Helper()
	lis := bufconn.Listen(meshBufSize)
	srv := grpc.NewServer()
	f := &fakeMirrorShard{
		shardID: shardID,
		store:   make(map[string]*flowv1.QueueItem),
		lis:     lis,
		srv:     srv,
	}
	flowv1.RegisterQueuePeerServiceServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)
	f.dialer = bufconnDialer(lis)
	return f
}

// ApplyItem is the generation-guarded apply: insert if absent, or overwrite
// only if the carried generation is >= the stored one (fixed-width time-ordered
// hex so >= is the correct ordering). An older re-delivery is a no-op but still
// acks. Records every apply for assertions.
func (f *fakeMirrorShard) ApplyItem(_ context.Context, req *flowv1.ApplyItemRequest) (*flowv1.ApplyItemResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyErr != nil {
		return nil, f.applyErr
	}
	item := req.GetItem()
	if item == nil {
		return &flowv1.ApplyItemResponse{Acknowledged: true}, nil
	}
	cur, ok := f.store[item.GetWorkitemId()]
	if !ok || item.GetGenerationId() >= cur.GetGenerationId() {
		cp := *item
		f.store[item.GetWorkitemId()] = &cp
	}
	f.applied = append(f.applied, item)
	return &flowv1.ApplyItemResponse{Acknowledged: true}, nil
}

// GetLocalQueue returns the stored items (optionally status-filtered) with the
// response's served_by_shard_id set to this shard's id for the owner tie-break.
func (f *fakeMirrorShard) GetLocalQueue(_ context.Context, req *flowv1.GetLocalQueueRequest) (*flowv1.GetLocalQueueResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.localErr != nil {
		return nil, f.localErr
	}
	var items []*flowv1.QueueItem
	for _, it := range f.store {
		if req.GetStatus() != "" && it.GetStatus() != req.GetStatus() {
			continue
		}
		cp := *it
		items = append(items, &cp)
	}
	return &flowv1.GetLocalQueueResponse{Items: items, ServedByShardId: f.shardID}, nil
}

// ClaimItem is the waiting→claimed CAS. A second claim of an already-claimed
// item returns gRPC AlreadyExists; an absent item returns NotFound.
func (f *fakeMirrorShard) ClaimItem(_ context.Context, req *flowv1.ClaimItemRequest) (*flowv1.ClaimItemResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	id := req.GetWorkitemId()
	it, ok := f.store[id]
	if !ok {
		return nil, status.Error(codes.NotFound, "item not found")
	}
	if it.GetStatus() != "waiting" {
		return nil, status.Error(codes.AlreadyExists, "item already claimed")
	}
	cp := *it
	cp.Status = "claimed"
	f.store[id] = &cp
	f.claimed = append(f.claimed, id)
	return &flowv1.ClaimItemResponse{Item: &cp}, nil
}

// ReleaseItem is the claimed→waiting CAS. Invalid (non-claimed) → FailedPrecondition;
// absent → NotFound.
func (f *fakeMirrorShard) ReleaseItem(_ context.Context, req *flowv1.ReleaseItemRequest) (*flowv1.ReleaseItemResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := req.GetWorkitemId()
	it, ok := f.store[id]
	if !ok {
		return nil, status.Error(codes.NotFound, "item not found")
	}
	if it.GetStatus() != "claimed" {
		return nil, status.Error(codes.FailedPrecondition, "item not claimed")
	}
	cp := *it
	cp.Status = "waiting"
	f.store[id] = &cp
	return &flowv1.ReleaseItemResponse{Item: &cp}, nil
}

// DecideItem deletes the claimed row. Invalid (non-claimed) → FailedPrecondition;
// absent → NotFound.
func (f *fakeMirrorShard) DecideItem(_ context.Context, req *flowv1.DecideItemRequest) (*flowv1.DecideItemResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.decideErr != nil {
		return nil, f.decideErr
	}
	id := req.GetWorkitemId()
	it, ok := f.store[id]
	if !ok {
		return nil, status.Error(codes.NotFound, "item not found")
	}
	if it.GetStatus() != "claimed" {
		return nil, status.Error(codes.FailedPrecondition, "item not claimed")
	}
	delete(f.store, id)
	f.decided = append(f.decided, req)
	return &flowv1.DecideItemResponse{Acknowledged: true}, nil
}

// DropItem deletes the row only if its generation matches the carried one
// (generation-guarded backup-copy drop).
func (f *fakeMirrorShard) DropItem(_ context.Context, req *flowv1.DropItemRequest) (*flowv1.DropItemResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if it, ok := f.store[req.GetWorkitemId()]; ok && it.GetGenerationId() == req.GetGenerationId() {
		delete(f.store, req.GetWorkitemId())
	}
	f.dropped = append(f.dropped, req.GetWorkitemId())
	return &flowv1.DropItemResponse{Acknowledged: true}, nil
}

// serve returns the item this shard holds for workitemID (nil if absent). Test
// helper for "every shard holds it" checks.
func (f *fakeMirrorShard) serve(workitemID string) *flowv1.QueueItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.store[workitemID]
	if !ok {
		return nil
	}
	cp := *it
	return &cp
}

// setItem directly seeds the store (test setup, bypassing the wire).
func (f *fakeMirrorShard) setItem(item *flowv1.QueueItem) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *item
	f.store[item.GetWorkitemId()] = &cp
}

func (f *fakeMirrorShard) appliedCalls() []*flowv1.QueueItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applied
}

func (f *fakeMirrorShard) claimedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claimed
}

func (f *fakeMirrorShard) decidedCalls() []*flowv1.DecideItemRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.decided
}

func (f *fakeMirrorShard) droppedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dropped
}

func (f *fakeMirrorShard) setApplyError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applyErr = err
}

func (f *fakeMirrorShard) setClaimError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimErr = err
}

func (f *fakeMirrorShard) setDecideError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decideErr = err
}

func (f *fakeMirrorShard) setLocalError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.localErr = err
}
