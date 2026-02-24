package proxy

import (
	"context"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// JuryProxy implements flowv1.JuryServiceServer by forwarding calls to the
// real Jury gRPC endpoint. All RPCs are simple passthroughs with metadata
// propagation.
type JuryProxy struct {
	flowv1.UnimplementedJuryServiceServer
	client flowv1.JuryServiceClient
	conn   *grpc.ClientConn
}

// NewJuryProxy dials the Jury gRPC endpoint and returns a proxy handler
// ready to be registered on the Sidecar's gRPC server.
func NewJuryProxy(juryAddr string) (*JuryProxy, error) {
	conn, err := grpc.NewClient(
		juryAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, err
	}

	return &JuryProxy{
		client: flowv1.NewJuryServiceClient(conn),
		conn:   conn,
	}, nil
}

// Close releases the underlying gRPC connection to the Jury.
func (p *JuryProxy) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// Deliberate forwards to the Jury (passthrough).
func (p *JuryProxy) Deliberate(
	ctx context.Context, req *flowv1.DeliberateRequest,
) (*flowv1.DeliberateResponse, error) {
	return p.client.Deliberate(propagateMetadata(ctx), req)
}
