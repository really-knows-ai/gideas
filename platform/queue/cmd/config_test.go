package main

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/foundry/flow/queue/internal/service"
)

// TestResolveQueueLeaseTTL pins the precedence of the --queue-lease-ttl flag,
// the QUEUE_LEASE_TTL env var, and the DefaultQueueLeaseTTL fallback, plus the
// warn-and-fallback branch for malformed values.
//
//nolint:dupl // every line is an intentional mirror of TestResolveQueueSweepInterval
func TestResolveQueueLeaseTTL(t *testing.T) {
	t.Run("flag wins over env and default", func(t *testing.T) {
		t.Setenv("QUEUE_LEASE_TTL", "60s")
		got := resolveQueueLeaseTTL("25s")
		if got != 25*time.Second {
			t.Fatalf("got %v, want 25s (flag must win)", got)
		}
	})

	t.Run("env wins over default when flag empty", func(t *testing.T) {
		t.Setenv("QUEUE_LEASE_TTL", "60s")
		got := resolveQueueLeaseTTL("")
		if got != 60*time.Second {
			t.Fatalf("got %v, want 60s (env must win over default)", got)
		}
	})

	t.Run("both empty falls back to default", func(t *testing.T) {
		t.Setenv("QUEUE_LEASE_TTL", "")
		got := resolveQueueLeaseTTL("")
		if got != service.DefaultQueueLeaseTTL {
			t.Fatalf("got %v, want default %v", got, service.DefaultQueueLeaseTTL)
		}
	})

	t.Run("malformed flag with valid env warns and falls back", func(t *testing.T) {
		t.Setenv("QUEUE_LEASE_TTL", "60s")
		var buf bytes.Buffer
		orig := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		defer slog.SetDefault(orig)

		got := resolveQueueLeaseTTL("not-a-duration")
		if got != service.DefaultQueueLeaseTTL {
			t.Fatalf("malformed flag: got %v, want default %v", got, service.DefaultQueueLeaseTTL)
		}
		if !bytes.Contains(buf.Bytes(), []byte("QUEUE_LEASE_TTL")) {
			t.Fatalf("expected a warn log, got: %s", buf.String())
		}
	})

	t.Run("malformed env with no flag warns and falls back, no crash", func(t *testing.T) {
		t.Setenv("QUEUE_LEASE_TTL", "not-a-duration")
		var buf bytes.Buffer
		orig := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		defer slog.SetDefault(orig)

		got := resolveQueueLeaseTTL("")
		if got != service.DefaultQueueLeaseTTL {
			t.Fatalf("malformed env: got %v, want default %v", got, service.DefaultQueueLeaseTTL)
		}
		if !bytes.Contains(buf.Bytes(), []byte("QUEUE_LEASE_TTL")) {
			t.Fatalf("expected a warn log, got: %s", buf.String())
		}
	})
}

// TestResolveQueueSweepInterval pins the precedence of the
// --convergence-sweep-interval flag, the QUEUE_SWEEP_INTERVAL env var, and the
// DefaultSweepInterval fallback, plus the warn-and-fallback branch for
// malformed values.
//
//nolint:dupl // every line is an intentional mirror of TestResolveQueueLeaseTTL
func TestResolveQueueSweepInterval(t *testing.T) {
	t.Run("flag wins over env and default", func(t *testing.T) {
		t.Setenv("QUEUE_SWEEP_INTERVAL", "90s")
		got := resolveQueueSweepInterval("25s")
		if got != 25*time.Second {
			t.Fatalf("got %v, want 25s (flag must win)", got)
		}
	})

	t.Run("env wins over default when flag empty", func(t *testing.T) {
		t.Setenv("QUEUE_SWEEP_INTERVAL", "90s")
		got := resolveQueueSweepInterval("")
		if got != 90*time.Second {
			t.Fatalf("got %v, want 90s (env must win over default)", got)
		}
	})

	t.Run("both empty falls back to default", func(t *testing.T) {
		t.Setenv("QUEUE_SWEEP_INTERVAL", "")
		got := resolveQueueSweepInterval("")
		if got != service.DefaultSweepInterval {
			t.Fatalf("got %v, want default %v", got, service.DefaultSweepInterval)
		}
	})

	t.Run("malformed flag with valid env warns and falls back", func(t *testing.T) {
		t.Setenv("QUEUE_SWEEP_INTERVAL", "90s")
		var buf bytes.Buffer
		orig := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		defer slog.SetDefault(orig)

		got := resolveQueueSweepInterval("not-a-duration")
		if got != service.DefaultSweepInterval {
			t.Fatalf("malformed flag: got %v, want default %v", got, service.DefaultSweepInterval)
		}
		if !bytes.Contains(buf.Bytes(), []byte("QUEUE_SWEEP_INTERVAL")) {
			t.Fatalf("expected a warn log, got: %s", buf.String())
		}
	})

	t.Run("malformed env with no flag warns and falls back, no crash", func(t *testing.T) {
		t.Setenv("QUEUE_SWEEP_INTERVAL", "not-a-duration")
		var buf bytes.Buffer
		orig := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		defer slog.SetDefault(orig)

		got := resolveQueueSweepInterval("")
		if got != service.DefaultSweepInterval {
			t.Fatalf("malformed env: got %v, want default %v", got, service.DefaultSweepInterval)
		}
		if !bytes.Contains(buf.Bytes(), []byte("QUEUE_SWEEP_INTERVAL")) {
			t.Fatalf("expected a warn log, got: %s", buf.String())
		}
	})
}
