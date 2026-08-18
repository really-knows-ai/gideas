package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"sync"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/pkg/eventbus"
	"google.golang.org/grpc"
)

// Test constants for the git remote auth URL-scheme resolver tests. They are
// hoisted to constants to satisfy the goconst linter.
const (
	tSSHUser        = "git"
	tSecretUsername = "secret-user"
	tSecretPassword = "secret-pass"
)

type initPullGitStore struct {
	gitstore.GitStore
	isEmpty      bool
	cloneCalls   int
	cloneErr     error
	cloneCtx     context.Context // the context CloneSingleBranch was invoked with
	pushCalls    int
	pushErr      error
	initStateErr error
}

func (g *initPullGitStore) IsEmpty(context.Context) (bool, error) { return g.isEmpty, g.initStateErr }
func (g *initPullGitStore) WithGitLock(fn func() error) error     { return fn() }
func (g *initPullGitStore) CloneSingleBranch(ctx context.Context, _ string, _ string) error {
	g.cloneCalls++
	g.cloneCtx = ctx
	return g.cloneErr
}
func (g *initPullGitStore) PushRemote(context.Context) error {
	g.pushCalls++
	return g.pushErr
}

// scenarioGitStore tracks the sequence of remote operations performed under a
// given init scenario so tests can assert the exact call order.
type scenarioGitStore struct {
	initPullGitStore
	ops []string
}

func (g *scenarioGitStore) CloneSingleBranch(context.Context, string, string) error {
	g.cloneCalls++
	g.ops = append(g.ops, "clone")
	return g.cloneErr
}
func (g *scenarioGitStore) PushRemote(context.Context) error {
	g.pushCalls++
	g.ops = append(g.ops, "push")
	return g.pushErr
}

// ed25519PEM returns a PEM-encoded (PKCS8) ed25519 private key, matching the
// unencrypted PEM format SPEC R1 requires for the ssh-privatekey Secret key.
func ed25519PEM(t *testing.T) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	_ = pub
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal ed25519 private key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// telemetrySpy implements flowv1.FlowEventBusServiceClient, capturing every
// PublishRequest so tests can assert the telemetry events tryRemotePullOnInit
// submits on startup failures (SPEC R1/R10).
type telemetrySpy struct {
	flowv1.FlowEventBusServiceClient

	mu    sync.Mutex
	calls []*flowv1.PublishRequest
}

func (s *telemetrySpy) Publish(
	_ context.Context, req *flowv1.PublishRequest, _ ...grpc.CallOption,
) (*flowv1.PublishResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, req)
	return &flowv1.PublishResponse{Acknowledged: true}, nil
}

func (s *telemetrySpy) getCalls() []*flowv1.PublishRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*flowv1.PublishRequest, len(s.calls))
	copy(out, s.calls)
	return out
}

// newTestAuditPub builds a real AsyncPublisher over a telemetrySpy so
// tryRemotePullOnInit's telemetry-publish branches are exercised end to end.
// The publisher drains asynchronously, so callers poll waitForTelemetry.
func newTestAuditPub(t *testing.T) (*telemetrySpy, *eventbus.AsyncPublisher) {
	t.Helper()
	spy := &telemetrySpy{}
	pub := eventbus.NewAsyncPublisher(spy, eventbus.WithBufferSize(10))
	t.Cleanup(pub.Stop)
	return spy, pub
}

// waitForTelemetry polls the spy until a PublishRequest with the given event
// type is published, then returns it.
func waitForTelemetry(t *testing.T, spy *telemetrySpy, eventType string) *flowv1.PublishRequest {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, req := range spy.getCalls() {
			if req.GetEvent().GetEventType() == eventType {
				return req
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no telemetry event %q published within deadline; got %d publish calls",
		eventType, len(spy.getCalls()))
	return nil
}
