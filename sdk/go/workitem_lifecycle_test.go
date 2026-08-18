package flow

import (
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/protobuf/types/known/durationpb"
)

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
