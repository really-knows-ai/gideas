// Clerk is the legislative drafter of Foundry Flow's Judiciary — a standalone
// gRPC service that converts jury verdicts into law representations and
// persists them via the Librarian.
//
// It listens on port 50058 (configurable via CLERK_PORT) and implements the
// ClerkService.DraftLaw RPC. The Clerk drafts text/markdown prose from verdict
// justifications and delegates persistence to the Librarian.
//
// Usage:
//
//	go run ./clerk/cmd/main.go
//	CLERK_PORT=50058 LIBRARIAN_ADDRESS=flow-librarian:50056 go run ./clerk/cmd/main.go
package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/gideas/flow/clerk/internal/service"
	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
)

const (
	defaultPort             = "50058"
	defaultLibrarianAddress = "flow-librarian:50056"

	envPort             = "CLERK_PORT"
	envLibrarianAddress = "LIBRARIAN_ADDRESS"
)

func main() {
	port := os.Getenv(envPort)
	if port == "" {
		port = defaultPort
	}

	librarianAddr := os.Getenv(envLibrarianAddress)
	if librarianAddr == "" {
		librarianAddr = defaultLibrarianAddress
	}

	slog.Info("Clerk starting", "port", port, "librarian_address", librarianAddr)

	// Connect to Librarian for law persistence.
	libConn, err := grpc.NewClient(
		librarianAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		slog.Error("Failed to connect to Librarian", "address", librarianAddr, "error", err)
		os.Exit(1)
	}
	defer func() { _ = libConn.Close() }()

	libClient := flowv1.NewLibrarianServiceClient(libConn)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		slog.Error("Failed to listen", "port", port, "error", err)
		os.Exit(1)
	}

	srv := grpc.NewServer()

	clerkSrv := service.NewClerkServer(libClient)
	flowv1.RegisterClerkServiceServer(srv, clerkSrv)

	// Enable gRPC reflection for debugging with grpcurl.
	reflection.Register(srv)

	// Graceful shutdown on SIGTERM/SIGINT.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		slog.Info("Received signal, shutting down gracefully", "signal", sig)
		srv.GracefulStop()
	}()

	slog.Info("Clerk listening", "address", lis.Addr().String())
	if err := srv.Serve(lis); err != nil {
		slog.Error("Clerk server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Clerk stopped")
}
