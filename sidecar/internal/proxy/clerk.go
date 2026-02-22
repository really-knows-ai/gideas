package proxy

import (
	"context"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ClerkProxy implements flowv1.ClerkServiceServer by forwarding calls to the
// real Clerk gRPC endpoint. All RPCs are simple passthroughs with metadata
// propagation.
type ClerkProxy struct {
	flowv1.UnimplementedClerkServiceServer
	client flowv1.ClerkServiceClient
	conn   *grpc.ClientConn
}

// NewClerkProxy dials the Clerk gRPC endpoint and returns a proxy handler
// ready to be registered on the Sidecar's gRPC server.
func NewClerkProxy(clerkAddr string) (*ClerkProxy, error) {
	conn, err := grpc.NewClient(
		clerkAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, err
	}

	return &ClerkProxy{
		client: flowv1.NewClerkServiceClient(conn),
		conn:   conn,
	}, nil
}

// Close releases the underlying gRPC connection to the Clerk.
func (p *ClerkProxy) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// DraftLaw forwards to the Clerk (passthrough).
func (p *ClerkProxy) DraftLaw(
	ctx context.Context, req *flowv1.DraftLawRequest,
) (*flowv1.DraftLawResponse, error) {
	return p.client.DraftLaw(propagateMetadata(ctx), req)
}
