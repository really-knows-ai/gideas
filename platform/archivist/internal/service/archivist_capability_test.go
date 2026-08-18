package service

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// nodeCtx creates a context simulating a node-originated call with
// the given capabilities. The x-flow-node-id presence signals that
// this is a node call subject to capability enforcement.
func nodeCtx(caps string) context.Context {
	md := metadata.Pairs(
		flowmeta.MetadataKeyNodeID, "node-1",
		flowmeta.MetadataKeyCapabilities, caps,
	)
	return metadata.NewIncomingContext(context.Background(), md)
}

// systemCtx creates a bare context with no node identity — simulating
// a system-to-system call (e.g. Operator calling Archivist).
func systemCtx() context.Context {
	return context.Background()
}

func TestCapability_StoreArtefact_Denied(t *testing.T) {
	s := newTestServer(t)
	ctx := nodeCtx("READ:artefact") // No WRITE:artefact.

	_, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("x"),
		ContentHash:      sha256Hex([]byte("x")),
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE:artefact")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestCapability_StoreArtefact_AllowedBroad(t *testing.T) {
	s := newTestServer(t)
	ctx := nodeCtx("WRITE:artefact")

	resp, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("x"),
		ContentHash:      sha256Hex([]byte("x")),
	})
	if err != nil {
		t.Fatalf("expected success with WRITE:artefact, got %v", err)
	}
	if !resp.GetIsNewVersion() {
		t.Fatal("expected is_new_version=true")
	}
}

func TestCapability_StoreArtefact_AllowedScoped(t *testing.T) {
	s := newTestServer(t)
	ctx := nodeCtx("WRITE:artefact/txt") // Scoped to "txt" governed artefact.

	resp, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("x"),
		ContentHash:      sha256Hex([]byte("x")),
	})
	if err != nil {
		t.Fatalf("expected success with WRITE:artefact/txt, got %v", err)
	}
	if !resp.GetIsNewVersion() {
		t.Fatal("expected is_new_version=true")
	}
}

func TestCapability_StoreArtefact_ScopedDenied(t *testing.T) {
	s := newTestServer(t)
	ctx := nodeCtx("WRITE:artefact/json") // Scoped to "json" but storing "txt".

	_, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("x"),
		ContentHash:      sha256Hex([]byte("x")),
	})
	if err == nil {
		t.Fatal("expected PermissionDenied when scope does not match")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestCapability_GetArtefact_Denied(t *testing.T) {
	s := newTestServer(t)
	ctx := nodeCtx("WRITE:artefact") // No READ:artefact.

	_, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: "wi-1",
		ArtefactId: "doc",
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing READ:artefact")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestCapability_SystemCall_BypassesEnforcement(t *testing.T) {
	s := newTestServer(t)
	ctx := systemCtx() // No node identity — system call.

	// Store an artefact with no capabilities at all (system call).
	resp, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-sys",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("system data"),
		ContentHash:      sha256Hex([]byte("system data")),
	})
	if err != nil {
		t.Fatalf("system call should bypass capability enforcement, got %v", err)
	}
	if !resp.GetIsNewVersion() {
		t.Fatal("expected is_new_version=true")
	}
}

func TestCapability_NodeCallNoCapabilities_Denied(t *testing.T) {
	s := newTestServer(t)
	// Node-originated call with no capabilities at all.
	md := metadata.Pairs(flowmeta.MetadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("x"),
		ContentHash:      sha256Hex([]byte("x")),
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for node call with empty capabilities")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestCapability_StampArtefact_ExactMatch(t *testing.T) {
	s := newTestServer(t)

	// First store an artefact (as system call to avoid capability gate).
	ctx := systemCtx()
	content := []byte("stampable content")
	hash := sha256Hex(content)
	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-stamp",
		ArtefactId:       "doc",
		GovernedArtefact: "petition-draft",
		Content:          content,
		ContentHash:      hash,
	}); err != nil {
		t.Fatalf("StoreArtefact: %v", err)
	}

	// Node with STAMP:artefact/petition-draft/linter can stamp linter on petition-draft.
	nodeWithStamp := nodeCtx("STAMP:artefact/petition-draft/linter")
	_, err := s.StampArtefact(nodeWithStamp, &flowv1.StampArtefactRequest{
		WorkitemId: "wi-stamp",
		ArtefactId: "doc",
		StampName:  "linter",
	})
	if err != nil {
		t.Fatalf("expected success with exact stamp capability, got %v", err)
	}

	// Same node cannot stamp "security-review" on petition-draft.
	_, err = s.StampArtefact(nodeWithStamp, &flowv1.StampArtefactRequest{
		WorkitemId: "wi-stamp",
		ArtefactId: "doc",
		StampName:  "security-review",
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for wrong stamp name")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestCapability_AddFeedback_Denied(t *testing.T) {
	s := newTestServer(t)
	ctx := nodeCtx("READ:feedback") // No WRITE:feedback/new.

	_, err := s.AddFeedback(ctx, &flowv1.AddFeedbackRequest{
		WorkitemId: "wi-1",
		ArtefactId: "doc",
		CanWontFix: false,
		Message:    "test",
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE:feedback/new")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestCapability_ListArtefacts_NoGateRequired(t *testing.T) {
	s := newTestServer(t)
	// Node call with no capabilities — ListArtefacts is implicit (no gate).
	md := metadata.Pairs(flowmeta.MetadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := s.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId: "wi-1",
	})
	if err != nil {
		t.Fatalf("ListArtefacts should not require capabilities, got %v", err)
	}
}
