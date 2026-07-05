package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeutil"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Well-known output name for routing to the human-approval node.
const outputHumanApproval = "human-approval"

// ---------------------------------------------------------------------------
// Test helper — spins up a real ephemeral TCP server with the sortSpy
// ---------------------------------------------------------------------------

// defaultConfig returns a sortConfig matching the reference arrangement.
func defaultConfig() *sortConfig {
	return &sortConfig{
		NodeOrder:         "quench,appraisal,human-approval",
		DeadlockThreshold: 3,
	}
}

func setupSortTest(t *testing.T, spy *sortSpy) (*flow.Client, *flow.Workitem) {
	t.Helper()

	lis, err := nodeutil.NewLocalListener()
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
	// Set the sidecar address env var so findActiveDisputeForFeedback (which
	// creates a direct gRPC connection to DefaultSidecarAddress) connects to
	// the spy instead of :50051. ponytail: Remove when findActiveDisputeForFeedback
	// is replaced with a proper Workitem.GetActiveDisputes() method (Phase 10).
	t.Setenv(flow.EnvSidecarAddress, lis.Addr().String())

	client, err := flow.NewClient(
		flow.WithSidecarAddress(lis.Addr().String()),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	workitem, err := client.GetWorkitem()
	if err != nil {
		t.Fatalf("failed to get workitem: %v", err)
	}

	return client, workitem
}

// ---------------------------------------------------------------------------
// Routing tests — the core decision tree (dynamic topology)
// ---------------------------------------------------------------------------

func TestSort_RoutesToQuench_MissingLinterStamp(t *testing.T) {
	spy := newSortSpy()
	// linter stamp absent (default false) — quench is first in nodeOrder.
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "quench" {
		t.Fatalf("expected route to quench, got %v", spy.RoutedOutputs)
	}
}

func TestSort_RoutesToRefine_UnresolvedFeedbackFromProvider(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["appraisal"] = true
	// Quench stamped linter but also left unresolved feedback.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-1", Source: "quench", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
	}
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputRefine {
		t.Fatalf("expected route to refine, got %v", spy.RoutedOutputs)
	}
}

func TestSort_RoutesToAppraise_MissingApprovalStamp(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["appraisal"] = true
	// Appraisal stamp present, approval stamp absent (default false).
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputHumanApproval {
		t.Fatalf("expected route to human-approval, got %v", spy.RoutedOutputs)
	}
}

func TestSort_RoutesToHumanApproval_MissingApprovalStamp(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["appraisal"] = true
	// Approval stamp is missing — Sort should route to human-approval.
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "human-approval" {
		t.Fatalf("expected route to human-approval, got %v", spy.RoutedOutputs)
	}
	if len(spy.StampedNames) != 0 {
		t.Fatalf("expected no stamping, got %v", spy.StampedNames)
	}
	if spy.Completed {
		t.Fatal("expected no Complete() call — approval still missing")
	}
}

// ---------------------------------------------------------------------------
// Deadlock detection tests
// ---------------------------------------------------------------------------

func TestSort_RoutesToArbiter_DepthExceedsThreshold_New(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["appraisal"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-1", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
	}
	spy.FeedbackDepths["fb-1"] = 4 // default threshold is 3
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.DeadlockedIDs) != 1 || spy.DeadlockedIDs[0] != "fb-1" {
		t.Fatalf("expected fb-1 deadlocked, got %v", spy.DeadlockedIDs)
	}
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputArbiter {
		t.Fatalf("expected route to arbiter, got %v", spy.RoutedOutputs)
	}
}

func TestSort_RoutesToArbiter_DepthExceedsThreshold_Actioned(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["appraisal"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-4", State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED},
	}
	spy.FeedbackDepths["fb-4"] = 10
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
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
	spy.StampState["appraisal"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-5", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
	}
	// No depth needed — already deadlocked.
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
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
	spy.StampState["appraisal"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-ok", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
		{Id: "fb-hot", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
	}
	spy.FeedbackDepths["fb-ok"] = 1
	spy.FeedbackDepths["fb-hot"] = 5
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
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

func TestSort_DoesNotRedeadlockWontFixFeedback(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["appraisal"] = true
	// WONT_FIX feedback from arbitration should NOT be re-deadlocked
	// even when depth exceeds threshold.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-arbitrated", State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX},
	}
	spy.FeedbackDepths["fb-arbitrated"] = 4
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.DeadlockedIDs) != 0 {
		t.Fatalf("expected no deadlocking for arbitrated WONT_FIX, got %v", spy.DeadlockedIDs)
	}
	// Should route to human-approval (missing approval stamp) instead of re-deadlocking.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputHumanApproval {
		t.Fatalf("expected route to human-approval, got %v", spy.RoutedOutputs)
	}
}

func TestSort_DoesNotRedeadlockRejectedFeedback(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["appraisal"] = true
	// REJECTED feedback from arbitration should NOT be re-deadlocked.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-rejected", State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED},
	}
	spy.FeedbackDepths["fb-rejected"] = 3
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.DeadlockedIDs) != 0 {
		t.Fatalf("expected no deadlocking for arbitrated REJECTED, got %v", spy.DeadlockedIDs)
	}
	// Should route to human-approval (missing approval stamp) instead of re-deadlocking.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputHumanApproval {
		t.Fatalf("expected route to human-approval, got %v", spy.RoutedOutputs)
	}
}

func TestSort_BelowThreshold_RoutesToRefine(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["appraisal"] = true
	// Quench left addressed (WONT_FIX) feedback below deadlock threshold.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-6", Source: "quench", State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX},
	}
	spy.FeedbackDepths["fb-6"] = 2 // below default threshold of 3
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.DeadlockedIDs) != 0 {
		t.Fatalf("expected no deadlocking, got %v", spy.DeadlockedIDs)
	}
	// Appraisal stamp present + WONT_FIX from quench → human-approval (missing approval).
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputHumanApproval {
		t.Fatalf("expected route to human-approval, got %v", spy.RoutedOutputs)
	}
}

func TestSort_ResolvedItemsSkippedInDeadlockScan(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["appraisal"] = true
	spy.StampState["approval"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-done", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
	}
	spy.FeedbackDepths["fb-done"] = 99 // would deadlock if not skipped
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
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
	spy.StampState["appraisal"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-a", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
		{Id: "fb-b", State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED},
	}
	spy.FeedbackDepths["fb-a"] = 5
	spy.FeedbackDepths["fb-b"] = 10
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
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
	spy.StampState["appraisal"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-7", Source: "quench", State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX},
	}
	spy.FeedbackDepths["fb-7"] = 4

	// Threshold=5: depth 4 is below → should NOT deadlock.
	cfg := &sortConfig{
		NodeOrder:         "quench,appraisal",
		DeadlockThreshold: 5,
	}
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, cfg); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.DeadlockedIDs) != 0 {
		t.Fatalf("expected no deadlocking with threshold=5, got %v",
			spy.DeadlockedIDs)
	}
	// Linter stamp present + WONT_FIX from quench → appraisal (adjudication).
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputAppraisal {
		t.Fatalf("expected route to appraisal, got %v", spy.RoutedOutputs)
	}
}

func TestSort_ZeroThresholdDefaultsTo3(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["appraisal"] = true
	// Quench left unresolved feedback below threshold.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-9", Source: "quench", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
	}
	spy.FeedbackDepths["fb-9"] = 2

	// Zero threshold → default 3 used → depth 2 below threshold.
	cfg := &sortConfig{
		NodeOrder:         "quench,appraisal",
		DeadlockThreshold: 0,
	}
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, cfg); err != nil {
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
		{"two nodes", "quench,appraisal", []string{"quench", "appraisal"}},
		{"with spaces", " quench , appraisal ", []string{"quench", "appraisal"}},
		{"trailing comma", "quench,appraisal,", []string{"quench", "appraisal"}},
		{"empty entries", "quench,,appraisal", []string{"quench", "appraisal"}},
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
		{"ATTEST:artefact/haiku/linter", "", "", false},
		{"ATTEST:artefact/doc/security-review", "", "", false},
		{"ATTEST:artefact/", "", "", false},
		{"ATTEST:artefact/haiku/", "", "", false},
		{"ATTEST:artefact//linter", "", "", false},
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
	spy.StampState["appraisal"] = true
	// Quench left unresolved feedback.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-quench", Source: "quench", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
	}
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	// Review stamp present + unresolved feedback from appraisal → refine.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputRefine {
		t.Fatalf("expected route to refine, got %v", spy.RoutedOutputs)
	}
}

func TestSort_ResolvedFeedbackIgnoredInSourceCheck(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["appraisal"] = true
	spy.StampState["approval"] = true
	// Quench left feedback but it's already resolved — should not block.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-resolved", Source: "quench", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
	}
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	// All stamps present (linter, review, approval), resolved feedback → complete.
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
	spy.StampState["appraisal"] = true
	// Only resolved + deadlocked feedback from quench — neither should block source check.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-dl", Source: "quench", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED},
	}
	client, workitem := setupSortTest(t, spy)

	// This test will hit the deadlock check first and route to arbiter.
	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputArbiter {
		t.Fatalf("expected route to arbiter, got %v", spy.RoutedOutputs)
	}
}

func TestSort_Error_GetFlowTopologyFails(t *testing.T) {
	spy := newSortSpy()
	spy.GetFlowTopologyErr = fmt.Errorf("topology unavailable")
	client, workitem := setupSortTest(t, spy)

	err := handleSort(context.Background(), workitem, client, defaultConfig())
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
	client, workitem := setupSortTest(t, spy)

	err := handleSort(context.Background(), workitem, client, defaultConfig())
	if err == nil {
		t.Fatal("expected error from HasStamp failure")
	}
}

func TestSort_Error_GetFeedbackFails(t *testing.T) {
	spy := newSortSpy()
	spy.GetFeedbackErr = fmt.Errorf("feedback list failed")
	client, workitem := setupSortTest(t, spy)

	err := handleSort(context.Background(), workitem, client, defaultConfig())
	if err == nil {
		t.Fatal("expected error from GetFeedback failure")
	}
}

func TestSort_Error_GetFeedbackDepthFails(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["appraisal"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-x", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
	}
	spy.GetFeedbackDepthErr = fmt.Errorf("depth lookup failed")
	client, workitem := setupSortTest(t, spy)

	err := handleSort(context.Background(), workitem, client, defaultConfig())
	if err == nil {
		t.Fatal("expected error from GetFeedbackDepth failure")
	}
}

func TestSort_Error_DeadlockFeedbackFails(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["appraisal"] = true
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-y", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW},
	}
	spy.FeedbackDepths["fb-y"] = 10
	spy.DeadlockFeedbackErr = fmt.Errorf("deadlock transition failed")
	client, workitem := setupSortTest(t, spy)

	err := handleSort(context.Background(), workitem, client, defaultConfig())
	if err == nil {
		t.Fatal("expected error from DeadlockFeedback failure")
	}
}

func TestSort_Error_RouteToOutputFails(t *testing.T) {
	spy := newSortSpy()
	// Missing linter stamp → routes to quench → error.
	spy.RouteToOutputErr = fmt.Errorf("routing failed")
	client, workitem := setupSortTest(t, spy)

	err := handleSort(context.Background(), workitem, client, defaultConfig())
	if err == nil {
		t.Fatal("expected error from RouteToOutput failure")
	}
}

func TestSort_Error_CompleteFails(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["appraisal"] = true
	spy.StampState["approval"] = true
	spy.CompleteErr = fmt.Errorf("completion rejected")
	client, workitem := setupSortTest(t, spy)

	err := handleSort(context.Background(), workitem, client, defaultConfig())
	if err == nil {
		t.Fatal("expected error from Complete failure")
	}
}

// ---------------------------------------------------------------------------
// Dispute record / pending-hold tests (Slice 12.5.2)
// ---------------------------------------------------------------------------

func TestSort_DeadlockedWithActiveDispute_SuspendsPendingHold(t *testing.T) {
	spy := newSortSpy()
	spy.StampState["appraisal"] = true
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
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
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
	spy.StampState["appraisal"] = true
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
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
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
	spy.StampState["appraisal"] = true
	// Feedback is NEW (not yet deadlocked) but depth exceeds threshold.
	// The citation references law-42 which has an active dispute.
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:    "fb-new",
			State: flowv1.FeedbackState_FEEDBACK_STATE_NEW,
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
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
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
	spy.StampState["appraisal"] = true
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
	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
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

// ---------------------------------------------------------------------------
// Attestation capability / routing helper unit tests
// ---------------------------------------------------------------------------

func TestNeededAttestCapability(t *testing.T) {
	tests := []struct {
		name  string
		stamp string
		want  string
	}{
		{"lawgrp-content", "lawgrp-content", "law-group"},
		{"law-no-weather-text-markdown", "law-no-weather-text-markdown", "law-id"},
		{"appraisal", "appraisal", ""},
		{"approval", "approval", ""},
		{"law- minimal", "law-", "law-id"},
		{"lawgrp- minimal", "lawgrp-", "law-group"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := neededAttestCapability(tt.stamp)
			if got != tt.want {
				t.Fatalf("neededAttestCapability(%q) = %q, want %q", tt.stamp, got, tt.want)
			}
		})
	}
}

func TestHasAttestCapability(t *testing.T) {
	tests := []struct {
		name         string
		capabilities []string
		kind         string
		sub          string
		want         bool
	}{
		{
			name:         "exact match law-group",
			capabilities: []string{"ATTEST:artefact/haiku/law-group"},
			kind:         "haiku",
			sub:          "law-group",
			want:         true,
		},
		{
			name:         "exact match law-id",
			capabilities: []string{"ATTEST:artefact/haiku/law-id"},
			kind:         "haiku",
			sub:          "law-id",
			want:         true,
		},
		{
			name:         "wildcard law-* matches law-group",
			capabilities: []string{"ATTEST:artefact/haiku/law-*"},
			kind:         "haiku",
			sub:          "law-group",
			want:         true,
		},
		{
			name:         "wildcard law-* matches law-id",
			capabilities: []string{"ATTEST:artefact/haiku/law-*"},
			kind:         "haiku",
			sub:          "law-id",
			want:         true,
		},
		{
			name:         "law-id does not match law-group",
			capabilities: []string{"ATTEST:artefact/haiku/law-id"},
			kind:         "haiku",
			sub:          "law-group",
			want:         false,
		},
		{
			name:         "STAMP cap does not match ATTEST",
			capabilities: []string{"STAMP:artefact/haiku/appraisal"},
			kind:         "haiku",
			sub:          "law-group",
			want:         false,
		},
		{
			name:         "different kind not matched",
			capabilities: []string{"ATTEST:artefact/doc/law-group"},
			kind:         "haiku",
			sub:          "law-group",
			want:         false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasAttestCapability(tt.capabilities, tt.kind, tt.sub)
			if got != tt.want {
				t.Fatalf("hasAttestCapability(%v, %q, %q) = %v, want %v",
					tt.capabilities, tt.kind, tt.sub, got, tt.want)
			}
		})
	}
}

func TestFindAttestationProvider(t *testing.T) {
	nodes := map[string]*flowv1.FlowNode{
		"quench": {
			Name: "quench",
			Capabilities: []string{
				"STAMP:artefact/haiku/linter",
			},
		},
		"appraisal": {
			Name: "appraisal",
			Capabilities: []string{
				"STAMP:artefact/haiku/review",
				"ATTEST:artefact/haiku/law-group",
			},
		},
		"attestor": {
			Name: "attestor",
			Capabilities: []string{
				"ATTEST:artefact/haiku/law-*",
			},
		},
		"exact-attestor": {
			Name: "exact-attestor",
			Capabilities: []string{
				"ATTEST:artefact/haiku/law-id",
			},
		},
	}

	tests := []struct {
		name      string
		stampName string
		kind      string
		nodeOrder []string
		nodes     map[string]*flowv1.FlowNode
		want      string
	}{
		{
			name:      "nodeOrder first match wins (appraisal has law-group)",
			stampName: "lawgrp-content",
			kind:      "haiku",
			nodeOrder: []string{"quench", "appraisal"},
			nodes:     nodes,
			want:      "appraisal",
		},
		{
			name:      "wildcard match through nodeOrder",
			stampName: "law-no-weather-text-markdown",
			kind:      "haiku",
			nodeOrder: []string{"quench", "attestor"},
			nodes:     nodes,
			want:      "attestor",
		},
		{
			name:      "no match in nodeOrder, fallback to exact match",
			stampName: "law-no-weather-text-markdown",
			kind:      "haiku",
			nodeOrder: []string{"quench", "appraisal"},
			nodes:     nodes,
			want:      "exact-attestor", // exact match (spec=2) beats wildcard (spec=1)
		},
		{
			name:      "no match at all returns empty",
			stampName: "law-unknown",
			kind:      "haiku",
			nodeOrder: []string{"quench"},
			// Only quench (no ATTEST caps) — no fallback match either.
			nodes: map[string]*flowv1.FlowNode{
				"quench": {
					Name:         "quench",
					Capabilities: []string{"STAMP:artefact/haiku/linter"},
				},
			},
			want: "",
		},
		{
			name:      "non-law stamp returns empty",
			stampName: "appraisal",
			kind:      "haiku",
			nodeOrder: []string{"quench", "appraisal"},
			nodes:     nodes,
			want:      "",
		},
		{
			name:      "nodeOrder priority over specificity",
			stampName: "law-no-weather-text-markdown",
			kind:      "haiku",
			nodeOrder: []string{"quench", "attestor", "exact-attestor"},
			nodes:     nodes,
			want:      "attestor", // first in nodeOrder wins
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findAttestationProvider(tt.stampName, tt.kind, tt.nodeOrder, tt.nodes)
			if got != tt.want {
				t.Fatalf("findAttestationProvider(%q, %q, %v, nodes) = %q, want %q",
					tt.stampName, tt.kind, tt.nodeOrder, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Attestation routing integration tests
// ---------------------------------------------------------------------------

// attestTopology returns a topology where attestation capabilities are on a
// node NOT in nodeOrder, so the static stamp loop does not pick them up.
// The attestation provider is only discovered via the fallback scan in
// findAttestationProvider.
func attestTopology() *flowv1.GetFlowTopologyResponse {
	return &flowv1.GetFlowTopologyResponse{
		Self: &flowv1.FlowNode{
			Name: "sort",
			Capabilities: []string{
				"READ:flow",
				"READ:artefact",
				"READ:feedback",
				"WRITE:feedback/deadlocked",
			},
			Outputs: []*flowv1.FlowOutput{
				{Name: "quench", Target: "quench"},
				{Name: "appraisal", Target: "appraisal"},
				{Name: "refine", Target: "refine"},
				{Name: "human-arbiter", Target: "human-arbiter"},
				{Name: "human-approval", Target: "human-approval"},
				{Name: "attest-law", Target: "attest-law"},
			},
		},
		Nodes: map[string]*flowv1.FlowNode{
			"sort": {
				Name: "sort",
				Capabilities: []string{
					"READ:flow",
				},
			},
			"quench": {
				Name:         "quench",
				Capabilities: []string{"STAMP:artefact/haiku/appraisal"},
			},
			"appraisal": {
				Name: "appraisal",
			},
			"refine": {
				Name: "refine",
			},
			"human-arbiter": {
				Name: "human-arbiter",
			},
			"human-approval": {
				Name:         "human-approval",
				Capabilities: []string{"STAMP:artefact/haiku/approval"},
			},
			// Attestation provider — NOT in nodeOrder, only reached via fallback.
			"attest-law": {
				Name: "attest-law",
				Capabilities: []string{
					"ATTEST:artefact/haiku/law-*",
				},
			},
		},
		ExitContract: map[string]*flowv1.StampRequirements{
			"haiku": {Stamps: []string{"appraisal", "approval"}},
		},
	}
}

func TestSort_RoutesToAttestProvider_MissingLawStamp(t *testing.T) {
	spy := newSortSpy()
	spy.TopologyResponse = attestTopology()
	// All static stamps present so the nodeOrder loop passes through.
	spy.StampState["appraisal"] = true
	spy.StampState["approval"] = true
	// Return a law that produces a missing attestation stamp "law-no-weather-text-markdown".
	spy.QueryLawsLaws = []*flowv1.Law{
		{
			Id:   "no-weather",
			Goal: "Ensure weather is mentioned",
			Representations: []*flowv1.Representation{
				{Type: "text/markdown", Content: "Must mention weather"},
			},
		},
	}
	// No existing stamps on the artefact — the law stamp is missing.
	spy.GetStampsStamps = nil

	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	// Should route to appraisal (the attestation provider for law-*).
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "attest-law" {
		t.Fatalf("expected route to attest-law, got %v", spy.RoutedOutputs)
	}
	if spy.Completed {
		t.Fatal("expected no Complete() call — attestation should return early after routing")
	}
}

func TestSort_NoAttestationProvider_ReturnsGuardError(t *testing.T) {
	spy := newSortSpy()
	// Use default topology which has NO ATTEST capabilities.
	spy.StampState["appraisal"] = true
	spy.StampState["approval"] = true
	// Return a law that produces a missing attestation stamp.
	spy.QueryLawsLaws = []*flowv1.Law{
		{
			Id:   "no-weather",
			Goal: "Ensure weather is mentioned",
			Representations: []*flowv1.Representation{
				{Type: "text/markdown", Content: "Must mention weather"},
			},
		},
	}
	spy.GetStampsStamps = nil

	client, workitem := setupSortTest(t, spy)

	err := handleSort(context.Background(), workitem, client, defaultConfig())
	if err == nil {
		t.Fatal("expected GuardError, got nil")
	}
	var guardErr *GuardError
	if !errors.As(err, &guardErr) {
		t.Fatalf("expected *GuardError, got %T: %v", err, err)
	}
	if guardErr.Code != "NO_ATTESTATION_PROVIDER" {
		t.Fatalf("expected Code NO_ATTESTATION_PROVIDER, got %q", guardErr.Code)
	}
	if guardErr.Stamp == "" {
		t.Fatal("expected non-empty Stamp in GuardError")
	}
}

func TestSort_AttestationRouting_SkipsNonLawStamps(t *testing.T) {
	spy := newSortSpy()
	// Use default topology with NO ATTEST capabilities.
	spy.StampState["appraisal"] = true
	spy.StampState["approval"] = true
	// Return a law with group that produces stamp "lawgrp-content".
	// Sends back a law in the default group → bundle mode → lawgrp-default.
	// No ATTEST capabilities in default topology, so guard error expected.
	spy.QueryLawsLaws = []*flowv1.Law{
		{
			Id:   "some-law",
			Goal: "Some requirement",
			Representations: []*flowv1.Representation{
				{Type: "text/plain", Content: "requirement"},
			},
		},
	}
	spy.GetStampsStamps = nil

	client, workitem := setupSortTest(t, spy)

	err := handleSort(context.Background(), workitem, client, defaultConfig())
	if err == nil {
		t.Fatal("expected GuardError, got nil")
	}
	var guardErr *GuardError
	if !errors.As(err, &guardErr) {
		t.Fatalf("expected *GuardError, got %T: %v", err, err)
	}
	if guardErr.Code != "NO_ATTESTATION_PROVIDER" {
		t.Fatalf("expected Code NO_ATTESTATION_PROVIDER, got %q", guardErr.Code)
	}
	// The stamp should be "lawgrp-default" since the law has no group
	// and defaults to bundle mode.
	expectedStamp := "lawgrp-default"
	if guardErr.Stamp != expectedStamp {
		t.Fatalf("expected Stamp %q, got %q", expectedStamp, guardErr.Stamp)
	}
}

func TestSort_AttestAllPresent_ContinuesToComplete(t *testing.T) {
	spy := newSortSpy()
	spy.TopologyResponse = attestTopology()
	spy.StampState["appraisal"] = true
	spy.StampState["approval"] = true
	// Return a law whose stamp IS already present on the artefact.
	// Law has no group → default group → bundle mode (per built-in defaults).
	spy.QueryLawsLaws = []*flowv1.Law{
		{
			Id:   "weather",
			Goal: "Weather check",
			Representations: []*flowv1.Representation{
				{Type: "text/markdown", Content: "Must mention weather"},
			},
		},
	}
	// The expected stamp "lawgrp-default" (bundle-mode group stamp) is present.
	spy.GetStampsStamps = []*flowv1.Stamp{
		{Name: "lawgrp-default"},
	}

	client, workitem := setupSortTest(t, spy)

	if err := handleSort(context.Background(), workitem, client, defaultConfig()); err != nil {
		t.Fatalf("handleSort() error: %v", err)
	}

	// All attestations present → should proceed to completion.
	if !spy.Completed {
		t.Fatal("expected workitem to be completed")
	}
}
