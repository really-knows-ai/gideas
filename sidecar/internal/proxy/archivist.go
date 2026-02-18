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
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ArchivistProxy implements flowv1.ArchivistServiceServer by forwarding
// calls to the real Archivist gRPC endpoint. For StoreArtefact, it acts
// as the security boundary by computing the SHA-256 content hash
// server-side — the node is not trusted to supply this value.
type ArchivistProxy struct {
	flowv1.UnimplementedArchivistServiceServer
	client flowv1.ArchivistServiceClient
	conn   *grpc.ClientConn
}

// NewArchivistProxy dials the Archivist gRPC endpoint and returns a proxy
// handler ready to be registered on the Sidecar's gRPC server.
func NewArchivistProxy(archivistAddr string) (*ArchivistProxy, error) {
	conn, err := grpc.NewClient(
		archivistAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, err
	}

	return &ArchivistProxy{
		client: flowv1.NewArchivistServiceClient(conn),
		conn:   conn,
	}, nil
}

// Close releases the underlying gRPC connection to the Archivist.
func (p *ArchivistProxy) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// StoreArtefact is the security boundary for artefact writes.
//
// The Sidecar:
//  1. Receives content from the Node (SDK).
//  2. Computes the SHA-256 hash of the raw bytes. Does NOT trust the node.
//  3. Forwards to archivistClient.StoreArtefact with the computed hash.
func (p *ArchivistProxy) StoreArtefact(
	ctx context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	outCtx := propagateMetadata(ctx)

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
	resp, err := p.client.StoreArtefact(outCtx, &flowv1.StoreArtefactRequest{
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

// GetArtefact forwards to the Archivist (passthrough).
func (p *ArchivistProxy) GetArtefact(
	ctx context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	outCtx := propagateMetadata(ctx)

	slog.Info("Sidecar: forwarding GetArtefact",
		"workitem_id", req.GetWorkitemId(),
		"artefact_id", req.GetArtefactId(),
	)
	return p.client.GetArtefact(outCtx, req)
}

// GetArtefactVersion forwards to the Archivist (passthrough).
func (p *ArchivistProxy) GetArtefactVersion(
	ctx context.Context, req *flowv1.GetArtefactVersionRequest,
) (*flowv1.GetArtefactVersionResponse, error) {
	outCtx := propagateMetadata(ctx)
	return p.client.GetArtefactVersion(outCtx, req)
}

// GetArtefactMetadata forwards to the Archivist (passthrough).
func (p *ArchivistProxy) GetArtefactMetadata(
	ctx context.Context, req *flowv1.GetArtefactMetadataRequest,
) (*flowv1.GetArtefactMetadataResponse, error) {
	outCtx := propagateMetadata(ctx)
	return p.client.GetArtefactMetadata(outCtx, req)
}

// ListArtefacts forwards to the Archivist (passthrough).
func (p *ArchivistProxy) ListArtefacts(
	ctx context.Context, req *flowv1.ListArtefactsRequest,
) (*flowv1.ListArtefactsResponse, error) {
	outCtx := propagateMetadata(ctx)
	return p.client.ListArtefacts(outCtx, req)
}

// QueryArtefactState forwards to the Archivist (passthrough).
func (p *ArchivistProxy) QueryArtefactState(
	ctx context.Context, req *flowv1.QueryArtefactStateRequest,
) (*flowv1.QueryArtefactStateResponse, error) {
	outCtx := propagateMetadata(ctx)
	return p.client.QueryArtefactState(outCtx, req)
}

// --- Stamp passthroughs ---

func (p *ArchivistProxy) GetStamps(
	ctx context.Context, req *flowv1.GetStampsRequest,
) (*flowv1.GetStampsResponse, error) {
	return p.client.GetStamps(propagateMetadata(ctx), req)
}

func (p *ArchivistProxy) HasStamp(ctx context.Context, req *flowv1.HasStampRequest) (*flowv1.HasStampResponse, error) {
	return p.client.HasStamp(propagateMetadata(ctx), req)
}

func (p *ArchivistProxy) StampArtefact(
	ctx context.Context, req *flowv1.StampArtefactRequest,
) (*flowv1.StampArtefactResponse, error) {
	return p.client.StampArtefact(propagateMetadata(ctx), req)
}

// --- Feedback passthroughs ---

func (p *ArchivistProxy) AddFeedback(
	ctx context.Context, req *flowv1.AddFeedbackRequest,
) (*flowv1.AddFeedbackResponse, error) {
	return p.client.AddFeedback(propagateMetadata(ctx), req)
}

func (p *ArchivistProxy) GetFeedback(
	ctx context.Context, req *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	return p.client.GetFeedback(propagateMetadata(ctx), req)
}

func (p *ArchivistProxy) HasUnresolvedFeedback(
	ctx context.Context, req *flowv1.HasUnresolvedFeedbackRequest,
) (*flowv1.HasUnresolvedFeedbackResponse, error) {
	return p.client.HasUnresolvedFeedback(propagateMetadata(ctx), req)
}

func (p *ArchivistProxy) ResolveFeedback(
	ctx context.Context, req *flowv1.ResolveFeedbackRequest,
) (*flowv1.ResolveFeedbackResponse, error) {
	return p.client.ResolveFeedback(propagateMetadata(ctx), req)
}

func (p *ArchivistProxy) RefuseFeedback(
	ctx context.Context, req *flowv1.RefuseFeedbackRequest,
) (*flowv1.RefuseFeedbackResponse, error) {
	return p.client.RefuseFeedback(propagateMetadata(ctx), req)
}

func (p *ArchivistProxy) AcceptFix(
	ctx context.Context, req *flowv1.AcceptFixRequest,
) (*flowv1.AcceptFixResponse, error) {
	return p.client.AcceptFix(propagateMetadata(ctx), req)
}

func (p *ArchivistProxy) RejectFix(
	ctx context.Context, req *flowv1.RejectFixRequest,
) (*flowv1.RejectFixResponse, error) {
	return p.client.RejectFix(propagateMetadata(ctx), req)
}

func (p *ArchivistProxy) AcceptRefusal(
	ctx context.Context, req *flowv1.AcceptRefusalRequest,
) (*flowv1.AcceptRefusalResponse, error) {
	return p.client.AcceptRefusal(propagateMetadata(ctx), req)
}

func (p *ArchivistProxy) RejectRefusal(
	ctx context.Context, req *flowv1.RejectRefusalRequest,
) (*flowv1.RejectRefusalResponse, error) {
	return p.client.RejectRefusal(propagateMetadata(ctx), req)
}

func (p *ArchivistProxy) GetFeedbackDepth(
	ctx context.Context, req *flowv1.GetFeedbackDepthRequest,
) (*flowv1.GetFeedbackDepthResponse, error) {
	return p.client.GetFeedbackDepth(propagateMetadata(ctx), req)
}

func (p *ArchivistProxy) DeadlockFeedback(
	ctx context.Context, req *flowv1.DeadlockFeedbackRequest,
) (*flowv1.DeadlockFeedbackResponse, error) {
	return p.client.DeadlockFeedback(propagateMetadata(ctx), req)
}
