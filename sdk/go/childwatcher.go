package flow

import (
	"context"
	"io"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ChildWatcher receives lifecycle events for child Workitems via a streaming
// EventBus subscription. Created by Workitem.WatchChildren. Recv blocks the
// calling goroutine — no background goroutines are started.
type ChildWatcher struct {
	stream flowv1.FlowEventBusService_SubscribeClient
	cancel context.CancelFunc
}

// Recv blocks until a ChildLifecycleEvent arrives, the stream ends normally
// (io.EOF), or Stop is called (also io.EOF). Real gRPC errors are propagated
// to the caller.
func (w *ChildWatcher) Recv() (*ChildLifecycleEvent, error) {
	evt, err := w.stream.Recv()
	if err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}
		if isContextCanceled(err) {
			return nil, io.EOF
		}
		return nil, err
	}
	return protoEventToChildLifecycleEvent(evt), nil
}

// Stop cancels the internal context, causing any in-flight Recv to return
// io.EOF. Idempotent — safe to call multiple times.
func (w *ChildWatcher) Stop() {
	w.cancel()
}

// protoEventToChildLifecycleEvent converts a proto FlowEvent to the SDK's
// ChildLifecycleEvent struct, extracting phase and node_id from labels.
func protoEventToChildLifecycleEvent(evt *flowv1.FlowEvent) *ChildLifecycleEvent {
	event := &ChildLifecycleEvent{
		WorkitemID: evt.GetWorkitemId(),
	}
	for _, lbl := range evt.GetLabels() {
		switch lbl.GetKey() {
		case "phase":
			event.Phase = lbl.GetValue()
		case "node_id":
			event.NodeID = lbl.GetValue()
		}
	}
	return event
}

// isContextCanceled returns true if the error is a gRPC cancellation error
// (caused by cancelling the context passed to the Subscribe call).
func isContextCanceled(err error) bool {
	st, ok := status.FromError(err)
	return ok && st.Code() == codes.Canceled
}
