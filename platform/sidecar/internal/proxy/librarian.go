package proxy

import (
	"context"
	"log/slog"
	"os"
	"strconv"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/sidecar/internal/buffer"
	"github.com/gideas/flow/sidecar/internal/service"
	"google.golang.org/grpc"
)

const (
	defaultCitationFrictionMagnitude = 1
	envCitationFrictionMagnitude     = "CITATION_FRICTION_MAGNITUDE"
)

// LibrarianProxy implements flowv1.LibrarianServiceServer by forwarding
// calls to the real Librarian gRPC endpoint. For Cite, it also submits a
// friction event to the TelemetryBuffer.
type LibrarianProxy struct {
	flowv1.UnimplementedLibrarianServiceServer
	client          flowv1.LibrarianServiceClient
	telemetryBuffer *buffer.TelemetryBuffer
	conn            *grpc.ClientConn
	magnitude       float64
}

// NewLibrarianProxy dials the Librarian gRPC endpoint and returns a proxy
// handler ready to be registered on the Sidecar's gRPC server. The
// telemetryBuffer is used to submit friction events on Cite calls; if nil,
// friction emission is skipped.
func NewLibrarianProxy(librarianAddr string, telemetryBuffer *buffer.TelemetryBuffer) (*LibrarianProxy, error) {
	conn, err := dialService(librarianAddr)
	if err != nil {
		return nil, err
	}

	return &LibrarianProxy{
		client:          flowv1.NewLibrarianServiceClient(conn),
		telemetryBuffer: telemetryBuffer,
		conn:            conn,
		magnitude:       citationMagnitude(),
	}, nil
}

// Close releases the underlying gRPC connection.
func (p *LibrarianProxy) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

func citationMagnitude() float64 {
	if s := os.Getenv(envCitationFrictionMagnitude); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
			return v
		}
	}
	return defaultCitationFrictionMagnitude
}

// ---------------------------------------------------------------------------
// RPC forwarding
// ---------------------------------------------------------------------------

// QueryLaws forwards to the Librarian (passthrough).
func (p *LibrarianProxy) QueryLaws(
	ctx context.Context, req *flowv1.QueryLawsRequest,
) (*flowv1.QueryLawsResponse, error) {
	return p.client.QueryLaws(ctx, req)
}

// Cite forwards to the Librarian and then submits a friction event to the
// TelemetryBuffer with fixed citation magnitude.
func (p *LibrarianProxy) Cite(ctx context.Context, req *flowv1.CiteRequest) (*flowv1.CiteResponse, error) {
	// Forward to Librarian.
	resp, err := p.client.Cite(ctx, req)
	if err != nil {
		return nil, err
	}

	// Submit friction to TelemetryBuffer.
	if p.telemetryBuffer != nil {
		namespace, workitemID, nodeID := service.ExtractIdentityFromMD(ctx)

		p.telemetryBuffer.Submit(buffer.Event{
			Priority:   buffer.PriorityHigh,
			Namespace:  namespace,
			WorkitemID: workitemID,
			NodeID:     nodeID,
			LawIDs:     req.GetLawIds(),
			Magnitude:  p.magnitude,
		})
		slog.Info("Cite: friction submitted",
			"law_ids", req.GetLawIds(),
			"magnitude", p.magnitude,
		)
	}

	return resp, nil
}

// RecordFinding forwards to the Librarian (passthrough).
func (p *LibrarianProxy) RecordFinding(
	ctx context.Context, req *flowv1.RecordFindingRequest,
) (*flowv1.RecordFindingResponse, error) {
	return p.client.RecordFinding(ctx, req)
}

// GetLaw forwards to the Librarian (passthrough).
func (p *LibrarianProxy) GetLaw(ctx context.Context, req *flowv1.GetLawRequest) (*flowv1.GetLawResponse, error) {
	return p.client.GetLaw(ctx, req)
}

// WriteLaw forwards to the Librarian (passthrough).
func (p *LibrarianProxy) WriteLaw(ctx context.Context, req *flowv1.WriteLawRequest) (*flowv1.WriteLawResponse, error) {
	return p.client.WriteLaw(ctx, req)
}

// RetireLaw forwards to the Librarian (passthrough).
func (p *LibrarianProxy) RetireLaw(
	ctx context.Context, req *flowv1.RetireLawRequest,
) (*flowv1.RetireLawResponse, error) {
	return p.client.RetireLaw(ctx, req)
}

// ReplicateLaws forwards to the Librarian (passthrough).
func (p *LibrarianProxy) ReplicateLaws(
	ctx context.Context, req *flowv1.ReplicateLawsRequest,
) (*flowv1.ReplicateLawsResponse, error) {
	return p.client.ReplicateLaws(ctx, req)
}

// ApplyLifecycleAction forwards to the Librarian (passthrough).
func (p *LibrarianProxy) ApplyLifecycleAction(
	ctx context.Context, req *flowv1.ApplyLifecycleActionRequest,
) (*flowv1.ApplyLifecycleActionResponse, error) {
	return p.client.ApplyLifecycleAction(ctx, req)
}

// GetActiveDisputes forwards to the Librarian (passthrough).
func (p *LibrarianProxy) GetActiveDisputes(
	ctx context.Context, req *flowv1.GetActiveDisputesRequest,
) (*flowv1.GetActiveDisputesResponse, error) {
	return p.client.GetActiveDisputes(ctx, req)
}

// SearchSimilarLaws forwards to the Librarian (passthrough).
func (p *LibrarianProxy) SearchSimilarLaws(
	ctx context.Context, req *flowv1.SearchSimilarLawsRequest,
) (*flowv1.SearchSimilarLawsResponse, error) {
	return p.client.SearchSimilarLaws(ctx, req)
}
