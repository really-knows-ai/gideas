package flow

import (
	"context"
	"fmt"
	"os"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// session holds the internal gRPC connections and service clients for a Client.
// It manages connection lifecycle and workitem context injection.
type session struct {
	workitemID   string
	namespace    string
	conn         *grpc.ClientConn
	eventBusConn *grpc.ClientConn

	timeout time.Duration

	// Base context for Cartographer operations, cancelled on Close().
	// ponytail: Cartographer operations use a session-scoped base context;
	// non-Cartographer domain objects use per-call background contexts.
	// A follow-up pass should unify all domain objects under this context.
	ctx    context.Context
	cancel context.CancelFunc

	// Exported gRPC service clients so methods in other files can access them.
	Sidecar        flowv1.SidecarServiceClient
	Operator       flowv1.OperatorServiceClient
	Archivist      flowv1.ArchivistServiceClient
	Librarian      flowv1.LibrarianServiceClient
	FrictionLedger flowv1.FrictionLedgerServiceClient
	EventBus       flowv1.FlowEventBusServiceClient
	Cartographer   flowv1.CartographerServiceClient
}

// newSession creates a session from the given client configuration.
// It reads workitem ID and namespace from environment variables and
// establishes gRPC connections to the Sidecar (and optionally the Event Bus).
func newSession(cfg *clientConfig) (*session, error) {
	workitemID := os.Getenv(EnvWorkitemID)
	if cfg.workitemID != "" {
		workitemID = cfg.workitemID
	}
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

	sessCtx, sessCancel := context.WithCancel(context.Background())

	sess := &session{
		workitemID:     workitemID,
		namespace:      namespace,
		conn:           conn,
		timeout:        cfg.timeout,
		ctx:            sessCtx,
		cancel:         sessCancel,
		Sidecar:        flowv1.NewSidecarServiceClient(conn),
		Operator:       flowv1.NewOperatorServiceClient(conn),
		Archivist:      flowv1.NewArchivistServiceClient(conn),
		Librarian:      flowv1.NewLibrarianServiceClient(conn),
		FrictionLedger: flowv1.NewFrictionLedgerServiceClient(conn),
		Cartographer:   flowv1.NewCartographerServiceClient(conn),
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
	// Cancel the session base context first so outstanding streams are
	// cleaned up before the gRPC connections are closed.
	if s.cancel != nil {
		s.cancel()
	}

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

// call invokes fn with the given context, annotating it with entity-type
// metadata when a key is supplied. fn must be a closure over a typed
// CartographerServiceClient method to preserve compile-time type safety.
// key is the metadata key (metadata.MetadataKeyEntityType per the
// operation-specific table); it is empty for RPCs that annotate nothing.
// types are the entity type(s) required for capability resolution by the
// Sidecar proxy. Each type is appended as its own metadata value.
// ExecuteCypher passes no key and no types: the SDK attaches no entity-type
// metadata for it (SPEC R3 — the Cartographer derives the types from its own
// server-side parse of the statement).
func (s *session) call(ctx context.Context, fn func(context.Context) error, key string, types ...string) error {
	if len(types) > 0 && key != "" {
		kv := make([]string, 0, 2*len(types))
		for _, t := range types {
			kv = append(kv, key, t)
		}
		ctx = metadata.AppendToOutgoingContext(ctx, kv...)
	}
	// Apply per-call timeout from the session config.
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	return fn(ctx)
}
