// Package service implements the Sidecar's gRPC service handlers.
//
// The SidecarServer handles both node-facing RPCs (Heartbeat, PauseTimer,
// ResumeTimer) and operator-facing RPCs (AssignWork). When AssignWork is
// called by the Operator, the Sidecar creates an assignment session with
// an inactivity timer and forwards the assignment to the co-located User
// Code container via the NodeService.Process RPC.
//
// Each active Workitem assignment maintains an independent session with
// its own inactivity timer and pause state. The timer measures idle time,
// not total execution time. See: specs/03-node/01-sidecar.md
package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

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
// It manages per-Workitem assignment sessions with inactivity timers
// and handles Heartbeat, PauseTimer, ResumeTimer (node-facing) and
// AssignWork (operator-facing).
type SidecarServer struct {
	flowv1.UnimplementedSidecarServiceServer

	// NodeID is the identity of the node this Sidecar is attached to.
	NodeID string

	// NodeAddress is the gRPC address of the co-located User Code container.
	NodeAddress string

	// Timeout is the inactivity timeout for assignments. Falls back to
	// DefaultTimeout if zero.
	Timeout time.Duration

	// sessions tracks active Workitem assignments by workitem_id.
	mu       sync.Mutex
	sessions map[string]*session

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
		sessions:    make(map[string]*session),
	}
}

// Close releases the gRPC connection to the User Code container.
func (s *SidecarServer) Close() error {
	if s.nodeConn != nil {
		return s.nodeConn.Close()
	}
	return nil
}

// timeout returns the configured inactivity timeout, falling back to
// DefaultTimeout.
func (s *SidecarServer) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return DefaultTimeout
}

// getSession returns the active session for a workitem_id, or nil.
func (s *SidecarServer) getSession(workitemID string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[workitemID]
}

// SessionIdentity holds the authoritative identity fields for an active
// Workitem assignment. Returned by LookupSession for use by the identity
// injection interceptor.
type SessionIdentity struct {
	FlowID     string
	WorkitemID string
	NodeID     string
}

// LookupSession returns the authoritative identity context for an active
// Workitem assignment. Returns nil if no session exists for the given
// workitem_id. This is used by the Sidecar's gRPC interceptor to inject
// identity metadata on every proxied request.
func (s *SidecarServer) LookupSession(workitemID string) *SessionIdentity {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[workitemID]
	if !ok {
		return nil
	}
	return &SessionIdentity{
		FlowID:     sess.flowID,
		WorkitemID: sess.workitemID,
		NodeID:     sess.nodeID,
	}
}

// Heartbeat resets the Sidecar's inactivity timer for the specified
// Workitem assignment. If no active session exists for the workitem_id,
// the heartbeat is acknowledged but has no timer effect.
func (s *SidecarServer) Heartbeat(_ context.Context, req *flowv1.HeartbeatRequest) (*flowv1.HeartbeatResponse, error) {
	workitemID := req.GetWorkitemId()
	slog.Info("Heartbeat received",
		"node_id", s.NodeID,
		"workitem_id", workitemID,
	)

	if sess := s.getSession(workitemID); sess != nil {
		sess.resetTimer()
	}

	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

// PauseTimer suspends the Sidecar's inactivity timer for the specified
// Workitem assignment. The timer remains suspended until ResumeTimer is
// called or the handler returns. Used by HITL nodes to park Workitems
// while awaiting external input without triggering timeout.
//
// See: specs/03-node/01-sidecar.md#heartbeat-and-activity-tracking
func (s *SidecarServer) PauseTimer(
	_ context.Context, req *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	workitemID := req.GetWorkitemId()

	sess := s.getSession(workitemID)
	if sess == nil {
		return nil, status.Errorf(codes.NotFound,
			"pause_timer: no active session for workitem %q", workitemID)
	}

	if !sess.pause() {
		// Already paused or timed out.
		if sess.isTimedOut() {
			return nil, status.Errorf(codes.FailedPrecondition,
				"pause_timer: workitem %q has already timed out", workitemID)
		}
		return nil, status.Errorf(codes.FailedPrecondition,
			"pause_timer: timer for workitem %q is already paused", workitemID)
	}

	slog.Info("Timer paused",
		"node_id", s.NodeID,
		"workitem_id", workitemID,
	)

	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

// ResumeTimer resumes the Sidecar's inactivity timer after a PauseTimer
// call. The timer resets to the full timeout window on resume.
//
// See: specs/03-node/01-sidecar.md#heartbeat-and-activity-tracking
func (s *SidecarServer) ResumeTimer(
	_ context.Context, req *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	workitemID := req.GetWorkitemId()

	sess := s.getSession(workitemID)
	if sess == nil {
		return nil, status.Errorf(codes.NotFound,
			"resume_timer: no active session for workitem %q", workitemID)
	}

	if !sess.resume() {
		if sess.isTimedOut() {
			return nil, status.Errorf(codes.FailedPrecondition,
				"resume_timer: workitem %q has already timed out", workitemID)
		}
		return nil, status.Errorf(codes.FailedPrecondition,
			"resume_timer: timer for workitem %q is not paused", workitemID)
	}

	slog.Info("Timer resumed",
		"node_id", s.NodeID,
		"workitem_id", workitemID,
	)

	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

// AssignWork is called by the Operator to push a work assignment to this
// Sidecar. The Sidecar creates an assignment session with an inactivity
// timer and forwards the assignment to the co-located User Code container
// via NodeService.Process. The session is removed when the handler
// completes or times out.
func (s *SidecarServer) AssignWork(ctx context.Context, req *flowv1.AssignWorkRequest) (*flowv1.Ack, error) {
	wctx := req.GetContext()
	if wctx == nil {
		return nil, status.Error(codes.InvalidArgument, "assign_work: missing workitem context")
	}

	workitemID := wctx.GetWorkitemId()
	flowID := wctx.GetFlowId()
	nodeID := wctx.GetNodeId()

	slog.Info("Received assignment from Operator",
		"flow_id", flowID,
		"workitem_id", workitemID,
		"node_id", nodeID,
	)

	// Create an assignment session with an inactivity timer.
	sess, sessionCtx := newSession(ctx, flowID, workitemID, nodeID, s.timeout())

	s.mu.Lock()
	s.sessions[workitemID] = sess
	s.mu.Unlock()

	// Ensure session cleanup on completion.
	defer func() {
		sess.stop()
		s.mu.Lock()
		delete(s.sessions, workitemID)
		s.mu.Unlock()
	}()

	// Lazily connect to the User Code container.
	if err := s.ensureNodeConnection(); err != nil {
		slog.Error("Failed to connect to User Code", "address", s.NodeAddress, "error", err)
		return nil, status.Error(codes.Unavailable,
			fmt.Sprintf("failed to connect to user code at %s: %v", s.NodeAddress, err))
	}

	// Forward to the User Code via NodeService.Process using the session
	// context. If the inactivity timer fires, sessionCtx is cancelled.
	slog.Info("Forwarding assignment to User Code",
		"address", s.NodeAddress,
		"workitem_id", workitemID,
	)

	ack, err := s.nodeClient.Process(sessionCtx, req)
	if err != nil {
		// Distinguish timeout cancellation from other errors.
		if sess.isTimedOut() {
			slog.Warn("Workitem timed out due to inactivity",
				"workitem_id", workitemID,
				"timeout", s.timeout(),
			)
			return nil, status.Errorf(codes.DeadlineExceeded,
				"workitem %q timed out after %s of inactivity", workitemID, s.timeout())
		}

		slog.Error("User Code Process call failed",
			"workitem_id", workitemID,
			"error", err,
		)
		return nil, status.Error(codes.Internal, fmt.Sprintf("user code process failed: %v", err))
	}

	slog.Info("User Code processing complete",
		"workitem_id", workitemID,
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
