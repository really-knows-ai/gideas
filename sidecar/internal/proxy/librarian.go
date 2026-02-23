package proxy

import (
	"context"
	"log/slog"
	"os"
	"strconv"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

const (
	defaultCitationFrictionMagnitude = 1
	envCitationFrictionMagnitude     = "CITATION_FRICTION_MAGNITUDE"
)

// LibrarianProxy implements flowv1.LibrarianServiceServer by forwarding
// calls to the real Librarian gRPC endpoint. For Cite, it also emits a
// friction event to the Event Bus via the EventBusProxy.
type LibrarianProxy struct {
	flowv1.UnimplementedLibrarianServiceServer
	client        flowv1.LibrarianServiceClient
	eventBusProxy *EventBusProxy
	conn          *grpc.ClientConn
	magnitude     float64
}

// NewLibrarianProxy dials the Librarian gRPC endpoint and returns a proxy
// handler ready to be registered on the Sidecar's gRPC server. The
// eventBusProxy is used to publish friction events on Cite calls; if nil,
// friction emission is skipped.
func NewLibrarianProxy(librarianAddr string, eventBusProxy *EventBusProxy) (*LibrarianProxy, error) {
	conn, err := grpc.NewClient(
		librarianAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, err
	}

	return &LibrarianProxy{
		client:        flowv1.NewLibrarianServiceClient(conn),
		eventBusProxy: eventBusProxy,
		conn:          conn,
		magnitude:     citationMagnitude(),
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
	return p.client.QueryLaws(propagateMetadata(ctx), req)
}

// Cite forwards to the Librarian and then emits a friction event to the
// Event Bus via the EventBusProxy with fixed citation magnitude.
func (p *LibrarianProxy) Cite(ctx context.Context, req *flowv1.CiteRequest) (*flowv1.CiteResponse, error) {
	outCtx := propagateMetadata(ctx)

	// Forward to Librarian.
	resp, err := p.client.Cite(outCtx, req)
	if err != nil {
		return nil, err
	}

	// Emit friction to Event Bus.
	if p.eventBusProxy != nil {
		flowID, workitemID, nodeID := extractIdentityFromMetadata(ctx)

		frictionErr := p.eventBusProxy.PublishFriction(
			outCtx,
			flowID, workitemID, nodeID,
			req.GetLawIds(),
			p.magnitude,
		)
		if frictionErr != nil {
			slog.Warn("Cite: failed to emit friction to Event Bus",
				"error", frictionErr,
				"law_ids", req.GetLawIds(),
			)
		} else {
			slog.Info("Cite: friction emitted",
				"law_ids", req.GetLawIds(),
				"magnitude", p.magnitude,
			)
		}
	}

	return resp, nil
}

// RecordFinding forwards to the Librarian (passthrough).
func (p *LibrarianProxy) RecordFinding(
	ctx context.Context, req *flowv1.RecordFindingRequest,
) (*flowv1.RecordFindingResponse, error) {
	return p.client.RecordFinding(propagateMetadata(ctx), req)
}

// GetLaw forwards to the Librarian (passthrough).
func (p *LibrarianProxy) GetLaw(ctx context.Context, req *flowv1.GetLawRequest) (*flowv1.GetLawResponse, error) {
	return p.client.GetLaw(propagateMetadata(ctx), req)
}

// WriteLaw forwards to the Librarian (passthrough).
func (p *LibrarianProxy) WriteLaw(ctx context.Context, req *flowv1.WriteLawRequest) (*flowv1.WriteLawResponse, error) {
	return p.client.WriteLaw(propagateMetadata(ctx), req)
}

// RetireLaw forwards to the Librarian (passthrough).
func (p *LibrarianProxy) RetireLaw(
	ctx context.Context, req *flowv1.RetireLawRequest,
) (*flowv1.RetireLawResponse, error) {
	return p.client.RetireLaw(propagateMetadata(ctx), req)
}

// ReplicateLaws forwards to the Librarian (passthrough).
func (p *LibrarianProxy) ReplicateLaws(
	ctx context.Context, req *flowv1.ReplicateLawsRequest,
) (*flowv1.ReplicateLawsResponse, error) {
	return p.client.ReplicateLaws(propagateMetadata(ctx), req)
}

// ApplyLifecycleAction forwards to the Librarian (passthrough).
func (p *LibrarianProxy) ApplyLifecycleAction(
	ctx context.Context, req *flowv1.ApplyLifecycleActionRequest,
) (*flowv1.ApplyLifecycleActionResponse, error) {
	return p.client.ApplyLifecycleAction(propagateMetadata(ctx), req)
}

// ---------------------------------------------------------------------------
// Identity extraction from metadata
// ---------------------------------------------------------------------------

func extractIdentityFromMetadata(ctx context.Context) (flowID, workitemID, nodeID string) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return
	}
	if vals := md.Get("x-flow-flow-id"); len(vals) > 0 {
		flowID = vals[0]
	}
	if vals := md.Get("x-flow-workitem-id"); len(vals) > 0 {
		workitemID = vals[0]
	}
	if vals := md.Get("x-flow-node-id"); len(vals) > 0 {
		nodeID = vals[0]
	}
	return
}
