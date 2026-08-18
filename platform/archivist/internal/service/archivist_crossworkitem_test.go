package service

import (
	"context"
	"testing"

	"github.com/foundry/flow/archivist/internal/store/sqlite"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// crossWorkitemCtx creates a context for a cross-Workitem read test: node
// identity, workitem identity, and READ:artefact capability.
func crossWorkitemCtx(workitemID string) context.Context {
	md := metadata.Pairs(
		flowmeta.MetadataKeyNodeID, "node-1",
		"x-flow-workitem-id", workitemID,
		flowmeta.MetadataKeyCapabilities, "READ:artefact",
	)
	return metadata.NewIncomingContext(context.Background(), md)
}

// mockOperatorClient implements flowv1.OperatorServiceClient for testing
// cross-Workitem reads. Only ValidateChildAccess is implemented; all
// other methods panic.
type mockOperatorClient struct {
	flowv1.OperatorServiceClient
	validateResp *flowv1.ValidateChildAccessResponse
	validateErr  error
}

func (m *mockOperatorClient) ValidateChildAccess(
	_ context.Context, _ *flowv1.ValidateChildAccessRequest, _ ...grpc.CallOption,
) (*flowv1.ValidateChildAccessResponse, error) {
	return m.validateResp, m.validateErr
}

// newTestServerWithOperator creates a test server with a mock Operator client.
func newTestServerWithOperator(t *testing.T, opClient *mockOperatorClient) *ArchivistServer {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewArchivistServer(store, WithOperatorClient(opClient))
}

// storeChildArtefact is a helper that stores an artefact under the canonical
// child workitem "child-wi".
func storeChildArtefact(t *testing.T, s *ArchivistServer, artefactID string, content []byte) {
	t.Helper()
	hash := sha256Hex(content)
	_, err := s.StoreArtefact(context.Background(), &flowv1.StoreArtefactRequest{
		WorkitemId:       "child-wi",
		ArtefactId:       artefactID,
		GovernedArtefact: "txt",
		Content:          content,
		ContentHash:      hash,
	})
	if err != nil {
		t.Fatalf("StoreArtefact(child-wi, %s): %v", artefactID, err)
	}
}

func TestGetArtefact_CrossWorkitem_Valid(t *testing.T) {
	opClient := &mockOperatorClient{
		validateResp: &flowv1.ValidateChildAccessResponse{Valid: true, Phase: "Completed"},
	}
	s := newTestServerWithOperator(t, opClient)

	// Store artefact under child workitem.
	storeChildArtefact(t, s, "doc", []byte("child data"))

	// Parent reads child's artefact via target_workitem_id.
	ctx := crossWorkitemCtx("parent-wi")
	resp, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId:       "parent-wi",
		ArtefactId:       "doc",
		TargetWorkitemId: "child-wi",
	})
	if err != nil {
		t.Fatalf("GetArtefact cross-Workitem: unexpected error: %v", err)
	}
	if string(resp.GetContent()) != "child data" {
		t.Fatalf("expected 'child data', got %q", string(resp.GetContent()))
	}
}

func TestGetArtefact_CrossWorkitem_WrongParent(t *testing.T) {
	opClient := &mockOperatorClient{
		validateResp: &flowv1.ValidateChildAccessResponse{Valid: false, Phase: "Completed"},
	}
	s := newTestServerWithOperator(t, opClient)

	storeChildArtefact(t, s, "doc", []byte("child data"))

	ctx := crossWorkitemCtx("wrong-parent")
	_, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId:       "wrong-parent",
		ArtefactId:       "doc",
		TargetWorkitemId: "child-wi",
	})
	if err == nil {
		t.Fatal("expected error for wrong parent")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestGetArtefact_CrossWorkitem_ChildNotCompleted(t *testing.T) {
	opClient := &mockOperatorClient{
		validateResp: &flowv1.ValidateChildAccessResponse{Valid: false, Phase: "Running"},
	}
	s := newTestServerWithOperator(t, opClient)

	ctx := crossWorkitemCtx("parent-wi")
	_, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId:       "parent-wi",
		ArtefactId:       "doc",
		TargetWorkitemId: "child-wi",
	})
	if err == nil {
		t.Fatal("expected error for non-completed child")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

func TestGetArtefact_CrossWorkitem_OperatorUnavailable(t *testing.T) {
	opClient := &mockOperatorClient{
		validateErr: status.Error(codes.Unavailable, "connection refused"),
	}
	s := newTestServerWithOperator(t, opClient)

	ctx := crossWorkitemCtx("parent-wi")
	_, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId:       "parent-wi",
		ArtefactId:       "doc",
		TargetWorkitemId: "child-wi",
	})
	if err == nil {
		t.Fatal("expected error when Operator is unavailable")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Internal {
		t.Fatalf("expected Internal (fail-closed), got %v", err)
	}
}

func TestGetArtefact_NoTargetWorkitem_UnchangedBehaviour(t *testing.T) {
	s := newTestServer(t) // No operator client — fine for normal reads.

	content := []byte("normal data")
	hash := sha256Hex(content)
	_, err := s.StoreArtefact(context.Background(), &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          content,
		ContentHash:      hash,
	})
	if err != nil {
		t.Fatalf("StoreArtefact: %v", err)
	}

	resp, err := s.GetArtefact(context.Background(), &flowv1.GetArtefactRequest{
		WorkitemId: "wi-1",
		ArtefactId: "doc",
	})
	if err != nil {
		t.Fatalf("GetArtefact (no target): unexpected error: %v", err)
	}
	if string(resp.GetContent()) != "normal data" {
		t.Fatalf("expected 'normal data', got %q", string(resp.GetContent()))
	}
}

func TestGetArtefact_CrossWorkitem_NoOperatorClient(t *testing.T) {
	s := newTestServer(t) // No operator client.

	ctx := crossWorkitemCtx("parent-wi")
	_, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId:       "parent-wi",
		ArtefactId:       "doc",
		TargetWorkitemId: "child-wi",
	})
	if err == nil {
		t.Fatal("expected error when operator client not configured")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", err)
	}
}

func TestListArtefacts_CrossWorkitem_Valid(t *testing.T) {
	opClient := &mockOperatorClient{
		validateResp: &flowv1.ValidateChildAccessResponse{Valid: true, Phase: "Completed"},
	}
	s := newTestServerWithOperator(t, opClient)

	storeChildArtefact(t, s, "doc1", []byte("a"))
	storeChildArtefact(t, s, "doc2", []byte("b"))

	ctx := crossWorkitemCtx("parent-wi")
	resp, err := s.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId:       "parent-wi",
		TargetWorkitemId: "child-wi",
	})
	if err != nil {
		t.Fatalf("ListArtefacts cross-Workitem: unexpected error: %v", err)
	}
	if len(resp.GetArtefactRefs()) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(resp.GetArtefactRefs()))
	}
}

func TestListArtefacts_CrossWorkitem_Invalid(t *testing.T) {
	opClient := &mockOperatorClient{
		validateResp: &flowv1.ValidateChildAccessResponse{Valid: false, Phase: "Completed"},
	}
	s := newTestServerWithOperator(t, opClient)

	ctx := crossWorkitemCtx("wrong-parent")
	_, err := s.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId:       "wrong-parent",
		TargetWorkitemId: "child-wi",
	})
	if err == nil {
		t.Fatal("expected error for invalid cross-Workitem read")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}
