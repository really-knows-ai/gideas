package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SEAM CONTRACT — PHASE_06 Unit 1 (SPEC R-4.4/R-4.5). These tests are RED until
// the production implementer adds the following seam EXACTLY, then they pin the
// behaviour: whenever a decide quorum-acks, the queue-service must publish a
// `queue.decided` EventBus event (channel "queue", event type "queue.decided",
// payload carrying workitem_id, queue_name, choice).
//
// Chosen shape: a function-type field on Registry — the same seam shape as the
// existing peerDialer / OnShardEvicted fields (registry.go:53-56) — nil ⇒
// default (no-op) behaviour. A function field (NOT an interface) is the
// smallest seam: it lets unit tests install a captured-call fake in one line
// and keeps the pkg/eventbus AsyncPublisher + flowv1 proto shape OUT of the
// unit under test (production cmd/main.go adapts the hook to them later).
//
// The production implementer MUST add, verbatim:
//
//  1. Registry field (with peerDialer / OnShardEvicted in registry.go):
//
//         // PublishQueueDecided fires (queueName, workitemID, choice) after a
//         // decide quorum-acks, so the queue-service can emit the
//         // queue.decided EventBus event (channel "queue", event type
//         // "queue.decided"). nil ⇒ no event — the EventBus is a notification
//         // channel, never the record, so a decision is NEVER blocked by
//         // publish. Production cmd/main.go adapts this hook to the
//         // pkg/eventbus AsyncPublisher + flowv1.PublishRequest shape.
//         PublishQueueDecided func(queueName, workitemID, choice string)
//
//  2. Registry method (private; nil-check, mirroring the OnShardEvicted call
//     in lease.go evictQueue):
//
//         // publishQueueDecided emits the queue.decided notification via the
//         // PublishQueueDecided hook. nil ⇒ silent no-op (unlike
//         // OnShardEvicted's nil ⇒ slog.Warn: an absent EventBus is a
//         // configured choice for the queue-service, not a fault).
//         func (r *Registry) publishQueueDecided(queueName, workitemID, choice string) {
//             if r.PublishQueueDecided != nil {
//                 r.PublishQueueDecided(queueName, workitemID, choice)
//             }
//         }
//
//  3. Every decide portal calls r.publishQueueDecided(...) AFTER its
//     decideBroadcast returned nil (success only), with the EXACT request
//     values:
//       - gateway.go GatewayServer.Decide: after the decideBroadcast error
//         check, before returning the ack:
//         g.reg.publishQueueDecided(req.GetQueueName(), req.GetWorkitemId(),
//         req.GetChoice()).
//       - itemgrpc.go Registry.DecideQueuedItem: after the decideBroadcast
//         error check:
//         r.publishQueueDecided(req.GetQueueName(), req.GetWorkitemId(),
//         req.GetChoice()).
//       - rest.go handleDecide: after the decideBroadcast error check:
//         s.reg.publishQueueDecided(name, id, choice).
//
// CancelQueuedItem intentionally does NOT publish: the phase pins exactly the
// three portals above, so a cancel (an empty-choice decision for internal
// callers) goes out over the same decideBroadcast but stays silent on the
// EventBus unless SPEC R-4.4/R-4.5 is later extended to cover it.
//
// The hook stays high-level (queueName, workitemID, choice): unit tests never
// construct flowv1.PublishRequest / flowv1.Event and never import pkg/eventbus.
// The channel/event-type/payload proto shape is adapted in production wiring
// (cmd/main.go, later) and asserted by the integration test, not here.

// decideCall is one captured PublishQueueDecided invocation.
type decideCall struct {
	queueName  string
	workitemID string
	choice     string
}

// capturedPublisher is the fake PublishQueueDecided hook: it records every
// invocation synchronously (the seam contract pins a synchronous call inside
// the decide portal) for post-call assertions.
type capturedPublisher struct {
	calls []decideCall
}

func (c *capturedPublisher) hook(queueName, workitemID, choice string) {
	c.calls = append(c.calls, decideCall{queueName: queueName, workitemID: workitemID, choice: choice})
}

// seedWaitingItem stores a pre-enqueued waiting item on every harness shard so
// decideBroadcast's ensure-claim step can bring it to the decidable state.
func seedWaitingItem(h *funnelHarness) {
	seed := &flowv1.QueueItem{
		WorkitemId: testWorkitemID, QueueName: testQueueName,
		Status: testStatusWaiting, GenerationId: "0000000000000001",
	}
	for _, addr := range h.addrs {
		h.shards[addr].setItem(seed)
	}
}

// TestDecide_PublishesQueueDecidedOnSuccess pins R-4.4/R-4.5 through the
// GatewayServer.Decide portal: on a quorum-acked decide the PublishQueueDecided
// hook fires EXACTLY ONCE with exactly (queueName, workitemID, choice).
func TestDecide_PublishesQueueDecidedOnSuccess(t *testing.T) {
	h := newFunnelHarness(t, "shard-a", "shard-b", "shard-c")
	seedWaitingItem(h)
	pub := &capturedPublisher{}
	h.reg.PublishQueueDecided = pub.hook

	resp, err := h.gateway.Decide(context.Background(), &flowv1.DecideRequest{
		QueueName: testQueueName, WorkitemId: testWorkitemID, Choice: testChoiceApprove,
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("decide not acknowledged")
	}

	if len(pub.calls) != 1 {
		t.Fatalf("PublishQueueDecided fired %d times, want exactly 1", len(pub.calls))
	}
	want := decideCall{queueName: testQueueName, workitemID: testWorkitemID, choice: testChoiceApprove}
	if pub.calls[0] != want {
		t.Fatalf("PublishQueueDecided fired %+v, want %+v", pub.calls[0], want)
	}
}

// TestDecide_DoesNotPublishQueueDecidedOnFailure pins the "publish only on
// success" contract: a decide that misses quorum (2 of 3 shards fail
// DecideItem mid-write ⇒ 1 confirmation < ⌊3/2⌋+1) surfaces codes.Unavailable
// and the hook must NOT fire.
func TestDecide_DoesNotPublishQueueDecidedOnFailure(t *testing.T) {
	h := newFunnelHarness(t, "shard-a", "shard-b", "shard-c")
	seedWaitingItem(h)
	pub := &capturedPublisher{}
	h.reg.PublishQueueDecided = pub.hook
	h.shards["addr-shard-a"].setDecideError(status.Error(codes.Unavailable, "shard a down"))
	h.shards["addr-shard-b"].setDecideError(status.Error(codes.Unavailable, "shard b down"))

	_, err := h.gateway.Decide(context.Background(), &flowv1.DecideRequest{
		QueueName: testQueueName, WorkitemId: testWorkitemID, Choice: testChoiceApprove,
	})
	if err == nil {
		t.Fatal("Decide with 1-of-3 confirmations must fail")
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %v, want Unavailable", status.Code(err))
	}
	if len(pub.calls) != 0 {
		t.Fatalf("PublishQueueDecided fired %d times on a failed decide, want 0", len(pub.calls))
	}
}

// TestDecide_NilPublisherIsNoOp pins that an absent EventBus never blocks (or
// panics) a decision: with no PublishQueueDecided installed, Decide still
// quorum-acks. The event bus is a notification channel, not the record.
func TestDecide_NilPublisherIsNoOp(t *testing.T) {
	h := newFunnelHarness(t, "shard-a") // no PublishQueueDecided installed
	seedWaitingItem(h)

	resp, err := h.gateway.Decide(context.Background(), &flowv1.DecideRequest{
		QueueName: testQueueName, WorkitemId: testWorkitemID, Choice: testChoiceApprove,
	})
	if err != nil {
		t.Fatalf("Decide with a nil publisher must still succeed, got err: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("decide not acknowledged")
	}
}

// TestDecideQueuedItem_PublishesQueueDecidedOnSuccess pins the seam at the
// item-gRPC portal (itemgrpc.go Registry.DecideQueuedItem): a successful
// decide fires the hook exactly once with the request values. The Registry is
// built directly (mirroring newItemGRPCHarness minus the gRPC server) so the
// hook can be installed and the method called in-process — zero real I/O.
func TestDecideQueuedItem_PublishesQueueDecidedOnSuccess(t *testing.T) {
	now := time.Now().UTC()
	owner := newFakeMirrorShard(t, "owner-id")
	owner.setItem(&flowv1.QueueItem{
		WorkitemId: testWorkitemID, Status: testStatusWaiting, GenerationId: "0000000000000001",
	})
	c := newFakeClient(t, queueCR(testQueueName, shard("owner-id", "owner-addr", phaseActive, now)))
	r := NewRegistry(c, time.Minute, time.Second)
	r.Namespace = testNamespace
	r.peerDialer = func(ctx context.Context, addr string) (*grpc.ClientConn, error) {
		if addr != "owner-addr" {
			return nil, errShardUnavailable
		}
		return owner.dialer(ctx, addr)
	}
	pub := &capturedPublisher{}
	r.PublishQueueDecided = pub.hook

	resp, err := r.DecideQueuedItem(context.Background(), &flowv1.DecideQueuedItemRequest{
		QueueName: testQueueName, WorkitemId: testWorkitemID, Choice: testChoiceApprove,
	})
	if err != nil {
		t.Fatalf("DecideQueuedItem: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("decide not acknowledged")
	}

	if len(pub.calls) != 1 {
		t.Fatalf("PublishQueueDecided fired %d times, want exactly 1", len(pub.calls))
	}
	want := decideCall{queueName: testQueueName, workitemID: testWorkitemID, choice: testChoiceApprove}
	if pub.calls[0] != want {
		t.Fatalf("PublishQueueDecided fired %+v, want %+v", pub.calls[0], want)
	}
}

// TestREST_Decide_PublishesQueueDecidedOnSuccess pins the seam at the REST
// portal (rest.go handleDecide): a 200 decide response fires the hook exactly
// once with the body choice.
func TestREST_Decide_PublishesQueueDecidedOnSuccess(t *testing.T) {
	h := newRestHarness(t, []string{"say:a"})
	h.shards["say:a"].setItem(&flowv1.QueueItem{
		WorkitemId: testWorkitemID, Status: testStatusWaiting, GenerationId: "0000000000000001",
	})
	pub := &capturedPublisher{}
	h.reg.PublishQueueDecided = pub.hook

	w := doReq(h, http.MethodPost, "/queues/hitl-approval/wi-1/decide", `{"choice":"approve"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	if len(pub.calls) != 1 {
		t.Fatalf("PublishQueueDecided fired %d times, want exactly 1", len(pub.calls))
	}
	want := decideCall{queueName: testQueueName, workitemID: testWorkitemID, choice: testChoiceApprove}
	if pub.calls[0] != want {
		t.Fatalf("PublishQueueDecided fired %+v, want %+v", pub.calls[0], want)
	}
}
