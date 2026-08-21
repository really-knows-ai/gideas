package service

// PHASE_06 integration test 4 (R-4.4/R-4.5): END-TO-END EVENT-DRIVEN WAIT.
//
// A decide lands via the queue-service -> the queue-service publishes
// "queue.decided" on the EventBus -> a waiting node's WaitForDecision wakes on
// the event, sub-second, with NO polling.
//
// This composes REAL components across REAL gRPC (bufconn) I/O boundaries:
//   - an in-memory EventBus gRPC server (flowv1.FlowEventBusServiceServer) with
//     a topic store + per-subscriber fan-out — a faithful stand-in for the real
//     Event Bus service, so the Publish/Subscribe contract is exercised on the
//     wire, not stubbed;
//   - the real queue-service stack: a Registry + GatewayServer over fake
//     mirror shards (exactly the real funnel the other integration tests use),
//     served over bufconn so a real gRPC client can reach it;
//   - the real production publish path minus the network: the Registry's
//     PublishQueueDecided hook drives a real pkg/eventbus.AsyncPublisher whose
//     client dials the in-memory EventBus over bufconn;
//   - a "waiting node": a thin in-package gRPC client to the queue-service
//     Gateway plus a direct FlowEventBusServiceClient Subscribe loop, mirroring
//     the SDK queuemgr.Manager.WaitForDecision shape (GetItem-first; subscribe
//     channel "queue"/"queue.decided"; Recv-loop filtered by workitem_id;
//     return Attributes["choice"]). The queue module does not depend on the SDK
//     module, so this side is composed in-package rather than importing
//     sdk/go.
//
// The test asserts WaitForDecision returns the choice WITHOUT any poll loop,
// bounded by a 5s context timeout (a broken path fails fast) — a working path
// wakes in milliseconds.
//
// -short-guarded (real-I/O queue mesh integration), so it only runs under the
// dedicated integration run (no -short), consistent with
// mirror_integration_test.go.

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/pkg/eventbus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// ---------------------------------------------------------------------------
// In-memory EventBus gRPC server
// ---------------------------------------------------------------------------

// memSubscription is one live subscriber's delivery channel plus the filter it
// asked for. New events are fanned out to every subscriber whose channel
// matches; the Subscribe handler replays stored events, then delivers from
// this channel.
type memSubscription struct {
	channel string
	filter  *flowv1.SubscribeFilter
	ch      chan *flowv1.FlowEvent
}

// memEventBus is a test-local, in-memory FlowEventBusServiceServer: Publish
// stores the event and fans it out to live subscribers whose channel matches;
// Subscribe replays stored events matching the channel+filter, then streams new
// ones. It is a faithful stand-in for the real Event Bus service so the
// Publish/Subscribe contract is exercised across the real gRPC wire.
type memEventBus struct {
	flowv1.UnimplementedFlowEventBusServiceServer

	mu     sync.Mutex
	seq    uint64
	events []*flowv1.FlowEvent // stored per-publish, in order
	subs   []*memSubscription
}

// matches reports whether the event satisfies the subscription's channel and
// optional event_type filter.
func (m *memEventBus) matches(s *memSubscription, evt *flowv1.FlowEvent) bool {
	if evt.GetChannel() != s.channel {
		return false
	}
	if s.filter != nil && s.filter.GetEventType() != "" &&
		s.filter.GetEventType() != evt.GetEventType() {
		return false
	}
	return true
}

// Publish stores the event and fans it out to live subscribers whose channel
// matches. Deliveries are best-effort (a full per-subscriber buffer drops the
// event for that subscriber — the real bus persists before fan-out, so a
// missing delivery is a bus-downtime concern, not a Publish failure).
func (m *memEventBus) Publish(_ context.Context, req *flowv1.PublishRequest) (*flowv1.PublishResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.seq++
	evt := req.GetEvent()
	// The bus assigns the channel (either carried on the event, or taken from
	// the request) — production publishers put it on PublishRequest; the real
	// bus tags stored events with it. Normalize so fan-out and subscriber
	// filtering see the channel the subscriber subscribed to.
	if evt.GetChannel() == "" {
		evt.Channel = req.GetChannel()
	}
	evt.Sequence = m.seq
	m.events = append(m.events, evt)

	for _, s := range m.subs {
		if s.channel == evt.GetChannel() {
			select {
			case s.ch <- evt:
			default: // subscriber too slow — drop for this subscriber
			}
		}
	}
	return &flowv1.PublishResponse{Acknowledged: true, Sequence: m.seq}, nil
}

// Subscribe opens a server stream: it registers a subscriber, replays stored
// events matching the channel+filter, then delivers new ones arriving on the
// subscriber's channel until the client closes the stream.
func (m *memEventBus) Subscribe(
	req *flowv1.SubscribeRequest,
	stream grpc.ServerStreamingServer[flowv1.FlowEvent],
) error {
	sub := &memSubscription{
		channel: req.GetChannel(),
		filter:  req.GetFilter(),
		ch:      make(chan *flowv1.FlowEvent, 256),
	}

	m.mu.Lock()
	m.subs = append(m.subs, sub)
	// Snapshot the stored events under the lock so replay is ordered and
	// consistent with respect to concurrent publishes.
	var replay []*flowv1.FlowEvent
	for _, evt := range m.events {
		if m.matches(sub, evt) {
			replay = append(replay, evt)
		}
	}
	m.mu.Unlock()

	// Replay first, then switch to live delivery.
	for _, evt := range replay {
		if err := stream.Send(evt); err != nil {
			return err
		}
	}
	for {
		select {
		case evt := <-sub.ch:
			if !m.matches(sub, evt) {
				continue
			}
			if err := stream.Send(evt); err != nil {
				return err
			}
		case <-stream.Context().Done():
			m.mu.Lock()
			m.removeSub(sub)
			m.mu.Unlock()
			return stream.Context().Err()
		}
	}
}

func (m *memEventBus) removeSub(sub *memSubscription) {
	for i, s := range m.subs {
		if s == sub {
			m.subs = append(m.subs[:i], m.subs[i+1:]...)
			return
		}
	}
}

// serve starts the in-memory EventBus on a bufconn listener and returns the
// listener (for dialing) plus a cleanup that stops the server.
func (m *memEventBus) serve(t *testing.T) *bufconn.Listener {
	t.Helper()
	lis := bufconn.Listen(meshBufSize)
	srv := grpc.NewServer()
	flowv1.RegisterFlowEventBusServiceServer(srv, m)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)
	return lis
}

// ---------------------------------------------------------------------------
// bufconn client-dial helpers
// ---------------------------------------------------------------------------

// dialBufconnConn returns a grpc.ClientConn backed by the given bufconn
// listener (zero real network), with a cleanup that closes it.
func dialBufconnConn(t *testing.T, lis *bufconn.Listener) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return cc
}

// ---------------------------------------------------------------------------
// The waiting side (in-package mirror of the SDK queuemgr.WaitForDecision)
// ---------------------------------------------------------------------------

// waitForDecision mirrors the SDK Manager.WaitForDecision event-driven shape:
// GetItem-first (absent => not found, never subscribe), then subscribes
// channel "queue"/"queue.decided" on the EventBus and Recv-loops filtering by
// workitem_id, returning Attributes["choice"]. It wakes on the event — no
// polling.
func waitForDecision(
	ctx context.Context,
	gw flowv1.QueueGatewayServiceClient,
	eb flowv1.FlowEventBusServiceClient,
	workitemID string,
) (string, error) {
	if _, err := gw.GetItem(ctx, &flowv1.GetItemRequest{
		QueueName: testQueueName, WorkitemId: workitemID,
	}); err != nil {
		return "", err
	}

	stream, err := eb.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: "queue",
		Filter:  &flowv1.SubscribeFilter{EventType: "queue.decided"},
	})
	if err != nil {
		return "", err
	}
	for {
		evt, err := stream.Recv()
		if err != nil {
			return "", err
		}
		if evt.GetWorkitemId() != workitemID {
			continue
		}
		return evt.GetAttributes()["choice"], nil
	}
}

// TestIntegration_EventDrivenWaitWakesOnDecide pins R-4.4/R-4.5 end-to-end: a
// decide lands via the queue-service Gateway, the queue-service publishes
// queue.decided on the EventBus (real AsyncPublisher -> in-memory EventBus),
// and the waiting client's WaitForDecision wakes on the event in milliseconds —
// NO polling. It also covers the already-decided-before-subscribe race: a
// decided item is absent, so WaitForDecision returns not-found immediately
// (never subscribes, never hangs).
func TestIntegration_EventDrivenWaitWakesOnDecide(t *testing.T) {
	if testing.Short() {
		t.Skip("real-I/O queue mesh integration")
	}
	ctx := context.Background()

	// ---- Composition over bufconn (no real network) ----

	// 1. In-memory EventBus server on its own bufconn listener.
	bus := &memEventBus{}
	busLis := bus.serve(t)

	// 2. The queue-service stack: real Registry + GatewayServer over fake
	//    mirror shards (the same funnel the other integration tests use),
	//    served over a bufconn listener so a real gRPC client can reach it.
	h := newFunnelHarness(t, "shard-a", "shard-b", "shard-c")
	gwLis := bufconn.Listen(meshBufSize)
	gwSrv := grpc.NewServer()
	flowv1.RegisterQueueGatewayServiceServer(gwSrv, h.gateway)
	go func() { _ = gwSrv.Serve(gwLis) }()
	t.Cleanup(gwSrv.GracefulStop)

	// 3. The REAL production publish path minus the network: a real
	//    AsyncPublisher whose FlowEventBusServiceClient dials the in-memory
	//    EventBus over bufconn. The Registry's PublishQueueDecided hook fires
	//    it after each quorum-acked decide.
	busConn := dialBufconnConn(t, busLis)
	pub := eventbus.NewAsyncPublisher(flowv1.NewFlowEventBusServiceClient(busConn))
	t.Cleanup(pub.Stop)
	h.reg.PublishQueueDecided = func(queueName, workitemID, choice string) {
		pub.Submit(&flowv1.PublishRequest{
			Channel: "queue",
			Event: &flowv1.FlowEvent{
				EventType:  "queue.decided",
				WorkitemId: workitemID,
				Attributes: map[string]string{"queue_name": queueName, "choice": choice},
			},
		})
	}

	// 4. The "waiting node": a real gRPC client to the Gateway + a direct
	//    FlowEventBusServiceClient Subscribe loop (in-package mirror of the
	//    SDK).

	gwClient := flowv1.NewQueueGatewayServiceClient(dialBufconnConn(t, gwLis))
	ebClient := flowv1.NewFlowEventBusServiceClient(dialBufconnConn(t, busLis))

	// ---- Scenario (sub-second, no polling) ----

	// Enqueue the workitem via the queue-service Gateway (real funnel fan-out).
	if _, err := gwClient.Enqueue(ctx, &flowv1.EnqueueRequest{
		QueueName: testQueueName, WorkitemId: testWorkitemID, Choices: []string{testChoiceApprove, "reject"},
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// Claim it (waiting -> claimed on every shard).
	if _, err := gwClient.Claim(ctx, &flowv1.ClaimRequest{
		QueueName: testQueueName, WorkitemId: testWorkitemID,
	}); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	// A waiting node blocks on WaitForDecision: it GetItem's (item present),
	// subscribes to the EventBus, and waits on the event. 5s outer bound is a
	// safety net, not a wait — a working path wakes in milliseconds.
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	type waitResult struct {
		choice string
		err    error
	}
	resCh := make(chan waitResult, 1)
	go func() {
		choice, err := waitForDecision(waitCtx, gwClient, ebClient, testWorkitemID)
		resCh <- waitResult{choice: choice, err: err}
	}()

	// Give the subscriber a moment to get live BEFORE the decide so the
	// scenario exercises the live fan-out path (the production shape: a
	// waiting node is already subscribed when the decide lands). This is not a
	// poll for the decision — the outcome is awaited event-driven on the
	// result channel, bounded by the 5s context. (Even if the subscriber were
	// late, the bus replays stored events on Subscribe, so the test cannot
	// flake on registration timing.)
	select {
	case <-time.After(200 * time.Millisecond):
		// subscriber is presumably registered; proceed to decide
	case <-resCh:
		t.Fatal("WaitForDecision returned before any decide was issued")
	}

	// Decide via the queue-service: quorum-acks, then fires publishQueueDecided
	// -> AsyncPublisher.Submit -> in-memory EventBus -> waiting client wakes.
	if _, err := gwClient.Decide(ctx, &flowv1.DecideRequest{
		QueueName: testQueueName, WorkitemId: testWorkitemID, Choice: testChoiceApprove,
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("WaitForDecision error: %v", res.err)
		}
		if res.choice != testChoiceApprove {
			t.Fatalf("WaitForDecision choice = %q, want %q", res.choice, testChoiceApprove)
		}
	case <-waitCtx.Done():
		t.Fatalf("WaitForDecision did not wake on queue.decided within the bound: %v", waitCtx.Err())
	}

	// No further polling: the event woke the waiter directly.

	// ---- Already-decided-before-subscribe race ----
	// Decide first (item removed from every shard), then WaitForDecision:
	// GetItem-first sees it absent and returns not-found immediately, never
	// subscribing — no hang.
	if _, err := gwClient.Enqueue(ctx, &flowv1.EnqueueRequest{
		QueueName: testQueueName, WorkitemId: testWorkitemID, Choices: []string{testChoiceApprove},
	}); err != nil {
		t.Fatalf("Enqueue (race case): %v", err)
	}
	if _, err := gwClient.Claim(ctx, &flowv1.ClaimRequest{
		QueueName: testQueueName, WorkitemId: testWorkitemID,
	}); err != nil {
		t.Fatalf("Claim (race case): %v", err)
	}
	if _, err := gwClient.Decide(ctx, &flowv1.DecideRequest{
		QueueName: testQueueName, WorkitemId: testWorkitemID, Choice: testChoiceApprove,
	}); err != nil {
		t.Fatalf("Decide (race case): %v", err)
	}
	raceCtx, raceCancel := context.WithTimeout(ctx, 2*time.Second)
	defer raceCancel()
	_, err := waitForDecision(raceCtx, gwClient, ebClient, testWorkitemID)
	if err == nil {
		t.Fatal("WaitForDecision on an already-decided (absent) item must fail, got success")
	}
}
