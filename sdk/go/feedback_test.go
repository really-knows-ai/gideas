package flow

import (
	"strings"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

// newTestFeedback creates a Feedback wired to the test env's session.
func newTestFeedback(t *testing.T, env *testEnv, id string) *Feedback {
	t.Helper()
	return &Feedback{
		item: &flowv1.FeedbackItem{
			Id:      id,
			Message: "test message",
			State:   flowv1.FeedbackState_FEEDBACK_STATE_NEW,
			Source:  "reviewer",
		},
		session: env.client.session,
	}
}

// ---------------------------------------------------------------------------
// Local getter tests (no server required)
// ---------------------------------------------------------------------------

func TestFeedback_GetID(t *testing.T) {
	fb := &Feedback{item: &flowv1.FeedbackItem{Id: "fb-001"}}
	if got := fb.GetID(); got != "fb-001" {
		t.Fatalf("GetID() = %q, want %q", got, "fb-001")
	}
}

func TestFeedback_GetMessage(t *testing.T) {
	fb := &Feedback{item: &flowv1.FeedbackItem{Message: "hello"}}
	if got := fb.GetMessage(); got != "hello" {
		t.Fatalf("GetMessage() = %q, want %q", got, "hello")
	}
}

func TestFeedback_GetState(t *testing.T) {
	states := []struct {
		name  string
		state flowv1.FeedbackState
	}{
		{"New", flowv1.FeedbackState_FEEDBACK_STATE_NEW},
		{"Actioned", flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED},
		{"WontFix", flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX},
		{"Rejected", flowv1.FeedbackState_FEEDBACK_STATE_REJECTED},
		{"Deadlocked", flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
		{"Resolved", flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
	}
	for _, tt := range states {
		t.Run(tt.name, func(t *testing.T) {
			fb := &Feedback{item: &flowv1.FeedbackItem{State: tt.state}}
			if got := fb.GetState(); got != tt.state {
				t.Fatalf("GetState() = %v, want %v", got, tt.state)
			}
		})
	}
}

func TestFeedback_GetSource(t *testing.T) {
	fb := &Feedback{item: &flowv1.FeedbackItem{Source: "appraiser"}}
	if got := fb.GetSource(); got != "appraiser" {
		t.Fatalf("GetSource() = %q, want %q", got, "appraiser")
	}
}

func TestFeedback_NilSafe(t *testing.T) {
	// Construct with zero-value item fields — must not panic.
	fb := &Feedback{item: &flowv1.FeedbackItem{}}
	if got := fb.GetID(); got != "" {
		t.Fatalf("expected empty GetID(), got %q", got)
	}
	if got := fb.GetMessage(); got != "" {
		t.Fatalf("expected empty GetMessage(), got %q", got)
	}
	if got := fb.GetState(); got != flowv1.FeedbackState_FEEDBACK_STATE_UNSPECIFIED {
		t.Fatalf("expected unspecified state, got %v", got)
	}
	if got := fb.GetSource(); got != "" {
		t.Fatalf("expected empty GetSource(), got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle method tests (spy server)
// ---------------------------------------------------------------------------

func TestFeedback_Resolve(t *testing.T) {
	const wantID = "fb-resolve-001"
	env := setupTestEnv(t, "wi-resolve-001")
	fb := newTestFeedback(t, env, wantID)

	err := fb.Resolve("fix applied")
	if err != nil {
		t.Fatalf("Resolve() returned error: %v", err)
	}

	req := env.spy.lastResolveFeedbackReq
	if req == nil {
		t.Fatal("ResolveFeedback was not called")
	}
	if req.GetFeedbackId() != wantID {
		t.Fatalf("feedback_id = %q, want %q", req.GetFeedbackId(), wantID)
	}
	if req.GetMessage() != "fix applied" {
		t.Fatalf("message = %q, want %q", req.GetMessage(), "fix applied")
	}
	if req.GetWorkitemId() != "wi-resolve-001" {
		t.Fatalf("workitem_id = %q, want %q", req.GetWorkitemId(), "wi-resolve-001")
	}
}

func TestFeedback_Refuse(t *testing.T) {
	const wantID = "fb-refuse-001"
	env := setupTestEnv(t, "wi-refuse-001")
	fb := newTestFeedback(t, env, wantID)

	just := &flowv1.Justification{
		Kind: &flowv1.Justification_Citation{
			Citation: &flowv1.Citation{CitationIds: []string{"law-42"}},
		},
	}
	err := fb.Refuse(just)
	if err != nil {
		t.Fatalf("Refuse() returned error: %v", err)
	}

	req := env.spy.lastRefuseFeedbackReq
	if req == nil {
		t.Fatal("RefuseFeedback was not called")
	}
	if req.GetFeedbackId() != wantID {
		t.Fatalf("feedback_id = %q, want %q", req.GetFeedbackId(), wantID)
	}
	if req.GetJustification() == nil {
		t.Fatal("justification is nil")
	}
	if req.GetJustification().GetCitation() == nil {
		t.Fatal("expected citation justification")
	}
	if len(req.GetJustification().GetCitation().GetCitationIds()) != 1 ||
		req.GetJustification().GetCitation().GetCitationIds()[0] != "law-42" {
		t.Fatalf("unexpected citation: %v", req.GetJustification().GetCitation().GetCitationIds())
	}
}

func TestFeedback_Refuse_NovelArgument(t *testing.T) {
	const wantID = "fb-refuse-novel-001"
	env := setupTestEnv(t, "wi-refuse-novel-001")
	fb := newTestFeedback(t, env, wantID)

	just := &flowv1.Justification{
		Kind: &flowv1.Justification_NovelArgument{
			NovelArgument: &flowv1.NovelArgument{Argument: "new reasoning"},
		},
	}
	err := fb.Refuse(just)
	if err != nil {
		t.Fatalf("Refuse() returned error: %v", err)
	}

	req := env.spy.lastRefuseFeedbackReq
	if req == nil {
		t.Fatal("RefuseFeedback was not called")
	}
	if req.GetJustification().GetNovelArgument() == nil {
		t.Fatal("expected novel argument")
	}
	if req.GetJustification().GetNovelArgument().GetArgument() != "new reasoning" {
		t.Fatalf("argument = %q, want %q",
			req.GetJustification().GetNovelArgument().GetArgument(), "new reasoning")
	}
}

func TestFeedback_AcceptFix(t *testing.T) {
	const wantID = "fb-acceptfix-001"
	env := setupTestEnv(t, "wi-acceptfix-001")
	fb := newTestFeedback(t, env, wantID)

	err := fb.AcceptFix()
	if err != nil {
		t.Fatalf("AcceptFix() returned error: %v", err)
	}

	req := env.spy.lastAcceptFixReq
	if req == nil {
		t.Fatal("AcceptFix was not called")
	}
	if req.GetFeedbackId() != wantID {
		t.Fatalf("feedback_id = %q, want %q", req.GetFeedbackId(), wantID)
	}
	if req.GetWorkitemId() != "wi-acceptfix-001" {
		t.Fatalf("workitem_id = %q, want %q", req.GetWorkitemId(), "wi-acceptfix-001")
	}
}

func TestFeedback_RejectFix(t *testing.T) {
	const wantID = "fb-rejectfix-001"
	env := setupTestEnv(t, "wi-rejectfix-001")
	fb := newTestFeedback(t, env, wantID)

	err := fb.RejectFix("still wrong")
	if err != nil {
		t.Fatalf("RejectFix() returned error: %v", err)
	}

	req := env.spy.lastRejectFixReq
	if req == nil {
		t.Fatal("RejectFix was not called")
	}
	if req.GetFeedbackId() != wantID {
		t.Fatalf("feedback_id = %q, want %q", req.GetFeedbackId(), wantID)
	}
	if req.GetMessage() != "still wrong" {
		t.Fatalf("message = %q, want %q", req.GetMessage(), "still wrong")
	}
}

func TestFeedback_AcceptRefusal(t *testing.T) {
	const wantID = "fb-acceptrefusal-001"
	env := setupTestEnv(t, "wi-acceptrefusal-001")
	fb := newTestFeedback(t, env, wantID)

	err := fb.AcceptRefusal()
	if err != nil {
		t.Fatalf("AcceptRefusal() returned error: %v", err)
	}

	req := env.spy.lastAcceptRefusalReq
	if req == nil {
		t.Fatal("AcceptRefusal was not called")
	}
	if req.GetFeedbackId() != wantID {
		t.Fatalf("feedback_id = %q, want %q", req.GetFeedbackId(), wantID)
	}
}

func TestFeedback_RejectRefusal(t *testing.T) {
	const wantID = "fb-rejectrefusal-001"
	env := setupTestEnv(t, "wi-rejectrefusal-001")
	fb := newTestFeedback(t, env, wantID)

	err := fb.RejectRefusal("invalid citation")
	if err != nil {
		t.Fatalf("RejectRefusal() returned error: %v", err)
	}

	req := env.spy.lastRejectRefusalReq
	if req == nil {
		t.Fatal("RejectRefusal was not called")
	}
	if req.GetFeedbackId() != wantID {
		t.Fatalf("feedback_id = %q, want %q", req.GetFeedbackId(), wantID)
	}
	if req.GetMessage() != "invalid citation" {
		t.Fatalf("message = %q, want %q", req.GetMessage(), "invalid citation")
	}
}

func TestFeedback_Deadlock(t *testing.T) {
	const wantID = "fb-deadlock-001"
	env := setupTestEnv(t, "wi-deadlock-001")
	fb := newTestFeedback(t, env, wantID)

	err := fb.Deadlock()
	if err != nil {
		t.Fatalf("Deadlock() returned error: %v", err)
	}

	req := env.spy.lastDeadlockFeedbackReq
	if req == nil {
		t.Fatal("DeadlockFeedback was not called")
	}
	if req.GetFeedbackId() != wantID {
		t.Fatalf("feedback_id = %q, want %q", req.GetFeedbackId(), wantID)
	}
	if req.GetWorkitemId() != "wi-deadlock-001" {
		t.Fatalf("workitem_id = %q, want %q", req.GetWorkitemId(), "wi-deadlock-001")
	}
}

func TestFeedback_LinkRuling(t *testing.T) {
	const wantID = "fb-linkruling-001"
	env := setupTestEnv(t, "wi-linkruling-001")
	fb := newTestFeedback(t, env, wantID)

	err := fb.LinkRuling("law-42", FeedbackStateResolved)
	if err != nil {
		t.Fatalf("LinkRuling() returned error: %v", err)
	}

	req := env.spy.lastLinkRulingReq
	if req == nil {
		t.Fatal("LinkRuling was not called")
	}
	if req.GetFeedbackId() != wantID {
		t.Fatalf("feedback_id = %q, want %q", req.GetFeedbackId(), wantID)
	}
	if req.GetLawId() != "law-42" {
		t.Fatalf("law_id = %q, want %q", req.GetLawId(), "law-42")
	}
	if req.GetTargetState() != FeedbackStateResolved {
		t.Fatalf("target_state = %v, want %v", req.GetTargetState(), FeedbackStateResolved)
	}
}

// ---------------------------------------------------------------------------
// Server error tests
// ---------------------------------------------------------------------------

func TestFeedback_ServerError(t *testing.T) {
	env := setupTestEnv(t, "wi-err-001")
	env.spy.feedbackErr = status.Error(codes.Internal, "server error")

	tests := []struct {
		name string
		fn   func(*Feedback) error
		want string
	}{
		{"Resolve", func(fb *Feedback) error { return fb.Resolve("msg") }, "resolve feedback"},
		{"Refuse", func(fb *Feedback) error { return fb.Refuse(&flowv1.Justification{}) }, "refuse feedback"},
		{"AcceptFix", func(fb *Feedback) error { return fb.AcceptFix() }, "accept fix"},
		{"RejectFix", func(fb *Feedback) error { return fb.RejectFix("msg") }, "reject fix"},
		{"AcceptRefusal", func(fb *Feedback) error { return fb.AcceptRefusal() }, "accept refusal"},
		{"RejectRefusal", func(fb *Feedback) error { return fb.RejectRefusal("msg") }, "reject refusal"},
		{"Deadlock", func(fb *Feedback) error { return fb.Deadlock() }, "deadlock feedback"},
		{"LinkRuling", func(fb *Feedback) error {
			return fb.LinkRuling("law-42", FeedbackStateResolved)
		}, "link ruling"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fb := newTestFeedback(t, env, "fb-err-001")
			err := tt.fn(fb)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want containing %q", err.Error(), tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Round-trip getter tests
// ---------------------------------------------------------------------------

func TestFeedback_GetDepth(t *testing.T) {
	const wantID = "fb-depth-001"
	env := setupTestEnv(t, "wi-depth-001")
	fb := newTestFeedback(t, env, wantID)

	depth, err := fb.GetDepth()
	if err != nil {
		t.Fatalf("GetDepth() returned error: %v", err)
	}
	if depth != 5 {
		t.Fatalf("GetDepth() = %d, want %d", depth, 5)
	}

	req := env.spy.lastGetFeedbackDepthReq
	if req == nil {
		t.Fatal("GetFeedbackDepth was not called")
	}
	if req.GetFeedbackId() != wantID {
		t.Fatalf("feedback_id = %q, want %q", req.GetFeedbackId(), wantID)
	}
}

func TestFeedback_GetDepth_ServerError(t *testing.T) {
	env := setupTestEnv(t, "wi-depth-err-001")
	env.spy.feedbackErr = status.Error(codes.Internal, "server error")
	fb := newTestFeedback(t, env, "fb-depth-err-001")

	_, err := fb.GetDepth()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "get feedback depth") {
		t.Fatalf("error = %q, want containing %q", err.Error(), "get feedback depth")
	}
}

// ---------------------------------------------------------------------------
// Metadata injection tests
// ---------------------------------------------------------------------------

func TestFeedback_Resolve_InjectsMetadata(t *testing.T) {
	const wantWID = "wi-metadata-resolve-001"
	env := setupTestEnv(t, wantWID)
	fb := newTestFeedback(t, env, "fb-metadata-001")

	err := fb.Resolve("fix applied")
	if err != nil {
		t.Fatalf("Resolve() returned error: %v", err)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 {
		t.Fatal("metadata x-flow-workitem-id was NOT present")
	}
	if got[0] != wantWID {
		t.Fatalf("metadata x-flow-workitem-id = %q, want %q", got[0], wantWID)
	}
}

func TestFeedback_LifecycleMethods_InjectMetadata(t *testing.T) {
	const wantWID = "wi-metadata-all-001"
	env := setupTestEnv(t, wantWID)

	tests := []struct {
		name string
		fn   func(*Feedback) error
	}{
		{"Resolve", func(fb *Feedback) error { return fb.Resolve("msg") }},
		{"Refuse", func(fb *Feedback) error {
			return fb.Refuse(&flowv1.Justification{})
		}},
		{"AcceptFix", func(fb *Feedback) error { return fb.AcceptFix() }},
		{"RejectFix", func(fb *Feedback) error { return fb.RejectFix("msg") }},
		{"AcceptRefusal", func(fb *Feedback) error { return fb.AcceptRefusal() }},
		{"RejectRefusal", func(fb *Feedback) error { return fb.RejectRefusal("msg") }},
		{"Deadlock", func(fb *Feedback) error { return fb.Deadlock() }},
		{"LinkRuling", func(fb *Feedback) error {
			return fb.LinkRuling("law-42", FeedbackStateResolved)
		}},
		{"GetDepth", func(fb *Feedback) error { _, err := fb.GetDepth(); return err }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fb := newTestFeedback(t, env, "fb-meta-"+tt.name)
			err := tt.fn(fb)
			if err != nil {
				t.Fatalf("%s() returned error: %v", tt.name, err)
			}
			got := env.spy.lastMD.Get("x-flow-workitem-id")
			if len(got) == 0 || got[0] != wantWID {
				t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantWID)
			}
		})
	}
}
