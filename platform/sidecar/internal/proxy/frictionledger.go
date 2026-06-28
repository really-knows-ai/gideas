package proxy

import (
	"context"
	"log/slog"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

// FrictionLedgerProxy implements flowv1.FrictionLedgerServiceServer by
// forwarding calls to the real Friction Ledger gRPC endpoint. The Sidecar
// exposes this as a passthrough so that SDK clients can query friction data
// through the Sidecar without knowing the Friction Ledger's address.
type FrictionLedgerProxy struct {
	flowv1.UnimplementedFrictionLedgerServiceServer
	client flowv1.FrictionLedgerServiceClient
	conn   *grpc.ClientConn
}

// NewFrictionLedgerProxy dials the Friction Ledger gRPC endpoint and returns
// a proxy handler ready to be registered on the Sidecar's gRPC server.
func NewFrictionLedgerProxy(frictionLedgerAddr string) (*FrictionLedgerProxy, error) {
	conn, err := dialService(frictionLedgerAddr)
	if err != nil {
		return nil, err
	}

	return &FrictionLedgerProxy{
		client: flowv1.NewFrictionLedgerServiceClient(conn),
		conn:   conn,
	}, nil
}

// Close releases the underlying gRPC connection to the Friction Ledger.
func (p *FrictionLedgerProxy) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// QueryFriction forwards to the upstream Friction Ledger (passthrough with
// metadata propagation).
func (p *FrictionLedgerProxy) QueryFriction(
	ctx context.Context, req *flowv1.QueryFrictionRequest,
) (*flowv1.QueryFrictionResponse, error) {
	slog.Info("Forwarding QueryFriction to Friction Ledger")
	return p.client.QueryFriction(ctx, req)
}
