package queue

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// ---------------------------------------------------------------------------
// Test infrastructure — bufconn-backed QueueRegistryService fake
// ---------------------------------------------------------------------------

// fakeRegistryServer records QueueRegistryService calls and can be switched to
// fail a configured number of times for failure-path tests.
type fakeRegistryServer struct {
	flowv1.UnimplementedQueueRegistryServiceServer

	mu        sync.Mutex
	registers []*flowv1.RegisterQueueRequest
	beats     []*flowv1.HeartbeatQueueRequest
	dereg     []*flowv1.DeregisterQueueRequest
	// failRegistrations / failBeats count remaining failures to return before
	// succeeding.
	failRegistrations int
	failBeats         int
}

func (f *fakeRegistryServer) RegisterQueue(
	_ context.Context, req *flowv1.RegisterQueueRequest,
) (*flowv1.RegisterQueueResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRegistrations > 0 {
		f.failRegistrations--
		return nil, status.Error(codes.Internal, "register failed")
	}
	f.registers = append(f.registers, req)
	return &flowv1.RegisterQueueResponse{Acknowledged: true}, nil
}

func (f *fakeRegistryServer) HeartbeatQueue(
	_ context.Context, req *flowv1.HeartbeatQueueRequest,
) (*flowv1.HeartbeatQueueResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failBeats > 0 {
		f.failBeats--
		return nil, status.Error(codes.Internal, "heartbeat failed")
	}
	f.beats = append(f.beats, req)
	return &flowv1.HeartbeatQueueResponse{Acknowledged: true}, nil
}

func (f *fakeRegistryServer) DeregisterQueue(
	_ context.Context, req *flowv1.DeregisterQueueRequest,
) (*flowv1.DeregisterQueueResponse, error) {
	f.mu.Lock()
	f.dereg = append(f.dereg, req)
	f.mu.Unlock()
	return &flowv1.DeregisterQueueResponse{Acknowledged: true}, nil
}

// registryFake bundles a fake server with a bufconn listener and a
// registryDialer closing over the listener.
type registryFake struct {
	server *fakeRegistryServer
	lis    *bufconn.Listener
	dialer registryDialer
	addr   string
}

// newFakeRegistry starts a bufconn QueueRegistryService server and returns a
// registryFake whose dialer connects to it.
func newFakeRegistry(t *testing.T) *registryFake {
	t.Helper()
	lis := bufconn.Listen(meshBufSize)
	srv := grpc.NewServer()
	s := &fakeRegistryServer{}
	flowv1.RegisterQueueRegistryServiceServer(srv, s)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	addr := "passthrough:///fake-registry"
	dialer := func(ctx context.Context, _ string) (*grpc.ClientConn, error) {
		return grpc.NewClient(addr,
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}
	return &registryFake{server: s, lis: lis, dialer: dialer, addr: addr}
}

func (f *registryFake) registerCalls() []*flowv1.RegisterQueueRequest {
	f.server.mu.Lock()
	defer f.server.mu.Unlock()
	return f.server.registers
}

func (f *registryFake) beatCalls() []*flowv1.HeartbeatQueueRequest {
	f.server.mu.Lock()
	defer f.server.mu.Unlock()
	return f.server.beats
}

func (f *registryFake) deregCalls() []*flowv1.DeregisterQueueRequest {
	f.server.mu.Lock()
	defer f.server.mu.Unlock()
	return f.server.dereg
}

// ---------------------------------------------------------------------------
// Tests — registration / heartbeat / deregistration lifecycle
// ---------------------------------------------------------------------------

// startConfiguredManager builds a Manager with the queue-service env var set
// BEFORE NewManager (the env var is read once at construction), wired to a
// fake registry, and starts it. Requires the I/O-crossing Start path (SQLite +
// HTTP listener), so it carries the -short guard per the repo contract.
func startConfiguredManager(t *testing.T, fake *registryFake) *Manager {
	t.Helper()
	t.Setenv("FLOW_QUEUE_SERVICE_ADDR", fake.addr)
	t.Setenv("FLOW_STORAGE_PATH", ":memory:")
	t.Setenv("FLOW_HITL_PORT", "0")

	qm, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	qm.registryDial = fake.dialer
	qm.heartbeatInterval = 50 * time.Millisecond

	t.Cleanup(func() { _ = qm.Stop() })
	return qm
}

func TestQueueManager_Start_Registers_WhenQueueServiceConfigured(t *testing.T) {
	if testing.Short() {
		t.Skip("opens SQLite store + HTTP listener; covered by make test")
	}
	fake := newFakeRegistry(t)
	qm := startConfiguredManager(t, fake)

	if err := qm.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	regs := fake.registerCalls()
	if len(regs) != 1 {
		t.Fatalf("expected exactly one RegisterQueue, got %d", len(regs))
	}
	reg := regs[0]
	if reg.GetQueueName() != qm.queueName {
		t.Errorf("queueName = %q, want %q", reg.GetQueueName(), qm.queueName)
	}
	if reg.GetShardId() != qm.shardID {
		t.Errorf("shardID = %q, want %q", reg.GetShardId(), qm.shardID)
	}
	// The registered shard addr must derive from the same shardID as the
	// registered identity — never from a second HOSTNAME read (LEARNINGS:
	// derive from the subject's own fields, not a hardcoded host:port).
	wantAddr := net.JoinHostPort(qm.shardID, defaultPeerPort)
	if got := reg.GetShardAddr(); got != wantAddr {
		t.Errorf("shardAddr = %q, want %q (net.JoinHostPort(qm.shardID, defaultPeerPort))", got, wantAddr)
	}
}

func TestQueueManager_Start_Standalone_WhenEnvUnset(t *testing.T) {
	if testing.Short() {
		t.Skip("opens SQLite store + HTTP listener; covered by make test")
	}
	t.Setenv("FLOW_STORAGE_PATH", ":memory:")
	t.Setenv("FLOW_HITL_PORT", "0")
	// Explicitly clear FLOW_QUEUE_SERVICE_ADDR so the standalone path is
	// deterministic and independent of the ambient environment (LEARNINGS).
	t.Setenv("FLOW_QUEUE_SERVICE_ADDR", "")

	qm, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if qm.registry != nil {
		t.Fatal("registry should be nil when FLOW_QUEUE_SERVICE_ADDR unset")
	}
	if err := qm.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if qm.registry != nil {
		t.Fatal("Start must not create a registry client when unset")
	}
	if err := qm.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestQueueManager_Heartbeat_TicksAtInterval(t *testing.T) {
	if testing.Short() {
		t.Skip("opens SQLite store + HTTP listener; covered by make test")
	}
	fake := newFakeRegistry(t)
	qm := startConfiguredManager(t, fake)

	if err := qm.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(fake.beatCalls()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(fake.beatCalls()) < 1 {
		t.Fatal("no heartbeat recorded within deadline")
	}

	if err := qm.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	// Capture AFTER Stop returns so an in-flight tick completing during Stop's
	// hbWG.Wait() is counted as pre-Stop, not miscounted as a post-Stop send
	// (LEARNINGS: never read a counter before the goroutine being asserted has
	// stopped). Then settle and assert no further beat lands.
	countAtStop := len(fake.beatCalls())
	time.Sleep(100 * time.Millisecond)
	if got := len(fake.beatCalls()); got != countAtStop {
		t.Fatalf("heartbeat landed after Stop returned: got %d calls, want %d", got, countAtStop)
	}
}

func TestQueueManager_HeartbeatFailure_WarnsAndRetries(t *testing.T) {
	if testing.Short() {
		t.Skip("opens SQLite store + HTTP listener; covered by make test")
	}
	fake := newFakeRegistry(t)
	fake.server.failBeats = 1
	qm := startConfiguredManager(t, fake)

	if err := qm.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// A later tick must succeed and the manager must survive the failed beat.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(fake.beatCalls()) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(fake.beatCalls()) < 2 {
		t.Fatalf("expected a successful heartbeat after the failed one, got %d calls", len(fake.beatCalls()))
	}
	if err := qm.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestQueueManager_Stop_Deregisters(t *testing.T) {
	if testing.Short() {
		t.Skip("opens SQLite store + HTTP listener; covered by make test")
	}
	fake := newFakeRegistry(t)
	qm := startConfiguredManager(t, fake)

	if err := qm.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := qm.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	dereg := fake.deregCalls()
	if len(dereg) != 1 {
		t.Fatalf("expected exactly one DeregisterQueue, got %d", len(dereg))
	}
	if dereg[0].GetQueueName() != qm.queueName || dereg[0].GetShardId() != qm.shardID {
		t.Errorf("DeregisterQueue recvd %+v, want queue=%q shard=%q", dereg[0], qm.queueName, qm.shardID)
	}
}

func TestQueueManager_RegistrationFailure_DoesNotFailStart(t *testing.T) {
	if testing.Short() {
		t.Skip("opens SQLite store + HTTP listener; covered by make test")
	}
	fake := newFakeRegistry(t)
	fake.server.failRegistrations = 1
	qm := startConfiguredManager(t, fake)

	// Registration fails but Start must still succeed (standalone parity).
	if err := qm.Start(context.Background()); err != nil {
		t.Fatalf("Start failed on registration error: %v", err)
	}
	if err := qm.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Pure heartbeatLoop unit tests (no SQLite/HTTP, no -short guard)
// ---------------------------------------------------------------------------

// newLoopClient builds a queueRegistryClient wired to a bufconn fake, so
// heartbeatLoop is exercised without opening a SQLite store or HTTP listener.
func newLoopClient(
	t *testing.T, fake *registryFake, interval time.Duration,
) (*sync.WaitGroup, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	reg, err := newQueueRegistryClient(fake.addr, fake.dialer, "shard-0", "hitl-approval", "shard-0:50053", interval)
	if err != nil {
		t.Fatalf("newQueueRegistryClient failed: %v", err)
	}
	t.Cleanup(func() { _ = reg.close() })
	var wg sync.WaitGroup
	wg.Add(1)
	go reg.heartbeatLoop(ctx, &wg)
	return &wg, cancel
}

func TestHeartbeatLoop_TicksAtInterval(t *testing.T) {
	fake := newFakeRegistry(t)
	wg, cancel := newLoopClient(t, fake, 20*time.Millisecond)
	defer wg.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(fake.beatCalls()) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(fake.beatCalls()); got < 2 {
		t.Fatalf("heartbeat did not tick at interval: got %d beats, want >= 2", got)
	}
	cancel()
	for _, b := range fake.beatCalls() {
		if b.GetShardId() != "shard-0" {
			t.Errorf("beat shard = %q, want shard-0", b.GetShardId())
		}
	}
}

func TestHeartbeatLoop_NoSendAfterCancel(t *testing.T) {
	fake := newFakeRegistry(t)
	wg, cancel := newLoopClient(t, fake, 10*time.Millisecond)

	// Wait for at least one beat so we know the loop is live.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(fake.beatCalls()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(fake.beatCalls()) < 1 {
		t.Fatal("no heartbeat recorded before cancel")
	}

	cancel()
	wg.Wait() // the loop must exit after cancel
	countAfterCancel := len(fake.beatCalls())
	time.Sleep(50 * time.Millisecond)
	if got := len(fake.beatCalls()); got != countAfterCancel {
		t.Fatalf("heartbeat sent after cancel: got %d beats, want %d", got, countAfterCancel)
	}
}

func TestHeartbeatLoop_FailureLogsAndRetries(t *testing.T) {
	fake := newFakeRegistry(t)
	fake.server.failBeats = 1
	wg, cancel := newLoopClient(t, fake, 20*time.Millisecond)
	defer wg.Wait()
	defer cancel()

	// The failed beat must not terminate the loop; the next tick succeeds.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(fake.beatCalls()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(fake.beatCalls()); got != 1 {
		t.Fatalf("expected exactly 1 successful beat after the failed one, got %d", got)
	}
}
