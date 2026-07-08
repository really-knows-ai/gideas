package api

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const bufSize = 1024 * 1024

// ─── Mock Archivist gRPC Server ────────────────────────────────────────────

// mockArchivistServer implements flowv1.ArchivistServiceServer with configurable responses.
type mockArchivistServer struct {
	flowv1.UnimplementedArchivistServiceServer

	// Configurable responses
	listArtefactsResp *flowv1.ListArtefactsResponse
	listArtefactsErr  error
	getArtefactResp   *flowv1.GetArtefactResponse
	getArtefactErr    error
	getFeedbackResp   *flowv1.GetFeedbackResponse
	getFeedbackErr    error
	storeArtefactErr  error

	// Recorded metadata for inspection
	lastMetadata metadata.MD
}

func (s *mockArchivistServer) ListArtefacts(ctx context.Context, req *flowv1.ListArtefactsRequest) (*flowv1.ListArtefactsResponse, error) {
	s.recordMetadata(ctx)
	if s.listArtefactsErr != nil {
		return nil, s.listArtefactsErr
	}
	if s.listArtefactsResp != nil {
		return s.listArtefactsResp, nil
	}
	return &flowv1.ListArtefactsResponse{}, nil
}

func (s *mockArchivistServer) GetArtefact(ctx context.Context, req *flowv1.GetArtefactRequest) (*flowv1.GetArtefactResponse, error) {
	s.recordMetadata(ctx)
	if s.getArtefactErr != nil {
		return nil, s.getArtefactErr
	}
	if s.getArtefactResp != nil {
		return s.getArtefactResp, nil
	}
	return &flowv1.GetArtefactResponse{}, nil
}

func (s *mockArchivistServer) GetFeedback(ctx context.Context, req *flowv1.GetFeedbackRequest) (*flowv1.GetFeedbackResponse, error) {
	s.recordMetadata(ctx)
	if s.getFeedbackErr != nil {
		return nil, s.getFeedbackErr
	}
	if s.getFeedbackResp != nil {
		return s.getFeedbackResp, nil
	}
	return &flowv1.GetFeedbackResponse{}, nil
}

func (s *mockArchivistServer) StoreArtefact(ctx context.Context, req *flowv1.StoreArtefactRequest) (*flowv1.StoreArtefactResponse, error) {
	s.recordMetadata(ctx)
	if s.storeArtefactErr != nil {
		return nil, s.storeArtefactErr
	}
	return &flowv1.StoreArtefactResponse{}, nil
}

func (s *mockArchivistServer) recordMetadata(ctx context.Context) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		s.lastMetadata = md
	}
}

// ─── Test Helpers ──────────────────────────────────────────────────────────

// newBufconnArchivist creates an in-process gRPC server with a mock Archivist.
// Returns the client, a cleanup function, and a reference to the mock server.
func newBufconnArchivist(mock *mockArchivistServer) (*ArchivistClient, func(), *mockArchivistServer, error) {
	listener := bufconn.Listen(bufSize)

	s := grpc.NewServer()
	flowv1.RegisterArchivistServiceServer(s, mock)

	go func() {
		if err := s.Serve(listener); err != nil {
			panic(fmt.Sprintf("mock server error: %v", err))
		}
	}()

	// Connect via bufconn dialer (using DialContext as the simplest bufconn-compatible approach)
	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, "bufconn",
		grpc.WithContextDialer(func(ctx context.Context, s string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, nil, mock, fmt.Errorf("dial: %w", err)
	}

	client := &ArchivistClient{Conn: conn}
	cleanup := func() {
		conn.Close()
		s.Stop()
		listener.Close()
	}

	return client, cleanup, mock, nil
}

// ─── T18: ListArtefacts returns expected artefacts ─────────────────────────

func TestArchivist_ListArtefacts(t *testing.T) {
	mock := &mockArchivistServer{
		listArtefactsResp: &flowv1.ListArtefactsResponse{
			ArtefactRefs: []*flowv1.ArtefactRef{
				{Id: "haiku", GovernedArtefact: "haiku"},
				{Id: "petition", GovernedArtefact: "petition"},
			},
		},
	}

	client, cleanup, _, err := newBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer cleanup()

	artefacts, err := client.ListArtefacts(context.Background(), "test-ns", "wi-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(artefacts) != 2 {
		t.Fatalf("expected 2 artefacts, got %d", len(artefacts))
	}

	// Verify sorted by artefact ID
	if artefacts[0].ID != "haiku" {
		t.Errorf("expected first artefact haiku, got %s", artefacts[0].ID)
	}
	if artefacts[1].ID != "petition" {
		t.Errorf("expected second artefact petition, got %s", artefacts[1].ID)
	}

	if artefacts[0].GovernedArtefact != "haiku" {
		t.Errorf("expected governed artefact haiku, got %s", artefacts[0].GovernedArtefact)
	}
}

// ─── T19: ListArtefacts returns empty slice for no artefacts ───────────────

func TestArchivist_ListArtefactsEmpty(t *testing.T) {
	mock := &mockArchivistServer{
		listArtefactsResp: &flowv1.ListArtefactsResponse{},
	}

	client, cleanup, _, err := newBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer cleanup()

	artefacts, err := client.ListArtefacts(context.Background(), "test-ns", "wi-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if artefacts == nil {
		t.Fatal("expected non-nil slice, got nil")
	}
	if len(artefacts) != 0 {
		t.Errorf("expected 0 artefacts, got %d", len(artefacts))
	}
}

// ─── T20: ListArtefacts propagates gRPC error ──────────────────────────────

func TestArchivist_ListArtefactsError(t *testing.T) {
	mock := &mockArchivistServer{
		listArtefactsErr: status.Error(codes.NotFound, "workitem not found"),
	}

	client, cleanup, _, err := newBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer cleanup()

	_, err = client.ListArtefacts(context.Background(), "test-ns", "wi-001")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected error containing 'not found', got %v", err)
	}
}

// ─── T21: GetArtefact returns content bytes ────────────────────────────────

func TestArchivist_GetArtefact(t *testing.T) {
	mock := &mockArchivistServer{
		getArtefactResp: &flowv1.GetArtefactResponse{
			Content: []byte("hello world"),
		},
	}

	client, cleanup, _, err := newBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer cleanup()

	content, err := client.GetArtefact(context.Background(), "test-ns", "wi-001", "haiku")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(content) != "hello world" {
		t.Errorf("expected 'hello world', got %q", string(content))
	}
}

// ─── T22: GetArtefact handles empty content ────────────────────────────────

func TestArchivist_GetArtefactEmpty(t *testing.T) {
	mock := &mockArchivistServer{
		getArtefactResp: &flowv1.GetArtefactResponse{
			Content: []byte{},
		},
	}

	client, cleanup, _, err := newBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer cleanup()

	content, err := client.GetArtefact(context.Background(), "test-ns", "wi-001", "haiku")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(content) != 0 {
		t.Errorf("expected empty content, got %d bytes", len(content))
	}
}

// ─── T23: GetArtefact propagates gRPC error ────────────────────────────────

func TestArchivist_GetArtefactError(t *testing.T) {
	mock := &mockArchivistServer{
		getArtefactErr: status.Error(codes.Internal, "storage error"),
	}

	client, cleanup, _, err := newBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer cleanup()

	_, err = client.GetArtefact(context.Background(), "test-ns", "wi-001", "haiku")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "storage error") {
		t.Errorf("expected error containing 'storage error', got %v", err)
	}
}

// ─── T24: GetFeedback returns ordered feedback items ───────────────────────

func TestArchivist_GetFeedbackOrdered(t *testing.T) {
	t1 := timestamppb.New(time.Date(2024, 1, 1, 0, 0, 1, 0, time.UTC))
	t2 := timestamppb.New(time.Date(2024, 1, 1, 0, 0, 2, 0, time.UTC))

	mock := &mockArchivistServer{
		getFeedbackResp: &flowv1.GetFeedbackResponse{
			FeedbackItems: []*flowv1.FeedbackItem{
				{Id: "fb-c", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW, Source: "node-1", Message: "third", CreatedAt: t2},
				{Id: "fb-a", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW, Source: "node-2", Message: "first", CreatedAt: t1},
				{Id: "fb-b", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW, Source: "node-3", Message: "second", CreatedAt: t2},
			},
		},
	}

	client, cleanup, _, err := newBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer cleanup()

	items, err := client.GetFeedback(context.Background(), "test-ns", "wi-001", "haiku")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 3 {
		t.Fatalf("expected 3 feedback items, got %d", len(items))
	}

	// Sorted: fb-a (T1), fb-b (T2 + secondary sort), fb-c (T2 + secondary sort)
	if items[0].ID != "fb-a" {
		t.Errorf("expected first feedback fb-a, got %s", items[0].ID)
	}
	// Both at T2 - stable secondary sort by ID: fb-b < fb-c
	if items[1].ID != "fb-b" {
		t.Errorf("expected second feedback fb-b, got %s", items[1].ID)
	}
	if items[2].ID != "fb-c" {
		t.Errorf("expected third feedback fb-c, got %s", items[2].ID)
	}
}

// ─── T25: GetFeedback handles empty feedback ───────────────────────────────

func TestArchivist_GetFeedbackEmpty(t *testing.T) {
	mock := &mockArchivistServer{
		getFeedbackResp: &flowv1.GetFeedbackResponse{},
	}

	client, cleanup, _, err := newBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer cleanup()

	items, err := client.GetFeedback(context.Background(), "test-ns", "wi-001", "haiku")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if items == nil {
		t.Fatal("expected non-nil slice, got nil")
	}
	if len(items) != 0 {
		t.Errorf("expected 0 feedback items, got %d", len(items))
	}
}

// ─── T26: GetFeedback maps state correctly ─────────────────────────────────

func TestArchivist_GetFeedbackStateMapping(t *testing.T) {
	t0 := timestamppb.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))

	mock := &mockArchivistServer{
		getFeedbackResp: &flowv1.GetFeedbackResponse{
			FeedbackItems: []*flowv1.FeedbackItem{
				{Id: "fb-0", State: flowv1.FeedbackState_FEEDBACK_STATE_UNSPECIFIED, Source: "n1", Message: "unspec", CreatedAt: t0},
				{Id: "fb-1", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW, Source: "n1", Message: "new", CreatedAt: t0},
				{Id: "fb-2", State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED, Source: "n1", Message: "actioned", CreatedAt: t0},
				{Id: "fb-3", State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX, Source: "n1", Message: "wontfix", CreatedAt: t0},
				{Id: "fb-4", State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED, Source: "n1", Message: "rejected", CreatedAt: t0},
				{Id: "fb-5", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Source: "n1", Message: "deadlocked", CreatedAt: t0},
				{Id: "fb-6", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED, Source: "n1", Message: "resolved", CreatedAt: t0},
				// Unknown value (not in the enum)
				{Id: "fb-7", State: flowv1.FeedbackState(99), Source: "n1", Message: "unknown", CreatedAt: t0},
			},
		},
	}

	client, cleanup, _, err := newBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer cleanup()

	items, err := client.GetFeedback(context.Background(), "test-ns", "wi-001", "haiku")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 8 {
		t.Fatalf("expected 8 feedback items, got %d", len(items))
	}

	// Check state mapping
	expectedStates := map[string]FeedbackState{
		"fb-0": FeedbackStateUnspecified,
		"fb-1": FeedbackStateNew,
		"fb-2": FeedbackStateActioned,
		"fb-3": FeedbackStateWontFix,
		"fb-4": FeedbackStateRejected,
		"fb-5": FeedbackStateDeadlocked,
		"fb-6": FeedbackStateResolved,
		"fb-7": FeedbackState(99), // unknown values are preserved for raw enum rendering
	}

	for _, item := range items {
		expected, ok := expectedStates[item.ID]
		if !ok {
			continue
		}
		if item.State != expected {
			t.Errorf("item %s: expected state %d, got %d", item.ID, expected, item.State)
		}
	}
}

// ─── T27: StoreArtefact succeeds ───────────────────────────────────────────

func TestArchivist_StoreArtefact(t *testing.T) {
	mock := &mockArchivistServer{}

	client, cleanup, _, err := newBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer cleanup()

	err = client.StoreArtefact(context.Background(), "test-ns", StoreArtefactRequest{
		WorkitemID:       "wi-001",
		ArtefactID:       "haiku",
		GovernedArtefact: "haiku",
		Content:          []byte("hello"),
		ContentHash:      "hash",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ─── T28: StoreArtefact propagates error ───────────────────────────────────

func TestArchivist_StoreArtefactError(t *testing.T) {
	mock := &mockArchivistServer{
		storeArtefactErr: status.Error(codes.InvalidArgument, "invalid content hash"),
	}

	client, cleanup, _, err := newBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer cleanup()

	err = client.StoreArtefact(context.Background(), "test-ns", StoreArtefactRequest{
		WorkitemID:       "wi-001",
		ArtefactID:       "haiku",
		GovernedArtefact: "haiku",
		Content:          []byte("hello"),
		ContentHash:      "bad",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid content hash") {
		t.Errorf("expected error containing 'invalid content hash', got %v", err)
	}
}

// ─── T29a-d: gRPC metadata is attached ─────────────────────────────────────

func TestArchivist_Metadata_ListArtefacts(t *testing.T) {
	mock := &mockArchivistServer{
		listArtefactsResp: &flowv1.ListArtefactsResponse{},
	}
	client, cleanup, mockRef, err := newBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer cleanup()

	_, _ = client.ListArtefacts(context.Background(), "my-ns", "my-wi")
	if mockRef.lastMetadata == nil {
		t.Fatal("expected metadata to be recorded")
	}
	if v := mockRef.lastMetadata.Get("x-flow-namespace"); len(v) == 0 || v[0] != "my-ns" {
		t.Errorf("expected x-flow-namespace 'my-ns', got %v", v)
	}
	if v := mockRef.lastMetadata.Get("x-flow-workitem-id"); len(v) == 0 || v[0] != "my-wi" {
		t.Errorf("expected x-flow-workitem-id 'my-wi', got %v", v)
	}
}

func TestArchivist_Metadata_GetArtefact(t *testing.T) {
	mock := &mockArchivistServer{
		getArtefactResp: &flowv1.GetArtefactResponse{Content: []byte("data")},
	}
	client, cleanup, mockRef, err := newBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer cleanup()

	_, _ = client.GetArtefact(context.Background(), "my-ns", "my-wi", "art-1")
	if v := mockRef.lastMetadata.Get("x-flow-namespace"); len(v) == 0 || v[0] != "my-ns" {
		t.Errorf("expected x-flow-namespace 'my-ns', got %v", v)
	}
	if v := mockRef.lastMetadata.Get("x-flow-workitem-id"); len(v) == 0 || v[0] != "my-wi" {
		t.Errorf("expected x-flow-workitem-id 'my-wi', got %v", v)
	}
}

func TestArchivist_Metadata_GetFeedback(t *testing.T) {
	mock := &mockArchivistServer{
		getFeedbackResp: &flowv1.GetFeedbackResponse{},
	}
	client, cleanup, mockRef, err := newBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer cleanup()

	_, _ = client.GetFeedback(context.Background(), "my-ns", "my-wi", "art-1")
	if v := mockRef.lastMetadata.Get("x-flow-namespace"); len(v) == 0 || v[0] != "my-ns" {
		t.Errorf("expected x-flow-namespace 'my-ns', got %v", v)
	}
	if v := mockRef.lastMetadata.Get("x-flow-workitem-id"); len(v) == 0 || v[0] != "my-wi" {
		t.Errorf("expected x-flow-workitem-id 'my-wi', got %v", v)
	}
}

func TestArchivist_Metadata_StoreArtefact(t *testing.T) {
	mock := &mockArchivistServer{}
	client, cleanup, mockRef, err := newBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer cleanup()

	_ = client.StoreArtefact(context.Background(), "my-ns", StoreArtefactRequest{
		WorkitemID: "my-wi",
		ArtefactID: "art-1",
	})
	if v := mockRef.lastMetadata.Get("x-flow-namespace"); len(v) == 0 || v[0] != "my-ns" {
		t.Errorf("expected x-flow-namespace 'my-ns', got %v", v)
	}
	if v := mockRef.lastMetadata.Get("x-flow-workitem-id"); len(v) == 0 || v[0] != "my-wi" {
		t.Errorf("expected x-flow-workitem-id 'my-wi', got %v", v)
	}
}

// ─── T30: ComputeSHA256 produces correct hex hash ─────────────────────────

func TestComputeSHA256_Hello(t *testing.T) {
	input := []byte("hello")
	expected := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	result := ComputeSHA256(input)
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// ─── T31: ComputeSHA256 handles empty input ───────────────────────────────

func TestComputeSHA256_Empty(t *testing.T) {
	expected := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	result := ComputeSHA256([]byte{})
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// ─── T32: ComputeSHA256 produces lowercase hex ─────────────────────────────

func TestComputeSHA256_Lowercase(t *testing.T) {
	result := ComputeSHA256([]byte("HELLO"))
	for _, c := range result {
		if c >= 'A' && c <= 'F' {
			t.Errorf("unexpected uppercase hex char %c in %q", c, result)
		}
	}
}
