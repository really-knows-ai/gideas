package flow

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// testTopology returns a reusable topology fixture with an exit contract.
func testTopology() *flowv1.GetFlowTopologyResponse {
	return &flowv1.GetFlowTopologyResponse{
		ExitContract: map[string]*flowv1.StampRequirements{
			"haiku": {Stamps: []string{"review", "review"}},
			"law":   {Stamps: []string{"law-group-content", "law-abc-def"}},
		},
	}
}

// ---------------------------------------------------------------------------
// Flow tests
// ---------------------------------------------------------------------------

func TestFlow_GetExitContract(t *testing.T) {
	f := newFlow(testTopology())
	ec := f.GetExitContract()

	// Check haiku entry.
	haikuStamps, ok := ec["haiku"]
	if !ok {
		t.Fatal("missing exit contract key: haiku")
	}
	if len(haikuStamps) != 2 {
		t.Fatalf("haiku stamps length = %d, want 2", len(haikuStamps))
	}
	if haikuStamps[0] != "review" {
		t.Errorf("haiku stamps[0] = %q, want %q", haikuStamps[0], "review")
	}
	if haikuStamps[1] != "review" {
		t.Errorf("haiku stamps[1] = %q, want %q", haikuStamps[1], "review")
	}

	// Check law entry.
	lawStamps, ok := ec["law"]
	if !ok {
		t.Fatal("missing exit contract key: law")
	}
	if len(lawStamps) != 2 {
		t.Fatalf("law stamps length = %d, want 2", len(lawStamps))
	}
	if lawStamps[0] != "law-group-content" {
		t.Errorf("law stamps[0] = %q, want %q", lawStamps[0], "law-group-content")
	}
}

func TestFlow_GetExitContract_nil(t *testing.T) {
	f := newFlow(&flowv1.GetFlowTopologyResponse{})
	ec := f.GetExitContract()
	if ec != nil {
		t.Errorf("expected nil exit contract for empty response, got %v", ec)
	}
}
