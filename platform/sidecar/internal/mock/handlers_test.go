package mock

import (
	"context"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/metadata"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ctxWithWorkitemMD returns a context with the given workitem_id set in
// incoming gRPC metadata, simulating what the real gRPC framework would
// provide after the SDK's interceptor injects the header.
func ctxWithWorkitemMD(workitemID string) context.Context {
	md := metadata.Pairs("x-flow-workitem-id", workitemID)
	return metadata.NewIncomingContext(context.Background(), md)
}

// ---------------------------------------------------------------------------
// SidecarHandler — Heartbeat
// ---------------------------------------------------------------------------

func TestSidecarHandler_Heartbeat(t *testing.T) {
	h := &SidecarHandler{NodeID: "test-node-1"}

	resp, err := h.Heartbeat(context.Background(), &flowv1.HeartbeatRequest{
		WorkitemId: "wid-100",
	})
	if err != nil {
		t.Fatalf("Heartbeat() returned error: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("Heartbeat() expected Acknowledged=true")
	}
}

func TestSidecarHandler_Heartbeat_FallsBackToMetadata(t *testing.T) {
	// When the request body has no workitem_id, the handler should fall
	// back to reading it from gRPC metadata. This test verifies that
	// path does not error and still acknowledges.
	h := &SidecarHandler{NodeID: "test-node-2"}

	ctx := ctxWithWorkitemMD("wid-from-metadata")
	resp, err := h.Heartbeat(ctx, &flowv1.HeartbeatRequest{})
	if err != nil {
		t.Fatalf("Heartbeat() returned error: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("Heartbeat() expected Acknowledged=true when workitem_id comes from metadata")
	}
}

func TestSidecarHandler_Heartbeat_DefaultNodeID(t *testing.T) {
	// Verify the handler works even with an empty NodeID.
	h := &SidecarHandler{}

	resp, err := h.Heartbeat(context.Background(), &flowv1.HeartbeatRequest{
		WorkitemId: "wid-200",
	})
	if err != nil {
		t.Fatalf("Heartbeat() returned error: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("Heartbeat() expected Acknowledged=true with default node ID")
	}
}

// ---------------------------------------------------------------------------
// OperatorHandler — SubmitResult
// ---------------------------------------------------------------------------

func TestOperatorHandler_SubmitResult(t *testing.T) {
	h := &OperatorHandler{}

	resp, err := h.SubmitResult(context.Background(), &flowv1.SubmitResultRequest{
		WorkitemId: "wid-300",
		Action: &flowv1.SubmitResultRequest_Complete{
			Complete: &flowv1.CompleteAction{},
		},
	})
	if err != nil {
		t.Fatalf("SubmitResult() returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("SubmitResult() expected Accepted=true")
	}
}

func TestOperatorHandler_SubmitResult_NoAction(t *testing.T) {
	h := &OperatorHandler{}

	resp, err := h.SubmitResult(context.Background(), &flowv1.SubmitResultRequest{
		WorkitemId: "wid-301",
	})
	if err != nil {
		t.Fatalf("SubmitResult() returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("SubmitResult() expected Accepted=true even without routing instruction")
	}
}

// ---------------------------------------------------------------------------
// OperatorHandler — Other RPCs
// ---------------------------------------------------------------------------

func TestOperatorHandler_CreateWorkitem(t *testing.T) {
	h := &OperatorHandler{}

	resp, err := h.CreateWorkitem(context.Background(), &flowv1.CreateWorkitemRequest{})
	if err != nil {
		t.Fatalf("CreateWorkitem() returned error: %v", err)
	}
	if resp.GetWorkitemId() != "mock-workitem-001" {
		t.Fatalf("expected mock-workitem-001, got %s", resp.GetWorkitemId())
	}
}

func TestOperatorHandler_ExportWorkitem(t *testing.T) {
	h := &OperatorHandler{}

	resp, err := h.ExportWorkitem(context.Background(), &flowv1.ExportWorkitemRequest{
		WorkitemId: "wid-export",
	})
	if err != nil {
		t.Fatalf("ExportWorkitem() returned error: %v", err)
	}
	if string(resp.GetExportPackage()) != "{}" {
		t.Fatalf("expected empty JSON object, got %s", resp.GetExportPackage())
	}
}

func TestOperatorHandler_ImportWorkitem(t *testing.T) {
	h := &OperatorHandler{}

	resp, err := h.ImportWorkitem(context.Background(), &flowv1.ImportWorkitemRequest{})
	if err != nil {
		t.Fatalf("ImportWorkitem() returned error: %v", err)
	}
	if resp.GetWorkitemId() != "mock-import-001" {
		t.Fatalf("expected mock-import-001, got %s", resp.GetWorkitemId())
	}
}

// ---------------------------------------------------------------------------
// ArchivistHandler
// ---------------------------------------------------------------------------

func TestArchivistHandler_GetArtefact(t *testing.T) {
	h := &ArchivistHandler{}

	resp, err := h.GetArtefact(context.Background(), &flowv1.GetArtefactRequest{
		WorkitemId: "wid-400",
		ArtefactId: "doc-1",
	})
	if err != nil {
		t.Fatalf("GetArtefact() returned error: %v", err)
	}
	if string(resp.GetContent()) != "mock-content" {
		t.Fatalf("expected mock-content, got %s", resp.GetContent())
	}
	if resp.GetVersionHash() != "mock-hash-000" {
		t.Fatalf("expected mock-hash-000, got %s", resp.GetVersionHash())
	}
	if resp.GetGovernedArtefact() != "mock-artefact" {
		t.Fatalf("expected mock-artefact, got %s", resp.GetGovernedArtefact())
	}
}

func TestArchivistHandler_ListArtefacts(t *testing.T) {
	h := &ArchivistHandler{}

	resp, err := h.ListArtefacts(context.Background(), &flowv1.ListArtefactsRequest{
		WorkitemId: "wid-401",
	})
	if err != nil {
		t.Fatalf("ListArtefacts() returned error: %v", err)
	}
	if len(resp.GetArtefactRefs()) != 0 {
		t.Fatalf("expected empty artefact list, got %d items", len(resp.GetArtefactRefs()))
	}
}

func TestArchivistHandler_StoreArtefact(t *testing.T) {
	h := &ArchivistHandler{}

	resp, err := h.StoreArtefact(context.Background(), &flowv1.StoreArtefactRequest{
		WorkitemId: "wid-402",
		ArtefactId: "doc-2",
		Content:    []byte("new-content"),
	})
	if err != nil {
		t.Fatalf("StoreArtefact() returned error: %v", err)
	}
	if resp.GetVersionHash() != "mock-hash-001" {
		t.Fatalf("expected mock-hash-001, got %s", resp.GetVersionHash())
	}
	if !resp.GetIsNewVersion() {
		t.Fatal("expected IsNewVersion=true")
	}
}

// ---------------------------------------------------------------------------
// extractWorkitemID helper
// ---------------------------------------------------------------------------

func TestExtractWorkitemID_Present(t *testing.T) {
	ctx := ctxWithWorkitemMD("wid-extract-test")
	got := extractWorkitemID(ctx)
	if got != "wid-extract-test" {
		t.Fatalf("expected wid-extract-test, got %s", got)
	}
}

func TestExtractWorkitemID_NoMetadata(t *testing.T) {
	got := extractWorkitemID(context.Background())
	if got != "<no-metadata>" {
		t.Fatalf("expected <no-metadata>, got %s", got)
	}
}

func TestExtractWorkitemID_EmptyMetadata(t *testing.T) {
	// Metadata exists but does not contain x-flow-workitem-id.
	md := metadata.Pairs("some-other-key", "value")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	got := extractWorkitemID(ctx)
	if got != "<not-set>" {
		t.Fatalf("expected <not-set>, got %s", got)
	}
}
