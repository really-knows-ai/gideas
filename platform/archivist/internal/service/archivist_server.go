// Package service implements the Archivist gRPC server.
//
// The Archivist is the "Memory" of the Flow system. It separates Content
// (raw bytes, deduplicated by SHA-256 hash) from Provenance (version history
// keyed by workitem + artefact). This Content-Addressable Storage (CAS)
// architecture ensures that identical payloads are stored once, while each
// artefact maintains its own ordered version history.
//
// The gRPC method handlers are split across cohesive files by sub-domain:
// artefacts.go (CAS artefact handlers), stamps.go (stamp handlers),
// feedback.go (the feedback workflow), capability.go (capability enforcement),
// and conversions.go (store-to-proto conversions). This file holds the server
// type, construction, and the cross-cutting helpers shared by those files
// (validateChildAccess, publishAudit, extractNodeID).
package service

import (
	"context"
	"log/slog"

	"github.com/foundry/flow/archivist/internal/store/sqlite"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	"github.com/foundry/flow/pkg/randid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AuditPublisher provides non-blocking audit event submission to the Event Bus.
// Satisfied by *eventbus.AsyncPublisher. A nil publisher silently disables
// audit publishing.
type AuditPublisher interface {
	Submit(req *flowv1.PublishRequest)
}

// ArchivistServer implements flowv1.ArchivistServiceServer backed by
// a SQLite CAS store.
type ArchivistServer struct {
	flowv1.UnimplementedArchivistServiceServer
	store          *sqlite.Store
	auditor        AuditPublisher               // nil-safe: audit publishing degrades gracefully
	operatorClient flowv1.OperatorServiceClient // nil-safe: cross-Workitem reads disabled when unset
	newIDFn        func() string
	namespaceFn    func(ctx context.Context) string
}

// NewArchivistServer returns an ArchivistServer backed by the given store.
// The auditor may be nil; audit publishing will be silently disabled.
func NewArchivistServer(s *sqlite.Store, opts ...ArchivistOption) *ArchivistServer {
	srv := &ArchivistServer{
		store:   s,
		newIDFn: randid.NewRandomID,
		namespaceFn: func(ctx context.Context) string {
			return flowmeta.MetadataValue(ctx, flowmeta.MetadataKeyNamespace)
		},
	}
	for _, o := range opts {
		o(srv)
	}
	return srv
}

// ArchivistOption configures an ArchivistServer.
type ArchivistOption func(*ArchivistServer)

// WithAuditPublisher sets the Event Bus client for audit event publishing.
func WithAuditPublisher(pub AuditPublisher) ArchivistOption {
	return func(s *ArchivistServer) { s.auditor = pub }
}

// WithOperatorClient sets the Operator gRPC client for parent-child validation
// on cross-Workitem reads. When unset, cross-Workitem reads are disabled.
func WithOperatorClient(client flowv1.OperatorServiceClient) ArchivistOption {
	return func(s *ArchivistServer) { s.operatorClient = client }
}

// validateChildAccess calls the Operator's ValidateChildAccess RPC to verify
// that parentWorkitemID is the parent of childWorkitemID and that the child is
// in Completed state. Returns nil on success.
//
// Error cases:
//   - PermissionDenied (CHILD_NOT_OWNED): parent mismatch
//   - FailedPrecondition: child not completed
//   - Internal: Operator unreachable (fail-closed)
//   - Unavailable: Operator client not configured
func (s *ArchivistServer) validateChildAccess(ctx context.Context, parentWorkitemID, childWorkitemID string) error {
	if s.operatorClient == nil {
		return status.Error(codes.Unavailable,
			"cross-Workitem reads not available: Operator client not configured")
	}

	// Propagate namespace metadata to outgoing Operator call.
	ns := flowmeta.MetadataValue(ctx, flowmeta.MetadataKeyNamespace)
	if ns != "" {
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-flow-namespace", ns))
	}

	resp, err := s.operatorClient.ValidateChildAccess(ctx, &flowv1.ValidateChildAccessRequest{
		ParentWorkitemId: parentWorkitemID,
		ChildWorkitemId:  childWorkitemID,
	})
	if err != nil {
		// Fail closed: deny access when the Operator is unreachable.
		slog.Error("ValidateChildAccess RPC failed, denying cross-Workitem read",
			"parent_workitem_id", parentWorkitemID,
			"child_workitem_id", childWorkitemID,
			"error", err,
		)
		return status.Errorf(codes.Internal,
			"failed to validate cross-Workitem access (fail-closed): %v", err)
	}

	if !resp.GetValid() {
		// Determine the specific reason for the rejection.
		if resp.GetPhase() == "" {
			// Phase is empty — child was not found (shouldn't happen as Operator
			// returns NotFound, but handle defensively).
			return status.Errorf(codes.PermissionDenied,
				"CHILD_NOT_OWNED: cross-Workitem read denied for child %q", childWorkitemID)
		}
		if resp.GetPhase() != "Completed" {
			return status.Errorf(codes.FailedPrecondition,
				"child workitem %q is in phase %q, must be Completed for cross-Workitem read",
				childWorkitemID, resp.GetPhase())
		}
		// Phase is Completed but valid=false — parent mismatch.
		return status.Errorf(codes.PermissionDenied,
			"CHILD_NOT_OWNED: cross-Workitem read denied for child %q", childWorkitemID)
	}

	return nil
}

// publishAudit submits an audit event to the async publisher for non-blocking
// delivery to the Event Bus. If the publisher is nil, audit publishing is
// silently disabled.
func (s *ArchivistServer) publishAudit(ctx context.Context, eventType string, attrs map[string]string) {
	if s.auditor == nil {
		return
	}
	s.auditor.Submit(&flowv1.PublishRequest{
		Channel: "audit",
		Event: &flowv1.FlowEvent{
			EventId:       s.newIDFn(),
			EventType:     eventType,
			FlowNamespace: s.namespaceFn(ctx),
			NodeId:        extractNodeID(ctx),
			WorkitemId:    flowmeta.MetadataValue(ctx, flowmeta.MetadataKeyWorkitemID),
			Timestamp:     timestamppb.Now(),
			Attributes:    attrs,
		},
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractNodeID reads the x-flow-node-id value from incoming gRPC metadata,
// delegating to the shared flowmeta.MetadataValue lookup — the single source
// of truth for Sidecar-injected x-flow-* identity values.
func extractNodeID(ctx context.Context) string {
	return flowmeta.MetadataValue(ctx, flowmeta.MetadataKeyNodeID)
}
