package proxy

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// dialService creates a gRPC client connection with the standard Sidecar
// dial options: insecure transport and metadata propagation interceptors.
func dialService(addr string) (*grpc.ClientConn, error) {
	return grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(metadataUnaryInterceptor),
		grpc.WithStreamInterceptor(metadataStreamInterceptor),
	)
}
