package service

import (
	"context"
	"net"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// newItemGRPCHarness seeds a CR with a living shard fake and returns a
// QueueRegistryServiceClient connected to a bufconn server backed by the
// registry.
func newItemGRPCHarness(t *testing.T) (flowv1.QueueRegistryServiceClient, *fakePeerShard) {
	now := time.Now().UTC()
	owner := newFakePeerShard(t)
	owner.setItems(&flowv1.QueueItem{WorkitemId: "wi-1", Status: "waiting"})
	c := newFakeClient(t, queueCR("hitl-approval",
		shard("owner-id", "owner-addr", phaseActive, now),
	))
	r := NewRegistry(c, time.Minute, time.Second)
	r.Namespace = testNamespace
	r.peerDialer = func(ctx context.Context, addr string) (*grpc.ClientConn, error) {
		if addr != "owner-addr" {
			return nil, errShardUnavailable
		}
		return owner.dialer(ctx, addr)
	}

	lis := bufconn.Listen(meshBufSize)
	srv := grpc.NewServer()
	flowv1.RegisterQueueRegistryServiceServer(srv, r)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///item-grpc",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return flowv1.NewQueueRegistryServiceClient(conn), owner
}

func TestItemGRPC_CancelQueuedItem_RoutesToLivingShard(t *testing.T) {
	client, owner := newItemGRPCHarness(t)

	resp, err := client.CancelQueuedItem(context.Background(), &flowv1.CancelQueuedItemRequest{
		QueueName: "hitl-approval", WorkitemId: "wi-1",
	})
	if err != nil {
		t.Fatalf("CancelQueuedItem: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("cancel not acknowledged")
	}
	// Cancel is delivered as DecideItem with an EMPTY choice to the living
	// owner.
	decided := owner.decidedCalls()
	if len(decided) != 1 {
		t.Fatalf("expected 1 DecideItem, got %d", len(decided))
	}
	if decided[0].GetWorkitemId() != "wi-1" || decided[0].GetChoice() != "" {
		t.Fatalf("cancel decide = %+v, want wi-1 with empty choice", decided[0])
	}
}

func TestItemGRPC_DecideQueuedItem_RoutesToLivingShard(t *testing.T) {
	client, owner := newItemGRPCHarness(t)

	resp, err := client.DecideQueuedItem(context.Background(), &flowv1.DecideQueuedItemRequest{
		QueueName: "hitl-approval", WorkitemId: "wi-1", Choice: "approve",
	})
	if err != nil {
		t.Fatalf("DecideQueuedItem: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("decide not acknowledged")
	}
	decided := owner.decidedCalls()
	if len(decided) != 1 || decided[0].GetChoice() != "approve" {
		t.Fatalf("decide = %+v, want choice approve", decided)
	}
}

func TestItemGRPC_UnknownQueueItem_NotFound(t *testing.T) {
	client, _ := newItemGRPCHarness(t) // owner is living but does not hold wi-unknown

	_, err := client.DecideQueuedItem(context.Background(), &flowv1.DecideQueuedItemRequest{
		QueueName: "hitl-approval", WorkitemId: "wi-unknown", Choice: "",
	})
	if err == nil {
		t.Fatal("expected error for unknown item")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}
