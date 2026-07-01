package flow

import (
	"fmt"
	"os"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// session holds the internal gRPC connections and service clients for a Client.
// It manages connection lifecycle and workitem context injection.
type session struct {
	workitemID   string
	namespace    string
	conn         *grpc.ClientConn
	eventBusConn *grpc.ClientConn

	timeout    time.Duration
	maxRetries int

	// Exported gRPC service clients so methods in other files can access them.
	Sidecar        flowv1.SidecarServiceClient
	Operator       flowv1.OperatorServiceClient
	Archivist      flowv1.ArchivistServiceClient
	Librarian      flowv1.LibrarianServiceClient
	FrictionLedger flowv1.FrictionLedgerServiceClient
	EventBus       flowv1.FlowEventBusServiceClient
}

// newSession creates a session from the given client configuration.
// It reads workitem ID and namespace from environment variables and
// establishes gRPC connections to the Sidecar (and optionally the Event Bus).
func newSession(cfg *clientConfig) (*session, error) {
	workitemID := os.Getenv(EnvWorkitemID)
	namespace := os.Getenv(EnvFlowNamespace)

	// Read Event Bus address from env if not explicitly set.
	eventBusAddr := cfg.eventBusAddr
	if eventBusAddr == "" {
		eventBusAddr = os.Getenv(EnvEventBusAddress)
	}

	conn, err := grpc.NewClient(
		cfg.sidecarAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(workitemContextInterceptor(workitemID)),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"flow sdk: failed to connect to sidecar at %s: %w (is the sidecar running?)",
			cfg.sidecarAddr, err,
		)
	}

	sess := &session{
		workitemID:     workitemID,
		namespace:      namespace,
		conn:           conn,
		timeout:        cfg.timeout,
		maxRetries:     cfg.maxRetries,
		Sidecar:        flowv1.NewSidecarServiceClient(conn),
		Operator:       flowv1.NewOperatorServiceClient(conn),
		Archivist:      flowv1.NewArchivistServiceClient(conn),
		Librarian:      flowv1.NewLibrarianServiceClient(conn),
		FrictionLedger: flowv1.NewFrictionLedgerServiceClient(conn),
	}

	// Optionally connect to Event Bus for streaming operations.
	if eventBusAddr != "" {
		ebConn, ebErr := grpc.NewClient(
			eventBusAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if ebErr != nil {
			_ = conn.Close()
			return nil, fmt.Errorf(
				"flow sdk: failed to connect to event bus at %s: %w",
				eventBusAddr, ebErr,
			)
		}
		sess.eventBusConn = ebConn
		sess.EventBus = flowv1.NewFlowEventBusServiceClient(ebConn)
	}

	return sess, nil
}

// Close releases the underlying gRPC connections.
func (s *session) Close() error {
	var firstErr error
	if s.eventBusConn != nil {
		if err := s.eventBusConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.conn != nil {
		if err := s.conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
