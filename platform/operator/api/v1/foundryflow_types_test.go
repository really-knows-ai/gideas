package v1

import (
	"encoding/json"
	"testing"
)

func TestFoundryFlowSpecCrossFlowJSONShape(t *testing.T) {
	spec := FoundryFlowSpec{
		EntryContracts: map[string]Contract{"default": {}},
		ExitContracts:  map[string]Contract{"default": {}},
		GovernancePolicy: GovernancePolicy{
			MaxVisits: 10,
		},
		CrossFlow: &CrossFlowConfig{
			FederationCA: "pem-data",
			ImportTypes: map[string]ImportTypeSpec{
				"law-petition": {
					Node: "clerk-sort",
					RequireForeignStamps: Contract{
						"petition": {"approval", "judiciary-consensus"},
					},
				},
			},
		},
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal FoundryFlowSpec: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal FoundryFlowSpec: %v", err)
	}

	if _, ok := decoded["importNode"]; ok {
		t.Fatal("unexpected legacy importNode field present")
	}

	crossFlow, ok := decoded["crossFlow"].(map[string]any)
	if !ok {
		t.Fatal("crossFlow missing from JSON")
	}

	if got := crossFlow["federationCA"]; got != "pem-data" {
		t.Fatalf("expected federationCA to be pem-data, got %#v", got)
	}

	importTypes, ok := crossFlow["importTypes"].(map[string]any)
	if !ok {
		t.Fatal("importTypes missing from JSON")
	}

	lawPetition, ok := importTypes["law-petition"].(map[string]any)
	if !ok {
		t.Fatal("law-petition import type missing from JSON")
	}

	if got := lawPetition["node"]; got != "clerk-sort" {
		t.Fatalf("expected node to be clerk-sort, got %#v", got)
	}

	requireForeignStamps, ok := lawPetition["requireForeignStamps"].(map[string]any)
	if !ok {
		t.Fatal("requireForeignStamps missing from JSON")
	}

	petition, ok := requireForeignStamps["petition"].([]any)
	if !ok || len(petition) != 2 {
		t.Fatalf("expected petition stamps in JSON, got %#v", requireForeignStamps["petition"])
	}
}

func TestTreatySpecAllowedImportTypesJSONShape(t *testing.T) {
	spec := TreatySpec{
		RemoteName:         "remote-flow",
		Direction:          "import",
		CACert:             "pem-data",
		AllowedImportTypes: []string{"law-petition", "custom-import"},
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal TreatySpec: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal TreatySpec: %v", err)
	}

	rawAllowed, ok := decoded["allowedImportTypes"].([]any)
	if !ok {
		t.Fatal("allowedImportTypes missing from JSON")
	}

	if len(rawAllowed) != 2 {
		t.Fatalf("expected 2 allowed import types, got %d", len(rawAllowed))
	}
}
