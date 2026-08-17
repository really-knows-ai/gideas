// Package service implements the Archivist gRPC server.
package service

import (
	"context"
	"log/slog"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// StoreArtefact persists content and creates a version record.
//
// The content_hash is Sidecar-computed (not node-supplied). Logic:
//  1. If content_hash is not in BlobStore, write content.
//  2. Look up provenance history for (workitem_id, artefact_id).
//  3. If history is empty OR head hash != content_hash: append new version, is_new_version=true.
//  4. If head hash == content_hash: no-op, is_new_version=false.
//  5. Return the version_hash (which equals content_hash for the stored version).
func (s *ArchivistServer) StoreArtefact(
	ctx context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()
	contentHash := req.GetContentHash()
	kind := req.GetGovernedArtefact()

	// Capability gate: WRITE:artefact or WRITE:artefact/<governed_artefact>.
	if err := checkCapabilityAny(ctx, "WRITE:artefact", "WRITE:artefact/"+kind); err != nil {
		return nil, err
	}

	slog.Info("StoreArtefact",
		"workitem_id", workitemID,
		"artefact_id", artefactID,
		"content_hash", contentHash,
		"governed_artefact", kind,
	)

	// Step 1: Store blob (deduplicated by hash).
	if _, err := s.store.StoreBlob(ctx, contentHash, req.GetContent()); err != nil {
		return nil, status.Errorf(codes.Internal, "store blob: %v", err)
	}

	// Step 2: Check provenance history.
	head, err := s.store.GetHead(ctx, workitemID, artefactID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get head: %v", err)
	}

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
	if err := s.store.AppendVersion(ctx, workitemID, artefactID, contentHash, kind); err != nil {
		return nil, status.Errorf(codes.Internal, "append version: %v", err)
	}

	slog.Info("StoreArtefact: new version created",
		"workitem_id", workitemID,
		"artefact_id", artefactID,
		"version_hash", contentHash,
	)

	// Auto-resolve stale feedback tied to older versions.
	if n, err := s.store.ResolveStaleFeedback(ctx, workitemID, artefactID, contentHash); err != nil {
		slog.Error("StoreArtefact: failed to resolve stale feedback",
			"workitem_id", workitemID,
			"artefact_id", artefactID,
			"error", err,
		)
	} else if n > 0 {
		slog.Info("StoreArtefact: resolved stale feedback",
			"workitem_id", workitemID,
			"artefact_id", artefactID,
			"resolved_stale_count", n,
		)
	}

	s.publishAudit(ctx, "audit.artefact.version_created", map[string]string{
		"action":      "version_created",
		"resource_id": artefactID,
		"workitem_id": workitemID,
	})

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
//
// When target_workitem_id is set, the Archivist validates the parent-child
// relationship via the Operator and uses the target as the lookup key.
func (s *ArchivistServer) GetArtefact(
	ctx context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	// Capability gate: READ:artefact.
	if err := checkCapability(ctx, "READ:artefact"); err != nil {
		return nil, err
	}

	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()

	// Cross-Workitem read: validate parent-child and use target as lookup key.
	if targetID := req.GetTargetWorkitemId(); targetID != "" {
		callerWorkitemID := flowmeta.MetadataValue(ctx, flowmeta.MetadataKeyWorkitemID)
		if callerWorkitemID == "" {
			callerWorkitemID = workitemID
		}
		if err := s.validateChildAccess(ctx, callerWorkitemID, targetID); err != nil {
			return nil, err
		}
		workitemID = targetID
		slog.Info("GetArtefact (cross-Workitem)",
			"caller_workitem_id", callerWorkitemID,
			"target_workitem_id", targetID,
			"artefact_id", artefactID,
		)
	} else {
		slog.Info("GetArtefact",
			"workitem_id", workitemID,
			"artefact_id", artefactID,
		)
	}

	head, err := s.store.GetHead(ctx, workitemID, artefactID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get head: %v", err)
	}
	if head == nil {
		return nil, status.Errorf(codes.NotFound,
			"artefact %q not found for workitem %q", artefactID, workitemID)
	}

	data, ok, err := s.store.GetBlob(ctx, head.Hash)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get blob: %v", err)
	}
	if !ok {
		// This should never happen — provenance points to a hash that was stored.
		return nil, status.Errorf(codes.Internal,
			"blob %q referenced by artefact %q not found (data corruption)", head.Hash, artefactID)
	}

	return &flowv1.GetArtefactResponse{
		Content:          data,
		VersionHash:      head.Hash,
		GovernedArtefact: head.GovernedArtefact,
	}, nil
}

// GetArtefactVersion returns content bytes for a specific version by hash.
func (s *ArchivistServer) GetArtefactVersion(
	ctx context.Context, req *flowv1.GetArtefactVersionRequest,
) (*flowv1.GetArtefactVersionResponse, error) {
	// Capability gate: READ:artefact.
	if err := checkCapability(ctx, "READ:artefact"); err != nil {
		return nil, err
	}

	versionHash := req.GetVersionHash()

	slog.Info("GetArtefactVersion", "version_hash", versionHash)

	data, ok, err := s.store.GetBlob(ctx, versionHash)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get blob: %v", err)
	}
	if !ok {
		return nil, status.Errorf(codes.NotFound,
			"version %q not found", versionHash)
	}

	return &flowv1.GetArtefactVersionResponse{
		Content: data,
	}, nil
}

// GetArtefactMetadata returns version history and stamps for the current version.
func (s *ArchivistServer) GetArtefactMetadata(
	ctx context.Context, req *flowv1.GetArtefactMetadataRequest,
) (*flowv1.GetArtefactMetadataResponse, error) {
	// Capability gate: READ:artefact.
	if err := checkCapability(ctx, "READ:artefact"); err != nil {
		return nil, err
	}

	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()

	history, err := s.store.GetHistory(ctx, workitemID, artefactID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get history: %v", err)
	}
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

	// Get stamps for the head (current) version.
	head := history[len(history)-1]
	stampRecords, err := s.store.GetStamps(ctx, workitemID, artefactID, head.Hash)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get stamps: %v", err)
	}

	stamps := make([]*flowv1.Stamp, 0, len(stampRecords))
	for _, sr := range stampRecords {
		stamps = append(stamps, &flowv1.Stamp{
			Name:         sr.Name,
			ApplyingNode: sr.ApplyingNode,
			ContentHash:  sr.ContentHash,
			Signature:    sr.Signature,
			CertChain:    sr.CertChain,
			CreatedAt:    timestamppb.New(sr.CreatedAt),
		})
	}

	return &flowv1.GetArtefactMetadataResponse{
		VersionHistory: entries,
		Stamps:         stamps,
	}, nil
}

// ListArtefacts returns all artefact refs for a workitem.
//
// When target_workitem_id is set, the Archivist validates the parent-child
// relationship via the Operator and uses the target as the lookup key.
func (s *ArchivistServer) ListArtefacts(
	ctx context.Context, req *flowv1.ListArtefactsRequest,
) (*flowv1.ListArtefactsResponse, error) {
	workitemID := req.GetWorkitemId()

	// Cross-Workitem read: validate parent-child and use target as lookup key.
	if targetID := req.GetTargetWorkitemId(); targetID != "" {
		callerWorkitemID := flowmeta.MetadataValue(ctx, flowmeta.MetadataKeyWorkitemID)
		if callerWorkitemID == "" {
			callerWorkitemID = workitemID
		}
		if err := s.validateChildAccess(ctx, callerWorkitemID, targetID); err != nil {
			return nil, err
		}
		workitemID = targetID
		slog.Info("ListArtefacts (cross-Workitem)",
			"caller_workitem_id", callerWorkitemID,
			"target_workitem_id", targetID,
		)
	} else {
		slog.Info("ListArtefacts", "workitem_id", workitemID)
	}

	entries, err := s.store.ListArtefacts(ctx, workitemID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list artefacts: %v", err)
	}

	refs := make([]*flowv1.ArtefactRef, 0, len(entries))
	for _, e := range entries {
		refs = append(refs, &flowv1.ArtefactRef{
			Id:               e.ID,
			GovernedArtefact: e.GovernedArtefact,
		})
	}

	return &flowv1.ListArtefactsResponse{
		ArtefactRefs: refs,
	}, nil
}

// QueryArtefactState returns artefact presence and stamp state for
// exit contract validation. Called by the Operator.
func (s *ArchivistServer) QueryArtefactState(
	ctx context.Context, req *flowv1.QueryArtefactStateRequest,
) (*flowv1.QueryArtefactStateResponse, error) {
	workitemID := req.GetWorkitemId()

	slog.Info("QueryArtefactState", "workitem_id", workitemID)

	entries, err := s.store.ListArtefacts(ctx, workitemID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list artefacts: %v", err)
	}

	states := make([]*flowv1.ArtefactState, 0, len(entries))
	for _, e := range entries {
		head, err := s.store.GetHead(ctx, workitemID, e.ID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "get head: %v", err)
		}
		if head == nil {
			continue
		}

		stampNames, err := s.store.GetStampNamesForVersion(ctx, workitemID, e.ID, head.Hash)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "get stamp names: %v", err)
		}

		states = append(states, &flowv1.ArtefactState{
			ArtefactId:         e.ID,
			GovernedArtefact:   e.GovernedArtefact,
			StampNames:         stampNames,
			CurrentVersionHash: head.Hash,
		})
	}

	return &flowv1.QueryArtefactStateResponse{
		ArtefactStates: states,
	}, nil
}
