package main

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flow "github.com/foundry/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

// newSpyGRPCServer creates a gRPC server with the hitlSpy registered
// for all five Foundry Flow service interfaces.
func newSpyGRPCServer(spy *hitlSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// hitlSpy captures calls to service operations for test assertions.
type hitlSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// Operation records.
	TopologyCalls    int                       // GetFlowTopology calls
	ReadArtefacts    []string                  // artefact IDs read via GetArtefact
	StampedArtefacts []stampRecord             // stamps applied
	RoutedOutputs    []string                  // output names routed to
	CompletedReasons []flowv1.CompletionReason // reasons from Complete actions
	PauseTimerCalls  int                       // PauseTimer calls
	ResumeTimerCalls int                       // ResumeTimer calls

	// Configurable responses.
	ArtefactContents map[string]string               // artefact ID -> content
	Topology         *flowv1.GetFlowTopologyResponse // returned by GetFlowTopology
}

type stampRecord struct {
	ArtefactID string
	StampName  string
}

// ---------------------------------------------------------------------------
// Factory functions — one per CRD instance pattern
// ---------------------------------------------------------------------------

// newHITLAppraiseSpy returns a spy configured like the hitl-appraise CRD
// instance: single "approved" output, WRITE:feedback, READ:artefact/petition,
// STAMP:artefact/petition/reviewed, exit-bound.
func newHITLAppraiseSpy() *hitlSpy {
	return &hitlSpy{
		ArtefactContents: map[string]string{
			"petition": "test-petition-content",
		},
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name: "hitl-appraise",
				Capabilities: []string{
					"READ:flow",
					"WRITE:feedback/new",
					"READ:artefact/petition",
					"STAMP:artefact/petition/reviewed",
				},
				Outputs: []*flowv1.FlowOutput{
					{Name: "approved", Target: "clerk-done-router"},
				},
			},
			ExitContract: map[string]*flowv1.StampRequirements{
				"petition": {Stamps: []string{"reviewed"}},
			},
		},
	}
}

// newArbiterHITLResolveSpy returns a spy configured like the
// arbiter-hitl-resolve CRD instance: single "resolution" output,
// READ:artefact/evidence-bundle, no feedback, exit-bound.
func newArbiterHITLResolveSpy() *hitlSpy {
	return &hitlSpy{
		ArtefactContents: map[string]string{
			"evidence-bundle": "test-evidence-bundle",
		},
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name: "arbiter-hitl-resolve",
				Capabilities: []string{
					"READ:flow",
					"READ:artefact/evidence-bundle",
				},
				Outputs: []*flowv1.FlowOutput{
					{Name: "resolution", Target: "arbiter"},
				},
			},
			ExitContract: map[string]*flowv1.StampRequirements{
				"evidence-bundle": {Stamps: []string{}},
			},
		},
	}
}

// newTribunalHITLResolveSpy returns a spy configured like the
// tribunal-hitl-resolve CRD instance: single "resolution" output,
// READ:artefact/evidence-bundle, no feedback, exit-bound.
func newTribunalHITLResolveSpy() *hitlSpy {
	return &hitlSpy{
		ArtefactContents: map[string]string{
			"evidence-bundle": "test-evidence-bundle",
		},
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name: "tribunal-hitl-resolve",
				Capabilities: []string{
					"READ:flow",
					"READ:artefact/evidence-bundle",
				},
				Outputs: []*flowv1.FlowOutput{
					{Name: "resolution", Target: "tribunal"},
				},
			},
			ExitContract: map[string]*flowv1.StampRequirements{
				"evidence-bundle": {Stamps: []string{}},
			},
		},
	}
}

// newMinimalSpy returns a spy with a single output, no stamps, no feedback,
// and no exit contract. Useful for testing the simplest HITL behaviour.
func newMinimalSpy() *hitlSpy {
	return &hitlSpy{
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name:         "hitl-minimal",
				Capabilities: []string{"READ:flow"},
				Outputs: []*flowv1.FlowOutput{
					{Name: "default", Target: "next-node"},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *hitlSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *hitlSpy) PauseTimer(
	_ context.Context, _ *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PauseTimerCalls++
	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

func (s *hitlSpy) ResumeTimer(
	_ context.Context, _ *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResumeTimerCalls++
	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

func (s *hitlSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch a := req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Route:
		if a.Route != nil {
			s.RoutedOutputs = append(s.RoutedOutputs, a.Route.GetTarget())
		}
	case *flowv1.SubmitResultRequest_Complete:
		reason := flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED
		if a.Complete != nil {
			reason = a.Complete.GetReason()
		}
		s.CompletedReasons = append(s.CompletedReasons, reason)
	default:
		// Suspend / nil — nothing to record.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

// ---------------------------------------------------------------------------
// Operator methods
// ---------------------------------------------------------------------------

func (s *hitlSpy) GetFlowTopology(
	_ context.Context, _ *flowv1.GetFlowTopologyRequest,
) (*flowv1.GetFlowTopologyResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TopologyCalls++
	return s.Topology, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *hitlSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ReadArtefacts = append(s.ReadArtefacts, req.GetArtefactId())

	content := "test-content"
	if s.ArtefactContents != nil {
		if c, ok := s.ArtefactContents[req.GetArtefactId()]; ok {
			content = c
		}
	}
	return &flowv1.GetArtefactResponse{
		Content:     []byte(content),
		VersionHash: "test-hash",
	}, nil
}

func (s *hitlSpy) StampArtefact(
	_ context.Context, req *flowv1.StampArtefactRequest,
) (*flowv1.StampArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StampedArtefacts = append(s.StampedArtefacts, stampRecord{
		ArtefactID: req.GetArtefactId(),
		StampName:  req.GetStampName(),
	})
	return &flowv1.StampArtefactResponse{Stamp: &flowv1.Stamp{Name: req.GetStampName()}}, nil
}

// ---------------------------------------------------------------------------
// FrictionLedger methods
// ---------------------------------------------------------------------------

func (s *hitlSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newSpyClient creates a flow.Client backed by a local gRPC server with
// the hitlSpy registered for all five service interfaces.
func newSpyClient(t *testing.T, spy *hitlSpy) *flow.Client {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := newSpyGRPCServer(spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	client, err := flow.NewClient(flow.WithSidecarAddress(lis.Addr().String()))
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// ---------------------------------------------------------------------------
// Fake queue-service (flowv1.QueueGatewayServiceServer)
// ---------------------------------------------------------------------------

// fakeQueueService is an in-memory, in-process implementation of the
// queue-service's SDK-facing surface (flowv1.QueueGatewayServiceServer). It is
// a true unit-test fake: no real I/O, no database. Items are stored in a
// mutex-guarded map keyed by workitem_id, and the RPCs return the PHASE_01
// gRPC status codes the thin client's reverse-mapping expects
// (NotFound/AlreadyExists/FailedPrecondition). Decisions are delivered over
// the EventBus exactly like the real queue-service: Decide deletes the item
// (delete-on-decide) and publishes the queue.decided event to the harness's
// in-memory bus, which is what the SDK's event-driven WaitForDecision
// subscribes on.
type fakeQueueService struct {
	flowv1.UnimplementedQueueGatewayServiceServer

	mu    sync.Mutex
	items map[string]*flowv1.QueueItem

	// eventBus is the in-memory bus the fake publishes queue.decided events
	// to when a workitem is decided. Wired by newTestQueueManager.
	eventBus *inMemoryEventBus
}

const (
	queueStatusWaiting = "waiting"
	queueStatusClaimed = "claimed"
)

// EventBus coordinates of the queue-service's decision notification (SPEC
// R-4.4; mirrored by the SDK's manager_waitdecision_test.go and
// platform/queue/internal/service/registry.go publishQueueDecided). The event
// carries workitem_id as the first-class FlowEvent.WorkitemId field and
// queue_name + choice as FlowEvent.Attributes entries.
const (
	queueDecidedChannel   = "queue"
	queueDecidedEventType = "queue.decided"
	eventAttrQueueName    = "queue_name"
	eventAttrChoice       = "choice"

	// testQueueName scopes every queue request in the harness (WithQueueName).
	testQueueName = "hitl"
)

func newFakeQueueService() *fakeQueueService {
	return &fakeQueueService{items: map[string]*flowv1.QueueItem{}}
}

func (f *fakeQueueService) Enqueue(_ context.Context, req *flowv1.EnqueueRequest) (*flowv1.EnqueueResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[req.GetWorkitemId()] = &flowv1.QueueItem{
		WorkitemId: req.GetWorkitemId(),
		QueueName:  req.GetQueueName(),
		Status:     queueStatusWaiting,
		Choices:    append([]string(nil), req.GetChoices()...),
	}
	return &flowv1.EnqueueResponse{Acknowledged: true}, nil
}

func (f *fakeQueueService) GetGlobalQueue(
	_ context.Context, _ *flowv1.GetGlobalQueueRequest,
) (*flowv1.GetGlobalQueueResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*flowv1.QueueItem, 0, len(f.items))
	for _, it := range f.items {
		cp := proto.Clone(it).(*flowv1.QueueItem)
		out = append(out, cp)
	}
	return &flowv1.GetGlobalQueueResponse{Items: out}, nil
}

func (f *fakeQueueService) GetItem(_ context.Context, req *flowv1.GetItemRequest) (*flowv1.GetItemResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[req.GetWorkitemId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "queue item not found")
	}
	cp := proto.Clone(it).(*flowv1.QueueItem)
	return &flowv1.GetItemResponse{Item: cp}, nil
}

func (f *fakeQueueService) Claim(_ context.Context, req *flowv1.ClaimRequest) (*flowv1.ClaimResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[req.GetWorkitemId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "queue item not found")
	}
	if it.GetStatus() == queueStatusClaimed {
		return nil, status.Error(codes.AlreadyExists, "queue item already claimed")
	}
	it.Status = queueStatusClaimed
	cp := proto.Clone(it).(*flowv1.QueueItem)
	return &flowv1.ClaimResponse{Item: cp}, nil
}

func (f *fakeQueueService) Release(_ context.Context, req *flowv1.ReleaseRequest) (*flowv1.ReleaseResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[req.GetWorkitemId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "queue item not found")
	}
	if it.GetStatus() != queueStatusClaimed {
		return nil, status.Error(codes.FailedPrecondition, "queue item invalid state transition")
	}
	it.Status = queueStatusWaiting
	cp := proto.Clone(it).(*flowv1.QueueItem)
	return &flowv1.ReleaseResponse{Item: cp}, nil
}

func (f *fakeQueueService) Decide(ctx context.Context, req *flowv1.DecideRequest) (*flowv1.DecideResponse, error) {
	f.mu.Lock()
	it, ok := f.items[req.GetWorkitemId()]
	if !ok {
		f.mu.Unlock()
		return nil, status.Error(codes.NotFound, "queue item not found")
	}
	if it.GetStatus() != queueStatusClaimed {
		f.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "queue item invalid state transition")
	}
	queueName := it.GetQueueName()
	delete(f.items, req.GetWorkitemId()) // delete-on-decide: decided items no longer GetItem-able
	f.mu.Unlock()

	// Deliver the decision over the EventBus exactly like the real
	// queue-service: the node's WaitForDecision (already subscribed — the
	// harness waits for the subscription before deciding) receives the choice
	// from this event's "choice" attribute.
	if f.eventBus != nil {
		_, _ = f.eventBus.Publish(ctx, &flowv1.PublishRequest{
			Channel: queueDecidedChannel,
			Event: &flowv1.FlowEvent{
				EventId:    "evt-" + req.GetWorkitemId(),
				Channel:    queueDecidedChannel,
				EventType:  queueDecidedEventType,
				WorkitemId: req.GetWorkitemId(),
				Attributes: map[string]string{
					eventAttrQueueName: queueName,
					eventAttrChoice:    req.GetChoice(),
				},
			},
		})
	}
	return &flowv1.DecideResponse{Acknowledged: true}, nil
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

// inMemoryEventBus is the node-harness analogue of the SDK's
// queueDecidedEventBusSpy (sdk/go/internal/queuemgr/manager_waitdecision_test.go):
// a fake FlowEventBusServiceServer reachable over a bufconn listener. Publish
// appends the event to a replay buffer (so a decision published before the
// node subscribes is still delivered — the harness must not lose the decision
// to a subscribe/publish race) and fans it out to live subscribers. subc
// signals when the first Subscribe reaches the server, so tests can wait until
// the node's WaitForDecision has subscribed before deciding — at that point
// its pre-subscription GetItem has already succeeded while the item was
// pending, so the subsequent delete-on-decide cannot race it.
type inMemoryEventBus struct {
	flowv1.UnimplementedFlowEventBusServiceServer

	mu        sync.Mutex
	events    []*flowv1.FlowEvent            // published-event replay buffer
	subs      map[int]chan *flowv1.FlowEvent // live subscriber send channels
	nextSubID int
	subc      chan struct{} // subscription signal (buffered 1)
}

func newInMemoryEventBus() *inMemoryEventBus {
	return &inMemoryEventBus{
		subs: map[int]chan *flowv1.FlowEvent{},
		subc: make(chan struct{}, 1),
	}
}

func (b *inMemoryEventBus) Publish(_ context.Context, req *flowv1.PublishRequest) (*flowv1.PublishResponse, error) {
	evt := req.GetEvent()
	if evt == nil {
		return &flowv1.PublishResponse{Acknowledged: true}, nil
	}
	b.mu.Lock()
	b.events = append(b.events, evt)
	seq := uint64(len(b.events))
	for _, ch := range b.subs {
		select {
		case ch <- evt:
		default: // subscriber buffer full — the replay buffer backstops the drop
		}
	}
	b.mu.Unlock()
	return &flowv1.PublishResponse{Acknowledged: true, Sequence: seq}, nil
}

func (b *inMemoryEventBus) Subscribe(
	_ *flowv1.SubscribeRequest,
	stream flowv1.FlowEventBusService_SubscribeServer,
) error {
	b.mu.Lock()
	replay := append([]*flowv1.FlowEvent(nil), b.events...)
	ch := make(chan *flowv1.FlowEvent, 8)
	id := b.nextSubID
	b.nextSubID++
	b.subs[id] = ch
	select {
	case b.subc <- struct{}{}:
	default: // subscription already signalled
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}()

	for _, evt := range replay {
		if err := stream.Send(evt); err != nil {
			return err
		}
	}
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(evt); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// waitForSubscribe blocks until the first Subscribe reaches the bus (bounded:
// in-process delivery fires within microseconds, so 2s is a generous hard cap
// that only trips on a genuinely broken harness). Test-side synchronization
// only — the no-poll rule applies to the SDK WaitForDecision body, not here.
func (b *inMemoryEventBus) waitForSubscribe(t *testing.T) {
	t.Helper()
	select {
	case <-b.subc:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the node's WaitForDecision to subscribe")
	}
}

// testQueueManager bundles the SDK QueueManager under test with the harness's
// in-memory EventBus. Pass qm to runHandler; call decide to deliver a human
// decision.
type testQueueManager struct {
	qm  flow.QueueManager
	bus *inMemoryEventBus
}

// newTestQueueManager wires the SDK QueueManager's two injected seams the way
// production intends (and the SDK's own manager_waitdecision_test.go does):
// WithDialer reaches the fake queue-service over bufconn, and WithEventBus
// reaches the in-memory bus over bufconn. The fake queue-service's Decide both
// deletes the item (delete-on-decide) and publishes the queue.decided event, so
// WaitForDecision sees the item pending in GetItem, subscribes, and receives the
// decision over the event stream. No real network I/O, no FLOW_QUEUE_SERVICE_ADDR,
// no event-bus address.
func newTestQueueManager(t *testing.T) *testQueueManager {
	t.Helper()

	bus := newInMemoryEventBus()
	fake := newFakeQueueService()
	fake.eventBus = bus

	queueConn := bufconnConn(t, "bufconn-queue", func(s *grpc.Server) {
		flowv1.RegisterQueueGatewayServiceServer(s, fake)
	})
	ebConn := bufconnConn(t, "bufconn-eventbus", func(s *grpc.Server) {
		flowv1.RegisterFlowEventBusServiceServer(s, bus)
	})

	qm, err := flow.NewQueueManager(
		flow.WithQueueName(testQueueName),
		flow.WithDialer(func(context.Context, string) (grpc.ClientConnInterface, error) {
			return queueConn, nil
		}),
		flow.WithEventBus(flowv1.NewFlowEventBusServiceClient(ebConn)),
	)
	if err != nil {
		t.Fatalf("NewQueueManager failed: %v", err)
	}

	return &testQueueManager{qm: qm, bus: bus}
}

// decide waits until the node's WaitForDecision has subscribed (its
// pre-subscription GetItem has already succeeded while the item was pending),
// then claims and decides the workitem. The fake queue-service's Decide both
// deletes the item and publishes the queue.decided event, which unblocks the
// handler's WaitForDecision with the choice.
func (h *testQueueManager) decide(t *testing.T, ctx context.Context, workitemID, choice string) {
	t.Helper()
	h.bus.waitForSubscribe(t)

	if _, err := h.qm.Claim(ctx, workitemID); err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	if err := h.qm.Decide(ctx, workitemID, choice); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
}

// waitForEnqueue retries GetItem until the given workitem appears in the queue.
// The fake enqueues synchronously inside the handler goroutine, so the item
// appears as soon as that goroutine runs; this tight bounded retry (no sleep)
// succeeds within microseconds of the handler enqueueing.
func waitForEnqueue(t *testing.T, qm flow.QueueManager, workitemID string) {
	t.Helper()
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := qm.GetItem(context.Background(), workitemID); err == nil {
			return
		}
	}
	t.Fatalf("timed out waiting for %s to appear in queue", workitemID)
}

// defaultConfig returns a hitlConfig with no labels (output names used as-is).
func defaultConfig() *hitlConfig {
	return &hitlConfig{}
}

// configWithLabels returns a hitlConfig with the given label map.
func configWithLabels(labels map[string]string) *hitlConfig {
	return &hitlConfig{ChoiceLabels: labels}
}

// configWithChoices returns a hitlConfig restricting the presented choices to
// the given output/label pairs (in order).
func configWithChoices(choices ...choiceEntry) *hitlConfig {
	return &hitlConfig{Choices: choices}
}

// newWorkitemContext creates a WorkitemContext for testing.
func newWorkitemContext(workitemID string) *flowv1.WorkitemContext {
	return &flowv1.WorkitemContext{
		WorkitemId:    workitemID,
		FlowNamespace: "test-flow",
		NodeId:        "hitl",
	}
}

// runHandler starts handleHITL in a goroutine and returns an error channel.
// The caller should simulate the human decision then read from errCh.
func runHandler(
	ctx context.Context,
	client *flow.Client,
	qm flow.QueueManager,
	cfg *hitlConfig,
	wctx *flowv1.WorkitemContext,
) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		wi, wiErr := client.GetWorkitem(wctx.GetWorkitemId())
		if wiErr != nil {
			errCh <- fmt.Errorf("runHandler: get workitem: %w", wiErr)
			return
		}
		errCh <- handleHITL(ctx, client, wi, qm, cfg, wctx)
	}()
	return errCh
}

// newFlowFromTopology creates a *flow.Flow from the spy's Topology response
// by creating a client that reads from the spy's GetFlowTopology implementation.
func newFlowFromTopology(t *testing.T, spy *hitlSpy) *flow.Flow {
	t.Helper()
	client := newSpyClient(t, spy)
	f, err := client.GetFlow()
	if err != nil {
		t.Fatalf("GetFlow() failed: %v", err)
	}
	return f
}

// ---------------------------------------------------------------------------
// Additional spy factories for edge case tests
// ---------------------------------------------------------------------------

// newMultiStampSpy returns a spy with two STAMP capabilities on the same
// artefact (petition/reviewed and petition/approved). Used to verify all
// stamps are applied before routing.
func newMultiStampSpy() *hitlSpy {
	return &hitlSpy{
		ArtefactContents: map[string]string{
			"petition": "test-petition-content",
		},
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name: "hitl-multi-stamp",
				Capabilities: []string{
					"READ:flow",
					"READ:artefact/petition",
					"STAMP:artefact/petition/reviewed",
					"STAMP:artefact/petition/approved",
				},
				Outputs: []*flowv1.FlowOutput{
					{Name: "done", Target: "next-node"},
				},
			},
		},
	}
}

// newThreeOutputSpy returns a spy with three routing outputs and no exit
// contract. Used to test the choices restriction against a config subset.
func newThreeOutputSpy() *hitlSpy {
	return &hitlSpy{
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name:         "hitl-three-output",
				Capabilities: []string{"READ:flow"},
				Outputs: []*flowv1.FlowOutput{
					{Name: "a", Target: "node-a"},
					{Name: "b", Target: "node-b"},
					{Name: "c", Target: "node-c"},
				},
			},
		},
	}
}

// newApprovalSpy returns a spy configured as a hitl:latest approval CRD
// instance: output "approve" → sort, READ:artefact/haiku + petition,
// STAMP:artefact/haiku/approval, exit-bound.
func newApprovalSpy() *hitlSpy {
	return &hitlSpy{
		ArtefactContents: map[string]string{
			"haiku":    "test-haiku-content",
			"petition": "test-petition-content",
		},
		Topology: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name: "hitl-approval",
				Capabilities: []string{
					"READ:flow",
					"READ:artefact/haiku",
					"READ:artefact/petition",
					"STAMP:artefact/haiku/approval",
				},
				Outputs: []*flowv1.FlowOutput{
					{Name: "approve", Target: "sort"},
				},
			},
			ExitContract: map[string]*flowv1.StampRequirements{
				"haiku": {Stamps: []string{"approval"}},
			},
		},
	}
}
