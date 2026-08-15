package main

import (
	"testing"

	"github.com/foundry/flow/operator/internal/controller"
)

// TestResolveProxyAddress pins the SPEC R6 OPERATOR_PROXY_PORT default branch
// (SPEC.md R6 operator env-var table): an unset flag/env resolves to the SPEC
// default address ":50053", the env var is a bare port bound with ":", and the
// --proxy-bind-address flag wins over the env var.
func TestResolveProxyAddress(t *testing.T) {
	t.Run("SPEC default when flag and env are unset", func(t *testing.T) {
		t.Setenv("OPERATOR_PROXY_PORT", "")
		if got := resolveProxyAddress(""); got != ":50053" {
			t.Errorf("resolveProxyAddress on unset flag/env = %q, want the SPEC default address", got)
		}
	})
	t.Run("env var is a bare port bound with :", func(t *testing.T) {
		t.Setenv("OPERATOR_PROXY_PORT", "9999")
		if got := resolveProxyAddress(""); got != ":9999" {
			t.Errorf("resolveProxyAddress with OPERATOR_PROXY_PORT=9999 = %q, want the port prefixed with :", got)
		}
	})
	t.Run("flag wins over the env var", func(t *testing.T) {
		t.Setenv("OPERATOR_PROXY_PORT", "9999")
		if got := resolveProxyAddress(":6000"); got != ":6000" {
			t.Errorf("resolveProxyAddress with flag %q set = %q, want the flag value", ":6000", got)
		}
	})
}

// TestResolveCartographerConfigDefaults pins the SPEC R6 env-var default
// branches for the remaining Cartographer config (SPEC.md R6 operator env-var
// table): each resolver returns its SPEC default when the flag and env var are
// unset, the env-var override wins when set, and the CLI flag wins over both.
func TestResolveCartographerConfigDefaults(t *testing.T) {
	cases := []struct {
		name    string
		envKey  string
		resolve func(string) string
		def     string
	}{
		{"readiness-timeout", "CARTOGRAPHER_READINESS_TIMEOUT", resolveReadinessTimeout, "5m"},
		{"cartographer-port", "CARTOGRAPHER_PORT", resolveCartographerPort, "50051"},
		{"cartographer-image", "CARTOGRAPHER_IMAGE", resolveCartographerImage, controller.DefaultCartographerImage},
		{"capability-staleness-window", "CAPABILITY_STALENESS_WINDOW", resolveCapabilityStalenessWindow, "30s"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/unset-uses-spec-default", func(t *testing.T) {
			t.Setenv(tc.envKey, "")
			if got := tc.resolve(""); got != tc.def {
				t.Errorf("%s on unset flag/env = %q, want the SPEC default %q", tc.name, got, tc.def)
			}
		})
		t.Run(tc.name+"/env-overrides-default", func(t *testing.T) {
			t.Setenv(tc.envKey, "9x")
			if got := tc.resolve(""); got != "9x" {
				t.Errorf("%s with %s set = %q, want the env value", tc.name, tc.envKey, got)
			}
		})
		t.Run(tc.name+"/flag-wins", func(t *testing.T) {
			t.Setenv(tc.envKey, "9x")
			if got := tc.resolve("fx"); got != "fx" {
				t.Errorf("%s with flag %q set = %q, want the flag value", tc.name, "fx", got)
			}
		})
	}
}
