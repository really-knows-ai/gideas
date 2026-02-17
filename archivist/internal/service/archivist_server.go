// Package service implements the Archivist gRPC server.
//
// The Archivist is the "Memory" of the Flow system. It separates Content
// (raw bytes, deduplicated by SHA-256 hash) from Provenance (version history
// keyed by workitem + artefact). This Content-Addressable Storage (CAS)
// architecture ensures that identical payloads are stored once, while each
// artefact maintains its own ordered version history.
package service

import (
	"context"
	"log/slog"

	"github.com/gideas/flow/archivist/internal/store"
	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ArchivistServer implements flowv1.ArchivistServiceServer backed by
// an in-memory CAS store.
type ArchivistServer struct {
	flowv1.UnimplementedArchivistServiceServer
	store *store.MemoryStore
}

// NewArchivistServer returns an ArchivistServer backed by the given store.
func NewArchivistServer(s *store.MemoryStore) *ArchivistServer {
	return &ArchivistServer{store: s}
}

// StoreArtefact persists content and creates a version record.
//
// The content_hash is Sidecar-computed (not node-supplied). Logic:
//  1. If content_hash is not in BlobStore, write content.
//  2. Look up provenance history for (workitem_id, artefact_id).
//  3. If history is empty OR head hash != content_hash: append new version, is_new_version=true.
//  4. If head hash == content_hash: no-op, is_new_version=false.
//  5. Return the version_hash (which equals content_hash for the stored version).
func (s *ArchivistServer) StoreArtefact(_ context.Context, req *flowv1.StoreArtefactRequest) (*flowv1.StoreArtefactResponse, error) {
	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()
	contentHash := req.GetContentHash()
	kind := req.GetKind()

	slog.Info("StoreArtefact",
		"workitem_id", workitemID,
		"artefact_id", artefactID,
		"content_hash", contentHash,
		"kind", kind,
	)

	// Step 1: Store blob (deduplicated by hash).
	s.store.StoreBlob(contentHash, req.GetContent())

	// Step 2: Check provenance history.
	head := s.store.GetHead(workitemID, artefactID)

	// Step 3: Compare — only append if this is actually a new version.
	if head != nil && head.Hash == contentHash {
		slog.Info("StoreArtefact: content unchanged, no new version",
			"workitem_id", workitemID,
			"artefact_id", artefactID,
		)
		return &flowv1.StoreArtefactResponse{
			VersionHash:  contentHash,
			IsNewVersion: false,
		}, nil
	}

	// New version — append to history.
	s.store.AppendVersion(workitemID, artefactID, contentHash, kind)

	slog.Info("StoreArtefact: new version created",
		"workitem_id", workitemID,
		"artefact_id", artefactID,
		"version_hash", contentHash,
	)

	return &flowv1.StoreArtefactResponse{
		VersionHash:  contentHash,
		IsNewVersion: true,
	}, nil
}

// GetArtefact returns the latest version's content bytes.
//
// Steps:
//  1. Look up provenance history for (workitem_id, artefact_id).
//  2. If empty, return NotFound.
//  3. Get head hash, retrieve bytes from BlobStore.
func (s *ArchivistServer) GetArtefact(_ context.Context, req *flowv1.GetArtefactRequest) (*flowv1.GetArtefactResponse, error) {
	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()

	slog.Info("GetArtefact",
		"workitem_id", workitemID,
		"artefact_id", artefactID,
	)

	head := s.store.GetHead(workitemID, artefactID)
	if head == nil {
		return nil, status.Errorf(codes.NotFound,
			"artefact %q not found for workitem %q", artefactID, workitemID)
	}

	data, ok := s.store.GetBlob(head.Hash)
	if !ok {
		// This should never happen — provenance points to a hash that was stored.
		return nil, status.Errorf(codes.Internal,
			"blob %q referenced by artefact %q not found (data corruption)", head.Hash, artefactID)
	}

	return &flowv1.GetArtefactResponse{
		Content:     data,
		VersionHash: head.Hash,
		Kind:        head.Kind,
	}, nil
}

// GetArtefactVersion returns content bytes for a specific version by hash.
func (s *ArchivistServer) GetArtefactVersion(_ context.Context, req *flowv1.GetArtefactVersionRequest) (*flowv1.GetArtefactVersionResponse, error) {
	versionHash := req.GetVersionHash()

	slog.Info("GetArtefactVersion", "version_hash", versionHash)

	data, ok := s.store.GetBlob(versionHash)
	if !ok {
		return nil, status.Errorf(codes.NotFound,
			"version %q not found", versionHash)
	}

	return &flowv1.GetArtefactVersionResponse{
		Content: data,
	}, nil
}

// GetArtefactMetadata returns version history and stamps (stamps stubbed for now).
func (s *ArchivistServer) GetArtefactMetadata(_ context.Context, req *flowv1.GetArtefactMetadataRequest) (*flowv1.GetArtefactMetadataResponse, error) {
	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()

	history := s.store.GetHistory(workitemID, artefactID)
	if history == nil {
		return nil, status.Errorf(codes.NotFound,
			"artefact %q not found for workitem %q", artefactID, workitemID)
	}

	entries := make([]*flowv1.VersionEntry, 0, len(history))
	for _, v := range history {
		entries = append(entries, &flowv1.VersionEntry{
			VersionHash: v.Hash,
			CreatedAt:   timestamppb.New(v.CreatedAt),
		})
	}

	return &flowv1.GetArtefactMetadataResponse{
		VersionHistory: entries,
		Stamps:         nil, // Stamps not yet implemented.
	}, nil
}

// ListArtefacts returns all artefact refs for a workitem.
func (s *ArchivistServer) ListArtefacts(_ context.Context, req *flowv1.ListArtefactsRequest) (*flowv1.ListArtefactsResponse, error) {
	workitemID := req.GetWorkitemId()

	slog.Info("ListArtefacts", "workitem_id", workitemID)

	entries := s.store.ListArtefacts(workitemID)

	refs := make([]*flowv1.ArtefactRef, 0, len(entries))
	for _, e := range entries {
		refs = append(refs, &flowv1.ArtefactRef{
			Id:   e.ID,
			Kind: e.Kind,
		})
	}

	return &flowv1.ListArtefactsResponse{
		ArtefactRefs: refs,
	}, nil
}

// QueryArtefactState returns artefact presence and stamp state for
// exit contract validation. Called by the Operator.
func (s *ArchivistServer) QueryArtefactState(_ context.Context, req *flowv1.QueryArtefactStateRequest) (*flowv1.QueryArtefactStateResponse, error) {
	workitemID := req.GetWorkitemId()

	slog.Info("QueryArtefactState", "workitem_id", workitemID)

	entries := s.store.ListArtefacts(workitemID)
	states := make([]*flowv1.ArtefactState, 0, len(entries))

	for _, e := range entries {
		head := s.store.GetHead(workitemID, e.ID)
		if head == nil {
			continue
		}
		states = append(states, &flowv1.ArtefactState{
			ArtefactId:         e.ID,
			Kind:               e.Kind,
			StampNames:         nil, // Stamps not yet implemented.
			CurrentVersionHash: head.Hash,
		})
	}

	return &flowv1.QueryArtefactStateResponse{
		ArtefactStates: states,
	}, nil
}

// ---------------------------------------------------------------------------
// Stamp Method Stubs
// ---------------------------------------------------------------------------

func (s *ArchivistServer) GetStamps(_ context.Context, _ *flowv1.GetStampsRequest) (*flowv1.GetStampsResponse, error) {
	return &flowv1.GetStampsResponse{}, nil
}

func (s *ArchivistServer) HasStamp(_ context.Context, _ *flowv1.HasStampRequest) (*flowv1.HasStampResponse, error) {
	return &flowv1.HasStampResponse{Exists: false}, nil
}

func (s *ArchivistServer) StampArtefact(_ context.Context, _ *flowv1.StampArtefactRequest) (*flowv1.StampArtefactResponse, error) {
	return &flowv1.StampArtefactResponse{}, nil
}

// ---------------------------------------------------------------------------
// Feedback Method Stubs
// ---------------------------------------------------------------------------

func (s *ArchivistServer) AddFeedback(_ context.Context, _ *flowv1.AddFeedbackRequest) (*flowv1.AddFeedbackResponse, error) {
	return &flowv1.AddFeedbackResponse{FeedbackId: "stub"}, nil
}

func (s *ArchivistServer) GetFeedback(_ context.Context, _ *flowv1.GetFeedbackRequest) (*flowv1.GetFeedbackResponse, error) {
	return &flowv1.GetFeedbackResponse{}, nil
}

func (s *ArchivistServer) HasUnresolvedFeedback(_ context.Context, _ *flowv1.HasUnresolvedFeedbackRequest) (*flowv1.HasUnresolvedFeedbackResponse, error) {
	return &flowv1.HasUnresolvedFeedbackResponse{HasUnresolved: false}, nil
}

func (s *ArchivistServer) ResolveFeedback(_ context.Context, _ *flowv1.ResolveFeedbackRequest) (*flowv1.ResolveFeedbackResponse, error) {
	return &flowv1.ResolveFeedbackResponse{}, nil
}

func (s *ArchivistServer) RefuseFeedback(_ context.Context, _ *flowv1.RefuseFeedbackRequest) (*flowv1.RefuseFeedbackResponse, error) {
	return &flowv1.RefuseFeedbackResponse{}, nil
}

func (s *ArchivistServer) AcceptFix(_ context.Context, _ *flowv1.AcceptFixRequest) (*flowv1.AcceptFixResponse, error) {
	return &flowv1.AcceptFixResponse{}, nil
}

func (s *ArchivistServer) RejectFix(_ context.Context, _ *flowv1.RejectFixRequest) (*flowv1.RejectFixResponse, error) {
	return &flowv1.RejectFixResponse{}, nil
}

func (s *ArchivistServer) AcceptRefusal(_ context.Context, _ *flowv1.AcceptRefusalRequest) (*flowv1.AcceptRefusalResponse, error) {
	return &flowv1.AcceptRefusalResponse{}, nil
}

func (s *ArchivistServer) RejectRefusal(_ context.Context, _ *flowv1.RejectRefusalRequest) (*flowv1.RejectRefusalResponse, error) {
	return &flowv1.RejectRefusalResponse{}, nil
}

func (s *ArchivistServer) GetFeedbackDepth(_ context.Context, _ *flowv1.GetFeedbackDepthRequest) (*flowv1.GetFeedbackDepthResponse, error) {
	return &flowv1.GetFeedbackDepthResponse{Depth: 0}, nil
}

func (s *ArchivistServer) DeadlockFeedback(_ context.Context, _ *flowv1.DeadlockFeedbackRequest) (*flowv1.DeadlockFeedbackResponse, error) {
	return &flowv1.DeadlockFeedbackResponse{}, nil
}
