package main

import (
	"encoding/json"
	"os"
)

// ---------------------------------------------------------------------------
// Environment variable names projected by the Operator to the Embassy
// container. These are consumed at startup to configure the Embassy node.
// ---------------------------------------------------------------------------

const (
	envSystemImportTypes    = "EMBASSY_SYSTEM_IMPORT_TYPES"
	envFlowImportTypes      = "EMBASSY_FLOW_IMPORT_TYPES"
	envFederationIdentity   = "EMBASSY_FEDERATION_IDENTITY"
	envFederationEndpoint   = "EMBASSY_FEDERATION_ENDPOINT"
	envFederationStates     = "EMBASSY_FEDERATION_STATES"
	envFederationCAPEM      = "EMBASSY_FEDERATION_CA_PEM"
	envNaturalisationConfig = "EMBASSY_NATURALISATION_CONFIG"
	envTreaties             = "EMBASSY_TREATIES"
)

// ---------------------------------------------------------------------------
// Config types — JSON-compatible with the operator's env var projection.
// ---------------------------------------------------------------------------

// systemImportType mirrors the builtInImportTypeConfig the operator serialises
// into EMBASSY_SYSTEM_IMPORT_TYPES.
type systemImportType struct {
	BuiltIn bool `json:"builtIn"`
}

// flowImportTypeSpec mirrors the CRD ImportTypeSpec the operator serialises
// into EMBASSY_FLOW_IMPORT_TYPES. The JSON shape matches the Kubernetes CRD
// struct tags (camelCase).
type flowImportTypeSpec struct {
	Node                 string              `json:"node"`
	RequireForeignStamps map[string][]string `json:"requireForeignStamps,omitempty"`
}

// naturalisationConfig mirrors the CRD NaturalisationConfig.
type naturalisationConfig struct {
	AutoNaturalise     *bool    `json:"autoNaturalise,omitempty"`
	RequireLocalStamps []string `json:"requireLocalStamps,omitempty"`
}

// treatyConfig mirrors the trust policy from a Treaty CRD projected by the
// Operator into the Embassy container as part of EMBASSY_TREATIES.
type treatyConfig struct {
	AllowedImportTypes []string `json:"allowedImportTypes,omitempty"`
	AllowedSubjects    []string `json:"allowedSubjects,omitempty"`
	MaxBundleSizeBytes int64    `json:"maxBundleSizeBytes,omitempty"`
}

// embassyConfig holds all Embassy node configuration loaded from environment
// variables at startup.
type embassyConfig struct {
	// Import type registries.
	SystemImportTypes map[string]systemImportType   `json:"systemImportTypes"`
	FlowImportTypes   map[string]flowImportTypeSpec `json:"flowImportTypes"`

	// Federation membership (empty when non-federated).
	FederationIdentity string   `json:"federationIdentity"`
	FederationEndpoint string   `json:"federationEndpoint"`
	FederationStates   []string `json:"federationStates"`
	FederationCAPEM    string   `json:"federationCAPEM"`

	// Treaty trust policies (map of treaty name → policy).
	Treaties map[string]treatyConfig `json:"treaties,omitempty"`

	// Naturalisation policy (nil when not configured).
	Naturalisation *naturalisationConfig `json:"naturalisation,omitempty"`
}

// IsFederated returns true when the Embassy operates within a Federation.
func (c *embassyConfig) IsFederated() bool {
	return c.FederationIdentity != ""
}

// loadConfig reads Embassy configuration from environment variables.
// Missing optional variables produce sensible defaults (non-federated mode
// with empty import type registries).
func loadConfig() (*embassyConfig, error) {
	cfg := &embassyConfig{
		SystemImportTypes: make(map[string]systemImportType),
		FlowImportTypes:   make(map[string]flowImportTypeSpec),
	}

	// System import types — JSON map.
	if raw := os.Getenv(envSystemImportTypes); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.SystemImportTypes); err != nil {
			return nil, err
		}
	}

	// Flow-authored import types — JSON map.
	if raw := os.Getenv(envFlowImportTypes); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.FlowImportTypes); err != nil {
			return nil, err
		}
	}

	// Federation identity (plain string).
	cfg.FederationIdentity = os.Getenv(envFederationIdentity)

	// Federation endpoint (plain string).
	cfg.FederationEndpoint = os.Getenv(envFederationEndpoint)

	// Federation states — JSON array of strings.
	if raw := os.Getenv(envFederationStates); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.FederationStates); err != nil {
			return nil, err
		}
	}

	// Federation CA PEM (plain string).
	cfg.FederationCAPEM = os.Getenv(envFederationCAPEM)

	// Treaties — JSON map of treaty name → treatyConfig.
	if raw := os.Getenv(envTreaties); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.Treaties); err != nil {
			return nil, err
		}
	}

	// Naturalisation config — JSON object.
	if raw := os.Getenv(envNaturalisationConfig); raw != "" {
		var nc naturalisationConfig
		if err := json.Unmarshal([]byte(raw), &nc); err != nil {
			return nil, err
		}
		cfg.Naturalisation = &nc
	}

	return cfg, nil
}
