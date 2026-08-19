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
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

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
// calls it receives. Implements NotifyShardDead for the fan-out test.
type fakePeerShard struct {
	flowv1.UnimplementedQueuePeerServiceServer

	mu       sync.Mutex
	items    []*flowv1.QueueItem
	claimed  []string
	released []string
	decided  []*flowv1.DecideItemRequest
	notified []string // shard_ids received via NotifyShardDead

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

	dialer := func(ctx context.Context, _ string) (*grpc.ClientConn, error) {
		return grpc.NewClient("passthrough:///fake-peer",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}
	f.dialer = dialer
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

func (f *fakePeerShard) NotifyShardDead(
	_ context.Context, req *flowv1.NotifyShardDeadRequest,
) (*flowv1.NotifyShardDeadResponse, error) {
	f.mu.Lock()
	f.notified = append(f.notified, req.GetShardId())
	f.mu.Unlock()
	return &flowv1.NotifyShardDeadResponse{Acknowledged: true}, nil
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

func (f *fakePeerShard) notifiedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notified
}

// failDialer returns errShardUnavailable immediately without dialing any
// network. Used by lease tests that only assert CR state changes and must not
// let the NotifyShardDead fan-out hit real addresses.
func failDialer(_ context.Context, _ string) (*grpc.ClientConn, error) {
	return nil, errShardUnavailable
}

const meshBufSize = 1024 * 1024
