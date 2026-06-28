package mock

import (
	"context"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

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
