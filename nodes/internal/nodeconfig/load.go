// Package nodeconfig provides a shared configuration loader for Foundry
// Flow nodes. Each node defines its own config struct and uses Load to
// populate it from a YAML file.
//
// The config file path is resolved from the NODE_CONFIG_PATH environment
// variable, falling back to DefaultConfigPath if unset. In Kubernetes the
// file is typically mounted from a ConfigMap volume.
package nodeconfig

import (
	"fmt"
	"os"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"gopkg.in/yaml.v3"
)

const (
	// EnvConfigPath is the environment variable that overrides the default
	// config file location.
	EnvConfigPath = "NODE_CONFIG_PATH"

	// DefaultConfigPath is the fallback path used when EnvConfigPath is
	// not set.
	DefaultConfigPath = "/etc/foundry/node-config.yaml"
)

// Path returns the config file path from the environment, falling back to
// DefaultConfigPath.
func Path() string {
	if p := os.Getenv(EnvConfigPath); p != "" {
		return p
	}
	return DefaultConfigPath
}

// Load reads the YAML file at path and unmarshals it into a new T.
// If the file does not exist, Load returns a zero-valued T (no error) so
// that callers can rely on struct-level defaults.
func Load[T any](path string) (*T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			var zero T
			return &zero, nil
		}
		return nil, fmt.Errorf("nodeconfig: read %s: %w", path, err)
	}

	var cfg T
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("nodeconfig: parse %s: %w", path, err)
	}
	return &cfg, nil
}

// ParseConsensusStrategy converts a config string to the protobuf enum.
// Recognised values (case-insensitive, whitespace-trimmed):
//
//	"SUPER_MAJORITY" -> CONSENSUS_STRATEGY_SUPER_MAJORITY
//	"UNANIMITY"      -> CONSENSUS_STRATEGY_UNANIMITY
//
// All other values (including empty) default to SIMPLE_MAJORITY.
func ParseConsensusStrategy(s string) flowv1.ConsensusStrategy {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "SUPER_MAJORITY":
		return flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SUPER_MAJORITY
	case "UNANIMITY":
		return flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNANIMITY
	default:
		return flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY
	}
}
