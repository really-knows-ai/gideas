package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Test constants for law IDs used across multiple tests.
const (
	testLawID1 = "law-1"
	testLawID2 = "law-2"
	testLawID3 = "law-3"
)

// ---------------------------------------------------------------------------
// Tests — ttlConfig
// ---------------------------------------------------------------------------

func TestTTLConfig_ScanInterval_Default(t *testing.T) {
	cfg := &ttlConfig{}
	if got := cfg.scanInterval(); got != defaultScanPeriod {
		t.Fatalf("expected default %v, got %v", defaultScanPeriod, got)
	}
}

func TestTTLConfig_ScanInterval_Configured(t *testing.T) {
	cfg := &ttlConfig{ScanPeriod: "10m"}
	if got := cfg.scanInterval(); got != 10*time.Minute {
		t.Fatalf("expected 10m, got %v", got)
	}
}

func TestTTLConfig_ScanInterval_Invalid(t *testing.T) {
	cfg := &ttlConfig{ScanPeriod: "not-a-duration"}
	if got := cfg.scanInterval(); got != defaultScanPeriod {
		t.Fatalf("expected default %v for invalid duration, got %v", defaultScanPeriod, got)
	}
}

func TestTTLConfig_TierTTL(t *testing.T) {
	cfg := &ttlConfig{
		Tier1: "168h",
		Tier2: "720h",
		Tier3: "invalid",
		// Tier4, Tier5 empty — omitted
	}
	m := cfg.tierTTL()

	if m[flowv1.LawTier_LAW_TIER_FINDING] != 168*time.Hour {
		t.Errorf("tier1: expected 168h, got %v", m[flowv1.LawTier_LAW_TIER_FINDING])
	}
	if m[flowv1.LawTier_LAW_TIER_RULING] != 720*time.Hour {
		t.Errorf("tier2: expected 720h, got %v", m[flowv1.LawTier_LAW_TIER_RULING])
	}
	if _, ok := m[flowv1.LawTier_LAW_TIER_LOCAL_STATUTE]; ok {
		t.Error("tier3: expected omission for invalid duration")
	}
	if _, ok := m[flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION]; ok {
		t.Error("tier4: expected omission for empty duration")
	}
	if _, ok := m[flowv1.LawTier_LAW_TIER_FEDERAL_ACCORD]; ok {
		t.Error("tier5: expected omission for empty duration")
	}
}

func TestTTLConfig_TierTTL_Empty(t *testing.T) {
	cfg := &ttlConfig{}
	m := cfg.tierTTL()
	if len(m) != 0 {
		t.Fatalf("expected empty map, got %d entries", len(m))
	}
}

// ---------------------------------------------------------------------------
// Tests — isExpired
// ---------------------------------------------------------------------------

func TestIsExpired_Expired(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	law := &flowv1.Law{
		Id:        testLawID1,
		Tier:      flowv1.LawTier_LAW_TIER_FINDING,
		UpdatedAt: timestamppb.New(now.Add(-200 * time.Hour)), // 200h old
	}
	tierTTLs := map[flowv1.LawTier]time.Duration{
		flowv1.LawTier_LAW_TIER_FINDING: 168 * time.Hour, // 7 days
	}

	if !isExpired(law, tierTTLs, now) {
		t.Fatal("expected law to be expired")
	}
}

func TestIsExpired_NotExpired(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	law := &flowv1.Law{
		Id:        testLawID1,
		Tier:      flowv1.LawTier_LAW_TIER_FINDING,
		UpdatedAt: timestamppb.New(now.Add(-100 * time.Hour)), // 100h old
	}
	tierTTLs := map[flowv1.LawTier]time.Duration{
		flowv1.LawTier_LAW_TIER_FINDING: 168 * time.Hour, // 7 days
	}

	if isExpired(law, tierTTLs, now) {
		t.Fatal("expected law to NOT be expired")
	}
}

func TestIsExpired_NoTierConfig(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	law := &flowv1.Law{
		Id:        testLawID1,
		Tier:      flowv1.LawTier_LAW_TIER_RULING,
		UpdatedAt: timestamppb.New(now.Add(-9999 * time.Hour)),
	}
	// Only tier1 configured, law is tier2.
	tierTTLs := map[flowv1.LawTier]time.Duration{
		flowv1.LawTier_LAW_TIER_FINDING: 168 * time.Hour,
	}

	if isExpired(law, tierTTLs, now) {
		t.Fatal("expected law to NOT be expired when tier has no config")
	}
}

func TestIsExpired_UnspecifiedTier(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	law := &flowv1.Law{
		Id:        testLawID1,
		Tier:      flowv1.LawTier_LAW_TIER_UNSPECIFIED,
		UpdatedAt: timestamppb.New(now.Add(-9999 * time.Hour)),
	}
	tierTTLs := map[flowv1.LawTier]time.Duration{
		flowv1.LawTier_LAW_TIER_FINDING: 168 * time.Hour,
	}

	if isExpired(law, tierTTLs, now) {
		t.Fatal("expected UNSPECIFIED tier to never expire")
	}
}

func TestIsExpired_FallbackToCreatedAt(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	law := &flowv1.Law{
		Id:        testLawID1,
		Tier:      flowv1.LawTier_LAW_TIER_FINDING,
		CreatedAt: timestamppb.New(now.Add(-200 * time.Hour)), // no updated_at
	}
	tierTTLs := map[flowv1.LawTier]time.Duration{
		flowv1.LawTier_LAW_TIER_FINDING: 168 * time.Hour,
	}

	if !isExpired(law, tierTTLs, now) {
		t.Fatal("expected law to be expired using created_at fallback")
	}
}

func TestIsExpired_NoTimestamp(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	law := &flowv1.Law{
		Id:   testLawID1,
		Tier: flowv1.LawTier_LAW_TIER_FINDING,
		// No timestamps at all.
	}
	tierTTLs := map[flowv1.LawTier]time.Duration{
		flowv1.LawTier_LAW_TIER_FINDING: 168 * time.Hour,
	}

	if isExpired(law, tierTTLs, now) {
		t.Fatal("expected law with no timestamp to NOT be expired")
	}
}

// ---------------------------------------------------------------------------
// Tests — lawTimestamp
// ---------------------------------------------------------------------------

func TestLawTimestamp_PreferUpdatedAt(t *testing.T) {
	updated := timestamppb.New(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	created := timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	law := &flowv1.Law{UpdatedAt: updated, CreatedAt: created}

	got := lawTimestamp(law)
	if got != updated {
		t.Fatalf("expected updated_at, got %v", got)
	}
}

func TestLawTimestamp_FallbackCreatedAt(t *testing.T) {
	created := timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	law := &flowv1.Law{CreatedAt: created}

	got := lawTimestamp(law)
	if got != created {
		t.Fatalf("expected created_at fallback, got %v", got)
	}
}

func TestLawTimestamp_NilBoth(t *testing.T) {
	law := &flowv1.Law{}
	if got := lawTimestamp(law); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Tests — pendingTracker
// ---------------------------------------------------------------------------

func TestPendingTracker_MarkAndClear(t *testing.T) {
	tracker := newPendingTracker()

	if !tracker.markPending(testLawID1) {
		t.Fatal("expected first markPending to return true")
	}
	if tracker.markPending(testLawID1) {
		t.Fatal("expected second markPending for same ID to return false")
	}
	if !tracker.markPending(testLawID2) {
		t.Fatal("expected markPending for different ID to return true")
	}

	tracker.clearPending(testLawID1)
	if !tracker.markPending(testLawID1) {
		t.Fatal("expected markPending after clearPending to return true")
	}
}

// ---------------------------------------------------------------------------
// Tests — scanAndCreate (integration with spy servers)
// ---------------------------------------------------------------------------

func TestScanAndCreate_CreatesWorkitems(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	tierTTLs := map[flowv1.LawTier]time.Duration{
		flowv1.LawTier_LAW_TIER_FINDING: 168 * time.Hour,
	}

	opSpy := &spyOperator{returnID: "wi-ttl-001"}
	libSpy := &spyLibrarian{returnLaws: []*flowv1.Law{
		{Id: testLawID1, Tier: flowv1.LawTier_LAW_TIER_FINDING,
			UpdatedAt: timestamppb.New(now.Add(-200 * time.Hour))}, // expired
		{Id: testLawID2, Tier: flowv1.LawTier_LAW_TIER_FINDING,
			UpdatedAt: timestamppb.New(now.Add(-100 * time.Hour))}, // not expired
		{Id: testLawID3, Tier: flowv1.LawTier_LAW_TIER_FINDING,
			UpdatedAt: timestamppb.New(now.Add(-300 * time.Hour))}, // expired
	}}

	ec := setupEntryTestClient(t, opSpy, libSpy)
	tracker := newPendingTracker()

	err := scanAndCreate(context.Background(), ec, tierTTLs, tracker, func() time.Time { return now })
	if err != nil {
		t.Fatalf("scanAndCreate() returned error: %v", err)
	}

	calls := opSpy.getCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 CreateWorkitem calls, got %d", len(calls))
	}

	if calls[0].GetMetadata()["law_id"] != testLawID1 {
		t.Errorf("first call law_id: expected %s, got %q", testLawID1, calls[0].GetMetadata()["law_id"])
	}
	if calls[1].GetMetadata()["law_id"] != testLawID3 {
		t.Errorf("second call law_id: expected %s, got %q", testLawID3, calls[1].GetMetadata()["law_id"])
	}
}

func TestScanAndCreate_DeduplicatesPending(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	tierTTLs := map[flowv1.LawTier]time.Duration{
		flowv1.LawTier_LAW_TIER_FINDING: 168 * time.Hour,
	}

	opSpy := &spyOperator{returnID: "wi-ttl-001"}
	libSpy := &spyLibrarian{returnLaws: []*flowv1.Law{
		{Id: testLawID1, Tier: flowv1.LawTier_LAW_TIER_FINDING,
			UpdatedAt: timestamppb.New(now.Add(-200 * time.Hour))},
	}}

	ec := setupEntryTestClient(t, opSpy, libSpy)
	tracker := newPendingTracker()

	// First scan — should create workitem.
	err := scanAndCreate(context.Background(), ec, tierTTLs, tracker, func() time.Time { return now })
	if err != nil {
		t.Fatalf("first scan error: %v", err)
	}

	// Second scan — same law still expired, but should be deduped.
	err = scanAndCreate(context.Background(), ec, tierTTLs, tracker, func() time.Time { return now })
	if err != nil {
		t.Fatalf("second scan error: %v", err)
	}

	calls := opSpy.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 CreateWorkitem call (dedup), got %d", len(calls))
	}
}

func TestScanAndCreate_CreateWorkitemError_ClearsPending(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	tierTTLs := map[flowv1.LawTier]time.Duration{
		flowv1.LawTier_LAW_TIER_FINDING: 168 * time.Hour,
	}

	opSpy := &spyOperator{returnErr: fmt.Errorf("permission denied")}
	libSpy := &spyLibrarian{returnLaws: []*flowv1.Law{
		{Id: testLawID1, Tier: flowv1.LawTier_LAW_TIER_FINDING,
			UpdatedAt: timestamppb.New(now.Add(-200 * time.Hour))},
	}}

	ec := setupEntryTestClient(t, opSpy, libSpy)
	tracker := newPendingTracker()

	err := scanAndCreate(context.Background(), ec, tierTTLs, tracker, func() time.Time { return now })
	if err != nil {
		t.Fatalf("scanAndCreate() returned error: %v", err)
	}

	// After error, pending should be cleared — re-mark should succeed.
	if !tracker.markPending(testLawID1) {
		t.Fatal("expected law to be cleared from pending after CreateWorkitem error")
	}
}

func TestScanAndCreate_QueryLawsError(t *testing.T) {
	tierTTLs := map[flowv1.LawTier]time.Duration{
		flowv1.LawTier_LAW_TIER_FINDING: 168 * time.Hour,
	}

	opSpy := &spyOperator{returnID: "unused"}
	libSpy := &spyLibrarian{returnErr: fmt.Errorf("librarian unavailable")}

	ec := setupEntryTestClient(t, opSpy, libSpy)
	tracker := newPendingTracker()

	err := scanAndCreate(context.Background(), ec, tierTTLs, tracker, time.Now)
	if err == nil {
		t.Fatal("expected error from scanAndCreate when QueryLaws fails")
	}
	if !strings.Contains(err.Error(), "query laws") {
		t.Fatalf("expected 'query laws' in error, got: %v", err)
	}
}

func TestScanAndCreate_NoExpiredLaws(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	tierTTLs := map[flowv1.LawTier]time.Duration{
		flowv1.LawTier_LAW_TIER_FINDING: 168 * time.Hour,
	}

	opSpy := &spyOperator{returnID: "wi-ttl-001"}
	libSpy := &spyLibrarian{returnLaws: []*flowv1.Law{
		{Id: testLawID1, Tier: flowv1.LawTier_LAW_TIER_FINDING,
			UpdatedAt: timestamppb.New(now.Add(-1 * time.Hour))}, // fresh
	}}

	ec := setupEntryTestClient(t, opSpy, libSpy)
	tracker := newPendingTracker()

	err := scanAndCreate(context.Background(), ec, tierTTLs, tracker, func() time.Time { return now })
	if err != nil {
		t.Fatalf("scanAndCreate() returned error: %v", err)
	}

	calls := opSpy.getCalls()
	if len(calls) != 0 {
		t.Fatalf("expected 0 CreateWorkitem calls, got %d", len(calls))
	}
}

func TestScanAndCreate_MultipleTiers(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	tierTTLs := map[flowv1.LawTier]time.Duration{
		flowv1.LawTier_LAW_TIER_FINDING: 168 * time.Hour, // 7d
		flowv1.LawTier_LAW_TIER_RULING:  720 * time.Hour, // 30d
	}

	opSpy := &spyOperator{returnID: "wi-ttl-001"}
	libSpy := &spyLibrarian{returnLaws: []*flowv1.Law{
		{Id: testLawID1, Tier: flowv1.LawTier_LAW_TIER_FINDING,
			UpdatedAt: timestamppb.New(now.Add(-200 * time.Hour))}, // expired (>168h)
		{Id: testLawID2, Tier: flowv1.LawTier_LAW_TIER_RULING,
			UpdatedAt: timestamppb.New(now.Add(-500 * time.Hour))}, // not expired (<720h)
		{Id: testLawID3, Tier: flowv1.LawTier_LAW_TIER_RULING,
			UpdatedAt: timestamppb.New(now.Add(-800 * time.Hour))}, // expired (>720h)
	}}

	ec := setupEntryTestClient(t, opSpy, libSpy)
	tracker := newPendingTracker()

	err := scanAndCreate(context.Background(), ec, tierTTLs, tracker, func() time.Time { return now })
	if err != nil {
		t.Fatalf("scanAndCreate() returned error: %v", err)
	}

	calls := opSpy.getCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 CreateWorkitem calls, got %d", len(calls))
	}
	if calls[0].GetMetadata()["law_id"] != testLawID1 {
		t.Errorf("first call: expected %s, got %q", testLawID1, calls[0].GetMetadata()["law_id"])
	}
	if calls[1].GetMetadata()["law_id"] != testLawID3 {
		t.Errorf("second call: expected %s, got %q", testLawID3, calls[1].GetMetadata()["law_id"])
	}
}

// ---------------------------------------------------------------------------
// Tests — config loading
// ---------------------------------------------------------------------------

func TestWatchTTL_LoadsConfig(t *testing.T) {
	// Write a temp config file and verify config parsing.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "node-config.yaml")
	cfgContent := "scanPeriod: \"1s\"\ntier1: \"168h\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Use nodeconfig.Load (the same function the node uses).
	t.Setenv("NODE_CONFIG_PATH", cfgPath)
	cfg, err := nodeconfig.Load[ttlConfig](cfgPath)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.scanInterval() != 1*time.Second {
		t.Errorf("expected scanInterval=1s, got %v", cfg.scanInterval())
	}
	ttls := cfg.tierTTL()
	if ttls[flowv1.LawTier_LAW_TIER_FINDING] != 168*time.Hour {
		t.Errorf("expected tier1 TTL=168h, got %v", ttls[flowv1.LawTier_LAW_TIER_FINDING])
	}
}

// ---------------------------------------------------------------------------
// Tests — processHearing (handler logic via spy client)
// ---------------------------------------------------------------------------

func TestProcessHearing_Success(t *testing.T) {
	spy := &handlerSpy{}
	client := newHandlerTestClient(t, spy)

	wctx := &flowv1.WorkitemContext{
		FlowNamespace: "test-ns",
		WorkitemId:    "wi-hearing-001",
		NodeId:        "ttl-watcher",
		Metadata:      map[string]string{"law_id": "law-42"},
	}

	err := processHearing(context.Background(), client, wctx)
	if err != nil {
		t.Fatalf("processHearing() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if spy.heartbeatCount != 1 {
		t.Errorf("expected 1 heartbeat, got %d", spy.heartbeatCount)
	}

	if len(spy.storedArtefacts) != 1 {
		t.Fatalf("expected 1 stored artefact, got %d", len(spy.storedArtefacts))
	}
	stored := spy.storedArtefacts[0]
	if stored.GetArtefactId() != "law-reference" {
		t.Errorf("expected artefact_id=law-reference, got %q", stored.GetArtefactId())
	}
	if stored.GetGovernedArtefact() != "law-reference" {
		t.Errorf("expected governed_artefact=law-reference, got %q", stored.GetGovernedArtefact())
	}
	if string(stored.GetContent()) != "law-42" {
		t.Errorf("expected content=law-42, got %q", string(stored.GetContent()))
	}

	if len(spy.routedOutputs) != 1 {
		t.Fatalf("expected 1 routed output, got %d", len(spy.routedOutputs))
	}
	if spy.routedOutputs[0] != "default" {
		t.Errorf("expected route target=default, got %q", spy.routedOutputs[0])
	}
}

func TestProcessHearing_MissingLawID(t *testing.T) {
	spy := &handlerSpy{}
	client := newHandlerTestClient(t, spy)

	wctx := &flowv1.WorkitemContext{
		FlowNamespace: "test-ns",
		WorkitemId:    "wi-hearing-002",
		NodeId:        "ttl-watcher",
		Metadata:      map[string]string{},
	}

	err := processHearing(context.Background(), client, wctx)
	if err == nil {
		t.Fatal("expected error for missing law_id, got nil")
	}
	if !strings.Contains(err.Error(), "missing law_id") {
		t.Fatalf("expected 'missing law_id' in error, got: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if spy.heartbeatCount != 0 {
		t.Errorf("expected 0 heartbeats on error path, got %d", spy.heartbeatCount)
	}
}

func TestProcessHearing_NilMetadata(t *testing.T) {
	spy := &handlerSpy{}
	client := newHandlerTestClient(t, spy)

	wctx := &flowv1.WorkitemContext{
		FlowNamespace: "test-ns",
		WorkitemId:    "wi-hearing-003",
		NodeId:        "ttl-watcher",
	}

	err := processHearing(context.Background(), client, wctx)
	if err == nil {
		t.Fatal("expected error for nil metadata, got nil")
	}
	if !strings.Contains(err.Error(), "missing law_id") {
		t.Fatalf("expected 'missing law_id' in error, got: %v", err)
	}
}

// Ensure flow import is used (for NewClient / WithSidecarAddress).
var _ = flow.DefaultSidecarAddress
