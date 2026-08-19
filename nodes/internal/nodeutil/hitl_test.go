package nodeutil

import (
	"context"
	"net"
	"sync"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flow "github.com/foundry/flow/sdk/go"
	"google.golang.org/grpc"
)

// topologySpy is a minimal local gRPC spy serving only GetFlowTopology, the
// one operator call DiscoverStamp makes.
type topologySpy struct {
	flowv1.UnimplementedOperatorServiceServer
	mu       sync.Mutex
	Topology *flowv1.GetFlowTopologyResponse
}

func (s *topologySpy) GetFlowTopology(
	_ context.Context, _ *flowv1.GetFlowTopologyRequest,
) (*flowv1.GetFlowTopologyResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Topology, nil
}

// newSpyClient creates a flow.Client backed by a local gRPC server with the
// topologySpy registered for the OperatorService.
func newSpyClient(t *testing.T, spy *topologySpy) *flow.Client {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	srv := grpc.NewServer()
	flowv1.RegisterOperatorServiceServer(srv, spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	client, err := flow.NewClient(flow.WithSidecarAddress(lis.Addr().String()))
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestDiscoverStamp(t *testing.T) {
	tests := []struct {
		name         string
		capabilities []string
		wantKind     string
		wantStamp    string
		wantErr      bool
	}{
		{
			name:         "single stamp capability",
			capabilities: []string{"READ:flow", "STAMP:artefact/haiku/review"},
			wantKind:     "haiku",
			wantStamp:    "review",
		},
		{
			name:         "multiple stamps uses first",
			capabilities: []string{"STAMP:artefact/haiku/review", "STAMP:artefact/doc/linter"},
			wantKind:     "haiku",
			wantStamp:    "review",
		},
		{
			name:         "no stamp capability",
			capabilities: []string{"READ:flow", "WRITE:feedback/new"},
			wantErr:      true,
		},
		{
			name:         "empty capabilities",
			capabilities: nil,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &topologySpy{
				Topology: &flowv1.GetFlowTopologyResponse{
					Self: &flowv1.FlowNode{
						Name:         "test-node",
						Capabilities: tt.capabilities,
					},
				},
			}
			client := newSpyClient(t, spy)

			kind, stamp, err := DiscoverStamp(context.Background(), client)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if kind != tt.wantKind {
				t.Errorf("kind=%q, want %q", kind, tt.wantKind)
			}
			if stamp != tt.wantStamp {
				t.Errorf("stamp=%q, want %q", stamp, tt.wantStamp)
			}
		})
	}
}
