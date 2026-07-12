package flow

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

const stampLinter = "linter"

func newTestArtefact(sess *session, artefactID, governedArtefact, versionHash string) *Artefact {
	return &Artefact{
		artefactID:       artefactID,
		governedArtefact: governedArtefact,
		versionHash:      versionHash,
		session:          sess,
	}
}

func setupArtefactTestEnv(t *testing.T, workitemID string) (*testEnv, *Artefact) {
	t.Helper()
	env := setupTestEnv(t, workitemID)
	art := newTestArtefact(env.client.session, "art-001", "doc", "v1-hash")
	return env, art
}

func setupArtefactErrorEnv(t *testing.T, workitemID string) *Artefact {
	t.Helper()
	spy := &spyServer{}
	errSpy := &errorSpyServer{}
	client, srv := setupGRPCTestEnv(t, workitemID, func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, errSpy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
		flowv1.RegisterFlowEventBusServiceServer(s, spy)
	})
	env := &testEnv{client: client, spy: spy, srv: srv}
	art := newTestArtefact(env.client.session, "art-001", "doc", "v1-hash")
	return art
}

func TestArtefact_ID(t *testing.T) {
	env := setupTestEnv(t, "wid-001")
	a := newTestArtefact(env.client.session, "art-xyz", "doc", "h1")
	if got := a.ID(); got != "art-xyz" {
		t.Errorf("ID() = %q, want %q", got, "art-xyz")
	}
}

func TestArtefact_GovernedArtefact(t *testing.T) {
	env := setupTestEnv(t, "wid-001")
	a := newTestArtefact(env.client.session, "art-xyz", "report", "h1")
	if got := a.GovernedArtefact(); got != "report" {
		t.Errorf("GovernedArtefact() = %q, want %q", got, "report")
	}
}

func TestArtefact_VersionHash(t *testing.T) {
	env := setupTestEnv(t, "wid-001")
	a := newTestArtefact(env.client.session, "art-xyz", "doc", "initial-hash")
	if got := a.VersionHash(); got != "initial-hash" {
		t.Errorf("VersionHash() = %q, want %q", got, "initial-hash")
	}
}

func TestArtefact_IsNewVersion_Default(t *testing.T) {
	env := setupTestEnv(t, "wid-001")
	a := newTestArtefact(env.client.session, "art-xyz", "doc", "h1")
	if a.IsNewVersion() {
		t.Error("IsNewVersion() = true, want false (default)")
	}
}

func TestArtefact_GetContent(t *testing.T) {
	env, art := setupArtefactTestEnv(t, "wid-content-001")
	content, err := art.GetContent()
	if err != nil {
		t.Fatalf("GetContent() returned error: %v", err)
	}
	if string(content) != "test-content" {
		t.Errorf("GetContent() = %q, want %q", string(content), "test-content")
	}
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != "wid-content-001" {
		t.Errorf("metadata x-flow-workitem-id = %v, want [wid-content-001]", got)
	}
}

func TestArtefact_GetContent_CachesContent(t *testing.T) {
	env, art := setupArtefactTestEnv(t, "wid-002")
	_, err := art.GetContent()
	if err != nil {
		t.Fatalf("first GetContent() failed: %v", err)
	}
	env.spy.lastMD = nil
	content, err := art.GetContent()
	if err != nil {
		t.Fatalf("second GetContent() failed: %v", err)
	}
	if string(content) != "test-content" {
		t.Errorf("GetContent() = %q, want %q", string(content), "test-content")
	}
	if env.spy.lastMD != nil {
		t.Error("expected no round-trip on cached GetContent() call")
	}
}

func TestArtefact_GetContent_UpdatesVersionHash(t *testing.T) {
	_, art := setupArtefactTestEnv(t, "wid-003")
	_, err := art.GetContent()
	if err != nil {
		t.Fatalf("GetContent() failed: %v", err)
	}
	if got := art.VersionHash(); got != "test-hash" {
		t.Errorf("VersionHash() after GetContent = %q, want %q", got, "test-hash")
	}
	if got := art.GovernedArtefact(); got != "test-artefact" {
		t.Errorf("GovernedArtefact() after GetContent = %q, want %q", got, "test-artefact")
	}
}

func TestArtefact_GetContent_Error(t *testing.T) {
	art := setupArtefactErrorEnv(t, "wid-err-001")
	_, err := art.GetContent()
	if err == nil {
		t.Fatal("GetContent() expected error, got nil")
	}
}

func TestArtefact_Store(t *testing.T) {
	env, art := setupArtefactTestEnv(t, "wid-store-001")
	err := art.Store([]byte("new-content"))
	if err != nil {
		t.Fatalf("Store() returned error: %v", err)
	}
	if got := art.VersionHash(); got != "hash-001" {
		t.Errorf("VersionHash() after Store = %q, want %q", got, "hash-001")
	}
	if !art.IsNewVersion() {
		t.Error("IsNewVersion() after Store = false, want true")
	}
	cached, _ := art.GetContent()
	if string(cached) != "new-content" {
		t.Errorf("GetContent() after Store = %q, want %q", string(cached), "new-content")
	}
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != "wid-store-001" {
		t.Errorf("metadata x-flow-workitem-id = %v, want [wid-store-001]", got)
	}
}

func TestArtefact_Store_Error(t *testing.T) {
	art := setupArtefactErrorEnv(t, "wid-store-err-001")
	err := art.Store([]byte("content"))
	if err == nil {
		t.Fatal("Store() expected error, got nil")
	}
}

func TestArtefact_Stamp(t *testing.T) {
	env, art := setupArtefactTestEnv(t, "wid-stamp-001")
	err := art.Stamp("approval")
	if err != nil {
		t.Fatalf("Stamp() returned error: %v", err)
	}
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != "wid-stamp-001" {
		t.Errorf("metadata x-flow-workitem-id = %v, want [wid-stamp-001]", got)
	}
}

func TestArtefact_Stamp_Error(t *testing.T) {
	art := setupArtefactErrorEnv(t, "wid-stamp-err-001")
	err := art.Stamp("approval")
	if err == nil {
		t.Fatal("Stamp() expected error, got nil")
	}
}

func TestArtefact_GetStamps(t *testing.T) {
	env, art := setupArtefactTestEnv(t, "wid-stamps-001")
	stamps, err := art.GetStamps()
	if err != nil {
		t.Fatalf("GetStamps() returned error: %v", err)
	}
	if len(stamps) != 2 {
		t.Fatalf("GetStamps() returned %d stamps, want 2", len(stamps))
	}
	if stamps[0].Name != stampLinter {
		t.Errorf("stamps[0].Name = %q, want %q", stamps[0].Name, stampLinter)
	}
	if stamps[0].ApplyingNode != "node-a" {
		t.Errorf("stamps[0].ApplyingNode = %q, want %q", stamps[0].ApplyingNode, "node-a")
	}
	if stamps[0].ContentHash != "ch-1" {
		t.Errorf("stamps[0].ContentHash = %q, want %q", stamps[0].ContentHash, "ch-1")
	}
	if stamps[1].Name != "approval" {
		t.Errorf("stamps[1].Name = %q, want %q", stamps[1].Name, "approval")
	}
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != "wid-stamps-001" {
		t.Errorf("metadata x-flow-workitem-id = %v, want [wid-stamps-001]", got)
	}
}

func TestArtefact_GetStamps_Error(t *testing.T) {
	art := setupArtefactErrorEnv(t, "wid-stamps-err-001")
	_, err := art.GetStamps()
	if err == nil {
		t.Fatal("GetStamps() expected error, got nil")
	}
}

func TestArtefact_HasStamp(t *testing.T) {
	env, art := setupArtefactTestEnv(t, "wid-hasstamp-001")
	ok, err := art.HasStamp(stampLinter)
	if err != nil {
		t.Fatalf("HasStamp() returned error: %v", err)
	}
	if !ok {
		t.Errorf("HasStamp(%q) = false, want true", stampLinter)
	}
	ok, err = art.HasStamp("nonexistent")
	if err != nil {
		t.Fatalf("HasStamp('nonexistent') returned error: %v", err)
	}
	if ok {
		t.Error("HasStamp('nonexistent') = true, want false")
	}
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != "wid-hasstamp-001" {
		t.Errorf("metadata x-flow-workitem-id = %v, want [wid-hasstamp-001]", got)
	}
}

func TestArtefact_HasStamp_Error(t *testing.T) {
	art := setupArtefactErrorEnv(t, "wid-hasstamp-err-001")
	_, err := art.HasStamp(stampLinter)
	if err == nil {
		t.Fatal("HasStamp() expected error, got nil")
	}
}

func TestArtefact_GetFeedback(t *testing.T) {
	env, art := setupArtefactTestEnv(t, "wid-fb-001")
	feedback, err := art.GetFeedback()
	if err != nil {
		t.Fatalf("GetFeedback() returned error: %v", err)
	}
	if len(feedback) != 2 {
		t.Fatalf("GetFeedback() returned %d items, want 2", len(feedback))
	}
	if feedback[0].GetID() != "fb-001" {
		t.Errorf("feedback[0].GetID() = %q, want %q", feedback[0].GetID(), "fb-001")
	}
	if feedback[0].GetMessage() != "needs revision" {
		t.Errorf("feedback[0].GetMessage() = %q, want %q", feedback[0].GetMessage(), "needs revision")
	}
	if feedback[1].GetID() != "fb-002" {
		t.Errorf("feedback[1].GetID() = %q, want %q", feedback[1].GetID(), "fb-002")
	}
	if feedback[1].GetMessage() != "looks good" {
		t.Errorf("feedback[1].GetMessage() = %q, want %q", feedback[1].GetMessage(), "looks good")
	}
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != "wid-fb-001" {
		t.Errorf("metadata x-flow-workitem-id = %v, want [wid-fb-001]", got)
	}
}

func TestArtefact_GetFeedback_Error(t *testing.T) {
	art := setupArtefactErrorEnv(t, "wid-fb-err-001")
	_, err := art.GetFeedback()
	if err == nil {
		t.Fatal("GetFeedback() expected error, got nil")
	}
}

func TestArtefact_HasUnresolvedFeedback(t *testing.T) {
	env, art := setupArtefactTestEnv(t, "wid-hasufb-001")
	has, err := art.HasUnresolvedFeedback()
	if err != nil {
		t.Fatalf("HasUnresolvedFeedback() returned error: %v", err)
	}
	if !has {
		t.Error("HasUnresolvedFeedback() = false, want true")
	}
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != "wid-hasufb-001" {
		t.Errorf("metadata x-flow-workitem-id = %v, want [wid-hasufb-001]", got)
	}
}

func TestArtefact_HasUnresolvedFeedback_Error(t *testing.T) {
	art := setupArtefactErrorEnv(t, "wid-hasufb-err-001")
	_, err := art.HasUnresolvedFeedback()
	if err == nil {
		t.Fatal("HasUnresolvedFeedback() expected error, got nil")
	}
}
