// Package service implements the LibrarianService gRPC server.
//
// The Librarian manages the Flow's body of law: creation, versioning,
// querying, retirement, and lifecycle actions. It integrates optional
// embedding-based conflict detection for duplicate Findings.
package service

import (
	"sync"

	"github.com/foundry/flow/librarian/internal/embed"
	"github.com/foundry/flow/librarian/internal/store/sqlite"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/pkg/randid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// IDGenerator produces unique law identifiers.
type IDGenerator func() string

// ConflictCandidate represents a potential duplicate law found by
// embedding similarity search.
type ConflictCandidate struct {
	LawID      string
	Similarity float64
}

// AuditPublisher provides non-blocking audit event submission to the Event Bus.
// Satisfied by *eventbus.AsyncPublisher. A nil publisher silently disables
// audit publishing.
type AuditPublisher interface {
	Submit(req *flowv1.PublishRequest)
}

// LibrarianServer implements flowv1.LibrarianServiceServer backed by a
// SQLite store and optional embedder for conflict detection.
type LibrarianServer struct {
	flowv1.UnimplementedLibrarianServiceServer
	store               *sqlite.Store
	embedder            embed.Embedder // nil-safe: conflict detection degrades gracefully
	newID               IDGenerator
	similarityThreshold float64
	auditor             AuditPublisher // nil-safe: audit publishing degrades gracefully
	bgWg                sync.WaitGroup // tracks in-flight background goroutines
}

// NewLibrarianServer returns a LibrarianServer backed by the given store.
// The embedder may be nil; embedding operations will degrade gracefully.
// The idGen function produces unique law identifiers.
func NewLibrarianServer(
	store *sqlite.Store, embedder embed.Embedder,
	idGen IDGenerator, similarityThreshold float64,
	opts ...LibrarianOption,
) *LibrarianServer {
	if similarityThreshold <= 0 {
		similarityThreshold = 0.85
	}
	srv := &LibrarianServer{
		store:               store,
		embedder:            embedder,
		newID:               idGen,
		similarityThreshold: similarityThreshold,
	}
	for _, o := range opts {
		o(srv)
	}
	return srv
}

// Wait blocks until all in-flight background goroutines (e.g. conflict
// detection) have completed. Callers should invoke Wait before closing the
// underlying store to avoid accessing a closed database.
func (s *LibrarianServer) Wait() { s.bgWg.Wait() }

// LibrarianOption configures a LibrarianServer.
type LibrarianOption func(*LibrarianServer)

// WithAuditPublisher sets the Event Bus client for audit event publishing.
func WithAuditPublisher(pub AuditPublisher) LibrarianOption {
	return func(s *LibrarianServer) { s.auditor = pub }
}

// publishAudit submits an audit event to the async publisher for non-blocking
// delivery to the Event Bus. If the publisher is nil, audit publishing is
// silently disabled.
func (s *LibrarianServer) publishAudit(eventType string, attrs map[string]string) {
	if s.auditor == nil {
		return
	}
	s.auditor.Submit(&flowv1.PublishRequest{
		Channel: "audit",
		Event: &flowv1.FlowEvent{
			EventId:    randid.NewRandomID(),
			EventType:  eventType,
			Timestamp:  timestamppb.Now(),
			Attributes: attrs,
		},
	})
}
