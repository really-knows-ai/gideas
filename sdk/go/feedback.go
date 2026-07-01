package flow

import (
	"context"
	"fmt"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// FeedbackState is the lifecycle state of a feedback item.
type FeedbackState = flowv1.FeedbackState

// Convenience constants for FeedbackState values, re-exported from the proto
// for SDK callers that should not import the proto package directly.
const (
	FeedbackStateNew        FeedbackState = flowv1.FeedbackState_FEEDBACK_STATE_NEW
	FeedbackStateActioned   FeedbackState = flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED
	FeedbackStateWontFix    FeedbackState = flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX
	FeedbackStateRejected   FeedbackState = flowv1.FeedbackState_FEEDBACK_STATE_REJECTED
	FeedbackStateDeadlocked FeedbackState = flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED
	FeedbackStateResolved   FeedbackState = flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED
)

// Feedback is a domain object wrapping a feedback item with session context.
// It provides local getters for cached proto fields and lifecycle methods
// that call the Archivist service.
type Feedback struct {
	item    *flowv1.FeedbackItem
	session *session
}

// newFeedback creates a Feedback domain object wrapping the proto item.
// It is called by other phases (e.g. Workitem.GetFeedback) when constructing
// Feedback values from Archivist responses.
func newFeedback(item *flowv1.FeedbackItem, sess *session) *Feedback {
	return &Feedback{item: item, session: sess}
}

// ---------------------------------------------------------------------------
// Local getters (no round-trip)
// ---------------------------------------------------------------------------

// GetID returns the feedback identifier.
func (f *Feedback) GetID() string {
	return f.item.GetId()
}

// GetMessage returns the feedback message.
func (f *Feedback) GetMessage() string {
	return f.item.GetMessage()
}

// GetState returns the current feedback state.
func (f *Feedback) GetState() FeedbackState {
	return f.item.GetState()
}

// GetSource returns the feedback source identifier.
func (f *Feedback) GetSource() string {
	return f.item.GetSource()
}

// ---------------------------------------------------------------------------
// Round-trip getter
// ---------------------------------------------------------------------------

// GetDepth returns the current history depth (number of transitions)
// for this feedback item. This is the only getter requiring a server
// round-trip (Archivist GetFeedbackDepth RPC).
//
// ponytail: uses context.Background(); switch to per-session context when
// the session struct carries one, so cancellation and timeout are
// configurable at the client level (WithTimeout ClientOption).
func (f *Feedback) GetDepth() (int32, error) {
	resp, err := f.session.Archivist.GetFeedbackDepth(context.Background(), &flowv1.GetFeedbackDepthRequest{
		WorkitemId: f.session.workitemID,
		FeedbackId: f.item.GetId(),
	})
	if err != nil {
		return 0, fmt.Errorf("flow sdk: get feedback depth: %w", err)
	}
	return resp.GetDepth(), nil
}

// ---------------------------------------------------------------------------
// Lifecycle methods
// ---------------------------------------------------------------------------

// Resolve transitions feedback from NEW/REJECTED to ACTIONED, indicating
// a fix has been applied.
//
// ponytail: uses context.Background(); see GetDepth.
func (f *Feedback) Resolve(message string) error {
	_, err := f.session.Archivist.ResolveFeedback(context.Background(), &flowv1.ResolveFeedbackRequest{
		WorkitemId: f.session.workitemID,
		FeedbackId: f.item.GetId(),
		Message:    message,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: resolve feedback: %w", err)
	}
	return nil
}

// Refuse transitions feedback from NEW/REJECTED to WONT_FIX, indicating
// the refining node refuses to fix the issue. The justification must be
// either a Citation (referencing existing laws) or a NovelArgument.
//
// ponytail: uses context.Background(); see GetDepth.
func (f *Feedback) Refuse(justification *flowv1.Justification) error {
	_, err := f.session.Archivist.RefuseFeedback(context.Background(), &flowv1.RefuseFeedbackRequest{
		WorkitemId:    f.session.workitemID,
		FeedbackId:    f.item.GetId(),
		Justification: justification,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: refuse feedback: %w", err)
	}
	return nil
}

// AcceptFix transitions feedback from ACTIONED to RESOLVED, indicating
// the reviewer accepts the applied fix.
//
// ponytail: uses context.Background(); see GetDepth.
func (f *Feedback) AcceptFix() error {
	_, err := f.session.Archivist.AcceptFix(context.Background(), &flowv1.AcceptFixRequest{
		WorkitemId: f.session.workitemID,
		FeedbackId: f.item.GetId(),
	})
	if err != nil {
		return fmt.Errorf("flow sdk: accept fix: %w", err)
	}
	return nil
}

// RejectFix transitions feedback from ACTIONED to REJECTED, indicating
// the reviewer finds the fix inadequate. The message explains why.
//
// ponytail: uses context.Background(); see GetDepth.
func (f *Feedback) RejectFix(message string) error {
	_, err := f.session.Archivist.RejectFix(context.Background(), &flowv1.RejectFixRequest{
		WorkitemId: f.session.workitemID,
		FeedbackId: f.item.GetId(),
		Message:    message,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: reject fix: %w", err)
	}
	return nil
}

// AcceptRefusal transitions feedback from WONT_FIX to RESOLVED,
// indicating the reviewer accepts the refiner's justification.
//
// ponytail: uses context.Background(); see GetDepth.
func (f *Feedback) AcceptRefusal() error {
	_, err := f.session.Archivist.AcceptRefusal(context.Background(), &flowv1.AcceptRefusalRequest{
		WorkitemId: f.session.workitemID,
		FeedbackId: f.item.GetId(),
	})
	if err != nil {
		return fmt.Errorf("flow sdk: accept refusal: %w", err)
	}
	return nil
}

// RejectRefusal transitions feedback from WONT_FIX to REJECTED,
// indicating the reviewer finds the justification inadequate.
// The message explains why the refusal is not acceptable.
//
// ponytail: uses context.Background(); see GetDepth.
func (f *Feedback) RejectRefusal(message string) error {
	_, err := f.session.Archivist.RejectRefusal(context.Background(), &flowv1.RejectRefusalRequest{
		WorkitemId: f.session.workitemID,
		FeedbackId: f.item.GetId(),
		Message:    message,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: reject refusal: %w", err)
	}
	return nil
}

// Deadlock transitions feedback from any non-resolved, non-deadlocked
// state to DEADLOCKED. Called when feedback depth exceeds threshold.
//
// ponytail: uses context.Background(); see GetDepth.
func (f *Feedback) Deadlock() error {
	_, err := f.session.Archivist.DeadlockFeedback(context.Background(), &flowv1.DeadlockFeedbackRequest{
		WorkitemId: f.session.workitemID,
		FeedbackId: f.item.GetId(),
	})
	if err != nil {
		return fmt.Errorf("flow sdk: deadlock feedback: %w", err)
	}
	return nil
}

// LinkRuling atomically links a judiciary ruling to this feedback,
// transitioning it to the specified target state. The feedback must
// be in DEADLOCKED state. targetState must be WONT_FIX or REJECTED.
//
// ponytail: uses context.Background(); see GetDepth.
func (f *Feedback) LinkRuling(lawID string, targetState FeedbackState) error {
	_, err := f.session.Archivist.LinkRuling(context.Background(), &flowv1.LinkRulingRequest{
		WorkitemId:  f.session.workitemID,
		FeedbackId:  f.item.GetId(),
		LawId:       lawID,
		TargetState: targetState,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: link ruling: %w", err)
	}
	return nil
}
