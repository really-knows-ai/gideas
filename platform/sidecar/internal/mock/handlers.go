// Package mock implements stub handlers for services that the Sidecar
// will eventually proxy to real backends. For the Walking Skeleton phase,
// these handlers log activity and return success, allowing the Node->Sidecar
// link to be verified without a running Control Plane.
package mock

import (
	"context"
	"log/slog"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// ArchivistService — Mock
// ---------------------------------------------------------------------------

// ArchivistHandler implements flowv1.ArchivistServiceServer as a passthrough stub.
type ArchivistHandler struct {
	flowv1.UnimplementedArchivistServiceServer
}

func (h *ArchivistHandler) GetArtefact(
	ctx context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	slog.Info("Sidecar intercepted GetArtefact (mock)",
		"workitem_id", req.GetWorkitemId(),
		"artefact_id", req.GetArtefactId(),
	)
	return &flowv1.GetArtefactResponse{
		Content:          []byte("mock-content"),
		VersionHash:      "mock-hash-000",
		GovernedArtefact: "mock-artefact",
	}, nil
}

func (h *ArchivistHandler) ListArtefacts(
	ctx context.Context, req *flowv1.ListArtefactsRequest,
) (*flowv1.ListArtefactsResponse, error) {
	slog.Info("Sidecar intercepted ListArtefacts (mock)", "workitem_id", req.GetWorkitemId())
	return &flowv1.ListArtefactsResponse{}, nil
}

func (h *ArchivistHandler) StoreArtefact(
	ctx context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	slog.Info("Sidecar intercepted StoreArtefact (mock)",
		"workitem_id", req.GetWorkitemId(),
		"artefact_id", req.GetArtefactId(),
	)
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "mock-hash-001",
		IsNewVersion: true,
	}, nil
}
