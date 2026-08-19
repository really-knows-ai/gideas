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
	testReplWorkID = "wi-7"
	testGeneration = "gen-abc-123"
	testEnqueuedAt = "2026-08-19T00:00:00Z"
)

// replicationFakeServer implements only the replication RPCs on
// QueuePeerService so the single-backup replication contract (R-C1..C5) is
// pinned: a full QueueItem (incl. generation_id) arrives intact on
// ReplicateItem, and workitem_id + generation_id arrive intact on DropItem.
type replicationFakeServer struct {
	flowv1.UnimplementedQueuePeerServiceServer

	mu      sync.Mutex
	replics []*flowv1.QueueItem
	drops   []*flowv1.DropItemRequest
}

func (f *replicationFakeServer) ReplicateItem(
	_ context.Context, req *flowv1.ReplicateItemRequest,
) (*flowv1.ReplicateItemResponse, error) {
	f.mu.Lock()
	f.replics = append(f.replics, req.GetItem())
	f.mu.Unlock()
	return &flowv1.ReplicateItemResponse{Acknowledged: true}, nil
}

func (f *replicationFakeServer) DropItem(
	_ context.Context, req *flowv1.DropItemRequest,
) (*flowv1.DropItemResponse, error) {
	f.mu.Lock()
	f.drops = append(f.drops, req)
	f.mu.Unlock()
	return &flowv1.DropItemResponse{Acknowledged: true}, nil
}

func newReplicationClient(t *testing.T, srv flowv1.QueuePeerServiceServer) flowv1.QueuePeerServiceClient {
	t.Helper()
	lis := bufconn.Listen(meshBufSize)
	grpcSrv := grpc.NewServer()
	flowv1.RegisterQueuePeerServiceServer(grpcSrv, srv)
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(grpcSrv.GracefulStop)

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
	return flowv1.NewQueuePeerServiceClient(conn)
}

func TestQueuePeerService_ReplicateItemContract(t *testing.T) {
	ctx := context.Background()
	fake := &replicationFakeServer{}
	client := newReplicationClient(t, fake)

	resp, err := client.ReplicateItem(ctx, &flowv1.ReplicateItemRequest{
		Item: &flowv1.QueueItem{
			WorkitemId:   testReplWorkID,
			ShardId:      testShard0,
			QueueName:    testQueueName,
			Status:       "waiting",
			EnqueuedAt:   testEnqueuedAt,
			GenerationId: testGeneration,
		},
	})
	if err != nil {
		t.Fatalf("ReplicateItem: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("ReplicateItem not acknowledged")
	}
	fake.mu.Lock()
	got := fake.replics[0]
	fake.mu.Unlock()
	if got.GetWorkitemId() != testReplWorkID || got.GetShardId() != testShard0 ||
		got.GetQueueName() != testQueueName || got.GetStatus() != "waiting" ||
		got.GetEnqueuedAt() != testEnqueuedAt || got.GetGenerationId() != testGeneration {
		t.Fatalf("ReplicateItem lost QueueItem fields on wire: %+v", got)
	}
}

func TestQueuePeerService_DropItemContract(t *testing.T) {
	ctx := context.Background()
	fake := &replicationFakeServer{}
	client := newReplicationClient(t, fake)

	resp, err := client.DropItem(ctx, &flowv1.DropItemRequest{
		WorkitemId: testReplWorkID, GenerationId: testGeneration,
	})
	if err != nil {
		t.Fatalf("DropItem: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("DropItem not acknowledged")
	}
	fake.mu.Lock()
	drop := fake.drops[0]
	fake.mu.Unlock()
	if drop.GetWorkitemId() != testReplWorkID || drop.GetGenerationId() != testGeneration {
		t.Fatalf("DropItem lost fields on wire: %+v", drop)
	}
}
