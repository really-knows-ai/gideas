package flow

import (
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Shared test constants to avoid goconst warnings.
const (
	testPetitionID    = "petition"
	testArtefactID    = "artefact-001"
	testArtefactID2   = "artefact-002"
	testArtXYZ        = "art-xyz-001"
	testNextOutput    = "next-output"
	testLaw1          = "law-1"
	testLaw2          = "law-2"
	testLawFriction   = "law-friction-001"
	testFB001         = "fb-001"
	testFB002         = "fb-002"
	testFBAuto001     = "fb-auto-001"
	testTextMarkdown  = "text/markdown"
	testChildAll      = `children.all(c, c.phase == "Completed")`
	testNeedsRevision = "needs revision"
	testLooksGood     = "looks good"
	testTestContent   = "test-content"
	testTestArtefact  = "test-artefact"
)

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

// setupWorkitemTestEnv creates a testEnv with a spy server and returns a
// *Workitem wired to the session. The workitem's ID is set to workitemID.
func setupWorkitemTestEnv(t *testing.T, workitemID string) (*Workitem, *testEnv) {
	t.Helper()
	env := setupTestEnv(t, workitemID)
	wi, err := env.client.GetWorkitem()
	if err != nil {
		t.Fatalf("GetWorkitem() failed: %v", err)
	}
	return wi, env
}

// ---------------------------------------------------------------------------
// Lifecycle — Complete
// ---------------------------------------------------------------------------

func TestWorkitem_Complete_NoOptions(t *testing.T) {
	const wantID = "wi-complete-noopts-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	err := wi.Complete()
	if err != nil {
		t.Fatalf("Complete() returned error: %v", err)
	}

	req := env.spy.lastSubmitReq
	if req == nil {
		t.Fatal("SubmitResult was not called")
	}
	complete, ok := req.GetAction().(*flowv1.SubmitResultRequest_Complete)
	if !ok {
		t.Fatalf("expected CompleteAction, got %T", req.GetAction())
	}
	if complete.Complete.GetReason() != flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED {
		t.Fatalf("reason = %v, want UNSPECIFIED", complete.Complete.GetReason())
	}
	if req.GetWorkitemId() != wantID {
		t.Fatalf("workitem_id = %q, want %q", req.GetWorkitemId(), wantID)
	}
}

func TestWorkitem_Complete_WithReason(t *testing.T) {
	const wantID = "wi-complete-reason-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	reason := flowv1.CompletionReason_COMPLETION_REASON_CANCELLED
	err := wi.Complete(WithReason(reason))
	if err != nil {
		t.Fatalf("Complete(WithReason) returned error: %v", err)
	}

	req := env.spy.lastSubmitReq
	if req == nil {
		t.Fatal("SubmitResult was not called")
	}
	complete, ok := req.GetAction().(*flowv1.SubmitResultRequest_Complete)
	if !ok {
		t.Fatalf("expected CompleteAction, got %T", req.GetAction())
	}
	if complete.Complete.GetReason() != reason {
		t.Fatalf("reason = %v, want %v", complete.Complete.GetReason(), reason)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle — RouteTo
// ---------------------------------------------------------------------------

func TestWorkitem_RouteTo(t *testing.T) {
	const wantID = "wi-route-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	err := wi.RouteTo(testNextOutput)
	if err != nil {
		t.Fatalf("RouteTo() returned error: %v", err)
	}

	req := env.spy.lastSubmitReq
	if req == nil {
		t.Fatal("SubmitResult was not called")
	}
	route, ok := req.GetAction().(*flowv1.SubmitResultRequest_Route)
	if !ok {
		t.Fatalf("expected RouteAction, got %T", req.GetAction())
	}
	if route.Route.GetTarget() != testNextOutput {
		t.Fatalf("target = %q, want %q", route.Route.GetTarget(), "next-output")
	}
	if !route.Route.GetOutput() {
		t.Fatal("expected Output=true")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle — Suspend
// ---------------------------------------------------------------------------

func TestWorkitem_Suspend_NoOptions(t *testing.T) {
	const wantID = "wi-suspend-noopts-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	err := wi.Suspend()
	if err != nil {
		t.Fatalf("Suspend() returned error: %v", err)
	}

	req := env.spy.lastSubmitReq
	if req == nil {
		t.Fatal("SubmitResult was not called")
	}
	suspend, ok := req.GetAction().(*flowv1.SubmitResultRequest_Suspend)
	if !ok {
		t.Fatalf("expected SuspendAction, got %T", req.GetAction())
	}
	if suspend.Suspend.GetCondition() != "" {
		t.Fatalf("expected empty condition, got %q", suspend.Suspend.GetCondition())
	}
	if suspend.Suspend.GetTimeout() != nil {
		t.Fatalf("expected nil timeout, got %v", suspend.Suspend.GetTimeout())
	}

	// Verify local cache was set.
	if !wi.suspended {
		t.Fatal("expected wi.suspended=true after Suspend()")
	}
}

func TestWorkitem_Suspend_WithCondition(t *testing.T) {
	const wantID = "wi-suspend-cond-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	err := wi.Suspend(WithCondition(testChildAll))
	if err != nil {
		t.Fatalf("Suspend(WithCondition) returned error: %v", err)
	}

	req := env.spy.lastSubmitReq
	suspend, ok := req.GetAction().(*flowv1.SubmitResultRequest_Suspend)
	if !ok {
		t.Fatalf("expected SuspendAction, got %T", req.GetAction())
	}
	if suspend.Suspend.GetCondition() != testChildAll {
		t.Fatalf("condition = %q, want %q", suspend.Suspend.GetCondition(), testChildAll)
	}
}

func TestWorkitem_Suspend_WithTimeout(t *testing.T) {
	const wantID = "wi-suspend-timeout-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	err := wi.Suspend(WithSuspendTimeout(5 * time.Minute))
	if err != nil {
		t.Fatalf("Suspend(WithTimeout) returned error: %v", err)
	}

	req := env.spy.lastSubmitReq
	suspend, ok := req.GetAction().(*flowv1.SubmitResultRequest_Suspend)
	if !ok {
		t.Fatalf("expected SuspendAction, got %T", req.GetAction())
	}
	wantTimeout := durationpb.New(5 * time.Minute)
	if suspend.Suspend.GetTimeout().GetSeconds() != wantTimeout.GetSeconds() {
		t.Fatalf("timeout = %v, want %v", suspend.Suspend.GetTimeout(), wantTimeout)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle — Resume
// ---------------------------------------------------------------------------

func TestWorkitem_Resume(t *testing.T) {
	const wantID = "wi-resume-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	// First suspend to set local state.
	wi.suspended = true

	err := wi.Resume()
	if err != nil {
		t.Fatalf("Resume() returned error: %v", err)
	}

	req := env.spy.lastResumeReq
	if req == nil {
		t.Fatal("ResumeWorkitem was not called")
	}
	if req.GetWorkitemId() != wantID {
		t.Fatalf("workitem_id = %q, want %q", req.GetWorkitemId(), wantID)
	}

	// Verify local cache was cleared.
	if wi.suspended {
		t.Fatal("expected wi.suspended=false after Resume()")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle — Heartbeat
// ---------------------------------------------------------------------------

func TestWorkitem_Heartbeat(t *testing.T) {
	const wantID = "wi-heartbeat-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	err := wi.Heartbeat()
	if err != nil {
		t.Fatalf("Heartbeat() returned error: %v", err)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle — PauseTimer / ResumeTimer
// ---------------------------------------------------------------------------

func TestWorkitem_PauseTimer(t *testing.T) {
	const wantID = "wi-pausetimer-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	err := wi.PauseTimer()
	if err != nil {
		t.Fatalf("PauseTimer() returned error: %v", err)
	}

	req := env.spy.lastPauseTimerReq
	if req == nil {
		t.Fatal("PauseTimer was not called")
	}
	if req.GetWorkitemId() != wantID {
		t.Fatalf("workitem_id = %q, want %q", req.GetWorkitemId(), wantID)
	}
}

func TestWorkitem_ResumeTimer(t *testing.T) {
	const wantID = "wi-resumetimer-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	err := wi.ResumeTimer()
	if err != nil {
		t.Fatalf("ResumeTimer() returned error: %v", err)
	}

	req := env.spy.lastResumeTimerReq
	if req == nil {
		t.Fatal("ResumeTimer was not called")
	}
	if req.GetWorkitemId() != wantID {
		t.Fatalf("workitem_id = %q, want %q", req.GetWorkitemId(), wantID)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle — IsSuspended (local cache)
// ---------------------------------------------------------------------------

func TestWorkitem_IsSuspended_DefaultFalse(t *testing.T) {
	wi, _ := setupWorkitemTestEnv(t, "wi-issuspended-false-001")

	suspended, err := wi.IsSuspended()
	if err != nil {
		t.Fatalf("IsSuspended() returned error: %v", err)
	}
	if suspended {
		t.Fatal("expected IsSuspended()=false by default")
	}
}

func TestWorkitem_IsSuspended_AfterSuspend(t *testing.T) {
	wi, _ := setupWorkitemTestEnv(t, "wi-issuspended-true-001")

	// Simulate a successful suspend — the local cache should be set.
	wi.suspended = true

	suspended, err := wi.IsSuspended()
	if err != nil {
		t.Fatalf("IsSuspended() returned error: %v", err)
	}
	if !suspended {
		t.Fatal("expected IsSuspended()=true after Suspend()")
	}
}

func TestWorkitem_IsSuspended_AfterResume(t *testing.T) {
	wi, _ := setupWorkitemTestEnv(t, "wi-issuspended-resume-001")

	// Simulate suspend then resume.
	wi.suspended = true
	wi.suspended = false

	suspended, err := wi.IsSuspended()
	if err != nil {
		t.Fatalf("IsSuspended() returned error: %v", err)
	}
	if suspended {
		t.Fatal("expected IsSuspended()=false after Resume()")
	}
}

// ---------------------------------------------------------------------------
// Artefacts — GetArtefact
// ---------------------------------------------------------------------------

func TestWorkitem_GetArtefact(t *testing.T) {
	const wantID = "wi-getartefact-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	art, err := wi.GetArtefact(testPetitionID)
	if err != nil {
		t.Fatalf("GetArtefact() returned error: %v", err)
	}
	if art == nil {
		t.Fatal("expected non-nil Artefact")
	}

	req := env.spy.lastGetArtefactReq
	if req == nil {
		t.Fatal("GetArtefact was not called on the server")
	}
	if req.GetWorkitemId() != wantID {
		t.Fatalf("workitem_id = %q, want %q", req.GetWorkitemId(), wantID)
	}
	if req.GetArtefactId() != testPetitionID {
		t.Fatalf("artefact_id = %q, want %q", req.GetArtefactId(), testPetitionID)
	}

	// Verify the returned domain object has correct fields.
	if art.ID() != testPetitionID {
		t.Fatalf("Artefact.ID() = %q, want %q", art.ID(), testPetitionID)
	}
	if art.GovernedArtefact() != testTestArtefact {
		t.Fatalf("Artefact.GovernedArtefact() = %q, want %q", art.GovernedArtefact(), testTestArtefact)
	}
}

func TestWorkitem_GetArtefact_SessionWired(t *testing.T) {
	const wantID = "wi-getartefact-session-001"
	wi, _ := setupWorkitemTestEnv(t, wantID)

	art, err := wi.GetArtefact(testPetitionID)
	if err != nil {
		t.Fatalf("GetArtefact() returned error: %v", err)
	}

	// ID() is a local getter — no RPC needed.
	if art.ID() != testPetitionID {
		t.Fatalf("Artefact.ID() = %q, want %q", art.ID(), testPetitionID)
	}

	// GetContent() makes an RPC through the session — verifies the session is wired.
	content, err := art.GetContent()
	if err != nil {
		t.Fatalf("Artefact.GetContent() returned error: %v", err)
	}
	if string(content) != testTestContent {
		t.Fatalf("content = %q, want %q", string(content), "test-content")
	}
}

// ---------------------------------------------------------------------------
// Artefacts — FindArtefact
// ---------------------------------------------------------------------------

func TestWorkitem_FindArtefact(t *testing.T) {
	const wantID = "wi-findartefact-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	art, err := wi.FindArtefact(testArtXYZ)
	if err != nil {
		t.Fatalf("FindArtefact() returned error: %v", err)
	}
	if art == nil {
		t.Fatal("expected non-nil Artefact")
	}

	req := env.spy.lastGetArtefactReq
	if req == nil {
		t.Fatal("GetArtefact was not called on the server")
	}
	if req.GetArtefactId() != testArtXYZ {
		t.Fatalf("artefact_id = %q, want %q", req.GetArtefactId(), "art-xyz-001")
	}

	if art.ID() != testArtXYZ {
		t.Fatalf("Artefact.ID() = %q, want %q", art.ID(), testArtXYZ)
	}
}

// ---------------------------------------------------------------------------
// Feedback — AddFeedback
// ---------------------------------------------------------------------------

func TestWorkitem_AddFeedback(t *testing.T) {
	const wantID = "wi-addfb-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	fbID, err := wi.AddFeedback(testArtefactID, true, testLooksGood)
	if err != nil {
		t.Fatalf("AddFeedback() returned error: %v", err)
	}

	req := env.spy.lastAddFeedbackReq
	if req == nil {
		t.Fatal("AddFeedback was not called on the server")
	}
	if req.GetArtefactId() != testArtefactID {
		t.Fatalf("artefact_id = %q, want %q", req.GetArtefactId(), "artefact-001")
	}
	if !req.GetCanWontFix() {
		t.Fatal("expected CanWontFix=true")
	}
	if req.GetMessage() != testLooksGood {
		t.Fatalf("message = %q, want %q", req.GetMessage(), testLooksGood)
	}

	if fbID != testFBAuto001 {
		t.Fatalf("feedback ID = %q, want %q", fbID, "fb-auto-001")
	}
}

// ---------------------------------------------------------------------------
// Feedback — GetFeedback
// ---------------------------------------------------------------------------

func TestWorkitem_GetFeedback(t *testing.T) {
	const wantID = "wi-getfb-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	fbs, err := wi.GetFeedback(testArtefactID)
	if err != nil {
		t.Fatalf("GetFeedback() returned error: %v", err)
	}
	if len(fbs) != 2 {
		t.Fatalf("expected 2 feedback items, got %d", len(fbs))
	}

	req := env.spy.lastGetFeedbackReq
	if req == nil {
		t.Fatal("GetFeedback was not called on the server")
	}
	if req.GetArtefactId() != testArtefactID {
		t.Fatalf("artefact_id = %q, want %q", req.GetArtefactId(), "artefact-001")
	}

	// Verify returned domain objects have correct fields.
	if fbs[0].GetID() != testFB001 {
		t.Fatalf("Feedback[0].GetID() = %q, want %q", fbs[0].GetID(), testFB001)
	}
	if fbs[0].GetMessage() != testNeedsRevision {
		t.Fatalf("Feedback[0].GetMessage() = %q, want %q", fbs[0].GetMessage(), testNeedsRevision)
	}
	if fbs[1].GetID() != testFB002 {
		t.Fatalf("Feedback[1].GetID() = %q, want %q", fbs[1].GetID(), testFB002)
	}
}

func TestWorkitem_GetFeedback_SessionWired(t *testing.T) {
	const wantID = "wi-getfb-session-001"
	wi, _ := setupWorkitemTestEnv(t, wantID)

	fbs, err := wi.GetFeedback(testArtefactID)
	if err != nil {
		t.Fatalf("GetFeedback() returned error: %v", err)
	}
	if len(fbs) == 0 {
		t.Fatal("expected at least one feedback item")
	}

	// GetID() is local — no RPC.
	if fbs[0].GetID() == "" {
		t.Fatal("expected non-empty Feedback ID")
	}

	// GetDepth() makes an RPC — verifies the session is wired.
	depth, err := fbs[0].GetDepth()
	if err != nil {
		t.Fatalf("Feedback.GetDepth() returned error: %v", err)
	}
	if depth != 5 {
		t.Fatalf("expected depth=5, got %d", depth)
	}
}

// ---------------------------------------------------------------------------
// Feedback — HasUnresolvedFeedback
// ---------------------------------------------------------------------------

func TestWorkitem_HasUnresolvedFeedback(t *testing.T) {
	const wantID = "wi-hasunresolved-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	unresolved, err := wi.HasUnresolvedFeedback(testArtefactID)
	if err != nil {
		t.Fatalf("HasUnresolvedFeedback() returned error: %v", err)
	}
	if !unresolved {
		t.Fatal("expected HasUnresolved=true from spy response")
	}

	req := env.spy.lastHasUnresolvedReq
	if req == nil {
		t.Fatal("HasUnresolvedFeedback was not called on the server")
	}
	if req.GetArtefactId() != testArtefactID {
		t.Fatalf("artefact_id = %q, want %q", req.GetArtefactId(), "artefact-001")
	}
}

// ---------------------------------------------------------------------------
// Laws — GetLawGroups
// ---------------------------------------------------------------------------

func TestWorkitem_GetLawGroups(t *testing.T) {
	const wantID = "wi-getlawgroups-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	// The spy QueryLaws returns [{Id: "law-1"}] (no group field, so "default").
	// The spy ListLawGroups returns [group-a, group-b].
	groups, err := wi.GetLawGroups(testTextMarkdown)
	_ = env
	if err != nil {
		t.Fatalf("GetLawGroups() returned error: %v", err)
	}

	// Laws have no group, so they fall under "default".
	// "default" is not in ListLawGroups, so built-in defaults are used.
	if len(groups) != 1 {
		t.Fatalf("expected 1 law group, got %d", len(groups))
	}
	if groups[0].Name() != "default" {
		t.Fatalf("group[0].Name() = %q, want %q", groups[0].Name(), "default")
	}
	if groups[0].Mode() != GroupModeBundle {
		t.Fatalf("group[0].Mode() = %q, want %q", groups[0].Mode(), GroupModeBundle)
	}
}

func TestWorkitem_GetLawGroups_EmptyRepType(t *testing.T) {
	const wantID = "wi-getlawgroups-empty-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	// Empty repType queries all laws (no filter).
	groups, err := wi.GetLawGroups("")
	_ = env // used below
	if err != nil {
		t.Fatalf("GetLawGroups(\"\") returned error: %v", err)
	}

	// Same result as with "text/markdown": one "default" group.
	if len(groups) != 1 {
		t.Fatalf("expected 1 law group, got %d", len(groups))
	}
	if groups[0].Name() != "default" {
		t.Fatalf("group[0].Name() = %q, want %q", groups[0].Name(), "default")
	}

	// Verify QueryLaws was called.
	if env.spy.lastQueryLawsReq == nil {
		t.Fatal("QueryLaws was not called")
	}
	// Filter should be nil for empty repType.
	if env.spy.lastQueryLawsReq.GetFilter() != nil {
		t.Fatal("expected nil filter for empty repType")
	}
}

// ---------------------------------------------------------------------------
// Laws — VerifyLawAttestations
// ---------------------------------------------------------------------------

func TestWorkitem_VerifyLawAttestations(t *testing.T) {
	const wantID = "wi-verifyattest-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	// The spy QueryLaws returns [{Id: "law-1"}] which has no representations.
	// So expected stamps is empty, and verify should return nil.
	missing, err := wi.VerifyLawAttestations(testPetitionID)
	if err != nil {
		t.Fatalf("VerifyLawAttestations() returned error: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("expected 0 missing stamps, got %d: %v", len(missing), missing)
	}

	// Verify QueryLaws was called with the correct governed artefact.
	if env.spy.lastQueryLawsReq == nil {
		t.Fatal("QueryLaws was not called")
	}
	f := env.spy.lastQueryLawsReq.GetFilter()
	if f == nil {
		t.Fatal("expected non-nil filter")
	}
	if f.GetGovernedArtefact() != testPetitionID {
		t.Fatalf("governed_artefact = %q, want %q", f.GetGovernedArtefact(), "petition")
	}
}

// ---------------------------------------------------------------------------
// Laws — Cite
// ---------------------------------------------------------------------------

func TestWorkitem_Cite(t *testing.T) {
	const wantID = "wi-cite-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	err := wi.Cite(testLaw1, testLaw2)
	if err != nil {
		t.Fatalf("Cite() returned error: %v", err)
	}

	req := env.spy.lastCiteReq
	if req == nil {
		t.Fatal("Cite was not called on the server")
	}
	if len(req.GetLawIds()) != 2 {
		t.Fatalf("expected 2 law IDs, got %d", len(req.GetLawIds()))
	}
	if req.GetLawIds()[0] != testLaw1 || req.GetLawIds()[1] != testLaw2 {
		t.Fatalf("law_ids = %v, want [law-1 law-2]", req.GetLawIds())
	}
}

// ---------------------------------------------------------------------------
// Topology — GetTopology
// ---------------------------------------------------------------------------

func TestWorkitem_GetTopology(t *testing.T) {
	const wantID = "wi-gettopology-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	flow, err := wi.GetTopology()
	if err != nil {
		t.Fatalf("GetTopology() returned error: %v", err)
	}
	if flow == nil {
		t.Fatal("expected non-nil Flow")
	}
	if flow.pb == nil {
		t.Fatal("expected non-nil Flow.pb")
	}

	// Verify metadata injection.
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}

	// Verify stub content (Phase 1 stub — full accessors in Phase 7).
	if flow.pb.GetSelf().GetName() != testNodeName {
		t.Fatalf("self.name = %q, want %q", flow.pb.GetSelf().GetName(), testNodeName)
	}
}

// ---------------------------------------------------------------------------
// Friction — QueryFriction
// ---------------------------------------------------------------------------

func TestWorkitem_QueryFriction(t *testing.T) {
	const wantID = "wi-queryfriction-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	aggs, err := wi.QueryFriction(&flowv1.FrictionFilter{LawId: testLawFriction})
	if err != nil {
		t.Fatalf("QueryFriction() returned error: %v", err)
	}
	if len(aggs) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggs))
	}
	if aggs[0].GetLawId() != testLawFriction {
		t.Fatalf("law_id = %q, want %q", aggs[0].GetLawId(), "law-friction-001")
	}

	// Verify metadata injection.
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Workitem fields — namespace mirrors session
// ---------------------------------------------------------------------------

func TestWorkitem_NamespaceMirrorsSession(t *testing.T) {
	const wantID = "wi-namespace-001"
	wi, _ := setupWorkitemTestEnv(t, wantID)

	if wi.namespace != wi.session.namespace {
		t.Fatalf("Workitem.namespace = %q, want session.namespace = %q", wi.namespace, wi.session.namespace)
	}
}

// ---------------------------------------------------------------------------
// Workitem — ID()
// ---------------------------------------------------------------------------

func TestWorkitem_ID(t *testing.T) {
	const wantID = "wi-id-001"
	wi, _ := setupWorkitemTestEnv(t, wantID)

	if wi.ID() != wantID {
		t.Fatalf("Workitem.ID() = %q, want %q", wi.ID(), wantID)
	}
}
