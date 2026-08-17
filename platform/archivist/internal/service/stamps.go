// Package service implements the Archivist gRPC server.
package service

import (
	"context"
	"log/slog"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// StampArtefact applies a named stamp to the current (head) version of an
// artefact. Returns the created stamp. The signature and cert_chain are
// Sidecar-injected from the node's identity material.
func (s *ArchivistServer) StampArtefact(
	ctx context.Context, req *flowv1.StampArtefactRequest,
) (*flowv1.StampArtefactResponse, error) {
	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()
	stampName := req.GetStampName()

	slog.Info("StampArtefact",
		"workitem_id", workitemID,
		"artefact_id", artefactID,
		"stamp_name", stampName,
	)

	// Resolve head version (needed for both capability check and stamp).
	head, err := s.store.GetHead(ctx, workitemID, artefactID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get head: %v", err)
	}
	if head == nil {
		return nil, status.Errorf(codes.NotFound,
			"artefact %q not found for workitem %q", artefactID, workitemID)
	}

	// Capability gate: STAMP:artefact/<governed_artefact>/<stamp_name> or
	// ATTEST:artefact/<governed_artefact>/<stamp_name> (migrated nodes).
	if err := checkCapabilityAny(ctx,
		"STAMP:artefact/"+head.GovernedArtefact+"/"+stampName,
		"ATTEST:artefact/"+head.GovernedArtefact+"/"+stampName,
	); err != nil {
		return nil, err
	}

	// Extract applying_node from gRPC metadata if available.
	applyingNode := extractNodeID(ctx)

	isNew, err := s.store.StampArtefact(
		ctx, workitemID, artefactID, head.Hash, stampName,
		applyingNode, req.GetSignature(), req.GetCertChain(),
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "stamp artefact: %v", err)
	}

	if !isNew {
		slog.Info("StampArtefact: stamp already exists",
			"workitem_id", workitemID,
			"artefact_id", artefactID,
			"stamp_name", stampName,
		)
	} else {
		s.publishAudit(ctx, "audit.artefact.stamped", map[string]string{
			"action":      "stamped",
			"resource_id": artefactID,
			"workitem_id": workitemID,
			"stamp_name":  stampName,
		})
	}

	return &flowv1.StampArtefactResponse{
		Stamp: &flowv1.Stamp{
			Name:         stampName,
			ApplyingNode: applyingNode,
			ContentHash:  head.Hash,
			Signature:    req.GetSignature(),
			CertChain:    req.GetCertChain(),
		},
	}, nil
}

// GetStamps returns all stamps on the current (head) version of an artefact.
func (s *ArchivistServer) GetStamps(
	ctx context.Context, req *flowv1.GetStampsRequest,
) (*flowv1.GetStampsResponse, error) {
	// Capability gate: READ:artefact.
	if err := checkCapability(ctx, "READ:artefact"); err != nil {
		return nil, err
	}

	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()

	head, err := s.store.GetHead(ctx, workitemID, artefactID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get head: %v", err)
	}
	if head == nil {
		return &flowv1.GetStampsResponse{}, nil
	}

	records, err := s.store.GetStamps(ctx, workitemID, artefactID, head.Hash)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get stamps: %v", err)
	}

	stamps := make([]*flowv1.Stamp, 0, len(records))
	for _, sr := range records {
		stamps = append(stamps, &flowv1.Stamp{
			Name:         sr.Name,
			ApplyingNode: sr.ApplyingNode,
			ContentHash:  sr.ContentHash,
			Signature:    sr.Signature,
			CertChain:    sr.CertChain,
			CreatedAt:    timestamppb.New(sr.CreatedAt),
		})
	}

	return &flowv1.GetStampsResponse{Stamps: stamps}, nil
}

// HasStamp checks whether the named stamp exists on the current version.
func (s *ArchivistServer) HasStamp(ctx context.Context, req *flowv1.HasStampRequest) (*flowv1.HasStampResponse, error) {
	// Capability gate: READ:artefact.
	if err := checkCapability(ctx, "READ:artefact"); err != nil {
		return nil, err
	}

	exists, err := s.store.HasStamp(ctx, req.GetWorkitemId(), req.GetArtefactId(), req.GetStampName())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "has stamp: %v", err)
	}
	return &flowv1.HasStampResponse{Exists: exists}, nil
}
