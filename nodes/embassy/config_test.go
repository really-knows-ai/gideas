package main

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Tests -- Embassy config loading
// ---------------------------------------------------------------------------

func TestLoadConfig_SystemImportTypes(t *testing.T) {
	t.Setenv("EMBASSY_SYSTEM_IMPORT_TYPES", `{"law-petition":{"Name":"law-petition","BuiltIn":true}}`)
	t.Setenv("EMBASSY_FLOW_IMPORT_TYPES", `{}`)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() returned error: %v", err)
	}

	if len(cfg.SystemImportTypes) != 1 {
		t.Fatalf("expected 1 system import type, got %d", len(cfg.SystemImportTypes))
	}
	lp, ok := cfg.SystemImportTypes["law-petition"]
	if !ok {
		t.Fatal("expected law-petition system import type")
	}
	if !lp.BuiltIn {
		t.Error("expected law-petition to be BuiltIn")
	}
}

func TestLoadConfig_FlowImportTypes(t *testing.T) {
	t.Setenv("EMBASSY_SYSTEM_IMPORT_TYPES", `{}`)
	t.Setenv("EMBASSY_FLOW_IMPORT_TYPES",
		`{"external-submission":{"node":"intake-triage","requireForeignStamps":{"submission":["approval"]}}}`)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() returned error: %v", err)
	}

	if len(cfg.FlowImportTypes) != 1 {
		t.Fatalf("expected 1 flow import type, got %d", len(cfg.FlowImportTypes))
	}
	es, ok := cfg.FlowImportTypes["external-submission"]
	if !ok {
		t.Fatal("expected external-submission flow import type")
	}
	if es.Node != "intake-triage" {
		t.Errorf("expected node=intake-triage, got %q", es.Node)
	}
	stamps, ok := es.RequireForeignStamps["submission"]
	if !ok || len(stamps) != 1 || stamps[0] != "approval" {
		t.Errorf("expected requireForeignStamps={submission:[approval]}, got %v", es.RequireForeignStamps)
	}
}

func TestLoadConfig_FederationIdentity(t *testing.T) {
	t.Setenv("EMBASSY_FEDERATION_IDENTITY", "flow-alpha")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() returned error: %v", err)
	}

	if cfg.FederationIdentity != "flow-alpha" {
		t.Errorf("expected FederationIdentity=flow-alpha, got %q", cfg.FederationIdentity)
	}
}

func TestLoadConfig_FederationEndpoint(t *testing.T) {
	t.Setenv("EMBASSY_FEDERATION_ENDPOINT", "flow-federation:50061")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() returned error: %v", err)
	}

	if cfg.FederationEndpoint != "flow-federation:50061" {
		t.Errorf("expected FederationEndpoint=flow-federation:50061, got %q", cfg.FederationEndpoint)
	}
}

func TestLoadConfig_FederationStates(t *testing.T) {
	t.Setenv("EMBASSY_FEDERATION_STATES", `["state-a","state-b"]`)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() returned error: %v", err)
	}

	if len(cfg.FederationStates) != 2 {
		t.Fatalf("expected 2 federation states, got %d", len(cfg.FederationStates))
	}
	if cfg.FederationStates[0] != "state-a" || cfg.FederationStates[1] != "state-b" {
		t.Errorf("expected [state-a, state-b], got %v", cfg.FederationStates)
	}
}

func TestLoadConfig_FederationCAPEM(t *testing.T) {
	t.Setenv("EMBASSY_FEDERATION_CA_PEM", "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() returned error: %v", err)
	}

	if cfg.FederationCAPEM == "" {
		t.Error("expected non-empty FederationCAPEM")
	}
}

func TestLoadConfig_NaturalisationConfig(t *testing.T) {
	t.Setenv("EMBASSY_NATURALISATION_CONFIG",
		`{"autoNaturalise":false,"requireLocalStamps":["compliance-check"]}`)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() returned error: %v", err)
	}

	if cfg.Naturalisation == nil {
		t.Fatal("expected non-nil Naturalisation")
	}
	if cfg.Naturalisation.AutoNaturalise == nil || *cfg.Naturalisation.AutoNaturalise {
		t.Error("expected AutoNaturalise=false")
	}
	if len(cfg.Naturalisation.RequireLocalStamps) != 1 ||
		cfg.Naturalisation.RequireLocalStamps[0] != "compliance-check" {
		t.Errorf("expected RequireLocalStamps=[compliance-check], got %v",
			cfg.Naturalisation.RequireLocalStamps)
	}
}

func TestLoadConfig_MissingOptionalVars_DefaultsToNonFederated(t *testing.T) {
	// Clear all Embassy-related env vars (t.Setenv guarantees restore).
	t.Setenv("EMBASSY_SYSTEM_IMPORT_TYPES", "")
	t.Setenv("EMBASSY_FLOW_IMPORT_TYPES", "")
	t.Setenv("EMBASSY_FEDERATION_IDENTITY", "")
	t.Setenv("EMBASSY_FEDERATION_ENDPOINT", "")
	t.Setenv("EMBASSY_FEDERATION_STATES", "")
	t.Setenv("EMBASSY_FEDERATION_CA_PEM", "")
	t.Setenv("EMBASSY_NATURALISATION_CONFIG", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() returned error: %v", err)
	}

	// Non-federated mode: empty federation fields, nil naturalisation.
	if cfg.FederationIdentity != "" {
		t.Errorf("expected empty FederationIdentity, got %q", cfg.FederationIdentity)
	}
	if cfg.FederationEndpoint != "" {
		t.Errorf("expected empty FederationEndpoint, got %q", cfg.FederationEndpoint)
	}
	if len(cfg.FederationStates) != 0 {
		t.Errorf("expected empty FederationStates, got %v", cfg.FederationStates)
	}
	if cfg.FederationCAPEM != "" {
		t.Errorf("expected empty FederationCAPEM, got %q", cfg.FederationCAPEM)
	}
	if cfg.Naturalisation != nil {
		t.Errorf("expected nil Naturalisation, got %v", cfg.Naturalisation)
	}
	if cfg.SystemImportTypes == nil {
		t.Error("expected non-nil SystemImportTypes (empty map)")
	}
	if cfg.FlowImportTypes == nil {
		t.Error("expected non-nil FlowImportTypes (empty map)")
	}
}

func TestLoadConfig_IsFederated(t *testing.T) {
	t.Setenv("EMBASSY_FEDERATION_IDENTITY", "flow-alpha")
	t.Setenv("EMBASSY_FEDERATION_ENDPOINT", "flow-federation:50061")
	t.Setenv("EMBASSY_FEDERATION_STATES", `["state-a"]`)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() returned error: %v", err)
	}

	if !cfg.IsFederated() {
		t.Error("expected IsFederated()=true when federation identity is set")
	}
}

func TestLoadConfig_IsNotFederated(t *testing.T) {
	t.Setenv("EMBASSY_FEDERATION_IDENTITY", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() returned error: %v", err)
	}

	if cfg.IsFederated() {
		t.Error("expected IsFederated()=false when federation identity is empty")
	}
}
