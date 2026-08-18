package main

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/health/grpc_health_v1"
)

func TestParseBoolEnv(t *testing.T) {
	cases := []struct {
		val  string
		def  bool
		want bool
	}{
		{"true", false, true},
		{"TRUE", false, true},
		{"1", false, true},
		{" false ", true, false},
		{"0", true, false},
		{"t", false, true},
		{"F", true, false},
		// empty and unset values fall back to the default, never panic.
		{"", true, true},
	}
	for _, tc := range cases {
		t.Setenv("REMOTE_PULL_ON_INIT", tc.val)
		got, err := parseBoolEnv(tc.def)
		if err != nil {
			t.Errorf("parseBoolEnv(%v) unexpected error: %v", tc.def, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseBoolEnv(%v) = %v, want %v", tc.def, got, tc.want)
		}
	}
	// Unset env falls back to default.
	t.Setenv("REMOTE_PULL_ON_INIT", "")
	got, err := parseBoolEnv(false)
	if err != nil {
		t.Fatalf("parseBoolEnv on unset var: %v", err)
	}
	if got {
		t.Error("parseBoolEnv on unset var = true, want false")
	}
	// An unparseable value returns an error (SPEC R5 fail-fast, mirroring
	// parseDurationEnv): the caller exits the process rather than silently
	// running with the default pull-on-init setting.
	t.Setenv("REMOTE_PULL_ON_INIT", "bogus")
	if _, err := parseBoolEnv(true); err == nil {
		t.Error("parseBoolEnv on invalid value = nil error, want error (fail-fast exit)")
	}
}

// TestGetEnv verifies the SPEC R5 env-var default fallbacks implemented by
// getEnv for the string-valued env vars main reads through it (LADYBUG_DB_PATH,
// CARTOGRAPHER_PORT, POD_NAMESPACE; main.go:50-51,59): each variable's default
// is returned when the env var is unset/empty, and the env value wins when
// present. The duration-typed vars (TRANSACTION_TIMEOUT,
// CAPABILITY_STALENESS_WINDOW) are read via parseDurationEnv with fail-fast
// semantics and are covered by TestParseDurationEnv, not here.
func TestGetEnv(t *testing.T) {
	cases := []struct {
		key     string
		def     string
		wantDef string
	}{
		{"LADYBUG_DB_PATH", "/data", "/data"},
		{"CARTOGRAPHER_PORT", "50051", "50051"},
		{"POD_NAMESPACE", "default", "default"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"/unset", func(t *testing.T) {
			// An empty env var is equivalent to unset for getEnv.
			t.Setenv(tc.key, "")
			if got := getEnv(tc.key, tc.def); got != tc.wantDef {
				t.Errorf("getEnv(%q, %q) on unset var = %q, want default %q", tc.key, tc.def, got, tc.wantDef)
			}
		})
		t.Run(tc.key+"/set", func(t *testing.T) {
			t.Setenv(tc.key, "env-value")
			if got := getEnv(tc.key, tc.def); got != "env-value" {
				t.Errorf("getEnv(%q, %q) with env set = %q, want env value %q", tc.key, tc.def, got, "env-value")
			}
		})
	}
}

// TestParseDurationEnv verifies the SPEC R5 fail-fast duration env parsing
// (main.go:58-68): an unparseable value returns an error (the caller exits),
// a valid value parses, and an unset/empty var falls back to the default.
func TestParseDurationEnv(t *testing.T) {
	t.Run("unset falls back to default", func(t *testing.T) {
		t.Setenv("TRANSACTION_TIMEOUT", "")
		got, err := parseDurationEnv("TRANSACTION_TIMEOUT", defaultTransactionTimeout)
		if err != nil {
			t.Fatalf("parseDurationEnv on unset var: %v", err)
		}
		if want := 30 * time.Minute; got != want {
			t.Errorf("parseDurationEnv on unset var = %v, want %v", got, want)
		}
	})
	t.Run("valid env value wins", func(t *testing.T) {
		t.Setenv("TRANSACTION_TIMEOUT", "45s")
		got, err := parseDurationEnv("TRANSACTION_TIMEOUT", defaultTransactionTimeout)
		if err != nil {
			t.Fatalf("parseDurationEnv: %v", err)
		}
		if want := 45 * time.Second; got != want {
			t.Errorf("parseDurationEnv = %v, want %v", got, want)
		}
	})
	t.Run("invalid env value errors", func(t *testing.T) {
		t.Setenv("CAPABILITY_STALENESS_WINDOW", "not-a-duration")
		if _, err := parseDurationEnv("CAPABILITY_STALENESS_WINDOW", defaultCapabilityStalenessWindow); err == nil {
			t.Error("parseDurationEnv on invalid value = nil error, want error (fail-fast exit)")
		}
	})
}

// TestNewHealthServerServing verifies SPEC R5: before the first ApplySchema,
// the standard health service reports SERVING. main registers this state at
// startup via newHealthServer (main.go:271-272); the shutdown path flips it
// to NOT_SERVING.
func TestNewHealthServerServing(t *testing.T) {
	srv := newHealthServer()
	resp, err := srv.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health Check: %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("startup health status = %v, want SERVING", resp.Status)
	}
}
