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

func TestFederationConfigSerialisesCorrectly(t *testing.T) {
	spec := FoundryFlowSpec{
		EntryContracts: map[string]Contract{"default": {}},
		ExitContracts:  map[string]Contract{"default": {}},
		GovernancePolicy: GovernancePolicy{
			MaxVisits: 10,
		},
		CrossFlow: &CrossFlowConfig{
			Federation: &FederationConfig{
				Identity:           "flow-alpha",
				States:             []string{"california", "nevada"},
				FederationEndpoint: "federation.example.com:50061",
				PublisherRoles: []FederationPublisherRole{
					{Scope: "security", Level: "state"},
					{Scope: "compliance", Level: "federation"},
				},
			},
		},
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal FoundryFlowSpec with federation config: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal FoundryFlowSpec: %v", err)
	}

	crossFlow, ok := decoded["crossFlow"].(map[string]any)
	if !ok {
		t.Fatal("crossFlow missing from JSON")
	}

	federation, ok := crossFlow["federation"].(map[string]any)
	if !ok {
		t.Fatal("federation missing from crossFlow JSON")
	}

	if got := federation["identity"]; got != "flow-alpha" {
		t.Fatalf("expected identity flow-alpha, got %#v", got)
	}

	if got := federation["federationEndpoint"]; got != "federation.example.com:50061" {
		t.Fatalf("expected federationEndpoint federation.example.com:50061, got %#v", got)
	}

	states, ok := federation["states"].([]any)
	if !ok || len(states) != 2 {
		t.Fatalf("expected 2 states, got %#v", federation["states"])
	}

	roles, ok := federation["publisherRoles"].([]any)
	if !ok || len(roles) != 2 {
		t.Fatalf("expected 2 publisherRoles, got %#v", federation["publisherRoles"])
	}

	role0, ok := roles[0].(map[string]any)
	if !ok {
		t.Fatal("first publisherRole not a map")
	}
	if role0["scope"] != "security" || role0["level"] != "state" {
		t.Fatalf("unexpected first publisherRole: %#v", role0)
	}
}

func TestFederationFieldsOptionalWhenAbsent(t *testing.T) {
	spec := FoundryFlowSpec{
		EntryContracts: map[string]Contract{"default": {}},
		ExitContracts:  map[string]Contract{"default": {}},
		GovernancePolicy: GovernancePolicy{
			MaxVisits: 10,
		},
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal FoundryFlowSpec without federation: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal FoundryFlowSpec: %v", err)
	}

	if _, ok := decoded["crossFlow"]; ok {
		t.Fatal("crossFlow should be absent when nil")
	}

	// Also verify that CrossFlowConfig without Federation omits the federation key.
	spec2 := FoundryFlowSpec{
		EntryContracts: map[string]Contract{"default": {}},
		ExitContracts:  map[string]Contract{"default": {}},
		GovernancePolicy: GovernancePolicy{
			MaxVisits: 10,
		},
		CrossFlow: &CrossFlowConfig{
			FederationCA: "pem-data",
		},
	}

	data2, err := json.Marshal(spec2)
	if err != nil {
		t.Fatalf("marshal FoundryFlowSpec with crossFlow but no federation: %v", err)
	}

	var decoded2 map[string]any
	if err := json.Unmarshal(data2, &decoded2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	crossFlow, ok := decoded2["crossFlow"].(map[string]any)
	if !ok {
		t.Fatal("crossFlow missing from JSON")
	}

	if _, ok := crossFlow["federation"]; ok {
		t.Fatal("federation should be absent when nil")
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
