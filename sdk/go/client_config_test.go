package flow

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Tests — Configuration
// ---------------------------------------------------------------------------

func TestNewClient_DefaultAddress(t *testing.T) {
	cfg := &clientConfig{sidecarAddr: DefaultSidecarAddress}

	// Apply zero options — the default should remain.
	opts := []ClientOption{}
	for _, o := range opts {
		o(cfg)
	}

	if cfg.sidecarAddr != "localhost:50051" {
		t.Fatalf("expected default address localhost:50051, got %s", cfg.sidecarAddr)
	}
}

func TestNewClient_CustomAddress(t *testing.T) {
	cfg := &clientConfig{sidecarAddr: DefaultSidecarAddress}
	WithSidecarAddress("custom:9090")(cfg)

	if cfg.sidecarAddr != "custom:9090" {
		t.Fatalf("expected custom address custom:9090, got %s", cfg.sidecarAddr)
	}
}

// ---------------------------------------------------------------------------
// Tests — New Client Options
// ---------------------------------------------------------------------------

func TestNewClient_WithRequestTimeout(t *testing.T) {
	cfg := &clientConfig{sidecarAddr: DefaultSidecarAddress}
	WithRequestTimeout(5 * time.Second)(cfg)
	if cfg.timeout != 5*time.Second {
		t.Fatalf("expected timeout=5s, got %v", cfg.timeout)
	}
}

func TestClient_Close(t *testing.T) {
	env := setupTestEnv(t, "workitem-close-001")
	err := env.client.Close()
	if err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}
}
