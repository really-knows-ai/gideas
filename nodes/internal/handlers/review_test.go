package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flow "github.com/foundry/flow/sdk/go"
	"google.golang.org/grpc"
)

// reviewSpy is a minimal spy backend for HandleReview tests. Deliberately
// does NOT pre-seed the review-output artefact — the first Store on a
// fresh child Workitem must succeed via NewArtefact, not GetArtefact
// (regression guard for the "artefact review-output not found" bug where
// the handler tried to GetArtefact a never-before-written artefact).
type reviewSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer

	mu               sync.Mutex
	ArtefactContents map[string][]byte
	StoredArtefacts  map[string][]byte
	CompleteCalls    int
}

func newReviewSpy(artefacts map[string][]byte) *reviewSpy {
	return &reviewSpy{
		ArtefactContents: artefacts,
		StoredArtefacts:  make(map[string][]byte),
	}
}

func (s *reviewSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *reviewSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := req.GetAction().(*flowv1.SubmitResultRequest_Complete); ok {
		s.CompleteCalls++
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *reviewSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if content, ok := s.StoredArtefacts[req.GetArtefactId()]; ok {
		return &flowv1.GetArtefactResponse{Content: content, VersionHash: "test-hash"}, nil
	}
	content, ok := s.ArtefactContents[req.GetArtefactId()]
	if !ok {
		return nil, fmt.Errorf("artefact %q not found", req.GetArtefactId())
	}
	return &flowv1.GetArtefactResponse{Content: content, VersionHash: "test-hash"}, nil
}

func (s *reviewSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StoredArtefacts[req.GetArtefactId()] = req.GetContent()
	return &flowv1.StoreArtefactResponse{VersionHash: "new-hash", IsNewVersion: true}, nil
}

// fakeReviewAgent is a minimal ReviewContract implementation for tests.
type fakeReviewAgent struct {
	result *flow.ReviewResult
	err    error
}

func (f *fakeReviewAgent) Run(
	_ context.Context, _, _ string, _ []flow.ReviewLaw, _ []flow.ReviewHistory,
) (*flow.ReviewResult, error) {
	return f.result, f.err
}

func newReviewTestWorkitem(t *testing.T, spy *reviewSpy) *flow.Workitem {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	t.Setenv(flow.EnvWorkitemID, "test-child-workitem")
	client, err := flow.NewClient(flow.WithSidecarAddress(lis.Addr().String()))
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	workitem, err := client.GetWorkitem()
	if err != nil {
		t.Fatalf("GetWorkitem() failed: %v", err)
	}
	return workitem
}

// TestHandleReview_StoresReviewOutputOnFreshChild is a regression guard for
// the bug where HandleReview called GetArtefact(ArtefactReviewOutput) to
// obtain a handle for the FIRST write of that artefact. GetArtefact requires
// the artefact to already exist in the Archivist; on a freshly fanned-out
// child Workitem it never does, so every review permanently failed with
// "artefact review-output not found" and the parent's AwaitAll never saw a
// terminal child. The fix uses NewArtefact for first-write scenarios.
func TestHandleReview_StoresReviewOutputOnFreshChild(t *testing.T) {
	lawsJSON, _ := json.Marshal([]LawData{{ID: "law-1", Tier: 2, Goal: "test goal"}})
	historyJSON, _ := json.Marshal([]HistoryData{})

	spy := newReviewSpy(map[string][]byte{
		"input":         []byte("petition text"),
		"review":        []byte("haiku content"),
		ArtefactLaws:    lawsJSON,
		ArtefactHistory: historyJSON,
		// Deliberately no ArtefactReviewOutput entry — it must not exist yet.
	})
	workitem := newReviewTestWorkitem(t, spy)

	agent := &fakeReviewAgent{result: &flow.ReviewResult{
		Feedback: []flow.ReviewFeedback{{Message: "issue found", CitedLaws: []string{"law-1"}}},
	}}

	cfg := ReviewConfig{InputArtefacts: []string{"input"}, ReviewArtefact: "review"}

	if err := HandleReview(context.Background(), workitem, agent, cfg); err != nil {
		t.Fatalf("HandleReview() returned error: %v", err)
	}

	stored, ok := spy.StoredArtefacts[ArtefactReviewOutput]
	if !ok {
		t.Fatal("expected review-output artefact to be stored")
	}
	var out reviewOutputData
	if err := json.Unmarshal(stored, &out); err != nil {
		t.Fatalf("failed to unmarshal stored review-output: %v", err)
	}
	if len(out.Feedback) != 1 || out.Feedback[0].Message != "issue found" {
		t.Fatalf("unexpected stored review-output: %+v", out)
	}

	if spy.CompleteCalls != 1 {
		t.Fatalf("expected 1 Complete() call, got %d", spy.CompleteCalls)
	}
}

func TestConsolidateFeedback_NoItems(t *testing.T) {
	if got := consolidateFeedback(nil); got != nil {
		t.Errorf("nil input got %v, want nil", got)
	}
	if got := consolidateFeedback([]reviewItem{}); len(got) != 0 {
		t.Errorf("empty input got %d items, want 0", len(got))
	}
}

func TestConsolidateFeedback_SingleItem(t *testing.T) {
	items := []reviewItem{{Message: "bad", CitedLaws: []string{"law-1"}}}
	got := consolidateFeedback(items)
	if len(got) != 1 || got[0].Message != "bad" {
		t.Fatalf("single item not preserved: %+v", got)
	}
}

func TestConsolidateFeedback_DeduplicatesByCitedLaws(t *testing.T) {
	items := []reviewItem{
		{Message: "line 1 violates no-weather because of cold temperature reference", CitedLaws: []string{"no-weather"}},
		{Message: "word cold is prohibited under no-weather law", CitedLaws: []string{"no-weather"}},
		{Message: "temperature reference cold violates the weather ban", CitedLaws: []string{"no-weather"}},
	}
	got := consolidateFeedback(items)
	if len(got) != 1 {
		t.Fatalf("expected 1 consolidated item for same cited laws, got %d: %+v", len(got), got)
	}
	if got[0].Message != "word cold is prohibited under no-weather law" {
		t.Errorf("expected shortest message, got %q", got[0].Message)
	}
}

func TestConsolidateFeedback_SeparateLawGroupsKept(t *testing.T) {
	items := []reviewItem{
		{Message: "cold prohibited under no-weather", CitedLaws: []string{"no-weather"}},
		{Message: "sky reference violates no-atmosphere", CitedLaws: []string{"no-atmosphere"}},
		{Message: "again cold violates no-weather", CitedLaws: []string{"no-weather"}},
	}
	got := consolidateFeedback(items)
	if len(got) != 2 {
		t.Fatalf("expected 2 groups (no-weather + no-atmosphere), got %d: %+v", len(got), got)
	}
}

func TestConsolidateFeedback_NoCitedLawsGroupedSeparately(t *testing.T) {
	items := []reviewItem{
		{Message: "deviates from brief, lacks storm imagery", CitedLaws: []string{"no-weather"}},
		{Message: "missing storm theme", CitedLaws: []string{}},
		{Message: "no frozen sea setting", CitedLaws: []string{}},
	}
	got := consolidateFeedback(items)
	if len(got) != 2 {
		t.Fatalf("expected 2 groups (no-weather + no-laws), got %d: %+v", len(got), got)
	}
}

func TestConsolidateFeedback_SameLawsSameMessageCount(t *testing.T) {
	items := []reviewItem{
		{Message: "x", CitedLaws: []string{"a", "b"}},
		{Message: "longer message about a and b violation here", CitedLaws: []string{"b", "a"}},
	}
	got := consolidateFeedback(items)
	if len(got) != 1 {
		t.Fatalf("expected 1 consolidated (same laws different order), got %d", len(got))
	}
	if got[0].Message != "x" {
		t.Errorf("expected shortest message 'x', got %q", got[0].Message)
	}
}
