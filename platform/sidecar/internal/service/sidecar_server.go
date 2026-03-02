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
	"strings"
	"sync"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/sidecar/internal/buffer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	// DefaultNodeAddress is the default gRPC endpoint of the User Code
	// container running the SDK server (NodeService).
	DefaultNodeAddress = "localhost:50053"
)

// SidecarServer implements the flowv1.SidecarServiceServer interface.
// It manages per-Workitem assignment sessions with inactivity timers
// and handles Heartbeat, PauseTimer, ResumeTimer (node-facing),
// AssignWork (operator-facing), and AddFriction/RecordTelemetry
// (telemetry ingestion via Event Bus).
type SidecarServer struct {
	flowv1.UnimplementedSidecarServiceServer

	// Namespace is the Kubernetes namespace this Sidecar runs in.
	// Set from the FLOW_NAMESPACE environment variable. This is the
	// flow identity boundary — one namespace = one FoundryFlow.
	Namespace string

	// NodeID is the identity of the node this Sidecar is attached to.
	NodeID string

	// NodeAddress is the gRPC address of the co-located User Code container.
	NodeAddress string

	// Timeout is the inactivity timeout for assignments. Falls back to
	// DefaultTimeout if zero.
	Timeout time.Duration

	// TelemetryBuffer is the async priority buffer for telemetry events.
	// If nil, AddFriction and RecordTelemetry return errors.
	TelemetryBuffer *buffer.TelemetryBuffer

	// sessions tracks active Workitem assignments by workitem_id.
	mu       sync.Mutex
	sessions map[string]*session

	// nodeConn is the lazy-initialized gRPC connection to the User Code.
	nodeConn   *grpc.ClientConn
	nodeClient flowv1.NodeServiceClient
}

// NewSidecarServer creates a SidecarServer with the given namespace,
// node identity, and User Code address.
func NewSidecarServer(namespace, nodeID, nodeAddress string) *SidecarServer {
	if nodeAddress == "" {
		nodeAddress = DefaultNodeAddress
	}
	return &SidecarServer{
		Namespace:   namespace,
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
// injection interceptor. The namespace is not included here — it is a
// Sidecar-level constant, not per-session.
type SessionIdentity struct {
	WorkitemID string
	NodeID     string
}

// ChildTracker records child Workitem IDs created during a session.
// The OperatorProxy calls TrackChild after a successful CreateChildWorkitem
// to register the child in the session's local cache.
type ChildTracker interface {
	TrackChild(parentWorkitemID, childWorkitemID string)
}

// ChildAccessDecision represents the outcome of a cross-Workitem
// authorisation check by the Sidecar.
type ChildAccessDecision int

const (
	// ChildAccessAllowed means the target is a known child of the session.
	ChildAccessAllowed ChildAccessDecision = iota
	// ChildAccessDenied means the session has children but the target is
	// not one of them, so the access should be blocked.
	ChildAccessDenied
	// ChildAccessUnknown means the session has no children (e.g. collection
	// phase at a different node) so the Sidecar cannot make a determination.
	// The Archivist should perform its own validation.
	ChildAccessUnknown
)

// ChildAuthorizer validates whether a target Workitem ID is a known child
// of the given parent session. Used by the ArchivistProxy to authorise
// cross-Workitem operations without an Operator round-trip.
type ChildAuthorizer interface {
	// AuthorizeChildAccess checks if the target is an authorised child of
	// the parent session.
	//
	// Returns:
	//   - ChildAccessAllowed: target is a known child, access permitted
	//   - ChildAccessDenied: session has children but target is not one of
	//     them, access should be blocked
	//   - ChildAccessUnknown: no session found or session has no children,
	//     the Sidecar cannot make a determination (defer to Archivist)
	AuthorizeChildAccess(parentWorkitemID, targetWorkitemID string) ChildAccessDecision
}

// TrackChild records a child Workitem ID in the session for the given
// parent. No-op if no session exists for the parent.
func (s *SidecarServer) TrackChild(parentWorkitemID, childWorkitemID string) {
	sess := s.getSession(parentWorkitemID)
	if sess == nil {
		return
	}
	sess.addChild(childWorkitemID)
}

// AuthorizeChildAccess implements ChildAuthorizer. It checks the session's
// local child cache to determine if the target is an authorised child.
func (s *SidecarServer) AuthorizeChildAccess(parentWorkitemID, targetWorkitemID string) ChildAccessDecision {
	sess := s.getSession(parentWorkitemID)
	if sess == nil {
		return ChildAccessUnknown
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.childIDs) == 0 {
		// Session has no children -- cannot make a determination.
		return ChildAccessUnknown
	}
	if _, ok := sess.childIDs[targetWorkitemID]; ok {
		return ChildAccessAllowed
	}
	return ChildAccessDenied
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
	nodeID := wctx.GetNodeId()

	slog.Info("Received assignment from Operator",
		"namespace", s.Namespace,
		"workitem_id", workitemID,
		"node_id", nodeID,
	)

	// Create an assignment session with an inactivity timer.
	sess, sessionCtx := newSession(ctx, workitemID, nodeID, s.timeout())

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

// ---------------------------------------------------------------------------
// Telemetry Ingestion — AddFriction and RecordTelemetry
// ---------------------------------------------------------------------------

// AddFriction enforces the WRITE:friction capability gate, injects
// Sidecar-authoritative identity, and submits the friction event to the
// async telemetry buffer with HIGH priority for delivery to the Event Bus.
//
// Per spec (specs/03-node/02-configuration.md), WRITE:friction is the one
// capability enforced by the Sidecar rather than the owning service.
func (s *SidecarServer) AddFriction(
	ctx context.Context, req *flowv1.AddFrictionRequest,
) (*flowv1.AddFrictionResponse, error) {
	if s.TelemetryBuffer == nil {
		return nil, status.Error(codes.Unavailable,
			"telemetry buffer not configured — Event Bus not available")
	}

	// WRITE:friction is Sidecar-enforced (spec exception).
	if err := checkCapability(ctx, "WRITE:friction"); err != nil {
		return nil, err
	}

	_, workitemID, nodeID := extractIdentityFromMD(ctx)

	slog.Info("AddFriction: submitting to telemetry buffer",
		"namespace", s.Namespace,
		"workitem_id", workitemID,
		"node_id", nodeID,
		"magnitude", req.GetMagnitude(),
	)

	s.TelemetryBuffer.Submit(buffer.Event{
		Priority:   buffer.PriorityHigh,
		Namespace:  s.Namespace,
		WorkitemID: workitemID,
		NodeID:     nodeID,
		LawIDs:     req.GetLawIds(),
		Magnitude:  float64(req.GetMagnitude()),
	})

	return &flowv1.AddFrictionResponse{Acknowledged: true}, nil
}

// RecordTelemetry injects Sidecar-authoritative identity and submits the
// telemetry event to the async buffer with NORMAL priority for delivery
// to the Event Bus.
func (s *SidecarServer) RecordTelemetry(
	ctx context.Context, req *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	if s.TelemetryBuffer == nil {
		return nil, status.Error(codes.Unavailable,
			"telemetry buffer not configured — Event Bus not available")
	}

	_, workitemID, nodeID := extractIdentityFromMD(ctx)

	slog.Info("RecordTelemetry: submitting to telemetry buffer",
		"namespace", s.Namespace,
		"workitem_id", workitemID,
		"node_id", nodeID,
		"event_type", req.GetEventType(),
	)

	s.TelemetryBuffer.Submit(buffer.Event{
		Priority:   buffer.PriorityNormal,
		Namespace:  s.Namespace,
		WorkitemID: workitemID,
		NodeID:     nodeID,
		EventType:  req.GetEventType(),
		Payload:    req.GetPayload(),
	})

	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// checkCapability is the Sidecar-side capability gate for WRITE:friction.
// It reads x-flow-capabilities and x-flow-node-id from incoming gRPC metadata.
// If x-flow-node-id is absent (system-to-system call), the check passes.
// If x-flow-node-id is present (node-originated), the required capability
// must be present in x-flow-capabilities or the request is denied.
func checkCapability(ctx context.Context, required string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil // No metadata — system call.
	}
	nodeIDs := md.Get("x-flow-node-id")
	if len(nodeIDs) == 0 {
		return nil // No node identity — system call.
	}

	caps := md.Get("x-flow-capabilities")
	for _, c := range caps {
		for cap := range strings.SplitSeq(c, ",") {
			if strings.TrimSpace(cap) == required {
				return nil
			}
		}
	}

	return status.Errorf(codes.PermissionDenied,
		"CAPABILITY_DENIED: missing required capability %q", required)
}

// extractIdentityFromMD extracts Sidecar-injected identity from incoming
// gRPC metadata. The namespace field reads x-flow-namespace; workitem_id
// and node_id are unchanged.
//
//nolint:unparam // namespace reserved for future callers
func extractIdentityFromMD(ctx context.Context) (namespace, workitemID, nodeID string) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return
	}
	if vals := md.Get("x-flow-namespace"); len(vals) > 0 {
		namespace = vals[0]
	}
	if vals := md.Get("x-flow-workitem-id"); len(vals) > 0 {
		workitemID = vals[0]
	}
	if vals := md.Get("x-flow-node-id"); len(vals) > 0 {
		nodeID = vals[0]
	}
	return
}
