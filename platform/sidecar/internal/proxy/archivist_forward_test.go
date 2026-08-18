package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Cross-Workitem forwarding tests (Phase 7D)
// ---------------------------------------------------------------------------

func TestArchivistProxy_GetArtefact_ForwardsTargetWorkitemID(t *testing.T) {
	proxy, capture := setupArchivistProxy(t)

	resp, err := proxy.GetArtefact(context.Background(), &flowv1.GetArtefactRequest{
		WorkitemId:       parentWIStr,
		ArtefactId:       "doc",
		TargetWorkitemId: childWIStr,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the target_workitem_id was forwarded to the backend.
	if capture.lastGetReq.GetTargetWorkitemId() != childWIStr {
		t.Fatalf("expected target_workitem_id=%s, got %q",
			childWIStr, capture.lastGetReq.GetTargetWorkitemId())
	}
	if string(resp.GetContent()) != testContentStr {
		t.Fatalf("expected passthrough content, got %q", string(resp.GetContent()))
	}
}

func TestArchivistProxy_ListArtefacts_ForwardsTargetWorkitemID(t *testing.T) {
	proxy, capture := setupArchivistProxy(t)

	resp, err := proxy.ListArtefacts(context.Background(), &flowv1.ListArtefactsRequest{
		WorkitemId:       parentWIStr,
		TargetWorkitemId: childWIStr,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the target_workitem_id was forwarded.
	if capture.lastListReq.GetTargetWorkitemId() != childWIStr {
		t.Fatalf("expected target_workitem_id=%s, got %q",
			childWIStr, capture.lastListReq.GetTargetWorkitemId())
	}
	if len(resp.GetArtefactRefs()) != 1 {
		t.Fatalf("expected 1 artefact ref, got %d", len(resp.GetArtefactRefs()))
	}
}
