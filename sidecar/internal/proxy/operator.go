// Package proxy implements forwarding handlers that relay gRPC calls
// from the Sidecar to the real cluster services. Each handler wraps a
// generated gRPC client and propagates identity metadata
// (x-flow-workitem-id) from the incoming server context to the outgoing
// client context.
package proxy

import (
	"context"
	"log/slog"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// OperatorProxy implements flowv1.OperatorServiceServer by forwarding
// all calls to the real Operator gRPC endpoint.
type OperatorProxy struct {
	flowv1.UnimplementedOperatorServiceServer
	client flowv1.OperatorServiceClient
	conn   *grpc.ClientConn
}

// NewOperatorProxy dials the Operator gRPC endpoint and returns a proxy
// handler ready to be registered on the Sidecar's gRPC server.
func NewOperatorProxy(operatorAddr string) (*OperatorProxy, error) {
	conn, err := grpc.NewClient(
		operatorAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, err
	}

	return &OperatorProxy{
		client: flowv1.NewOperatorServiceClient(conn),
		conn:   conn,
	}, nil
}

// Close releases the underlying gRPC connection to the Operator.
func (p *OperatorProxy) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// SubmitResult forwards the routing instruction to the Operator.
// The x-flow-workitem-id metadata header is propagated from the incoming
// Node request to the outgoing Operator request.
func (p *OperatorProxy) SubmitResult(ctx context.Context, req *flowv1.SubmitResultRequest) (*flowv1.SubmitResultResponse, error) {
	outCtx := propagateMetadata(ctx)

	slog.Info("Forwarding SubmitResult to Operator",
		"workitem_id", req.GetWorkitemId(),
	)

	resp, err := p.client.SubmitResult(outCtx, req)
	if err != nil {
		slog.Error("SubmitResult forwarding failed", "error", err)
		return nil, err
	}

	slog.Info("SubmitResult forwarded successfully",
		"workitem_id", req.GetWorkitemId(),
		"accepted", resp.GetAccepted(),
	)
	return resp, nil
}

// CreateWorkitem forwards to the Operator.
func (p *OperatorProxy) CreateWorkitem(ctx context.Context, req *flowv1.CreateWorkitemRequest) (*flowv1.CreateWorkitemResponse, error) {
	outCtx := propagateMetadata(ctx)
	slog.Info("Forwarding CreateWorkitem to Operator")
	return p.client.CreateWorkitem(outCtx, req)
}

// CreateHearingWorkitem forwards to the Operator.
func (p *OperatorProxy) CreateHearingWorkitem(ctx context.Context, req *flowv1.CreateHearingWorkitemRequest) (*flowv1.CreateHearingWorkitemResponse, error) {
	outCtx := propagateMetadata(ctx)
	slog.Info("Forwarding CreateHearingWorkitem to Operator", "law_id", req.GetLawId())
	return p.client.CreateHearingWorkitem(outCtx, req)
}

// ExportWorkitem forwards to the Operator.
func (p *OperatorProxy) ExportWorkitem(ctx context.Context, req *flowv1.ExportWorkitemRequest) (*flowv1.ExportWorkitemResponse, error) {
	outCtx := propagateMetadata(ctx)
	slog.Info("Forwarding ExportWorkitem to Operator", "workitem_id", req.GetWorkitemId())
	return p.client.ExportWorkitem(outCtx, req)
}

// ImportWorkitem forwards to the Operator.
func (p *OperatorProxy) ImportWorkitem(ctx context.Context, req *flowv1.ImportWorkitemRequest) (*flowv1.ImportWorkitemResponse, error) {
	outCtx := propagateMetadata(ctx)
	slog.Info("Forwarding ImportWorkitem to Operator", "treaty", req.GetTreatyName())
	return p.client.ImportWorkitem(outCtx, req)
}

// propagateMetadata copies incoming gRPC metadata from the server context
// to outgoing metadata on a new client context. This is the critical bridge
// that carries the Sidecar-injected identity (x-flow-workitem-id) from the
// Node's request to the Operator.
func propagateMetadata(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	return metadata.NewOutgoingContext(ctx, md)
}
