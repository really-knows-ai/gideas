// Package service implements the Archivist gRPC server.
//
// The Archivist is the "Memory" of the Flow system. It separates Content
// (raw bytes, deduplicated by SHA-256 hash) from Provenance (version history
// keyed by workitem + artefact). This Content-Addressable Storage (CAS)
// architecture ensures that identical payloads are stored once, while each
// artefact maintains its own ordered version history.
package service

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"

	"github.com/gideas/flow/archivist/internal/store/sqlite"
	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/pkg/randid"
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
			return extractMetadataValue(ctx, "x-flow-namespace")
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
	ns := extractMetadataValue(ctx, "x-flow-namespace")
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
			WorkitemId:    extractMetadataValue(ctx, "x-flow-workitem-id"),
			Timestamp:     timestamppb.Now(),
			Attributes:    attrs,
		},
	})
}

// extractMetadataValue reads a single value from incoming gRPC metadata.
func extractMetadataValue(ctx context.Context, key string) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// ---------------------------------------------------------------------------
// Capability enforcement
// ---------------------------------------------------------------------------

const (
	metadataKeyCapabilities = "x-flow-capabilities"
	metadataKeyNodeID       = "x-flow-node-id"
)

// isNodeOriginatedCall returns true when the request carries a Sidecar-injected
// node identity, meaning it is a node-originated call through the Sidecar
// proxy layer. System-to-system calls (Operator, Librarian, etc.) do not
// carry this header and are not subject to capability enforcement.
func isNodeOriginatedCall(ctx context.Context) bool {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}
	return len(md.Get(metadataKeyNodeID)) > 0
}

// capabilitiesFromContext reads the comma-separated capability grants from
// gRPC metadata injected by the Sidecar.
func capabilitiesFromContext(ctx context.Context) []string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil
	}
	var caps []string
	for _, c := range md.Get(metadataKeyCapabilities) {
		for cap := range strings.SplitSeq(c, ",") {
			if s := strings.TrimSpace(cap); s != "" {
				caps = append(caps, s)
			}
		}
	}
	return caps
}

// hasCapability checks whether the capability list contains the given
// capability string. The check is exact.
func hasCapability(caps []string, required string) bool {
	return slices.Contains(caps, required)
}

// checkCapability enforces deny-by-default capability gating for
// node-originated requests. System-to-system calls (no x-flow-node-id)
// pass through unconditionally.
//
// Per spec (specs/05-reference/grpc-api.md, API Invariant #3):
// "Capability enforcement is performed by the owning service."
func checkCapability(ctx context.Context, required string) error {
	if !isNodeOriginatedCall(ctx) {
		return nil // System call — no capability check.
	}
	caps := capabilitiesFromContext(ctx)
	if hasCapability(caps, required) {
		return nil
	}
	return status.Errorf(codes.PermissionDenied,
		"CAPABILITY_DENIED: missing required capability %q", required)
}

// checkCapabilityAny checks that at least one of the required capabilities is
// present. Used for operations like StoreArtefact where either a broad
// WRITE:artefact or a scoped WRITE:artefact/<name> grant suffices.
func checkCapabilityAny(ctx context.Context, required ...string) error {
	if !isNodeOriginatedCall(ctx) {
		return nil
	}
	caps := capabilitiesFromContext(ctx)
	for _, r := range required {
		if hasCapability(caps, r) {
			return nil
		}
	}
	return status.Errorf(codes.PermissionDenied,
		"CAPABILITY_DENIED: missing required capability (one of %v)", required)
}

// StoreArtefact persists content and creates a version record.
//
// The content_hash is Sidecar-computed (not node-supplied). Logic:
//  1. If content_hash is not in BlobStore, write content.
//  2. Look up provenance history for (workitem_id, artefact_id).
//  3. If history is empty OR head hash != content_hash: append new version, is_new_version=true.
//  4. If head hash == content_hash: no-op, is_new_version=false.
//  5. Return the version_hash (which equals content_hash for the stored version).
func (s *ArchivistServer) StoreArtefact(
	ctx context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()
	contentHash := req.GetContentHash()
	kind := req.GetGovernedArtefact()

	// Capability gate: WRITE:artefact or WRITE:artefact/<governed_artefact>.
	if err := checkCapabilityAny(ctx, "WRITE:artefact", "WRITE:artefact/"+kind); err != nil {
		return nil, err
	}

	slog.Info("StoreArtefact",
		"workitem_id", workitemID,
		"artefact_id", artefactID,
		"content_hash", contentHash,
		"governed_artefact", kind,
	)

	// Step 1: Store blob (deduplicated by hash).
	if _, err := s.store.StoreBlob(ctx, contentHash, req.GetContent()); err != nil {
		return nil, status.Errorf(codes.Internal, "store blob: %v", err)
	}

	// Step 2: Check provenance history.
	head, err := s.store.GetHead(ctx, workitemID, artefactID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get head: %v", err)
	}

	// Step 3: Compare — only append if this is actually a new version.
	if head != nil && head.Hash == contentHash {
		slog.Info("StoreArtefact: content unchanged, no new version",
			"workitem_id", workitemID,
			"artefact_id", artefactID,
		)
		return &flowv1.StoreArtefactResponse{
			VersionHash:  contentHash,
			IsNewVersion: false,
		}, nil
	}

	// New version — append to history.
	if err := s.store.AppendVersion(ctx, workitemID, artefactID, contentHash, kind); err != nil {
		return nil, status.Errorf(codes.Internal, "append version: %v", err)
	}

	slog.Info("StoreArtefact: new version created",
		"workitem_id", workitemID,
		"artefact_id", artefactID,
		"version_hash", contentHash,
	)

	// Auto-resolve stale feedback tied to older versions.
	if n, err := s.store.ResolveStaleFeedback(ctx, workitemID, artefactID, contentHash); err != nil {
		slog.Error("StoreArtefact: failed to resolve stale feedback",
			"workitem_id", workitemID,
			"artefact_id", artefactID,
			"error", err,
		)
	} else if n > 0 {
		slog.Info("StoreArtefact: resolved stale feedback",
			"workitem_id", workitemID,
			"artefact_id", artefactID,
			"resolved_stale_count", n,
		)
	}

	s.publishAudit(ctx, "audit.artefact.version_created", map[string]string{
		"action":      "version_created",
		"resource_id": artefactID,
		"workitem_id": workitemID,
	})

	return &flowv1.StoreArtefactResponse{
		VersionHash:  contentHash,
		IsNewVersion: true,
	}, nil
}

// GetArtefact returns the latest version's content bytes.
//
// Steps:
//  1. Look up provenance history for (workitem_id, artefact_id).
//  2. If empty, return NotFound.
//  3. Get head hash, retrieve bytes from BlobStore.
//
// When target_workitem_id is set, the Archivist validates the parent-child
// relationship via the Operator and uses the target as the lookup key.
func (s *ArchivistServer) GetArtefact(
	ctx context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	// Capability gate: READ:artefact.
	if err := checkCapability(ctx, "READ:artefact"); err != nil {
		return nil, err
	}

	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()

	// Cross-Workitem read: validate parent-child and use target as lookup key.
	if targetID := req.GetTargetWorkitemId(); targetID != "" {
		callerWorkitemID := extractMetadataValue(ctx, "x-flow-workitem-id")
		if callerWorkitemID == "" {
			callerWorkitemID = workitemID
		}
		if err := s.validateChildAccess(ctx, callerWorkitemID, targetID); err != nil {
			return nil, err
		}
		workitemID = targetID
		slog.Info("GetArtefact (cross-Workitem)",
			"caller_workitem_id", callerWorkitemID,
			"target_workitem_id", targetID,
			"artefact_id", artefactID,
		)
	} else {
		slog.Info("GetArtefact",
			"workitem_id", workitemID,
			"artefact_id", artefactID,
		)
	}

	head, err := s.store.GetHead(ctx, workitemID, artefactID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get head: %v", err)
	}
	if head == nil {
		return nil, status.Errorf(codes.NotFound,
			"artefact %q not found for workitem %q", artefactID, workitemID)
	}

	data, ok, err := s.store.GetBlob(ctx, head.Hash)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get blob: %v", err)
	}
	if !ok {
		// This should never happen — provenance points to a hash that was stored.
		return nil, status.Errorf(codes.Internal,
			"blob %q referenced by artefact %q not found (data corruption)", head.Hash, artefactID)
	}

	return &flowv1.GetArtefactResponse{
		Content:          data,
		VersionHash:      head.Hash,
		GovernedArtefact: head.GovernedArtefact,
	}, nil
}

// GetArtefactVersion returns content bytes for a specific version by hash.
func (s *ArchivistServer) GetArtefactVersion(
	ctx context.Context, req *flowv1.GetArtefactVersionRequest,
) (*flowv1.GetArtefactVersionResponse, error) {
	// Capability gate: READ:artefact.
	if err := checkCapability(ctx, "READ:artefact"); err != nil {
		return nil, err
	}

	versionHash := req.GetVersionHash()

	slog.Info("GetArtefactVersion", "version_hash", versionHash)

	data, ok, err := s.store.GetBlob(ctx, versionHash)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get blob: %v", err)
	}
	if !ok {
		return nil, status.Errorf(codes.NotFound,
			"version %q not found", versionHash)
	}

	return &flowv1.GetArtefactVersionResponse{
		Content: data,
	}, nil
}

// GetArtefactMetadata returns version history and stamps for the current version.
func (s *ArchivistServer) GetArtefactMetadata(
	ctx context.Context, req *flowv1.GetArtefactMetadataRequest,
) (*flowv1.GetArtefactMetadataResponse, error) {
	// Capability gate: READ:artefact.
	if err := checkCapability(ctx, "READ:artefact"); err != nil {
		return nil, err
	}

	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()

	history, err := s.store.GetHistory(ctx, workitemID, artefactID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get history: %v", err)
	}
	if history == nil {
		return nil, status.Errorf(codes.NotFound,
			"artefact %q not found for workitem %q", artefactID, workitemID)
	}

	entries := make([]*flowv1.VersionEntry, 0, len(history))
	for _, v := range history {
		entries = append(entries, &flowv1.VersionEntry{
			VersionHash: v.Hash,
			CreatedAt:   timestamppb.New(v.CreatedAt),
		})
	}

	// Get stamps for the head (current) version.
	head := history[len(history)-1]
	stampRecords, err := s.store.GetStamps(ctx, workitemID, artefactID, head.Hash)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get stamps: %v", err)
	}

	stamps := make([]*flowv1.Stamp, 0, len(stampRecords))
	for _, sr := range stampRecords {
		stamps = append(stamps, &flowv1.Stamp{
			Name:         sr.Name,
			ApplyingNode: sr.ApplyingNode,
			ContentHash:  sr.ContentHash,
			Signature:    sr.Signature,
			CertChain:    sr.CertChain,
			CreatedAt:    timestamppb.New(sr.CreatedAt),
		})
	}

	return &flowv1.GetArtefactMetadataResponse{
		VersionHistory: entries,
		Stamps:         stamps,
	}, nil
}

// ListArtefacts returns all artefact refs for a workitem.
//
// When target_workitem_id is set, the Archivist validates the parent-child
// relationship via the Operator and uses the target as the lookup key.
func (s *ArchivistServer) ListArtefacts(
	ctx context.Context, req *flowv1.ListArtefactsRequest,
) (*flowv1.ListArtefactsResponse, error) {
	workitemID := req.GetWorkitemId()

	// Cross-Workitem read: validate parent-child and use target as lookup key.
	if targetID := req.GetTargetWorkitemId(); targetID != "" {
		callerWorkitemID := extractMetadataValue(ctx, "x-flow-workitem-id")
		if callerWorkitemID == "" {
			callerWorkitemID = workitemID
		}
		if err := s.validateChildAccess(ctx, callerWorkitemID, targetID); err != nil {
			return nil, err
		}
		workitemID = targetID
		slog.Info("ListArtefacts (cross-Workitem)",
			"caller_workitem_id", callerWorkitemID,
			"target_workitem_id", targetID,
		)
	} else {
		slog.Info("ListArtefacts", "workitem_id", workitemID)
	}

	entries, err := s.store.ListArtefacts(ctx, workitemID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list artefacts: %v", err)
	}

	refs := make([]*flowv1.ArtefactRef, 0, len(entries))
	for _, e := range entries {
		refs = append(refs, &flowv1.ArtefactRef{
			Id:               e.ID,
			GovernedArtefact: e.GovernedArtefact,
		})
	}

	return &flowv1.ListArtefactsResponse{
		ArtefactRefs: refs,
	}, nil
}

// QueryArtefactState returns artefact presence and stamp state for
// exit contract validation. Called by the Operator.
func (s *ArchivistServer) QueryArtefactState(
	ctx context.Context, req *flowv1.QueryArtefactStateRequest,
) (*flowv1.QueryArtefactStateResponse, error) {
	workitemID := req.GetWorkitemId()

	slog.Info("QueryArtefactState", "workitem_id", workitemID)

	entries, err := s.store.ListArtefacts(ctx, workitemID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list artefacts: %v", err)
	}

	states := make([]*flowv1.ArtefactState, 0, len(entries))
	for _, e := range entries {
		head, err := s.store.GetHead(ctx, workitemID, e.ID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "get head: %v", err)
		}
		if head == nil {
			continue
		}

		stampNames, err := s.store.GetStampNamesForVersion(ctx, workitemID, e.ID, head.Hash)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "get stamp names: %v", err)
		}

		states = append(states, &flowv1.ArtefactState{
			ArtefactId:         e.ID,
			GovernedArtefact:   e.GovernedArtefact,
			StampNames:         stampNames,
			CurrentVersionHash: head.Hash,
		})
	}

	return &flowv1.QueryArtefactStateResponse{
		ArtefactStates: states,
	}, nil
}

// ---------------------------------------------------------------------------
// Stamp Methods
// ---------------------------------------------------------------------------

// StampArtefact applies a named stamp to the current (head) version of an
// artefact. Returns the created stamp. The signature and cert_chain are
// Sidecar-injected from the node's identity material.
func (s *ArchivistServer) StampArtefact(
	ctx context.Context, req *flowv1.StampArtefactRequest,
) (*flowv1.StampArtefactResponse, error) {
	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()
	stampName := req.GetStampName()

	slog.Info("StampArtefact",
		"workitem_id", workitemID,
		"artefact_id", artefactID,
		"stamp_name", stampName,
	)

	// Resolve head version (needed for both capability check and stamp).
	head, err := s.store.GetHead(ctx, workitemID, artefactID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get head: %v", err)
	}
	if head == nil {
		return nil, status.Errorf(codes.NotFound,
			"artefact %q not found for workitem %q", artefactID, workitemID)
	}

	// Capability gate: STAMP:artefact/<governed_artefact>/<stamp_name>.
	// Enforcement is exact per spec (specs/03-node/02-configuration.md).
	if err := checkCapability(ctx, "STAMP:artefact/"+head.GovernedArtefact+"/"+stampName); err != nil {
		return nil, err
	}

	// Extract applying_node from gRPC metadata if available.
	applyingNode := extractNodeID(ctx)

	isNew, err := s.store.StampArtefact(
		ctx, workitemID, artefactID, head.Hash, stampName,
		applyingNode, req.GetSignature(), req.GetCertChain(),
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "stamp artefact: %v", err)
	}

	if !isNew {
		slog.Info("StampArtefact: stamp already exists",
			"workitem_id", workitemID,
			"artefact_id", artefactID,
			"stamp_name", stampName,
		)
	} else {
		s.publishAudit(ctx, "audit.artefact.stamped", map[string]string{
			"action":      "stamped",
			"resource_id": artefactID,
			"workitem_id": workitemID,
			"stamp_name":  stampName,
		})
	}

	return &flowv1.StampArtefactResponse{
		Stamp: &flowv1.Stamp{
			Name:         stampName,
			ApplyingNode: applyingNode,
			ContentHash:  head.Hash,
			Signature:    req.GetSignature(),
			CertChain:    req.GetCertChain(),
		},
	}, nil
}

// GetStamps returns all stamps on the current (head) version of an artefact.
func (s *ArchivistServer) GetStamps(
	ctx context.Context, req *flowv1.GetStampsRequest,
) (*flowv1.GetStampsResponse, error) {
	// Capability gate: READ:artefact.
	if err := checkCapability(ctx, "READ:artefact"); err != nil {
		return nil, err
	}

	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()

	head, err := s.store.GetHead(ctx, workitemID, artefactID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get head: %v", err)
	}
	if head == nil {
		return &flowv1.GetStampsResponse{}, nil
	}

	records, err := s.store.GetStamps(ctx, workitemID, artefactID, head.Hash)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get stamps: %v", err)
	}

	stamps := make([]*flowv1.Stamp, 0, len(records))
	for _, sr := range records {
		stamps = append(stamps, &flowv1.Stamp{
			Name:         sr.Name,
			ApplyingNode: sr.ApplyingNode,
			ContentHash:  sr.ContentHash,
			Signature:    sr.Signature,
			CertChain:    sr.CertChain,
			CreatedAt:    timestamppb.New(sr.CreatedAt),
		})
	}

	return &flowv1.GetStampsResponse{Stamps: stamps}, nil
}

// HasStamp checks whether the named stamp exists on the current version.
func (s *ArchivistServer) HasStamp(ctx context.Context, req *flowv1.HasStampRequest) (*flowv1.HasStampResponse, error) {
	// Capability gate: READ:artefact.
	if err := checkCapability(ctx, "READ:artefact"); err != nil {
		return nil, err
	}

	exists, err := s.store.HasStamp(ctx, req.GetWorkitemId(), req.GetArtefactId(), req.GetStampName())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "has stamp: %v", err)
	}
	return &flowv1.HasStampResponse{Exists: exists}, nil
}

// ---------------------------------------------------------------------------
// Feedback Methods
// ---------------------------------------------------------------------------

// Feedback state constants matching the proto enum values.
const (
	feedbackStateNew      int32 = 1 // FEEDBACK_STATE_NEW
	feedbackStateActioned int32 = 2 // FEEDBACK_STATE_ACTIONED
	feedbackStateWontFix  int32 = 3 // FEEDBACK_STATE_WONT_FIX
	feedbackStateRejected int32 = 4 // FEEDBACK_STATE_REJECTED
	feedbackStateResolved int32 = 6 // FEEDBACK_STATE_RESOLVED
)

// AddFeedback creates a new feedback item in NEW state.
func (s *ArchivistServer) AddFeedback(
	ctx context.Context, req *flowv1.AddFeedbackRequest,
) (*flowv1.AddFeedbackResponse, error) {
	// Capability gate: WRITE:feedback/new.
	if err := checkCapability(ctx, "WRITE:feedback/new"); err != nil {
		return nil, err
	}

	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()
	source := extractNodeID(ctx)

	slog.Info("AddFeedback",
		"workitem_id", workitemID,
		"artefact_id", artefactID,
		"can_wont_fix", req.GetCanWontFix(),
		"source", source,
	)

	// Resolve version_hash: use provided one or fall back to head.
	versionHash := req.GetVersionHash()
	if versionHash == "" {
		head, err := s.store.GetHead(ctx, workitemID, artefactID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "get head: %v", err)
		}
		if head != nil {
			versionHash = head.Hash
		}
	}

	feedbackID, err := s.store.AddFeedback(
		ctx, workitemID, artefactID, source,
		req.GetCanWontFix(), req.GetMessage(), versionHash,
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "add feedback: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.add", map[string]string{
		"action":      "add",
		"resource_id": feedbackID,
		"workitem_id": workitemID,
		"artefact_id": artefactID,
	})

	return &flowv1.AddFeedbackResponse{FeedbackId: feedbackID}, nil
}

// GetFeedback returns all feedback items for an artefact.
func (s *ArchivistServer) GetFeedback(
	ctx context.Context, req *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	// Capability gate: READ:feedback.
	if err := checkCapability(ctx, "READ:feedback"); err != nil {
		return nil, err
	}

	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()

	records, err := s.store.GetFeedback(ctx, workitemID, artefactID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get feedback: %v", err)
	}

	items := make([]*flowv1.FeedbackItem, 0, len(records))
	for _, r := range records {
		events, err := s.store.GetFeedbackEvents(ctx, r.ID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "get feedback events: %v", err)
		}

		protoEvents := make([]*flowv1.FeedbackEvent, 0, len(events))
		for _, e := range events {
			protoEvents = append(protoEvents, &flowv1.FeedbackEvent{
				Actor:     e.Actor,
				Action:    e.Action,
				Message:   e.Message,
				Timestamp: timestamppb.New(e.CreatedAt),
			})
		}

		items = append(items, &flowv1.FeedbackItem{
			Id:           r.ID,
			Source:       r.Source,
			CanWontFix:   r.CanWontFix,
			State:        flowv1.FeedbackState(r.State),
			Message:      r.Message,
			LinkedRuling: r.LinkedRuling,
			VersionHash:  r.VersionHash,
			History:      protoEvents,
			CreatedAt:    timestamppb.New(r.CreatedAt),
		})
	}

	return &flowv1.GetFeedbackResponse{FeedbackItems: items}, nil
}

// HasUnresolvedFeedback returns true if any feedback for the artefact is
// not in RESOLVED state.
func (s *ArchivistServer) HasUnresolvedFeedback(
	ctx context.Context, req *flowv1.HasUnresolvedFeedbackRequest,
) (*flowv1.HasUnresolvedFeedbackResponse, error) {
	// Capability gate: READ:feedback.
	if err := checkCapability(ctx, "READ:feedback"); err != nil {
		return nil, err
	}

	has, err := s.store.HasUnresolvedFeedback(ctx, req.GetWorkitemId(), req.GetArtefactId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "has unresolved feedback: %v", err)
	}
	return &flowv1.HasUnresolvedFeedbackResponse{HasUnresolved: has}, nil
}

// ResolveFeedback transitions feedback from NEW or REJECTED to ACTIONED.
func (s *ArchivistServer) ResolveFeedback(
	ctx context.Context, req *flowv1.ResolveFeedbackRequest,
) (*flowv1.ResolveFeedbackResponse, error) {
	// Capability gate: WRITE:feedback/actioned.
	if err := checkCapability(ctx, "WRITE:feedback/actioned"); err != nil {
		return nil, err
	}

	actor := extractNodeID(ctx)

	record, err := s.store.TransitionFeedback(ctx, req.GetFeedbackId(),
		[]int32{feedbackStateNew, feedbackStateRejected},
		feedbackStateActioned,
		actor, "actioned", req.GetMessage(),
	)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "resolve feedback: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.resolve", map[string]string{
		"action":      "resolve",
		"resource_id": req.GetFeedbackId(),
	})

	return &flowv1.ResolveFeedbackResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}

// RefuseFeedback transitions feedback from NEW or REJECTED to WONT_FIX.
func (s *ArchivistServer) RefuseFeedback(
	ctx context.Context, req *flowv1.RefuseFeedbackRequest,
) (*flowv1.RefuseFeedbackResponse, error) {
	// Capability gate: WRITE:feedback/wont_fix.
	if err := checkCapability(ctx, "WRITE:feedback/wont_fix"); err != nil {
		return nil, err
	}

	actor := extractNodeID(ctx)

	record, err := s.store.TransitionFeedback(ctx, req.GetFeedbackId(),
		[]int32{feedbackStateNew, feedbackStateRejected},
		feedbackStateWontFix,
		actor, "refused", "",
	)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "refuse feedback: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.refuse", map[string]string{
		"action":      "refuse",
		"resource_id": req.GetFeedbackId(),
	})

	return &flowv1.RefuseFeedbackResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}

// AcceptFix transitions feedback from ACTIONED to RESOLVED.
func (s *ArchivistServer) AcceptFix(
	ctx context.Context, req *flowv1.AcceptFixRequest,
) (*flowv1.AcceptFixResponse, error) {
	// Capability gate: WRITE:feedback/resolved.
	if err := checkCapability(ctx, "WRITE:feedback/resolved"); err != nil {
		return nil, err
	}

	actor := extractNodeID(ctx)

	record, err := s.store.TransitionFeedback(ctx, req.GetFeedbackId(),
		[]int32{feedbackStateActioned},
		feedbackStateResolved,
		actor, "accepted_fix", "",
	)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "accept fix: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.accept", map[string]string{
		"action":      "accept",
		"resource_id": req.GetFeedbackId(),
	})

	return &flowv1.AcceptFixResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}

// RejectFix transitions feedback from ACTIONED to REJECTED.
func (s *ArchivistServer) RejectFix(
	ctx context.Context, req *flowv1.RejectFixRequest,
) (*flowv1.RejectFixResponse, error) {
	// Capability gate: WRITE:feedback/rejected.
	if err := checkCapability(ctx, "WRITE:feedback/rejected"); err != nil {
		return nil, err
	}

	actor := extractNodeID(ctx)

	record, err := s.store.TransitionFeedback(ctx, req.GetFeedbackId(),
		[]int32{feedbackStateActioned},
		feedbackStateRejected,
		actor, "rejected_fix", req.GetMessage(),
	)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "reject fix: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.reject", map[string]string{
		"action":      "reject",
		"resource_id": req.GetFeedbackId(),
	})

	return &flowv1.RejectFixResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}

// AcceptRefusal transitions feedback from WONT_FIX to RESOLVED.
func (s *ArchivistServer) AcceptRefusal(
	ctx context.Context, req *flowv1.AcceptRefusalRequest,
) (*flowv1.AcceptRefusalResponse, error) {
	// Capability gate: WRITE:feedback/resolved.
	if err := checkCapability(ctx, "WRITE:feedback/resolved"); err != nil {
		return nil, err
	}

	actor := extractNodeID(ctx)

	record, err := s.store.TransitionFeedback(ctx, req.GetFeedbackId(),
		[]int32{feedbackStateWontFix},
		feedbackStateResolved,
		actor, "accepted_refusal", "",
	)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "accept refusal: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.accept", map[string]string{
		"action":      "accept",
		"resource_id": req.GetFeedbackId(),
	})

	return &flowv1.AcceptRefusalResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}

// RejectRefusal transitions feedback from WONT_FIX to REJECTED.
func (s *ArchivistServer) RejectRefusal(
	ctx context.Context, req *flowv1.RejectRefusalRequest,
) (*flowv1.RejectRefusalResponse, error) {
	// Capability gate: WRITE:feedback/rejected.
	if err := checkCapability(ctx, "WRITE:feedback/rejected"); err != nil {
		return nil, err
	}

	actor := extractNodeID(ctx)

	record, err := s.store.TransitionFeedback(ctx, req.GetFeedbackId(),
		[]int32{feedbackStateWontFix},
		feedbackStateRejected,
		actor, "rejected_refusal", req.GetMessage(),
	)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "reject refusal: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.reject", map[string]string{
		"action":      "reject",
		"resource_id": req.GetFeedbackId(),
	})

	return &flowv1.RejectRefusalResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}

// GetFeedbackDepth returns the number of events in a feedback item's history.
func (s *ArchivistServer) GetFeedbackDepth(
	ctx context.Context, req *flowv1.GetFeedbackDepthRequest,
) (*flowv1.GetFeedbackDepthResponse, error) {
	// Capability gate: READ:feedback.
	if err := checkCapability(ctx, "READ:feedback"); err != nil {
		return nil, err
	}

	depth, err := s.store.GetFeedbackDepth(ctx, req.GetFeedbackId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get feedback depth: %v", err)
	}
	return &flowv1.GetFeedbackDepthResponse{Depth: depth}, nil
}

// DeadlockFeedback transitions feedback from any non-resolved, non-deadlocked
// state to DEADLOCKED. The gate node calls this when feedback depth exceeds
// the configured threshold.
func (s *ArchivistServer) DeadlockFeedback(
	ctx context.Context, req *flowv1.DeadlockFeedbackRequest,
) (*flowv1.DeadlockFeedbackResponse, error) {
	// Capability gate: WRITE:feedback/deadlocked.
	if err := checkCapability(ctx, "WRITE:feedback/deadlocked"); err != nil {
		return nil, err
	}

	actor := extractNodeID(ctx)

	record, err := s.store.TransitionFeedback(ctx, req.GetFeedbackId(),
		[]int32{
			feedbackStateNew, feedbackStateActioned,
			feedbackStateWontFix, feedbackStateRejected,
		},
		5, // FEEDBACK_STATE_DEADLOCKED
		actor, "deadlocked", "",
	)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "deadlock feedback: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.deadlock", map[string]string{
		"action":      "deadlock",
		"resource_id": req.GetFeedbackId(),
	})

	return &flowv1.DeadlockFeedbackResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}

// LinkRuling atomically links a judiciary ruling to a deadlocked feedback
// item, transitioning it to the specified terminal state and enabling the
// contempt guard. The feedback must be in DEADLOCKED state and must not
// already have a linked ruling. The target_state must be WONT_FIX or REJECTED.
func (s *ArchivistServer) LinkRuling(
	ctx context.Context, req *flowv1.LinkRulingRequest,
) (*flowv1.LinkRulingResponse, error) {
	// Capability gate: WRITE:feedback/link-ruling.
	if err := checkCapability(ctx, "WRITE:feedback/link-ruling"); err != nil {
		return nil, err
	}

	feedbackID := req.GetFeedbackId()
	lawID := req.GetLawId()
	targetState := req.GetTargetState()

	slog.Info("LinkRuling",
		"workitem_id", req.GetWorkitemId(),
		"feedback_id", feedbackID,
		"law_id", lawID,
		"target_state", targetState.String(),
	)

	if feedbackID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "feedback_id is required")
	}
	if lawID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "law_id is required")
	}
	if targetState == flowv1.FeedbackState_FEEDBACK_STATE_UNSPECIFIED {
		return nil, status.Errorf(codes.InvalidArgument, "target_state is required (must be WONT_FIX or REJECTED)")
	}

	record, err := s.store.LinkRuling(ctx, feedbackID, lawID, int32(targetState))
	if err != nil {
		switch {
		case errors.Is(err, sqlite.ErrFeedbackNotFound):
			return nil, status.Errorf(codes.NotFound, "link ruling: %v", err)
		case errors.Is(err, sqlite.ErrFeedbackNotDeadlocked),
			errors.Is(err, sqlite.ErrContemptGuard):
			return nil, status.Errorf(codes.FailedPrecondition, "link ruling: %v", err)
		default:
			return nil, status.Errorf(codes.Internal, "link ruling: %v", err)
		}
	}

	s.publishAudit(ctx, "audit.artefact.feedback.link-ruling", map[string]string{
		"action":      "link-ruling",
		"resource_id": feedbackID,
		"law_id":      lawID,
	})

	return &flowv1.LinkRulingResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractNodeID reads the x-flow-node-id value from incoming gRPC metadata.
func extractNodeID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("x-flow-node-id")
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// feedbackRecordToProto converts a store FeedbackRecord to a proto FeedbackItem.
// Note: history is not populated here; use GetFeedback for full history.
func feedbackRecordToProto(r *sqlite.FeedbackRecord) *flowv1.FeedbackItem {
	if r == nil {
		return nil
	}
	return &flowv1.FeedbackItem{
		Id:           r.ID,
		Source:       r.Source,
		CanWontFix:   r.CanWontFix,
		State:        flowv1.FeedbackState(r.State),
		Message:      r.Message,
		LinkedRuling: r.LinkedRuling,
		VersionHash:  r.VersionHash,
		CreatedAt:    timestamppb.New(r.CreatedAt),
	}
}
