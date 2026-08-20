package queue

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// Registry test identities. Sharing them via consts (instead of re-declaring
// the same literals inline) keeps the registry tests consistent and satisfies
// goconst.
const (
	registryQueueName = "queue-a"
	registryShardID   = "shard-1"
	registryShardAddr = "host:50051"
)

// fakeRegistryServer is a test-local, in-process implementation of
// QueueRegistryServiceServer. It records every request it receives and returns
// canned responses. It runs over a bufconn listener (in-memory, zero real
// network) so the RegistryClient can dial it exactly like production.
type fakeRegistryServer struct {
	flowv1.UnimplementedQueueRegistryServiceServer

	mu             sync.Mutex
	registerReqs   []*flowv1.RegisterQueueRequest
	heartbeatReqs  []*flowv1.HeartbeatQueueRequest
	deregisterReqs []*flowv1.DeregisterQueueRequest

	// heartbeatShards is returned as the living-shard set on every
	// HeartbeatQueue call.
	heartbeatShards []*flowv1.ShardRegistration
}

func (f *fakeRegistryServer) RegisterQueue(
	_ context.Context, in *flowv1.RegisterQueueRequest,
) (*flowv1.RegisterQueueResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registerReqs = append(f.registerReqs, in)
	return &flowv1.RegisterQueueResponse{Acknowledged: true}, nil
}

func (f *fakeRegistryServer) HeartbeatQueue(
	_ context.Context, in *flowv1.HeartbeatQueueRequest,
) (*flowv1.HeartbeatQueueResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeatReqs = append(f.heartbeatReqs, in)
	return &flowv1.HeartbeatQueueResponse{Acknowledged: true, Shards: f.heartbeatShards}, nil
}

func (f *fakeRegistryServer) DeregisterQueue(
	_ context.Context, in *flowv1.DeregisterQueueRequest,
) (*flowv1.DeregisterQueueResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deregisterReqs = append(f.deregisterReqs, in)
	return &flowv1.DeregisterQueueResponse{Acknowledged: true}, nil
}

// bufconnDialer returns a registryDialer that ignores addr and dials the
// given in-memory bufconn listener.
func bufconnDialer(lis *bufconn.Listener) registryDialer {
	return func(_ context.Context, _ string) (*grpc.ClientConn, error) {
		return grpc.NewClient(
			"passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}
}

// newBufconnRegistryClient wires a fakeRegistryServer onto a fresh bufconn
// grpc server and returns a RegistryClient that dials it. The bufconn server
// is stopped and the client closed via t.Cleanup.
func newBufconnRegistryClient(t *testing.T, fake *fakeRegistryServer, interval time.Duration) *RegistryClient {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	flowv1.RegisterQueueRegistryServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	r, err := NewRegistryClient(
		"ignored-addr",
		bufconnDialer(lis),
		registryQueueName, registryShardID, registryShardAddr,
		interval,
	)
	if err != nil {
		t.Fatalf("NewRegistryClient error: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestRegistryRegisterSendsShardRegistration(t *testing.T) {
	fake := &fakeRegistryServer{}
	r := newBufconnRegistryClient(t, fake, time.Second)

	ctx := context.Background()
	err := r.Register(ctx)
	if err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.registerReqs) != 1 {
		t.Fatalf("RegisterQueue calls = %d, want 1", len(fake.registerReqs))
	}
	got := fake.registerReqs[0]
	if got.QueueName != registryQueueName {
		t.Errorf("QueueName = %q, want %q", got.QueueName, registryQueueName)
	}
	if got.ShardId != registryShardID {
		t.Errorf("ShardId = %q, want %q", got.ShardId, registryShardID)
	}
	if got.ShardAddr != registryShardAddr {
		t.Errorf("ShardAddr = %q, want %q", got.ShardAddr, registryShardAddr)
	}
}

func TestRegistryHeartbeatTriggersOnHeartbeatWithLivingShards(t *testing.T) {
	shards := []*flowv1.ShardRegistration{
		{ShardId: registryShardID, ShardAddr: registryShardAddr},
		{ShardId: "shard-2", ShardAddr: "other:50051"},
	}
	fake := &fakeRegistryServer{heartbeatShards: shards}
	r := newBufconnRegistryClient(t, fake, time.Second)

	// onHeartbeat is a private field; we're in-package so we can inject it.
	var got []flowv1.ShardRegistration
	hb := make(chan []flowv1.ShardRegistration, 1)
	r.onHeartbeat = func(s []flowv1.ShardRegistration) { hb <- s }

	ctx := context.Background()
	if err := r.Heartbeat(ctx); err != nil {
		t.Fatalf("Heartbeat() error: %v", err)
	}

	select {
	case got = <-hb:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("onHeartbeat was not invoked after a successful Heartbeat")
	}

	if len(got) != 2 {
		t.Fatalf("onHeartbeat received %d shards, want 2", len(got))
	}
	if got[0].ShardId != registryShardID || got[1].ShardId != "shard-2" {
		t.Errorf("onHeartbeat shards = %+v, want [%s shard-2]", got, registryShardID)
	}
}

func TestRegistryDeregisterSendsShard(t *testing.T) {
	fake := &fakeRegistryServer{}
	r := newBufconnRegistryClient(t, fake, time.Second)

	ctx := context.Background()
	if err := r.Deregister(ctx); err != nil {
		t.Fatalf("Deregister() error: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.deregisterReqs) != 1 {
		t.Fatalf("DeregisterQueue calls = %d, want 1", len(fake.deregisterReqs))
	}
	got := fake.deregisterReqs[0]
	if got.QueueName != registryQueueName {
		t.Errorf("QueueName = %q, want %q", got.QueueName, registryQueueName)
	}
	if got.ShardId != registryShardID {
		t.Errorf("ShardId = %q, want %q", got.ShardId, registryShardID)
	}
}

func TestRegistryHeartbeatLoopBeatsUntilCancelled(t *testing.T) {
	fake := &fakeRegistryServer{}
	r := newBufconnRegistryClient(t, fake, 10*time.Millisecond)

	var beats atomic.Int64
	r.onHeartbeat = func([]flowv1.ShardRegistration) { beats.Add(1) }

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go r.HeartbeatLoop(ctx, &wg)

	// Give the loop at most ~400ms to produce >= 2 beats; cancel after.
	deadline := time.Now().Add(400 * time.Millisecond)
	for beats.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if got := beats.Load(); got < 2 {
		t.Fatalf("HeartbeatLoop produced %d beats, want >= 2", got)
	}

	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("HeartbeatLoop did not stop after context cancellation")
	}
}
