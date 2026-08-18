package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestHealthCheck_Healthy(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.HealthCheck(ctx, &flowv1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("HealthCheck failed: %v", err)
	}
	if !resp.LadybugOk {
		t.Fatal("expected LadybugOk to be true")
	}
	if resp.SchemaApplied {
		t.Fatal("expected SchemaApplied to be false (no schema applied)")
	}
	if !resp.PvcWritable {
		t.Fatal("expected PvcWritable to be true")
	}
}

func TestHealthCheck_PropagatesStoreError(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	failing := &fakeStore{Store: base, onHealth: func(context.Context) (*store.HealthResult, error) {
		return nil, fmt.Errorf("store health probe failed: pvc unreadable")
	}}
	gs, _ := gitstore.New(t.TempDir())
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(failing, gs, opPub, initTestKey(), nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	resp, err := srv.HealthCheck(context.Background(), &flowv1.HealthCheckRequest{})
	if err == nil {
		t.Fatal("expected HealthCheck to propagate store error, got nil")
	}
	// A healthy-probe store must not be reported as a successful (non-nil)
	// response while the probe failed.
	if resp != nil {
		t.Fatalf("expected nil response on error, got %+v", resp)
	}
	if !strings.Contains(err.Error(), "pvc unreadable") {
		t.Fatalf("expected store error message, got %v", err)
	}
	// The SPEC error table names no HealthCheck failure code, so the probe
	// failure must surface as the generic INTERNAL code — never a raw
	// non-status error crossing as gRPC Unknown, and never the
	// ApplySchema-only FAILED_PRECONDITION that mapStoreError would assign to
	// ErrDatabaseNotReady.
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("expected HealthCheck probe failure to surface as Internal, got %v", got)
	}
}
