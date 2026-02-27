package main

import (
	"context"
	"encoding/json"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Happy Path — Tier 1 (Finding) routes to Clerk
// ---------------------------------------------------------------------------

func TestRouter_Tier1Finding_RoutesToClerk(t *testing.T) {
	spy := newRouterSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "retire",
		RoundsUsed: 1,
	})
	seedLawReference(spy, "law-001")
	spy.Laws["law-001"] = &flowv1.Law{
		Id:   "law-001",
		Goal: "test finding",
		Tier: flowv1.LawTier_LAW_TIER_FINDING,
	}

	client := setupRouterTest(t, spy)

	if err := handleTribunalRouter(context.Background(), client); err != nil {
		t.Fatalf("handleTribunalRouter: %v", err)
	}

	assertRoutedTo(t, spy, outputClerk)
}

// ---------------------------------------------------------------------------
// Tier 2 (Ruling) with non-promote outcome routes to Clerk
// ---------------------------------------------------------------------------

func TestRouter_Tier2Ruling_Retire_RoutesToClerk(t *testing.T) {
	spy := newRouterSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "retire",
		RoundsUsed: 2,
	})
	seedLawReference(spy, "law-002")
	spy.Laws["law-002"] = &flowv1.Law{
		Id:   "law-002",
		Goal: "test ruling",
		Tier: flowv1.LawTier_LAW_TIER_RULING,
	}

	client := setupRouterTest(t, spy)

	if err := handleTribunalRouter(context.Background(), client); err != nil {
		t.Fatalf("handleTribunalRouter: %v", err)
	}

	assertRoutedTo(t, spy, outputClerk)
}

func TestRouter_Tier2Ruling_Demote_RoutesToClerk(t *testing.T) {
	spy := newRouterSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "demote",
		RoundsUsed: 1,
	})
	seedLawReference(spy, "law-003")
	spy.Laws["law-003"] = &flowv1.Law{
		Id:   "law-003",
		Goal: "demote ruling",
		Tier: flowv1.LawTier_LAW_TIER_RULING,
	}

	client := setupRouterTest(t, spy)

	if err := handleTribunalRouter(context.Background(), client); err != nil {
		t.Fatalf("handleTribunalRouter: %v", err)
	}

	assertRoutedTo(t, spy, outputClerk)
}

// ---------------------------------------------------------------------------
// Tier 2 (Ruling) promote -> Advocate (HITL ratification for Tier 3)
// ---------------------------------------------------------------------------

func TestRouter_Tier2Ruling_Promote_RoutesToAdvocate(t *testing.T) {
	spy := newRouterSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    verdictPromote,
		RoundsUsed: 1,
	})
	seedLawReference(spy, "law-004")
	spy.Laws["law-004"] = &flowv1.Law{
		Id:   "law-004",
		Goal: "promote to tier 3",
		Tier: flowv1.LawTier_LAW_TIER_RULING,
	}

	client := setupRouterTest(t, spy)

	if err := handleTribunalRouter(context.Background(), client); err != nil {
		t.Fatalf("handleTribunalRouter: %v", err)
	}

	assertRoutedTo(t, spy, outputAdvocate)
}

// ---------------------------------------------------------------------------
// Tier 1 (Finding) promote -> Advocate
// ---------------------------------------------------------------------------

func TestRouter_Tier1Finding_Promote_RoutesToAdvocate(t *testing.T) {
	spy := newRouterSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    verdictPromote,
		RoundsUsed: 1,
	})
	seedLawReference(spy, "law-005")
	spy.Laws["law-005"] = &flowv1.Law{
		Id:   "law-005",
		Goal: "promote finding",
		Tier: flowv1.LawTier_LAW_TIER_FINDING,
	}

	client := setupRouterTest(t, spy)

	if err := handleTribunalRouter(context.Background(), client); err != nil {
		t.Fatalf("handleTribunalRouter: %v", err)
	}

	assertRoutedTo(t, spy, outputAdvocate)
}

// ---------------------------------------------------------------------------
// Tier 3 (Local Statute) -> always Advocate
// ---------------------------------------------------------------------------

func TestRouter_Tier3LocalStatute_RoutesToAdvocate(t *testing.T) {
	spy := newRouterSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "retire",
		RoundsUsed: 1,
	})
	seedLawReference(spy, "law-006")
	spy.Laws["law-006"] = &flowv1.Law{
		Id:   "law-006",
		Goal: "local statute",
		Tier: flowv1.LawTier_LAW_TIER_LOCAL_STATUTE,
	}

	client := setupRouterTest(t, spy)

	if err := handleTribunalRouter(context.Background(), client); err != nil {
		t.Fatalf("handleTribunalRouter: %v", err)
	}

	assertRoutedTo(t, spy, outputAdvocate)
}

// ---------------------------------------------------------------------------
// Tier 4 (State Constitution) -> Advocate
// ---------------------------------------------------------------------------

func TestRouter_Tier4StateConstitution_RoutesToAdvocate(t *testing.T) {
	spy := newRouterSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "demote",
		RoundsUsed: 3,
	})
	seedLawReference(spy, "law-007")
	spy.Laws["law-007"] = &flowv1.Law{
		Id:   "law-007",
		Goal: "state constitution",
		Tier: flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION,
	}

	client := setupRouterTest(t, spy)

	if err := handleTribunalRouter(context.Background(), client); err != nil {
		t.Fatalf("handleTribunalRouter: %v", err)
	}

	assertRoutedTo(t, spy, outputAdvocate)
}

// ---------------------------------------------------------------------------
// Tier 5 (Federal Accord) -> Advocate
// ---------------------------------------------------------------------------

func TestRouter_Tier5FederalAccord_RoutesToAdvocate(t *testing.T) {
	spy := newRouterSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "retire",
		RoundsUsed: 2,
	})
	seedLawReference(spy, "law-008")
	spy.Laws["law-008"] = &flowv1.Law{
		Id:   "law-008",
		Goal: "federal accord",
		Tier: flowv1.LawTier_LAW_TIER_FEDERAL_ACCORD,
	}

	client := setupRouterTest(t, spy)

	if err := handleTribunalRouter(context.Background(), client); err != nil {
		t.Fatalf("handleTribunalRouter: %v", err)
	}

	assertRoutedTo(t, spy, outputAdvocate)
}

// ---------------------------------------------------------------------------
// Law ID is fetched correctly
// ---------------------------------------------------------------------------

func TestRouter_FetchesCorrectLawID(t *testing.T) {
	spy := newRouterSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "retire",
		RoundsUsed: 1,
	})
	seedLawReference(spy, "specific-law-42")
	spy.Laws["specific-law-42"] = &flowv1.Law{
		Id:   "specific-law-42",
		Goal: "test",
		Tier: flowv1.LawTier_LAW_TIER_FINDING,
	}

	client := setupRouterTest(t, spy)

	if err := handleTribunalRouter(context.Background(), client); err != nil {
		t.Fatalf("handleTribunalRouter: %v", err)
	}

	if spy.RequestedLawID != "specific-law-42" {
		t.Errorf("requested law ID = %q, want %q", spy.RequestedLawID, "specific-law-42")
	}
}

// ---------------------------------------------------------------------------
// Law reference with whitespace is trimmed
// ---------------------------------------------------------------------------

func TestRouter_LawReference_WhitespaceTrimmed(t *testing.T) {
	spy := newRouterSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "retire",
		RoundsUsed: 1,
	})
	// Seed with trailing newline/whitespace.
	spy.Artefacts[artefactLawReference] = []byte("  law-trimmed \n")
	spy.Laws["law-trimmed"] = &flowv1.Law{
		Id:   "law-trimmed",
		Goal: "whitespace test",
		Tier: flowv1.LawTier_LAW_TIER_RULING,
	}

	client := setupRouterTest(t, spy)

	if err := handleTribunalRouter(context.Background(), client); err != nil {
		t.Fatalf("handleTribunalRouter: %v", err)
	}

	if spy.RequestedLawID != "law-trimmed" {
		t.Errorf("requested law ID = %q, want %q", spy.RequestedLawID, "law-trimmed")
	}
	assertRoutedTo(t, spy, outputClerk)
}

// ---------------------------------------------------------------------------
// Error: deliberation-result artefact missing
// ---------------------------------------------------------------------------

func TestRouter_Error_MissingDeliberationResult(t *testing.T) {
	spy := newRouterSpy()
	// No deliberation-result artefact seeded.
	seedLawReference(spy, "law-x")
	spy.Laws["law-x"] = &flowv1.Law{
		Id:   "law-x",
		Goal: "test",
		Tier: flowv1.LawTier_LAW_TIER_FINDING,
	}

	client := setupRouterTest(t, spy)

	err := handleTribunalRouter(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when deliberation-result is missing")
	}
}

// ---------------------------------------------------------------------------
// Error: invalid deliberation-result JSON
// ---------------------------------------------------------------------------

func TestRouter_Error_InvalidDeliberationResultJSON(t *testing.T) {
	spy := newRouterSpy()
	spy.Artefacts[artefactDeliberationResult] = []byte("not valid json{{{")
	seedLawReference(spy, "law-y")
	spy.Laws["law-y"] = &flowv1.Law{
		Id:   "law-y",
		Goal: "test",
		Tier: flowv1.LawTier_LAW_TIER_FINDING,
	}

	client := setupRouterTest(t, spy)

	err := handleTribunalRouter(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when deliberation-result has invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// Error: law-reference artefact missing
// ---------------------------------------------------------------------------

func TestRouter_Error_MissingLawReference(t *testing.T) {
	spy := newRouterSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "retire",
		RoundsUsed: 1,
	})
	// No law-reference seeded.

	client := setupRouterTest(t, spy)

	err := handleTribunalRouter(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when law-reference is missing")
	}
}

// ---------------------------------------------------------------------------
// Error: law-reference artefact is empty
// ---------------------------------------------------------------------------

func TestRouter_Error_EmptyLawReference(t *testing.T) {
	spy := newRouterSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "retire",
		RoundsUsed: 1,
	})
	spy.Artefacts[artefactLawReference] = []byte("  \n  ")

	client := setupRouterTest(t, spy)

	err := handleTribunalRouter(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when law-reference is empty/whitespace")
	}
}

// ---------------------------------------------------------------------------
// Error: GetLaw fails
// ---------------------------------------------------------------------------

func TestRouter_Error_GetLawFails(t *testing.T) {
	spy := newRouterSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "retire",
		RoundsUsed: 1,
	})
	seedLawReference(spy, "law-fail")
	spy.GetLawErr = status.Errorf(codes.Internal, "librarian down")

	client := setupRouterTest(t, spy)

	err := handleTribunalRouter(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when GetLaw fails")
	}
}

// ---------------------------------------------------------------------------
// Error: RouteToOutput fails
// ---------------------------------------------------------------------------

func TestRouter_Error_RouteToOutputFails(t *testing.T) {
	spy := newRouterSpy()
	seedDeliberationResult(t, spy, deliberationResult{
		Outcome:    "retire",
		RoundsUsed: 1,
	})
	seedLawReference(spy, "law-route-fail")
	spy.Laws["law-route-fail"] = &flowv1.Law{
		Id:   "law-route-fail",
		Goal: "test",
		Tier: flowv1.LawTier_LAW_TIER_FINDING,
	}
	spy.RouteToOutputErr = status.Errorf(codes.Unavailable, "sidecar down")

	client := setupRouterTest(t, spy)

	err := handleTribunalRouter(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when RouteToOutput fails")
	}
}

// ---------------------------------------------------------------------------
// Error: GetArtefact returns generic error
// ---------------------------------------------------------------------------

func TestRouter_Error_GetArtefactFails(t *testing.T) {
	spy := newRouterSpy()
	spy.GetArtefactErr = status.Errorf(codes.Internal, "archivist down")

	client := setupRouterTest(t, spy)

	err := handleTribunalRouter(context.Background(), client)
	if err == nil {
		t.Fatal("expected error when GetArtefact fails")
	}
}

// ---------------------------------------------------------------------------
// routeByTierAndOutcome unit tests
// ---------------------------------------------------------------------------

func TestRouteByTierAndOutcome(t *testing.T) {
	tests := []struct {
		name    string
		tier    int32
		outcome string
		want    string
	}{
		{
			name:    "tier 1, retire -> clerk",
			tier:    1,
			outcome: "retire",
			want:    outputClerk,
		},
		{
			name:    "tier 2, demote -> clerk",
			tier:    2,
			outcome: "demote",
			want:    outputClerk,
		},
		{
			name:    "tier 1, promote -> advocate",
			tier:    1,
			outcome: verdictPromote,
			want:    outputAdvocate,
		},
		{
			name:    "tier 2, promote -> advocate",
			tier:    2,
			outcome: verdictPromote,
			want:    outputAdvocate,
		},
		{
			name:    "tier 3, retire -> advocate",
			tier:    3,
			outcome: "retire",
			want:    outputAdvocate,
		},
		{
			name:    "tier 3, promote -> advocate",
			tier:    3,
			outcome: verdictPromote,
			want:    outputAdvocate,
		},
		{
			name:    "tier 4 -> advocate",
			tier:    4,
			outcome: "demote",
			want:    outputAdvocate,
		},
		{
			name:    "tier 5 -> advocate",
			tier:    5,
			outcome: "retire",
			want:    outputAdvocate,
		},
		{
			name:    "tier 0 (unspecified), retire -> clerk",
			tier:    0,
			outcome: "retire",
			want:    outputClerk,
		},
		{
			name:    "tier 0 (unspecified), promote -> advocate",
			tier:    0,
			outcome: verdictPromote,
			want:    outputAdvocate,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := routeByTierAndOutcome(tt.tier, tt.outcome)
			if got != tt.want {
				t.Errorf("routeByTierAndOutcome(%d, %q) = %q, want %q", tt.tier, tt.outcome, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// lawTierNumber unit tests
// ---------------------------------------------------------------------------

func TestLawTierNumber(t *testing.T) {
	tests := []struct {
		tier flowv1.LawTier
		want int32
	}{
		{flowv1.LawTier_LAW_TIER_UNSPECIFIED, 0},
		{flowv1.LawTier_LAW_TIER_FINDING, 1},
		{flowv1.LawTier_LAW_TIER_RULING, 2},
		{flowv1.LawTier_LAW_TIER_LOCAL_STATUTE, 3},
		{flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION, 4},
		{flowv1.LawTier_LAW_TIER_FEDERAL_ACCORD, 5},
	}
	for _, tt := range tests {
		t.Run(tt.tier.String(), func(t *testing.T) {
			got := lawTierNumber(tt.tier)
			if got != tt.want {
				t.Errorf("lawTierNumber(%v) = %d, want %d", tt.tier, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func seedDeliberationResult(t *testing.T, spy *routerSpy, result deliberationResult) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal deliberation result: %v", err)
	}
	spy.Artefacts[artefactDeliberationResult] = data
}

func seedLawReference(spy *routerSpy, lawID string) {
	spy.Artefacts[artefactLawReference] = []byte(lawID)
}
