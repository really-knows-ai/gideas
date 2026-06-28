// Package proxy implements forwarding handlers that relay gRPC calls
// from the Sidecar to the real cluster services. Each handler wraps a
// generated gRPC client and propagates identity metadata
// (x-flow-workitem-id) from the incoming server context to the outgoing
// client context.
package proxy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/sidecar/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// ArchivistProxy implements flowv1.ArchivistServiceServer by forwarding
// calls to the real Archivist gRPC endpoint. For StoreArtefact, it acts
// as the security boundary by computing the SHA-256 content hash
// server-side — the node is not trusted to supply this value.
//
// When a SidecarServer is set (childAuth), the proxy validates
// cross-Workitem operations (target_workitem_id) against the session's
// local child cache. If the session created children and the target is
// not one of them, the request is rejected. If the session has no children
// (collection phase at a different node), the request passes through and
// the Archivist performs its own Operator-backed validation (defense in depth).
type ArchivistProxy struct {
	flowv1.UnimplementedArchivistServiceServer
	client flowv1.ArchivistServiceClient
	conn   *grpc.ClientConn

	// childAuth validates cross-Workitem operations against the session's
	// local child cache. May be nil (authorization disabled).
	childAuth *service.SidecarServer
}

// NewArchivistProxy dials the Archivist gRPC endpoint and returns a proxy
// handler ready to be registered on the Sidecar's gRPC server.
// The childAuth, if non-nil, is used to validate cross-Workitem operations
// against the session's local child cache.
func NewArchivistProxy(archivistAddr string, childAuth *service.SidecarServer) (*ArchivistProxy, error) {
	conn, err := grpc.NewClient(
		archivistAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(metadataUnaryInterceptor),
		grpc.WithStreamInterceptor(metadataStreamInterceptor),
	)
	if err != nil {
		return nil, err
	}

	return &ArchivistProxy{
		client:    flowv1.NewArchivistServiceClient(conn),
		conn:      conn,
		childAuth: childAuth,
	}, nil
}

// Close releases the underlying gRPC connection to the Archivist.
func (p *ArchivistProxy) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// authorizeTargetWorkitem validates a cross-Workitem operation against the
// session's local child cache. Returns nil if the operation is allowed,
// or a gRPC PermissionDenied error if the target is not a known child.
//
// The decision matrix:
//   - No childAuth configured → allow (no authorization layer)
//   - No target_workitem_id → allow (not a cross-Workitem operation)
//   - ChildAccessAllowed → allow (target is a known child)
//   - ChildAccessDenied → reject (session has children, target is not one)
//   - ChildAccessUnknown → allow (session has no children; Archivist
//     will perform Operator-backed validation)
func (p *ArchivistProxy) authorizeTargetWorkitem(ctx context.Context, targetWorkitemID string) error {
	if p.childAuth == nil || targetWorkitemID == "" {
		return nil
	}

	callerWorkitemID := extractWorkitemIDFromMD(ctx)
	if callerWorkitemID == "" {
		return nil
	}

	decision := p.childAuth.AuthorizeChildAccess(callerWorkitemID, targetWorkitemID)
	switch decision {
	case service.ChildAccessAllowed:
		slog.Debug("Sidecar: cross-Workitem access authorised (known child)",
			"parent_workitem_id", callerWorkitemID,
			"target_workitem_id", targetWorkitemID,
		)
		return nil
	case service.ChildAccessDenied:
		slog.Warn("Sidecar: cross-Workitem access denied (CHILD_NOT_OWNED)",
			"parent_workitem_id", callerWorkitemID,
			"target_workitem_id", targetWorkitemID,
		)
		return status.Errorf(codes.PermissionDenied,
			"CHILD_NOT_OWNED: workitem %q is not a child of %q", targetWorkitemID, callerWorkitemID)
	default: // ChildAccessUnknown
		slog.Info("Sidecar: cross-Workitem access deferred to Archivist (no local children)",
			"parent_workitem_id", callerWorkitemID,
			"target_workitem_id", targetWorkitemID,
		)
		return nil
	}
}

// authorizeWorkitemWrite validates that a write operation targeting a
// workitem_id is authorised. If the target workitem is the same as the
// session's (normal case), the write is always allowed. If different
// (cross-Workitem child write), it must be a known child.
func (p *ArchivistProxy) authorizeWorkitemWrite(ctx context.Context, targetWorkitemID string) error {
	if p.childAuth == nil || targetWorkitemID == "" {
		return nil
	}

	callerWorkitemID := extractWorkitemIDFromMD(ctx)
	if callerWorkitemID == "" || callerWorkitemID == targetWorkitemID {
		return nil // Same workitem or no session identity -- normal case.
	}

	// The target differs from the session's workitem -- this is a
	// cross-Workitem write. Validate it's a known child.
	decision := p.childAuth.AuthorizeChildAccess(callerWorkitemID, targetWorkitemID)
	switch decision {
	case service.ChildAccessAllowed:
		slog.Debug("Sidecar: cross-Workitem write authorised (known child)",
			"parent_workitem_id", callerWorkitemID,
			"target_workitem_id", targetWorkitemID,
		)
		return nil
	case service.ChildAccessDenied:
		slog.Warn("Sidecar: cross-Workitem write denied (CHILD_NOT_OWNED)",
			"parent_workitem_id", callerWorkitemID,
			"target_workitem_id", targetWorkitemID,
		)
		return status.Errorf(codes.PermissionDenied,
			"CHILD_NOT_OWNED: workitem %q is not a child of %q", targetWorkitemID, callerWorkitemID)
	default: // ChildAccessUnknown
		// Session has no children -- can't determine. Allow through,
		// but this case shouldn't happen for writes (you can't write to
		// a child you haven't created).
		slog.Warn("Sidecar: cross-Workitem write to unknown target (no local children)",
			"parent_workitem_id", callerWorkitemID,
			"target_workitem_id", targetWorkitemID,
		)
		return status.Errorf(codes.PermissionDenied,
			"CHILD_NOT_OWNED: workitem %q is not a child of %q", targetWorkitemID, callerWorkitemID)
	}
}

// StoreArtefact is the security boundary for artefact writes.
//
// The Sidecar:
//  1. Validates cross-Workitem writes: if the request's workitem_id differs
//     from the session's workitem_id, the target must be a known child.
//  2. Receives content from the Node (SDK).
//  3. Computes the SHA-256 hash of the raw bytes. Does NOT trust the node.
//  4. Forwards to archivistClient.StoreArtefact with the computed hash.
func (p *ArchivistProxy) StoreArtefact(
	ctx context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	// Validate cross-Workitem writes: if the request targets a different
	// workitem than the session's, it must be a known child.
	if err := p.authorizeWorkitemWrite(ctx, req.GetWorkitemId()); err != nil {
		return nil, err
	}

	// Security: compute the content hash server-side.
	hash := sha256.Sum256(req.GetContent())
	contentHash := fmt.Sprintf("%x", hash[:])

	slog.Info("Sidecar: StoreArtefact (hashing content)",
		"workitem_id", req.GetWorkitemId(),
		"artefact_id", req.GetArtefactId(),
		"content_hash", contentHash,
		"content_size", len(req.GetContent()),
	)

	// Forward with the Sidecar-computed hash, overriding any node-supplied value.
	resp, err := p.client.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       req.GetWorkitemId(),
		ArtefactId:       req.GetArtefactId(),
		GovernedArtefact: req.GetGovernedArtefact(),
		Content:          req.GetContent(),
		ContentHash:      contentHash,
	})
	if err != nil {
		slog.Error("StoreArtefact forwarding failed", "error", err)
		return nil, err
	}

	slog.Info("StoreArtefact forwarded successfully",
		"workitem_id", req.GetWorkitemId(),
		"artefact_id", req.GetArtefactId(),
		"version_hash", resp.GetVersionHash(),
		"is_new_version", resp.GetIsNewVersion(),
	)
	return resp, nil
}

// GetArtefact forwards to the Archivist. If target_workitem_id is set,
// the Sidecar validates the cross-Workitem read against the session's
// local child cache before forwarding.
func (p *ArchivistProxy) GetArtefact(
	ctx context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	if err := p.authorizeTargetWorkitem(ctx, req.GetTargetWorkitemId()); err != nil {
		return nil, err
	}

	if targetID := req.GetTargetWorkitemId(); targetID != "" {
		slog.Info("Sidecar: forwarding GetArtefact (cross-Workitem)",
			"workitem_id", req.GetWorkitemId(),
			"target_workitem_id", targetID,
			"artefact_id", req.GetArtefactId(),
		)
	} else {
		slog.Info("Sidecar: forwarding GetArtefact",
			"workitem_id", req.GetWorkitemId(),
			"artefact_id", req.GetArtefactId(),
		)
	}
	return p.client.GetArtefact(ctx, req)
}

// GetArtefactVersion forwards to the Archivist (passthrough).
func (p *ArchivistProxy) GetArtefactVersion(
	ctx context.Context, req *flowv1.GetArtefactVersionRequest,
) (*flowv1.GetArtefactVersionResponse, error) {
	return p.client.GetArtefactVersion(ctx, req)
}

// GetArtefactMetadata forwards to the Archivist (passthrough).
func (p *ArchivistProxy) GetArtefactMetadata(
	ctx context.Context, req *flowv1.GetArtefactMetadataRequest,
) (*flowv1.GetArtefactMetadataResponse, error) {
	return p.client.GetArtefactMetadata(ctx, req)
}

// ListArtefacts forwards to the Archivist. If target_workitem_id is set,
// the Sidecar validates the cross-Workitem read against the session's
// local child cache before forwarding.
func (p *ArchivistProxy) ListArtefacts(
	ctx context.Context, req *flowv1.ListArtefactsRequest,
) (*flowv1.ListArtefactsResponse, error) {
	if err := p.authorizeTargetWorkitem(ctx, req.GetTargetWorkitemId()); err != nil {
		return nil, err
	}

	if targetID := req.GetTargetWorkitemId(); targetID != "" {
		slog.Info("Sidecar: forwarding ListArtefacts (cross-Workitem)",
			"workitem_id", req.GetWorkitemId(),
			"target_workitem_id", targetID,
		)
	}
	return p.client.ListArtefacts(ctx, req)
}

// QueryArtefactState forwards to the Archivist (passthrough).
func (p *ArchivistProxy) QueryArtefactState(
	ctx context.Context, req *flowv1.QueryArtefactStateRequest,
) (*flowv1.QueryArtefactStateResponse, error) {
	return p.client.QueryArtefactState(ctx, req)
}

// --- Stamp passthroughs ---

func (p *ArchivistProxy) GetStamps(
	ctx context.Context, req *flowv1.GetStampsRequest,
) (*flowv1.GetStampsResponse, error) {
	return p.client.GetStamps(ctx, req)
}

func (p *ArchivistProxy) HasStamp(ctx context.Context, req *flowv1.HasStampRequest) (*flowv1.HasStampResponse, error) {
	return p.client.HasStamp(ctx, req)
}

func (p *ArchivistProxy) StampArtefact(
	ctx context.Context, req *flowv1.StampArtefactRequest,
) (*flowv1.StampArtefactResponse, error) {
	return p.client.StampArtefact(ctx, req)
}

// --- Feedback passthroughs ---

func (p *ArchivistProxy) AddFeedback(
	ctx context.Context, req *flowv1.AddFeedbackRequest,
) (*flowv1.AddFeedbackResponse, error) {
	return p.client.AddFeedback(ctx, req)
}

func (p *ArchivistProxy) GetFeedback(
	ctx context.Context, req *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	return p.client.GetFeedback(ctx, req)
}

func (p *ArchivistProxy) HasUnresolvedFeedback(
	ctx context.Context, req *flowv1.HasUnresolvedFeedbackRequest,
) (*flowv1.HasUnresolvedFeedbackResponse, error) {
	return p.client.HasUnresolvedFeedback(ctx, req)
}

func (p *ArchivistProxy) ResolveFeedback(
	ctx context.Context, req *flowv1.ResolveFeedbackRequest,
) (*flowv1.ResolveFeedbackResponse, error) {
	return p.client.ResolveFeedback(ctx, req)
}

func (p *ArchivistProxy) RefuseFeedback(
	ctx context.Context, req *flowv1.RefuseFeedbackRequest,
) (*flowv1.RefuseFeedbackResponse, error) {
	return p.client.RefuseFeedback(ctx, req)
}

func (p *ArchivistProxy) AcceptFix(
	ctx context.Context, req *flowv1.AcceptFixRequest,
) (*flowv1.AcceptFixResponse, error) {
	return p.client.AcceptFix(ctx, req)
}

func (p *ArchivistProxy) RejectFix(
	ctx context.Context, req *flowv1.RejectFixRequest,
) (*flowv1.RejectFixResponse, error) {
	return p.client.RejectFix(ctx, req)
}

func (p *ArchivistProxy) AcceptRefusal(
	ctx context.Context, req *flowv1.AcceptRefusalRequest,
) (*flowv1.AcceptRefusalResponse, error) {
	return p.client.AcceptRefusal(ctx, req)
}

func (p *ArchivistProxy) RejectRefusal(
	ctx context.Context, req *flowv1.RejectRefusalRequest,
) (*flowv1.RejectRefusalResponse, error) {
	return p.client.RejectRefusal(ctx, req)
}

func (p *ArchivistProxy) GetFeedbackDepth(
	ctx context.Context, req *flowv1.GetFeedbackDepthRequest,
) (*flowv1.GetFeedbackDepthResponse, error) {
	return p.client.GetFeedbackDepth(ctx, req)
}

func (p *ArchivistProxy) DeadlockFeedback(
	ctx context.Context, req *flowv1.DeadlockFeedbackRequest,
) (*flowv1.DeadlockFeedbackResponse, error) {
	return p.client.DeadlockFeedback(ctx, req)
}

// LinkRuling forwards to the Archivist (passthrough).
func (p *ArchivistProxy) LinkRuling(
	ctx context.Context, req *flowv1.LinkRulingRequest,
) (*flowv1.LinkRulingResponse, error) {
	return p.client.LinkRuling(ctx, req)
}
