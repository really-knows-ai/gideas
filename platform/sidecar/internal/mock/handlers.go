// Package mock implements stub handlers for services that the Sidecar
// will eventually proxy to real backends. For the Walking Skeleton phase,
// these handlers log activity and return success, allowing the Node->Sidecar
// link to be verified without a running Control Plane.
package mock

import (
	"context"
	"log/slog"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/metadata"
)

// extractWorkitemID reads the x-flow-workitem-id from incoming gRPC metadata.
func extractWorkitemID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "<no-metadata>"
	}
	vals := md.Get("x-flow-workitem-id")
	if len(vals) == 0 {
		return "<not-set>"
	}
	return vals[0]
}

// ---------------------------------------------------------------------------
// SidecarService — Heartbeat
// ---------------------------------------------------------------------------

// SidecarHandler implements flowv1.SidecarServiceServer.
type SidecarHandler struct {
	flowv1.UnimplementedSidecarServiceServer
	NodeID string
}

func (h *SidecarHandler) Heartbeat(
	ctx context.Context, req *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	workitemID := req.GetWorkitemId()
	if workitemID == "" {
		workitemID = extractWorkitemID(ctx)
	}

	nodeID := h.NodeID
	if nodeID == "" {
		nodeID = "unknown-node"
	}

	slog.Info("Heartbeat received", "node_id", nodeID, "workitem_id", workitemID)

	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// OperatorService — Mock
// ---------------------------------------------------------------------------

// OperatorHandler implements flowv1.OperatorServiceServer as a passthrough stub.
type OperatorHandler struct {
	flowv1.UnimplementedOperatorServiceServer
}

func (h *OperatorHandler) SubmitResult(
	ctx context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	workitemID := req.GetWorkitemId()
	if workitemID == "" {
		workitemID = extractWorkitemID(ctx)
	}

	routingType := "unknown"
	if req.GetRoutingInstruction() != nil {
		routingType = req.GetRoutingInstruction().GetType().String()
	}

	slog.Info("Sidecar intercepted completion",
		"workitem_id", workitemID,
		"routing_type", routingType,
		"target", req.GetRoutingInstruction().GetTarget(),
	)

	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (h *OperatorHandler) CreateWorkitem(
	ctx context.Context, req *flowv1.CreateWorkitemRequest,
) (*flowv1.CreateWorkitemResponse, error) {
	slog.Info("Sidecar intercepted CreateWorkitem (mock)")
	return &flowv1.CreateWorkitemResponse{WorkitemId: "mock-workitem-001"}, nil
}

func (h *OperatorHandler) CreateHearingWorkitem(
	ctx context.Context, req *flowv1.CreateHearingWorkitemRequest,
) (*flowv1.CreateHearingWorkitemResponse, error) {
	slog.Info("Sidecar intercepted CreateHearingWorkitem (mock)", "law_id", req.GetLawId())
	return &flowv1.CreateHearingWorkitemResponse{WorkitemId: "mock-hearing-001"}, nil
}

func (h *OperatorHandler) ExportWorkitem(
	ctx context.Context, req *flowv1.ExportWorkitemRequest,
) (*flowv1.ExportWorkitemResponse, error) {
	slog.Info("Sidecar intercepted ExportWorkitem (mock)", "workitem_id", req.GetWorkitemId())
	return &flowv1.ExportWorkitemResponse{ExportPackage: []byte("{}")}, nil
}

func (h *OperatorHandler) ImportWorkitem(
	ctx context.Context, req *flowv1.ImportWorkitemRequest,
) (*flowv1.ImportWorkitemResponse, error) {
	slog.Info("Sidecar intercepted ImportWorkitem (mock)", "treaty", req.GetTreatyName())
	return &flowv1.ImportWorkitemResponse{WorkitemId: "mock-import-001"}, nil
}

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
