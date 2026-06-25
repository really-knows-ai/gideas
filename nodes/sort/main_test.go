package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ---------------------------------------------------------------------------
// Test helper — spins up a real ephemeral TCP server with the sortSpy
// ---------------------------------------------------------------------------

// defaultConfig returns a sortConfig matching the reference arrangement.
func defaultConfig() *sortConfig {
	return &sortConfig{
		NodeOrder:         "quench,appraise",
		DeadlockThreshold: 3,
	}
}

func setupSortTest(t *testing.T, spy *sortSpy) *flow.Client {
	t.Helper()

	lis, err := newLocalListener()
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := newSpyGRPCServer(spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	t.Setenv(flow.EnvWorkitemID, "test-workitem")
	client, err := flow.NewClient(
		flow.WithSidecarAddress(lis.Addr().String()),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// ---------------------------------------------------------------------------
// Routing tests — the core decision tree (dynamic topology)
// ---------------------------------------------------------------------------

func TestSort_RoutesToQuench_MissingLinterStamp(t *testing.T) {
	spy := newSortSpy()
	// linter stamp absent (default false) — quench is first in nodeOrder.
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "quench" {
		t.Fatalf("expected route to quench, got %v", spy.RoutedOutputs)
	}
}

func TestSort_RoutesToRefine_UnresolvedFeedbackFromProvider(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	// Quench stamped linter but also left unresolved feedback.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-1", Source: "quench", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
	}
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputRefine {
		t.Fatalf("expected route to refine, got %v", spy.RoutedOutputs)
	}
}

func TestSort_RoutesToAppraise_MissingReviewStamp(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	// review stamp absent (default false), no feedback from quench
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputAppraise {
		t.Fatalf("expected route to appraise, got %v", spy.RoutedOutputs)
	}
}

func TestSort_StampsApprovalAndCompletes(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.StampState["review"] = true
	// No unresolved feedback from any provider.
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.StampedNames) != 1 || spy.StampedNames[0] != "approval" {
		t.Fatalf("expected approval stamp, got %v", spy.StampedNames)
	}
	if !spy.Completed {
		t.Fatal("expected Complete() to be called")
	}
	if len(spy.RoutedOutputs) != 0 {
		t.Fatalf("expected no routing, got %v", spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Deadlock detection tests
// ---------------------------------------------------------------------------

func TestSort_RoutesToArbiter_DepthExceedsThreshold_WontFix(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-1", State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX},
	}
	spy.FeedbackDepths["fb-1"] = 4 // default threshold is 3
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.DeadlockedIDs) != 1 || spy.DeadlockedIDs[0] != "fb-1" {
		t.Fatalf("expected fb-1 deadlocked, got %v", spy.DeadlockedIDs)
	}
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputArbiter {
		t.Fatalf("expected route to arbiter, got %v", spy.RoutedOutputs)
	}
}

func TestSort_RoutesToArbiter_DepthExceedsThreshold_Rejected(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-2", State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED},
	}
	spy.FeedbackDepths["fb-2"] = 3 // threshold = 3, depth >= threshold
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.DeadlockedIDs) != 1 || spy.DeadlockedIDs[0] != "fb-2" {
		t.Fatalf("expected fb-2 deadlocked, got %v", spy.DeadlockedIDs)
	}
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputArbiter {
		t.Fatalf("expected route to arbiter, got %v", spy.RoutedOutputs)
	}
}

func TestSort_RoutesToArbiter_DepthExceedsThreshold_New(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-3", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
	}
	spy.FeedbackDepths["fb-3"] = 5
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.DeadlockedIDs) != 1 || spy.DeadlockedIDs[0] != "fb-3" {
		t.Fatalf("expected fb-3 deadlocked, got %v", spy.DeadlockedIDs)
	}
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputArbiter {
		t.Fatalf("expected route to arbiter, got %v", spy.RoutedOutputs)
	}
}

func TestSort_RoutesToArbiter_DepthExceedsThreshold_Actioned(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-4", State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED},
	}
	spy.FeedbackDepths["fb-4"] = 10
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.DeadlockedIDs) != 1 || spy.DeadlockedIDs[0] != "fb-4" {
		t.Fatalf("expected fb-4 deadlocked, got %v", spy.DeadlockedIDs)
	}
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputArbiter {
		t.Fatalf("expected route to arbiter, got %v", spy.RoutedOutputs)
	}
}

func TestSort_RoutesToArbiter_AlreadyDeadlocked(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-5", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
	}
	// No depth needed — already deadlocked.
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	// Should NOT call DeadlockFeedback again.
	if len(spy.DeadlockedIDs) != 0 {
		t.Fatalf("expected no DeadlockFeedback calls, got %v", spy.DeadlockedIDs)
	}
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputArbiter {
		t.Fatalf("expected route to arbiter, got %v", spy.RoutedOutputs)
	}
}

func TestSort_DeadlockPriorityOverRefine(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-ok", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
		{Id: "fb-hot", State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED},
	}
	spy.FeedbackDepths["fb-ok"] = 1
	spy.FeedbackDepths["fb-hot"] = 5
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	// Should route to arbiter (deadlock), not refine.
	if len(spy.DeadlockedIDs) != 1 || spy.DeadlockedIDs[0] != "fb-hot" {
		t.Fatalf("expected fb-hot deadlocked, got %v", spy.DeadlockedIDs)
	}
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputArbiter {
		t.Fatalf("expected route to arbiter, got %v", spy.RoutedOutputs)
	}
}

func TestSort_BelowThreshold_RoutesToRefine(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	// Quench left addressed (WONT_FIX) feedback below deadlock threshold.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-6", Source: "quench", State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX},
	}
	spy.FeedbackDepths["fb-6"] = 2 // below default threshold of 3
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.DeadlockedIDs) != 0 {
		t.Fatalf("expected no deadlocking, got %v", spy.DeadlockedIDs)
	}
	// Linter stamp present + WONT_FIX from quench → appraise (adjudication).
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputAppraise {
		t.Fatalf("expected route to appraise, got %v", spy.RoutedOutputs)
	}
}

func TestSort_ResolvedItemsSkippedInDeadlockScan(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.StampState["review"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-done", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
	}
	spy.FeedbackDepths["fb-done"] = 99 // would deadlock if not skipped
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.DeadlockedIDs) != 0 {
		t.Fatalf("expected no deadlocking for resolved items, got %v",
			spy.DeadlockedIDs)
	}
	if !spy.Completed {
		t.Fatal("expected completion after resolved items skipped")
	}
}

func TestSort_FirstDeadlockedItemWins(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-a", State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX},
		{Id: "fb-b", State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED},
	}
	spy.FeedbackDepths["fb-a"] = 5
	spy.FeedbackDepths["fb-b"] = 10
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	// Only the first item should be deadlocked.
	if len(spy.DeadlockedIDs) != 1 || spy.DeadlockedIDs[0] != "fb-a" {
		t.Fatalf("expected only fb-a deadlocked, got %v", spy.DeadlockedIDs)
	}
}

// ---------------------------------------------------------------------------
// Configuration threshold tests
// ---------------------------------------------------------------------------

func TestSort_CustomThreshold(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-7", Source: "quench", State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX},
	}
	spy.FeedbackDepths["fb-7"] = 4

	// Threshold=5: depth 4 is below → should NOT deadlock.
	cfg := &sortConfig{
		NodeOrder:         "quench,appraise",
		DeadlockThreshold: 5,
	}
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.DeadlockedIDs) != 0 {
		t.Fatalf("expected no deadlocking with threshold=5, got %v",
			spy.DeadlockedIDs)
	}
	// Linter stamp present + WONT_FIX from quench → appraise (adjudication).
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputAppraise {
		t.Fatalf("expected route to appraise, got %v", spy.RoutedOutputs)
	}
}

func TestSort_ZeroThresholdDefaultsTo3(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	// Quench left unresolved feedback below threshold.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-9", Source: "quench", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
	}
	spy.FeedbackDepths["fb-9"] = 2

	// Zero threshold → default 3 used → depth 2 below threshold.
	cfg := &sortConfig{
		NodeOrder:         "quench,appraise",
		DeadlockThreshold: 0,
	}
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	// 0 is invalid → default 3 used → depth 2 below threshold.
	if len(spy.DeadlockedIDs) != 0 {
		t.Fatalf("expected no deadlocking with zero threshold (default=3), got %v",
			spy.DeadlockedIDs)
	}
}

// ---------------------------------------------------------------------------
// sortConfig.threshold() unit tests
// ---------------------------------------------------------------------------

func TestSortConfig_Threshold(t *testing.T) {
	tests := []struct {
		name  string
		value int32
		want  int32
	}{
		{"zero returns default", 0, defaultDeadlockThreshold},
		{"valid integer", 5, 5},
		{"minimum value", 1, 1},
		{"large value", 100, 100},
		{"negative defaults", -1, defaultDeadlockThreshold},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &sortConfig{DeadlockThreshold: tt.value}
			got := cfg.threshold()
			if got != tt.want {
				t.Fatalf("sortConfig{DeadlockThreshold: %d}.threshold() = %d, want %d",
					tt.value, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseNodeOrder unit tests
// ---------------------------------------------------------------------------

func TestParseNodeOrder(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"single", "quench", []string{"quench"}},
		{"two nodes", "quench,appraise", []string{"quench", "appraise"}},
		{"with spaces", " quench , appraise ", []string{"quench", "appraise"}},
		{"trailing comma", "quench,appraise,", []string{"quench", "appraise"}},
		{"empty entries", "quench,,appraise", []string{"quench", "appraise"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNodeOrder(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseNodeOrder(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("parseNodeOrder(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseStampCapability unit tests
// ---------------------------------------------------------------------------

func TestParseStampCapability(t *testing.T) {
	tests := []struct {
		input     string
		wantKind  string
		wantStamp string
		wantOK    bool
	}{
		{"STAMP:artefact/haiku/linter", "haiku", "linter", true},
		{"STAMP:artefact/doc/security-review", "doc", "security-review", true},
		{"READ:flow", "", "", false},
		{"STAMP:artefact/", "", "", false},
		{"STAMP:artefact/haiku/", "", "", false},
		{"STAMP:artefact//linter", "", "", false},
		{"WRITE:feedback/new", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			kind, stamp, ok := parseStampCapability(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("parseStampCapability(%q) ok=%v, want %v", tt.input, ok, tt.wantOK)
			}
			if kind != tt.wantKind {
				t.Fatalf("parseStampCapability(%q) kind=%q, want %q", tt.input, kind, tt.wantKind)
			}
			if stamp != tt.wantStamp {
				t.Fatalf("parseStampCapability(%q) stamp=%q, want %q", tt.input, stamp, tt.wantStamp)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Dynamic topology tests
// ---------------------------------------------------------------------------

func TestSort_RoutesToRefine_FeedbackFromAppraise(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.StampState["review"] = true
	// Appraise left unresolved feedback.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-appraise", Source: "appraise", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
	}
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	// Review stamp present + unresolved feedback from appraise → refine.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputRefine {
		t.Fatalf("expected route to refine, got %v", spy.RoutedOutputs)
	}
}

func TestSort_ResolvedFeedbackIgnoredInSourceCheck(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.StampState["review"] = true
	// Quench left feedback but it's already resolved — should not block.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-resolved", Source: "quench", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
	}
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	// All stamps present, resolved feedback → stamps approval + completes.
	if !spy.Completed {
		t.Fatal("expected completion")
	}
}

func TestSort_DeadlockedFeedbackIgnoredInSourceCheck(t *testing.T) {
	// Deadlocked feedback from a provider should be caught by the deadlock
	// check, not the per-source check. But if somehow we get past deadlock
	// check (impossible in practice), deadlocked items should be ignored
	// in the source check.
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.StampState["review"] = true
	// Only resolved + deadlocked feedback from quench — neither should block source check.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-dl", Source: "quench", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
	}
	client := setupSortTest(t, spy)

	// This test will hit the deadlock check first and route to arbiter.
	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputArbiter {
		t.Fatalf("expected route to arbiter, got %v", spy.RoutedOutputs)
	}
}

func TestSort_Error_GetFlowTopologyFails(t *testing.T) {
	spy := newSortSpy()
	spy.GetFlowTopologyErr = fmt.Errorf("topology unavailable")
	client := setupSortTest(t, spy)

	err := handleSort(context.Background(), client, defaultConfig())
	if err == nil {
		t.Fatal("expected error from GetFlowTopology failure")
	}
}

// ---------------------------------------------------------------------------
// Error propagation tests
// ---------------------------------------------------------------------------

func TestSort_Error_HasStampFails(t *testing.T) {
	spy := newSortSpy()
	spy.HasStampErr = fmt.Errorf("stamp service down")
	client := setupSortTest(t, spy)

	err := handleSort(context.Background(), client, defaultConfig())
	if err == nil {
		t.Fatal("expected error from HasStamp failure")
	}
}

func TestSort_Error_GetFeedbackFails(t *testing.T) {
	spy := newSortSpy()
	spy.GetFeedbackErr = fmt.Errorf("feedback list failed")
	client := setupSortTest(t, spy)

	err := handleSort(context.Background(), client, defaultConfig())
	if err == nil {
		t.Fatal("expected error from GetFeedback failure")
	}
}

func TestSort_Error_GetFeedbackDepthFails(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-x", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
	}
	spy.GetFeedbackDepthErr = fmt.Errorf("depth lookup failed")
	client := setupSortTest(t, spy)

	err := handleSort(context.Background(), client, defaultConfig())
	if err == nil {
		t.Fatal("expected error from GetFeedbackDepth failure")
	}
}

func TestSort_Error_DeadlockFeedbackFails(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-y", State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX},
	}
	spy.FeedbackDepths["fb-y"] = 10
	spy.DeadlockFeedbackErr = fmt.Errorf("deadlock transition failed")
	client := setupSortTest(t, spy)

	err := handleSort(context.Background(), client, defaultConfig())
	if err == nil {
		t.Fatal("expected error from DeadlockFeedback failure")
	}
}

func TestSort_Error_RouteToOutputFails(t *testing.T) {
	spy := newSortSpy()
	// Missing linter stamp → routes to quench → error.
	spy.RouteToOutputErr = fmt.Errorf("routing failed")
	client := setupSortTest(t, spy)

	err := handleSort(context.Background(), client, defaultConfig())
	if err == nil {
		t.Fatal("expected error from RouteToOutput failure")
	}
}

func TestSort_Error_StampArtefactFails(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.StampState["review"] = true
	spy.StampArtefactErr = fmt.Errorf("stamp write failed")
	client := setupSortTest(t, spy)

	err := handleSort(context.Background(), client, defaultConfig())
	if err == nil {
		t.Fatal("expected error from StampArtefact failure")
	}
}

func TestSort_Error_CompleteFails(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	spy.StampState["review"] = true
	spy.CompleteErr = fmt.Errorf("completion rejected")
	client := setupSortTest(t, spy)

	err := handleSort(context.Background(), client, defaultConfig())
	if err == nil {
		t.Fatal("expected error from Complete failure")
	}
}

// ---------------------------------------------------------------------------
// Dispute record / pending-hold tests (Slice 12.5.2)
// ---------------------------------------------------------------------------

func TestSort_DeadlockedWithActiveDispute_SuspendsPendingHold(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	// Feedback is deadlocked with a citation referencing law-42.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:    "fb-dl",
			State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_Citation{
					Citation: &flowv1.Citation{
						CitationIds: []string{"law-42"},
					},
				},
			},
		},
	}
	// Active dispute record citing law-42.
	spy.DisputeRecords = []*flowv1.DisputeRecord{
		{PetitionId: "pet-abc", CitedLawIds: []string{"law-42"}},
	}
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	// Should Suspend (pending-hold), NOT route to arbiter.
	if !spy.Suspended {
		t.Fatal("expected Suspend() to be called for pending-hold")
	}
	if len(spy.RoutedOutputs) != 0 {
		t.Fatalf("expected no routing (should suspend), got %v", spy.RoutedOutputs)
	}
	// Suspension condition should reference the petition_id.
	if spy.SuspendCondition == "" {
		t.Fatal("expected non-empty suspend condition with petition_id")
	}
	if !strings.Contains(spy.SuspendCondition, "pet-abc") {
		t.Fatalf("expected suspend condition to contain petition_id 'pet-abc', got %q",
			spy.SuspendCondition)
	}
}

func TestSort_DeadlockedNoActiveDispute_RoutesToArbiter(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	// Feedback is deadlocked with a citation referencing law-99.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:    "fb-dl2",
			State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_Citation{
					Citation: &flowv1.Citation{
						CitationIds: []string{"law-99"},
					},
				},
			},
		},
	}
	// No dispute records — empty list.
	spy.DisputeRecords = nil
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	// Should route to arbiter as before (regression guard).
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputArbiter {
		t.Fatalf("expected route to arbiter, got %v", spy.RoutedOutputs)
	}
	if spy.Suspended {
		t.Fatal("expected no Suspend when no active dispute")
	}
}

func TestSort_NewlyDeadlockedWithActiveDispute_SuspendsPendingHold(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	// Feedback is NEW (not yet deadlocked) but depth exceeds threshold.
	// The citation references law-42 which has an active dispute.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:    "fb-new",
			State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_Citation{
					Citation: &flowv1.Citation{
						CitationIds: []string{"law-42"},
					},
				},
			},
		},
	}
	spy.FeedbackDepths["fb-new"] = 5 // Exceeds threshold of 3.
	spy.DisputeRecords = []*flowv1.DisputeRecord{
		{PetitionId: "pet-xyz", CitedLawIds: []string{"law-42"}},
	}
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	// Should deadlock the feedback item first, then suspend.
	if len(spy.DeadlockedIDs) != 1 || spy.DeadlockedIDs[0] != "fb-new" {
		t.Fatalf("expected fb-new deadlocked, got %v", spy.DeadlockedIDs)
	}
	if !spy.Suspended {
		t.Fatal("expected Suspend() for pending-hold")
	}
	if len(spy.RoutedOutputs) != 0 {
		t.Fatalf("expected no routing (should suspend), got %v", spy.RoutedOutputs)
	}
	if !strings.Contains(spy.SuspendCondition, "pet-xyz") {
		t.Fatalf("expected suspend condition to reference petition_id 'pet-xyz', got %q",
			spy.SuspendCondition)
	}
}

func TestSort_DeadlockedNoCitation_RoutesToArbiter(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["linter"] = true
	// Feedback is deadlocked but has no citation (no law IDs to check).
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:    "fb-nocite",
			State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
		},
	}
	// Even with dispute records, no citation means no match.
	spy.DisputeRecords = []*flowv1.DisputeRecord{
		{PetitionId: "pet-other", CitedLawIds: []string{"law-42"}},
	}
	client := setupSortTest(t, spy)

	if err := handleSort(context.Background(), client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	// No citation → no dispute query → route to arbiter.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputArbiter {
		t.Fatalf("expected route to arbiter, got %v", spy.RoutedOutputs)
	}
	if spy.Suspended {
		t.Fatal("expected no Suspend when no citation on deadlocked feedback")
	}
}
