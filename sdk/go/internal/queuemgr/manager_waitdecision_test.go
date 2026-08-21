package queuemgr_test

// SEAM CONTRACT — PHASE_06 (SPEC R-4.4): event-driven WaitForDecision. These
// tests are RED until the production implementer adds the seam EXACTLY, then
// they pin the behaviour: WaitForDecision must GetItem first, then subscribe to
// the EventBus (channel "queue", event type "queue.decided") and return the
// choice carried by the matching workitem_id event; an absent item returns
// ErrQueueItemNotFound immediately WITHOUT subscribing; a cancelled ctx while
// subscribed returns ctx.Err(). The body must contain no time.Sleep/Ticker.
//
// Chosen seam — the smallest injectable EventBus dependency, mirroring the
// existing WithDialer testability style (WithDialer injects the queue-service
// dial function; WithEventBus injects the queue-service's EventBus client).
//
//  1. Manager field (with svc in manager.go):
//
//         // eventBus is the EventBus client WaitForDecision subscribes on.
//         // nil ⇒ WaitForDecision of a present item fails fast (see 3e).
//         eventBus flowv1.FlowEventBusServiceClient
//
//  2. Option (with the constructor Options in manager.go):
//
//         // WithEventBus injects the EventBus client used by WaitForDecision.
//         // It is the EventBus counterpart of WithDialer: production wiring
//         // (flowv1.NewFlowEventBusServiceClient over the Event Bus connection)
//         // and bufconn-backed tests share it.
//         func WithEventBus(eb flowv1.FlowEventBusServiceClient) Option
//
//  3. WaitForDecision body (replacing the client-local decCh read):
//     a. GetItem on the queue service FIRST. NotFound ⇒ return
//        ErrQueueItemNotFound immediately (absent == never enqueued OR already
//        decided+deleted); do NOT subscribe. Non-NotFound errors pass through.
//     b. Subscribe via the injected client with EXACTLY:
//            m.eventBus.Subscribe(ctx, &flowv1.SubscribeRequest{
//                Channel: "queue",
//                Filter:  &flowv1.SubscribeFilter{EventType: "queue.decided"},
//            })
//     c. Loop stream.Recv(). The FIRST event whose GetWorkitemId() ==
//        workitemID decides: return evt.GetAttributes()["choice"], nil.
//     d. Any Subscribe/Recv error while ctx.Err() != nil ⇒ return ctx.Err()
//        (the raw context error, NOT a gRPC-wrapped status error).
//     e. Subscribe error with a live ctx (or nil eventBus) ⇒ fail fast with an
//        error; never block, never panic.
//
// The unit tests never construct flowv1.PublishRequest and never import
// pkg/eventbus: the queue-service publish side is pinned by
// platform/queue/internal/service/publish_test.go; this file pins the SDK
// subscribe side. Both servers below are in-process bufconn — zero real I/O.

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/foundry/flow/sdk/go/internal/queuemgr"
)

// Fixed EventBus coordinates of the queue-service's decision notification
// (SPEC R-4.4; mirrored in platform/queue/internal/service/registry.go
// publishQueueDecided). The event carries workitem_id as the first-class
// FlowEvent.WorkitemId field and queue_name + choice as FlowEvent.Attributes
// entries — the wire shape these tests pin.
const (
	queueDecidedChannel   = "queue"
	queueDecidedEventType = "queue.decided"

	eventAttrQueueName = "queue_name"
	eventAttrChoice    = "choice"

	// testQueueName is the queue name every WaitForDecision test Manager is
	// configured with (WithQueueName).
	testQueueName = "hitl"
)

// queueDecidedEvent builds the wire shape the queue-service publishes for a
// decision: channel "queue", event type "queue.decided", workitem_id as the
// first-class FlowEvent.WorkitemId field, and queue_name + choice as
// FlowEvent.Attributes entries.
func queueDecidedEvent(workitemID, queueName, choice string) *flowv1.FlowEvent {
	return &flowv1.FlowEvent{
		EventId:    "evt-" + workitemID,
		Channel:    queueDecidedChannel,
		EventType:  queueDecidedEventType,
		WorkitemId: workitemID,
		Attributes: map[string]string{
			eventAttrQueueName: queueName,
			eventAttrChoice:    choice,
		},
	}
}

// queueDecidedEventBusSpy is a fake FlowEventBusService Server for
// WaitForDecision tests — the queuemgr analogue of the SDK's
// controllableEventBusSpy (sdk/go/childwatcher_test.go): events pushed to the
// events channel are sent on the subscribed stream; closing subscribed signals
// that the client's Subscribe round-trip reached the server. lastReq and the
// subscribe count are captured for assertions.
type queueDecidedEventBusSpy struct {
	flowv1.UnimplementedFlowEventBusServiceServer

	events     chan *flowv1.FlowEvent // test pushes events to deliver
	subscribed chan struct{}          // closed once Subscribe is entered
	done       chan struct{}          // closed to end the stream normally

	mu           sync.Mutex // guards lastReq and subscribeCnt
	lastReq      *flowv1.SubscribeRequest
	subscribeCnt int
}

func newQueueDecidedEventBusSpy(eventBuffer int) *queueDecidedEventBusSpy {
	return &queueDecidedEventBusSpy{
		events:     make(chan *flowv1.FlowEvent, eventBuffer),
		subscribed: make(chan struct{}),
		done:       make(chan struct{}),
	}
}

func (s *queueDecidedEventBusSpy) Subscribe(
	req *flowv1.SubscribeRequest,
	stream flowv1.FlowEventBusService_SubscribeServer,
) error {
	s.mu.Lock()
	s.lastReq = req
	s.subscribeCnt++
	s.mu.Unlock()
	close(s.subscribed)

	for {
		select {
		case evt, ok := <-s.events:
			if !ok {
				return nil // stream end -> client sees io.EOF
			}
			if err := stream.Send(evt); err != nil {
				return err
			}
		case <-s.done:
			return nil
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// lastSubscribeRequest returns the captured SubscribeRequest (nil if
// Subscribe was never called).
func (s *queueDecidedEventBusSpy) lastSubscribeRequest() *flowv1.SubscribeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastReq
}

// subscribeCount returns how many times Subscribe was called.
func (s *queueDecidedEventBusSpy) subscribeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subscribeCnt
}

// seedQueueItem enqueues an item directly on the fake queue service (in-process
// method call, zero I/O) so the queue-service's GetItem sees the workitem as
// present and WaitForDecision proceeds to subscribe.
func seedQueueItem(t *testing.T, fake *fakeQueueService, workitemID string) {
	t.Helper()
	if _, err := fake.Enqueue(context.Background(), &flowv1.EnqueueRequest{
		QueueName:  testQueueName,
		WorkitemId: workitemID,
	}); err != nil {
		t.Fatalf("seed Enqueue(%s): %v", workitemID, err)
	}
}

// bufconnConn starts a gRPC server on an in-process bufconn listener with the
// given registrar and returns a client connection to it. Zero real I/O.
func bufconnConn(t *testing.T, name string, register func(*grpc.Server)) grpc.ClientConnInterface {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	register(srv)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///"+name,
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(context.Background())
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial %s: %v", name, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// newWaitDecisionManager wires a fakeQueueService (bufconn) and a
// queueDecidedEventBusSpy (bufconn) behind the Manager's two injected seams:
// WithDialer for the queue service and WithEventBus for the Event Bus. opts are
// appended before the seams so test-supplied options (queue name) apply; the
// seams always win.
func newWaitDecisionManager(
	t *testing.T,
	spy *queueDecidedEventBusSpy,
	opts ...queuemgr.Option,
) (*queuemgr.Manager, *fakeQueueService) {
	t.Helper()
	fake := newFakeQueueService()
	queueConn := bufconnConn(t, "bufconn-queue", func(s *grpc.Server) {
		flowv1.RegisterQueueGatewayServiceServer(s, fake)
	})
	ebConn := bufconnConn(t, "bufconn-eventbus", func(s *grpc.Server) {
		flowv1.RegisterFlowEventBusServiceServer(s, spy)
	})

	m, err := queuemgr.NewManager(append(opts,
		queuemgr.WithDialer(func(_ context.Context, _ string) (grpc.ClientConnInterface, error) {
			return queueConn, nil
		}),
		queuemgr.WithEventBus(flowv1.NewFlowEventBusServiceClient(ebConn)),
	)...)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m, fake
}

// waitForDecisionResult carries the returning goroutine's values.
type waitForDecisionResult struct {
	choice string
	err    error
}

// ---------------------------------------------------------------------------
// Tests — event-driven WaitForDecision (SPEC R-4.4)
// ---------------------------------------------------------------------------

// A workitem that was never enqueued is absent from the queue-service's
// GetItem, so WaitForDecision must return ErrQueueItemNotFound immediately —
// the pre-subscription check short-circuits BEFORE any EventBus interaction.
func TestManager_WaitForDecision_UnknownID_ErrQueueItemNotFound(t *testing.T) {
	spy := newQueueDecidedEventBusSpy(1)
	m, _ := newWaitDecisionManager(t, spy)

	_, err := m.WaitForDecision(context.Background(), "never-enqueued")
	if !errors.Is(err, queuemgr.ErrQueueItemNotFound) {
		t.Fatalf("WaitForDecision err = %v, want ErrQueueItemNotFound", err)
	}
	if got := spy.subscribeCount(); got != 0 {
		t.Fatalf("Subscribe called %d times for a never-enqueued item, want 0 (pre-sub GetItem must short-circuit)", got)
	}
}

// The item is present, so WaitForDecision subscribes to the EventBus; the
// queue.decided event with the workitem's choice wakes it. This pins that the
// decision travels over the EVENT stream, not the in-process decCh channel.
func TestManager_WaitForDecision_ReturnsChoiceAfterDecide(t *testing.T) {
	spy := newQueueDecidedEventBusSpy(1)
	m, fake := newWaitDecisionManager(t, spy, queuemgr.WithQueueName(testQueueName))
	seedQueueItem(t, fake, "wi-wd") // present ⇒ WaitForDecision proceeds to subscribe

	// The decision lands on the EventBus: push the queue.decided event onto
	// the subscribed stream before waiting.
	spy.events <- queueDecidedEvent("wi-wd", testQueueName, choiceApprove)

	choice, err := m.WaitForDecision(context.Background(), "wi-wd")
	if err != nil {
		t.Fatalf("WaitForDecision: %v", err)
	}
	if choice != choiceApprove {
		t.Fatalf("choice = %q, want %q", choice, choiceApprove)
	}
}

// Cancelling the ctx while subscribed and waiting (the event never arrives)
// must make WaitForDecision return ctx.Err() without hanging: the choice is
// empty on cancellation.
func TestManager_WaitForDecision_ContextCancelled(t *testing.T) {
	spy := newQueueDecidedEventBusSpy(1)
	m, fake := newWaitDecisionManager(t, spy, queuemgr.WithQueueName(testQueueName))
	seedQueueItem(t, fake, "wi-pending") // present ⇒ WaitForDecision subscribes

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan waitForDecisionResult, 1)
	go func() {
		choice, err := m.WaitForDecision(ctx, "wi-pending")
		done <- waitForDecisionResult{choice: choice, err: err}
	}()

	// Wait for the subscription to reach the server, then cancel while the
	// stream is idle — the decision event never arrives.
	select {
	case <-spy.subscribed:
	case res := <-done:
		t.Fatalf("WaitForDecision returned before subscribing: %+v", res)
	}
	cancel()

	res := <-done // deterministic: gRPC Recv unblocks on ctx cancellation
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("WaitForDecision err = %v, want context.Canceled", res.err)
	}
	if res.choice != "" {
		t.Fatalf("choice = %q, want empty on cancellation", res.choice)
	}
}

// A queue.decided event for a DIFFERENT workitem arriving first must be
// ignored; only the event matching the requested workitem decides the wait.
func TestManager_WaitForDecision_FiltersToWorkitem(t *testing.T) {
	spy := newQueueDecidedEventBusSpy(2)
	m, fake := newWaitDecisionManager(t, spy, queuemgr.WithQueueName(testQueueName))
	seedQueueItem(t, fake, "wi-want")

	spy.events <- queueDecidedEvent("wi-other", "hitl-2", "reject")
	spy.events <- queueDecidedEvent("wi-want", testQueueName, choiceApprove)

	choice, err := m.WaitForDecision(context.Background(), "wi-want")
	if err != nil {
		t.Fatalf("WaitForDecision: %v", err)
	}
	if choice != choiceApprove {
		t.Fatalf("choice = %q, want %q (the other workitem's event must be filtered out)", choice, choiceApprove)
	}
}

// The Subscribe request sent to the EventBus must be exactly channel "queue"
// with an EventType filter of "queue.decided" (live-only, no replay).
func TestManager_WaitForDecision_SubscribeFilter(t *testing.T) {
	spy := newQueueDecidedEventBusSpy(1)
	m, fake := newWaitDecisionManager(t, spy, queuemgr.WithQueueName(testQueueName))
	seedQueueItem(t, fake, "wi-sub")

	spy.events <- queueDecidedEvent("wi-sub", testQueueName, choiceApprove)
	if _, err := m.WaitForDecision(context.Background(), "wi-sub"); err != nil {
		t.Fatalf("WaitForDecision: %v", err)
	}

	req := spy.lastSubscribeRequest()
	if req == nil {
		t.Fatal("Subscribe was never called on the EventBus")
	}
	if got := req.GetChannel(); got != queueDecidedChannel {
		t.Fatalf("Subscribe channel = %q, want %q", got, queueDecidedChannel)
	}
	if req.GetFilter() == nil {
		t.Fatal("Subscribe filter is nil, want an EventType filter")
	}
	if got := req.GetFilter().GetEventType(); got != queueDecidedEventType {
		t.Fatalf("Subscribe event_type filter = %q, want %q", got, queueDecidedEventType)
	}
	if got := req.GetLastSequence(); got != 0 {
		t.Fatalf("Subscribe last_sequence = %d, want 0 (live-only, no replay)", got)
	}
}

// A workitem that was ALREADY decided is absent from the queue-service's
// GetItem (the service deletes decided items; the fake mirrors this — the item
// is gone after Decide), so WaitForDecision must return immediately with
// ErrQueueItemNotFound instead of subscribing and waiting for an event that
// cannot come. Design decision: absent (never enqueued / already decided)
// collapses onto the existing ErrQueueItemNotFound sentinel — a separate
// "already decided" outcome would require a new production symbol, and the
// choice is unknowable on a live-only subscription.
func TestManager_WaitForDecision_ItemAlreadyDecided_ReturnsErrQueueItemNotFound(t *testing.T) {
	spy := newQueueDecidedEventBusSpy(1)
	m, fake := newWaitDecisionManager(t, spy, queuemgr.WithQueueName(testQueueName))
	ctx := context.Background()
	if err := m.Enqueue(ctx, "wi-decided"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := m.Claim(ctx, "wi-decided"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := m.Decide(ctx, "wi-decided", choiceApprove); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if fake.item("wi-decided") != nil {
		t.Fatal("decided item should be absent from the queue service")
	}

	_, err := m.WaitForDecision(ctx, "wi-decided")
	if !errors.Is(err, queuemgr.ErrQueueItemNotFound) {
		t.Fatalf("WaitForDecision err = %v, want ErrQueueItemNotFound (already decided ⇒ absent ⇒ no hang)", err)
	}
	if got := spy.subscribeCount(); got != 0 {
		t.Fatalf("Subscribe called %d times for an already-decided item, want 0 (pre-sub GetItem must short-circuit)", got)
	}
}
