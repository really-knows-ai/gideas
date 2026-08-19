package queue

import (
	"context"
	"net"
	"sync"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const (
	testQueueName = "hitl-approval"
	testShard0    = "shard-0"
	testShard1    = "shard-1"
	testAddr0     = "10.0.0.1:50053"
	testAddr1     = "10.0.0.2:50053"
	testBeatTS    = "2026-08-19T00:00:00Z"
	testWorkID    = "wi-1"
	testChoice    = "approve"
	testDeadShard = "shard-2"
)

// registryFakeServer records the requests received on the queue-service's
// QueueRegistryService surface and answers with canned responses. It pins the
// wire contract: the fields a PHASE_03 queue-service would persist must arrive
// intact, and no queue-scoping semantics are invented.
type registryFakeServer struct {
	flowv1.UnimplementedQueueRegistryServiceServer

	mu        sync.Mutex
	registers []*flowv1.RegisterQueueRequest
	beats     []*flowv1.HeartbeatQueueRequest
	beatResp  *flowv1.HeartbeatQueueResponse
	dereg     []*flowv1.DeregisterQueueRequest
	cancels   []*flowv1.CancelQueuedItemRequest
	decides   []*flowv1.DecideQueuedItemRequest
}

func (f *registryFakeServer) RegisterQueue(
	_ context.Context, req *flowv1.RegisterQueueRequest,
) (*flowv1.RegisterQueueResponse, error) {
	f.mu.Lock()
	f.registers = append(f.registers, req)
	f.mu.Unlock()
	return &flowv1.RegisterQueueResponse{Acknowledged: true}, nil
}

func (f *registryFakeServer) HeartbeatQueue(
	_ context.Context, req *flowv1.HeartbeatQueueRequest,
) (*flowv1.HeartbeatQueueResponse, error) {
	f.mu.Lock()
	f.beats = append(f.beats, req)
	f.mu.Unlock()
	return f.beatResp, nil
}

func (f *registryFakeServer) DeregisterQueue(
	_ context.Context, req *flowv1.DeregisterQueueRequest,
) (*flowv1.DeregisterQueueResponse, error) {
	f.mu.Lock()
	f.dereg = append(f.dereg, req)
	f.mu.Unlock()
	return &flowv1.DeregisterQueueResponse{Acknowledged: true}, nil
}

func (f *registryFakeServer) ListQueues(
	_ context.Context, _ *flowv1.ListQueuesRequest,
) (*flowv1.ListQueuesResponse, error) {
	return &flowv1.ListQueuesResponse{Queues: []*flowv1.QueueRegistration{
		{
			QueueName: testQueueName,
			Shards: []*flowv1.ShardRegistration{
				{ShardId: testShard0, ShardAddr: testAddr0},
			},
		},
	}}, nil
}

func (f *registryFakeServer) CancelQueuedItem(
	_ context.Context, req *flowv1.CancelQueuedItemRequest,
) (*flowv1.CancelQueuedItemResponse, error) {
	f.mu.Lock()
	f.cancels = append(f.cancels, req)
	f.mu.Unlock()
	return &flowv1.CancelQueuedItemResponse{Acknowledged: true}, nil
}

func (f *registryFakeServer) DecideQueuedItem(
	_ context.Context, req *flowv1.DecideQueuedItemRequest,
) (*flowv1.DecideQueuedItemResponse, error) {
	f.mu.Lock()
	f.decides = append(f.decides, req)
	f.mu.Unlock()
	return &flowv1.DecideQueuedItemResponse{Acknowledged: true}, nil
}

// peerShardDeadFake is a QueuePeerServiceServer implementing only
// NotifyShardDead, so the death-notification transport (R-B6) is pinned.
type peerShardDeadFake struct {
	flowv1.UnimplementedQueuePeerServiceServer
	mu   sync.Mutex
	dead []string
}

func (f *peerShardDeadFake) NotifyShardDead(
	_ context.Context, req *flowv1.NotifyShardDeadRequest,
) (*flowv1.NotifyShardDeadResponse, error) {
	f.mu.Lock()
	f.dead = append(f.dead, req.GetShardId())
	f.mu.Unlock()
	return &flowv1.NotifyShardDeadResponse{Acknowledged: true}, nil
}

// newRegistryBufconn starts an in-memory gRPC server registering the registry
// fake and a NotifyShardDead-capable peer fake, returning a dialer + client.
func newRegistryBufconn(
	t *testing.T, registry flowv1.QueueRegistryServiceServer, peer flowv1.QueuePeerServiceServer,
) (flowv1.QueueRegistryServiceClient, flowv1.QueuePeerServiceClient) {
	t.Helper()
	lis := bufconn.Listen(meshBufSize)
	srv := grpc.NewServer()
	if registry != nil {
		flowv1.RegisterQueueRegistryServiceServer(srv, registry)
	}
	if peer != nil {
		flowv1.RegisterQueuePeerServiceServer(srv, peer)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.DialContext(context.TODO())
	}
	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return flowv1.NewQueueRegistryServiceClient(conn), flowv1.NewQueuePeerServiceClient(conn)
}

// newRegistryFake returns a registry fake whose HeartbeatQueue answers with a
// populated living-shard set, so the HeartbeatQueueResponse.shards wire
// round-trip (R-B3) can be pinned.
func newRegistryFake() *registryFakeServer {
	return &registryFakeServer{
		beatResp: &flowv1.HeartbeatQueueResponse{
			Acknowledged: true,
			Shards: []*flowv1.ShardRegistration{
				{ShardId: testShard0, ShardAddr: testAddr0, LastHeartbeatAt: testBeatTS, Phase: "active"},
				{ShardId: testShard1, ShardAddr: testAddr1, LastHeartbeatAt: testBeatTS, Phase: "active"},
			},
		},
	}
}

func TestQueueRegistryService_RegisterQueueContract(t *testing.T) {
	ctx := context.Background()
	registry := &registryFakeServer{}
	regClient, _ := newRegistryBufconn(t, registry, nil)

	resp, err := regClient.RegisterQueue(ctx, &flowv1.RegisterQueueRequest{
		QueueName: testQueueName, ShardId: testShard0, ShardAddr: testAddr0,
	})
	if err != nil {
		t.Fatalf("RegisterQueue: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("RegisterQueue not acknowledged")
	}
	registry.mu.Lock()
	got := registry.registers[0]
	registry.mu.Unlock()
	if got.GetQueueName() != testQueueName || got.GetShardId() != testShard0 || got.GetShardAddr() != testAddr0 {
		t.Fatalf("RegisterQueue fields lost on wire: %+v", got)
	}
}

func TestQueueRegistryService_HeartbeatContract(t *testing.T) {
	ctx := context.Background()
	registry := newRegistryFake()
	regClient, _ := newRegistryBufconn(t, registry, nil)

	resp, err := regClient.HeartbeatQueue(ctx, &flowv1.HeartbeatQueueRequest{
		QueueName: testQueueName, ShardId: testShard0, ShardAddr: testAddr0,
	})
	if err != nil {
		t.Fatalf("HeartbeatQueue: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("HeartbeatQueue not acknowledged")
	}

	// The response carries the live shard set; every field must survive.
	if len(resp.GetShards()) != 2 {
		t.Fatalf("expected 2 living shards, got %d", len(resp.GetShards()))
	}
	want := map[string]string{testShard0: testAddr0, testShard1: testAddr1}
	for _, s := range resp.GetShards() {
		if s.GetShardAddr() != want[s.GetShardId()] {
			t.Fatalf("shard %s addr %q lost, want %q", s.GetShardId(), s.GetShardAddr(), want[s.GetShardId()])
		}
		if s.GetLastHeartbeatAt() != testBeatTS || s.GetPhase() != "active" {
			t.Fatalf("shard %s lost lastHeartbeatAt/phase on wire: %+v", s.GetShardId(), s)
		}
	}
	registry.mu.Lock()
	got := registry.beats[0]
	registry.mu.Unlock()
	if got.GetQueueName() != testQueueName || got.GetShardId() != testShard0 {
		t.Fatalf("HeartbeatQueue fields lost on wire: %+v", got)
	}
}

func TestQueueRegistryService_DeregisterQueueContract(t *testing.T) {
	ctx := context.Background()
	registry := &registryFakeServer{}
	regClient, _ := newRegistryBufconn(t, registry, nil)

	resp, err := regClient.DeregisterQueue(ctx, &flowv1.DeregisterQueueRequest{
		QueueName: testQueueName, ShardId: testShard0,
	})
	if err != nil {
		t.Fatalf("DeregisterQueue: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("DeregisterQueue not acknowledged")
	}
	registry.mu.Lock()
	got := registry.dereg[0]
	registry.mu.Unlock()
	if got.GetQueueName() != testQueueName || got.GetShardId() != testShard0 {
		t.Fatalf("DeregisterQueue fields lost on wire: %+v", got)
	}
}

func TestQueueRegistryService_ListQueuesContract(t *testing.T) {
	ctx := context.Background()
	registry := &registryFakeServer{}
	regClient, _ := newRegistryBufconn(t, registry, nil)

	resp, err := regClient.ListQueues(ctx, &flowv1.ListQueuesRequest{})
	if err != nil {
		t.Fatalf("ListQueues: %v", err)
	}
	if len(resp.GetQueues()) != 1 {
		t.Fatalf("expected 1 queue, got %d", len(resp.GetQueues()))
	}
	q := resp.GetQueues()[0]
	if q.GetQueueName() != testQueueName || len(q.GetShards()) != 1 {
		t.Fatalf("ListQueues lost queue/shard fields: %+v", q)
	}
	if q.GetShards()[0].GetShardId() != testShard0 || q.GetShards()[0].GetShardAddr() != testAddr0 {
		t.Fatalf("ListQueues lost shard fields: %+v", q.GetShards()[0])
	}
}

func TestQueueRegistryService_CancelQueuedItemContract(t *testing.T) {
	ctx := context.Background()
	registry := &registryFakeServer{}
	regClient, _ := newRegistryBufconn(t, registry, nil)

	resp, err := regClient.CancelQueuedItem(ctx, &flowv1.CancelQueuedItemRequest{
		QueueName: testQueueName, WorkitemId: testWorkID,
	})
	if err != nil {
		t.Fatalf("CancelQueuedItem: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("CancelQueuedItem not acknowledged")
	}
	registry.mu.Lock()
	got := registry.cancels[0]
	registry.mu.Unlock()
	if got.GetQueueName() != testQueueName || got.GetWorkitemId() != testWorkID {
		t.Fatalf("CancelQueuedItem fields lost on wire: %+v", got)
	}
}

func TestQueueRegistryService_DecideQueuedItemContract(t *testing.T) {
	ctx := context.Background()
	registry := &registryFakeServer{}
	regClient, _ := newRegistryBufconn(t, registry, nil)

	resp, err := regClient.DecideQueuedItem(ctx, &flowv1.DecideQueuedItemRequest{
		QueueName: testQueueName, WorkitemId: testWorkID, Choice: testChoice,
	})
	if err != nil {
		t.Fatalf("DecideQueuedItem: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("DecideQueuedItem not acknowledged")
	}
	registry.mu.Lock()
	got := registry.decides[0]
	registry.mu.Unlock()
	if got.GetQueueName() != testQueueName || got.GetWorkitemId() != testWorkID || got.GetChoice() != testChoice {
		t.Fatalf("DecideQueuedItem fields lost on wire: %+v", got)
	}
}

func TestQueuePeerService_NotifyShardDeadContract(t *testing.T) {
	ctx := context.Background()
	peer := &peerShardDeadFake{}
	_, peerClient := newRegistryBufconn(t, nil, peer)

	resp, err := peerClient.NotifyShardDead(ctx, &flowv1.NotifyShardDeadRequest{ShardId: testDeadShard})
	if err != nil {
		t.Fatalf("NotifyShardDead: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("NotifyShardDead not acknowledged")
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if len(peer.dead) != 1 || peer.dead[0] != testDeadShard {
		t.Fatalf("NotifyShardDead shard_id lost on wire: %v", peer.dead)
	}
}
