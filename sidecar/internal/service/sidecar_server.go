// Package service implements the Sidecar's gRPC service handlers.
//
// The SidecarServer handles both node-facing RPCs (Heartbeat) and
// operator-facing RPCs (AssignWork). When AssignWork is called by the
// Operator, the Sidecar forwards the assignment to the co-located User
// Code container via the NodeService.Process RPC over localhost.
package service

import (
	"context"
	"fmt"
	"log/slog"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const (
	// DefaultNodeAddress is the default gRPC endpoint of the User Code
	// container running the SDK server (NodeService).
	DefaultNodeAddress = "localhost:50053"
)

// SidecarServer implements the flowv1.SidecarServiceServer interface.
// It handles Heartbeat (node-facing) and AssignWork (operator-facing).
type SidecarServer struct {
	flowv1.UnimplementedSidecarServiceServer

	// NodeID is the identity of the node this Sidecar is attached to.
	NodeID string

	// NodeAddress is the gRPC address of the co-located User Code container.
	NodeAddress string

	// nodeConn is the lazy-initialized gRPC connection to the User Code.
	nodeConn   *grpc.ClientConn
	nodeClient flowv1.NodeServiceClient
}

// NewSidecarServer creates a SidecarServer with the given node identity
// and User Code address.
func NewSidecarServer(nodeID, nodeAddress string) *SidecarServer {
	if nodeAddress == "" {
		nodeAddress = DefaultNodeAddress
	}
	return &SidecarServer{
		NodeID:      nodeID,
		NodeAddress: nodeAddress,
	}
}

// Close releases the gRPC connection to the User Code container.
func (s *SidecarServer) Close() error {
	if s.nodeConn != nil {
		return s.nodeConn.Close()
	}
	return nil
}

// Heartbeat resets the Sidecar's inactivity timer.
func (s *SidecarServer) Heartbeat(_ context.Context, req *flowv1.HeartbeatRequest) (*flowv1.HeartbeatResponse, error) {
	slog.Info("Heartbeat received",
		"node_id", s.NodeID,
		"workitem_id", req.GetWorkitemId(),
	)
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

// AssignWork is called by the Operator to push a work assignment to this
// Sidecar. The Sidecar validates the request and forwards it to the
// co-located User Code container via NodeService.Process.
func (s *SidecarServer) AssignWork(ctx context.Context, req *flowv1.AssignWorkRequest) (*flowv1.Ack, error) {
	wctx := req.GetContext()
	if wctx == nil {
		return nil, status.Error(codes.InvalidArgument, "assign_work: missing workitem context")
	}

	slog.Info("Received assignment from Operator",
		"flow_id", wctx.GetFlowId(),
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	// Lazily connect to the User Code container.
	if err := s.ensureNodeConnection(); err != nil {
		slog.Error("Failed to connect to User Code", "address", s.NodeAddress, "error", err)
		return nil, status.Error(codes.Unavailable,
			fmt.Sprintf("failed to connect to user code at %s: %v", s.NodeAddress, err))
	}

	// Forward to the User Code via NodeService.Process.
	slog.Info("Forwarding assignment to User Code",
		"address", s.NodeAddress,
		"workitem_id", wctx.GetWorkitemId(),
	)

	ack, err := s.nodeClient.Process(ctx, req)
	if err != nil {
		slog.Error("User Code Process call failed",
			"workitem_id", wctx.GetWorkitemId(),
			"error", err,
		)
		return nil, status.Error(codes.Internal, fmt.Sprintf("user code process failed: %v", err))
	}

	slog.Info("User Code processing complete",
		"workitem_id", wctx.GetWorkitemId(),
		"accepted", ack.GetAccepted(),
		"message", ack.GetMessage(),
	)

	return ack, nil
}

// ensureNodeConnection lazily initializes the gRPC connection to the
// User Code container.
func (s *SidecarServer) ensureNodeConnection() error {
	if s.nodeClient != nil {
		return nil
	}

	conn, err := grpc.NewClient(
		s.NodeAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("dial user code: %w", err)
	}

	s.nodeConn = conn
	s.nodeClient = flowv1.NewNodeServiceClient(conn)
	return nil
}
