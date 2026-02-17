package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/gideas/flow/archivist/internal/store/sqlite"
	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:])
}

func newTestServer(t *testing.T) *ArchivistServer {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewArchivistServer(store)
}

func TestStoreArtefact_NewVersion(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	content := []byte("Hello from Step 1")
	hash := sha256Hex(content)

	resp, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "greeting",
		Kind:        "txt",
		Content:     content,
		ContentHash: hash,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetIsNewVersion() {
		t.Fatal("expected is_new_version=true for first store")
	}
	if resp.GetVersionHash() != hash {
		t.Fatalf("expected version_hash=%q, got %q", hash, resp.GetVersionHash())
	}
}

func TestStoreArtefact_DuplicateContent(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	content := []byte("Hello from Step 1")
	hash := sha256Hex(content)

	req := &flowv1.StoreArtefactRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "greeting",
		Kind:        "txt",
		Content:     content,
		ContentHash: hash,
	}

	// First store.
	if _, err := s.StoreArtefact(ctx, req); err != nil {
		t.Fatalf("first StoreArtefact: %v", err)
	}

	// Second store with same content.
	resp, err := s.StoreArtefact(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetIsNewVersion() {
		t.Fatal("expected is_new_version=false for duplicate content")
	}
}

func TestStoreArtefact_UpdatedContent(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	// Store v1.
	v1Content := []byte("version 1")
	v1Hash := sha256Hex(v1Content)
	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "doc",
		Kind:        "txt",
		Content:     v1Content,
		ContentHash: v1Hash,
	}); err != nil {
		t.Fatalf("StoreArtefact v1: %v", err)
	}

	// Store v2 with different content.
	v2Content := []byte("version 2")
	v2Hash := sha256Hex(v2Content)
	resp, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "doc",
		Kind:        "txt",
		Content:     v2Content,
		ContentHash: v2Hash,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetIsNewVersion() {
		t.Fatal("expected is_new_version=true for updated content")
	}
	if resp.GetVersionHash() != v2Hash {
		t.Fatalf("expected version_hash=%q, got %q", v2Hash, resp.GetVersionHash())
	}
}

func TestGetArtefact_LatestVersion(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	content := []byte("Hello from Step 1")
	hash := sha256Hex(content)

	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "greeting",
		Kind:        "txt",
		Content:     content,
		ContentHash: hash,
	}); err != nil {
		t.Fatalf("StoreArtefact: %v", err)
	}

	resp, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: "wi-1",
		ArtefactId: "greeting",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.GetContent()) != "Hello from Step 1" {
		t.Fatalf("expected 'Hello from Step 1', got %q", string(resp.GetContent()))
	}
	if resp.GetVersionHash() != hash {
		t.Fatalf("expected version_hash=%q, got %q", hash, resp.GetVersionHash())
	}
	if resp.GetKind() != "txt" {
		t.Fatalf("expected kind=txt, got %q", resp.GetKind())
	}
}

func TestGetArtefact_NotFound(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	_, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: "wi-1",
		ArtefactId: "missing",
	})
	if err == nil {
		t.Fatal("expected error for missing artefact")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestGetArtefactVersion(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	content := []byte("specific version")
	hash := sha256Hex(content)

	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "doc",
		Kind:        "txt",
		Content:     content,
		ContentHash: hash,
	}); err != nil {
		t.Fatalf("StoreArtefact: %v", err)
	}

	resp, err := s.GetArtefactVersion(ctx, &flowv1.GetArtefactVersionRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "doc",
		VersionHash: hash,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.GetContent()) != "specific version" {
		t.Fatalf("expected 'specific version', got %q", string(resp.GetContent()))
	}
}

func TestGetArtefactVersion_NotFound(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	_, err := s.GetArtefactVersion(ctx, &flowv1.GetArtefactVersionRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "doc",
		VersionHash: "nonexistent-hash",
	})
	if err == nil {
		t.Fatal("expected error for missing version")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestListArtefacts(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "doc1",
		Kind:        "txt",
		Content:     []byte("a"),
		ContentHash: sha256Hex([]byte("a")),
	}); err != nil {
		t.Fatalf("StoreArtefact doc1: %v", err)
	}
	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "doc2",
		Kind:        "json",
		Content:     []byte("b"),
		ContentHash: sha256Hex([]byte("b")),
	}); err != nil {
		t.Fatalf("StoreArtefact doc2: %v", err)
	}

	resp, err := s.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId: "wi-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetArtefactRefs()) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(resp.GetArtefactRefs()))
	}
}

func TestGetArtefactMetadata(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	v1 := []byte("v1")
	v2 := []byte("v2")
	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "doc",
		Kind:        "txt",
		Content:     v1,
		ContentHash: sha256Hex(v1),
	}); err != nil {
		t.Fatalf("StoreArtefact v1: %v", err)
	}
	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "doc",
		Kind:        "txt",
		Content:     v2,
		ContentHash: sha256Hex(v2),
	}); err != nil {
		t.Fatalf("StoreArtefact v2: %v", err)
	}

	resp, err := s.GetArtefactMetadata(ctx, &flowv1.GetArtefactMetadataRequest{
		WorkitemId: "wi-1",
		ArtefactId: "doc",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetVersionHistory()) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(resp.GetVersionHistory()))
	}
}

func TestEndToEnd_StoreAndRetrieve(t *testing.T) {
	// Simulates the full data handover: Step 1 writes, Step 2 reads.
	s := newTestServer(t)
	ctx := context.Background()

	content := []byte("Hello from Step 1")
	hash := sha256Hex(content)

	// Step 1: Store
	storeResp, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:  "test-workitem-001",
		ArtefactId:  "greeting",
		Kind:        "txt",
		Content:     content,
		ContentHash: hash,
	})
	if err != nil {
		t.Fatalf("StoreArtefact failed: %v", err)
	}
	if !storeResp.GetIsNewVersion() {
		t.Fatal("expected new version")
	}

	// Step 2: Retrieve
	getResp, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: "test-workitem-001",
		ArtefactId: "greeting",
	})
	if err != nil {
		t.Fatalf("GetArtefact failed: %v", err)
	}
	if string(getResp.GetContent()) != "Hello from Step 1" {
		t.Fatalf("expected 'Hello from Step 1', got %q", string(getResp.GetContent()))
	}
}
