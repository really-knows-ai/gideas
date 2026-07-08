// Package config provides configuration parsing with flag/env/fallback precedence.
package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"
)

// Config holds the runtime configuration for flowctl.
type Config struct {
	Namespace          string
	NamespaceExplicit  bool   // true when set via --namespace flag or FLOW_NAMESPACE env var
	SystemNamespace    string
	HitlPort           int
	LogFile            string
}

// ParseFlags reads configuration with precedence: flag > env var > fallback.
func ParseFlags(cmd *cobra.Command) (*Config, error) {
	cfg := &Config{}

	// Namespace: flag > FLOW_NAMESPACE > (resolved by caller: kube context > "default")
	if cmd.Flags().Changed("namespace") {
		cfg.Namespace, _ = cmd.Flags().GetString("namespace")
		cfg.NamespaceExplicit = true
	} else if v := os.Getenv("FLOW_NAMESPACE"); v != "" {
		cfg.Namespace = v
		cfg.NamespaceExplicit = true
	} // else: leave empty for caller to resolve via kube context > "default"

	// SystemNamespace: flag > FLOW_SYSTEM_NAMESPACE > resolved Namespace
	if cmd.Flags().Changed("system-namespace") {
		cfg.SystemNamespace, _ = cmd.Flags().GetString("system-namespace")
	} else if v := os.Getenv("FLOW_SYSTEM_NAMESPACE"); v != "" {
		cfg.SystemNamespace = v
	} else {
		cfg.SystemNamespace = cfg.Namespace
	}

	// HitlPort: flag > FLOW_HITL_PORT > 8080
	if cmd.Flags().Changed("hitl-port") {
		cfg.HitlPort, _ = cmd.Flags().GetInt("hitl-port")
	} else if v := os.Getenv("FLOW_HITL_PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("FLOW_HITL_PORT parse error: %w", err)
		}
		cfg.HitlPort = port
	} else {
		cfg.HitlPort = 8080
	}

	// LogFile: FLOW_LOG_FILE only (no flag, no fallback)
	cfg.LogFile = os.Getenv("FLOW_LOG_FILE")

	return cfg, nil
}
