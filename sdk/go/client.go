// Package flow provides a high-level Go SDK for Foundry Flow nodes.
//
// The Client wraps the generated gRPC service stubs and handles connection
// management, workitem context injection, and convenience methods for common
// operations. All calls are routed through the in-pod Sidecar.
package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// DefaultSidecarAddress is the default gRPC endpoint for the Sidecar proxy.
	DefaultSidecarAddress = "localhost:50051"

	// EnvWorkitemID is the environment variable injected by the runtime
	// to identify the current workitem.
	EnvWorkitemID = "FLOW_WORKITEM_ID"

	// metadataKeyWorkitemID is the gRPC metadata key used to propagate
	// the workitem context on every outgoing call.
	metadataKeyWorkitemID = "x-flow-workitem-id"

	// EnvFlowNamespace is the environment variable injected by the runtime
	// to identify the current flow's Kubernetes namespace.
	EnvFlowNamespace = "FLOW_NAMESPACE"
)

// EnvEventBusAddress is the environment variable injected by the runtime
// to specify the Event Bus gRPC endpoint. When set, the Client connects
// directly to the Event Bus for streaming operations (e.g. WatchChildren).
const EnvEventBusAddress = "EVENT_BUS_ADDRESS"

// ClientOption configures the Client.
type ClientOption func(*clientConfig)

type clientConfig struct {
	sidecarAddr  string
	eventBusAddr string
	timeout      time.Duration
	maxRetries   int
}

// WithSidecarAddress overrides the default Sidecar gRPC address.
func WithSidecarAddress(addr string) ClientOption {
	return func(c *clientConfig) {
		c.sidecarAddr = addr
	}
}

// WithEventBusAddress overrides the Event Bus gRPC address read from
// the environment. When set, the Client establishes a direct connection
// to the Event Bus for streaming RPCs (e.g. WatchChildren).
func WithEventBusAddress(addr string) ClientOption {
	return func(c *clientConfig) {
		c.eventBusAddr = addr
	}
}

// WithTimeout sets the per-call timeout for gRPC requests made through
// the session. If not set, calls use a default background context with no
// timeout.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *clientConfig) {
		c.timeout = d
	}
}

// WithRetry sets the maximum number of retry attempts for transient gRPC
// errors. If not set, no retries are performed.
func WithRetry(maxAttempts int) ClientOption {
	return func(c *clientConfig) {
		c.maxRetries = maxAttempts
	}
}

// Client is the primary SDK entry point for Foundry Flow nodes.
// It wraps a session that manages gRPC connections and service clients.
type Client struct {
	session *session
}

// NewClient connects to the Sidecar and returns a configured Client.
//
// It reads FLOW_WORKITEM_ID from the environment and attaches it as gRPC
// metadata on every outgoing call. If the environment variable is not set,
// the client still initialises but convenience methods that require a
// workitem context will return errors.
func NewClient(opts ...ClientOption) (*Client, error) {
	cfg := &clientConfig{
		sidecarAddr: DefaultSidecarAddress,
	}
	for _, o := range opts {
		o(cfg)
	}

	// Read Event Bus address from env if not explicitly set.
	if cfg.eventBusAddr == "" {
		cfg.eventBusAddr = os.Getenv(EnvEventBusAddress)
	}

	sess, err := newSession(cfg)
	if err != nil {
		return nil, err
	}

	return &Client{session: sess}, nil
}

// Close releases the underlying gRPC connections.
func (c *Client) Close() error {
	if c.session == nil {
		return nil
	}
	return c.session.Close()
}

// WorkitemID returns the workitem ID read from the environment at init time.
func (c *Client) WorkitemID() string {
	if c.session == nil {
		return ""
	}
	return c.session.workitemID
}

// FlowNamespace returns the flow namespace read from the environment at init time.
func (c *Client) FlowNamespace() string {
	if c.session == nil {
		return ""
	}
	return c.session.namespace
}

// ---------------------------------------------------------------------------
// New Entry-Point Methods (Phase 1 redesign)
// ---------------------------------------------------------------------------

// GetWorkitem returns a Workitem domain object. With no arguments, it
// returns the current workitem (from the session's workitemID). With one
// argument, it returns a Workitem wrapping that ID. Passing more than one
// ID returns an error (multi-get not yet supported).
func (c *Client) GetWorkitem(workitemID ...string) (*Workitem, error) {
	switch len(workitemID) {
	case 0:
		if c.session == nil || c.session.workitemID == "" {
			return nil, fmt.Errorf("flow sdk: no workitem ID available — FLOW_WORKITEM_ID not set")
		}
		return &Workitem{
			session:   c.session,
			id:        c.session.workitemID,
			namespace: c.session.namespace,
		}, nil
	case 1:
		return &Workitem{
			session:   c.session,
			id:        workitemID[0],
			namespace: c.session.namespace,
		}, nil
	default:
		return nil, fmt.Errorf("flow sdk: GetWorkitem accepts 0 or 1 workitem IDs, got %d", len(workitemID))
	}
}

// GetFlow returns the Flow topology for the current namespace.
func (c *Client) GetFlow() (*Flow, error) {
	if c.session == nil {
		return nil, fmt.Errorf("flow sdk: client not initialised")
	}
	resp, err := c.session.Operator.GetFlowTopology(context.Background(), &flowv1.GetFlowTopologyRequest{})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get flow topology failed: %w", err)
	}
	return newFlow(resp, c.session.namespace), nil
}

// GetNode returns the calling node's identity and capabilities.
func (c *Client) GetNode() (*Node, error) {
	if c.session == nil {
		return nil, fmt.Errorf("flow sdk: client not initialised")
	}
	resp, err := c.session.Operator.GetFlowTopology(context.Background(), &flowv1.GetFlowTopologyRequest{})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get node failed: %w", err)
	}
	return newNode(resp.GetSelf()), nil
}

// GetLaw returns a single law by ID from the Librarian. Returns the domain
// *Law wrapping the proto flowv1.Law.
func (c *Client) GetLaw(lawID string) (*Law, error) {
	if c.session == nil {
		return nil, fmt.Errorf("flow sdk: client not initialised")
	}
	resp, err := c.session.Librarian.GetLaw(context.Background(), &flowv1.GetLawRequest{
		LawId: lawID,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get law failed: %w", err)
	}
	return newLaw(resp.GetLaw(), c.session.Librarian), nil
}

// RecordFinding creates a Tier 1 Finding and returns the law ID.
func (c *Client) RecordFinding(
	goal string, appliesTo []string, representations []*flowv1.Representation,
) (string, error) {
	if c.session == nil {
		return "", fmt.Errorf("flow sdk: client not initialised")
	}
	resp, err := c.session.Librarian.RecordFinding(context.Background(), &flowv1.RecordFindingRequest{
		Goal:            goal,
		AppliesTo:       appliesTo,
		Representations: representations,
	})
	if err != nil {
		return "", fmt.Errorf("flow sdk: record finding failed: %w", err)
	}
	return resp.GetLawId(), nil
}

// ---------------------------------------------------------------------------
// Convenience Methods
// ---------------------------------------------------------------------------

// Heartbeat sends an explicit heartbeat to the Sidecar, resetting the
// inactivity timer. Returns the acknowledged flag.
func (c *Client) Heartbeat(ctx context.Context) (bool, error) {
	resp, err := c.session.Sidecar.Heartbeat(ctx, &flowv1.HeartbeatRequest{
		WorkitemId: c.session.workitemID,
	})
	if err != nil {
		return false, fmt.Errorf("flow sdk: heartbeat failed: %w", err)
	}
	return resp.GetAcknowledged(), nil
}

// PauseTimer suspends the Sidecar's inactivity timer for the current
// Workitem assignment. The timer remains suspended until ResumeTimer is
// called or the handler returns. Used by HITL nodes to park Workitems
// while awaiting human decisions without triggering timeout.
func (c *Client) PauseTimer(ctx context.Context) error {
	_, err := c.session.Sidecar.PauseTimer(ctx, &flowv1.PauseTimerRequest{
		WorkitemId: c.session.workitemID,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: pause timer failed: %w", err)
	}
	return nil
}

// ResumeTimer resumes the Sidecar's inactivity timer after a PauseTimer call.
// The timer resets to the full timeout window on resume.
func (c *Client) ResumeTimer(ctx context.Context) error {
	_, err := c.session.Sidecar.ResumeTimer(ctx, &flowv1.ResumeTimerRequest{
		WorkitemId: c.session.workitemID,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: resume timer failed: %w", err)
	}
	return nil
}

// CompleteOption configures a Complete() call.
type CompleteOption func(*flowv1.CompleteAction)

// WithReason sets the completion reason (e.g. CompletionReasonCancelled).
func WithReason(r flowv1.CompletionReason) CompleteOption {
	return func(a *flowv1.CompleteAction) {
		a.Reason = r
	}
}

// SuspendOption configures a Suspend() call.
type SuspendOption func(*flowv1.SuspendAction)

// WithCondition sets a CEL expression for automatic resume evaluation.
// Available variables: children (list with phase).
// Example: children.all(c, c.phase == "Completed")
func WithCondition(cel string) SuspendOption {
	return func(a *flowv1.SuspendAction) {
		a.Condition = cel
	}
}

// WithSuspendTimeout sets the maximum duration the workitem may remain suspended.
// Capped by the Flow CRD's maxSuspendTimeout. If the resume condition is
// not met before the deadline, the workitem fails.
func WithSuspendTimeout(d time.Duration) SuspendOption {
	return func(a *flowv1.SuspendAction) {
		a.Timeout = durationpb.New(d)
	}
}

// Complete submits a completion action to the Operator via the Sidecar,
// signalling that the node has finished processing.
func (c *Client) Complete(ctx context.Context, opts ...CompleteOption) (bool, error) {
	action := &flowv1.CompleteAction{}
	for _, o := range opts {
		o(action)
	}
	resp, err := c.session.Operator.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: c.session.workitemID,
		Action: &flowv1.SubmitResultRequest_Complete{
			Complete: action,
		},
	})
	if err != nil {
		return false, fmt.Errorf("flow sdk: complete failed: %w", err)
	}
	return resp.GetAccepted(), nil
}

// RouteToOutput submits a routing action that routes the workitem through
// the named output channel of the current node. The Operator resolves the
// output name to the target node defined in the FoundryNode CRD.
func (c *Client) RouteToOutput(ctx context.Context, outputName string) (bool, error) {
	resp, err := c.session.Operator.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: c.session.workitemID,
		Action: &flowv1.SubmitResultRequest_Route{
			Route: &flowv1.RouteAction{
				Target: outputName,
				Output: true,
			},
		},
	})
	if err != nil {
		return false, fmt.Errorf("flow sdk: route to output failed: %w", err)
	}
	return resp.GetAccepted(), nil
}

// Suspend submits a suspend action to the Operator via the Sidecar,
// transitioning the workitem to the Suspended phase. The handler should
// return nil after calling this. On resume, the workitem is re-dispatched
// to the same node type that suspended it.
//
// Use WithCondition to set a CEL expression for automatic resume and
// WithSuspendTimeout to cap the suspension duration.
func (c *Client) Suspend(ctx context.Context, opts ...SuspendOption) error {
	action := &flowv1.SuspendAction{}
	for _, o := range opts {
		o(action)
	}
	_, err := c.session.Operator.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: c.session.workitemID,
		Action: &flowv1.SubmitResultRequest_Suspend{
			Suspend: action,
		},
	})
	if err != nil {
		return fmt.Errorf("flow sdk: suspend failed: %w", err)
	}
	return nil
}

// Resume requests that a suspended workitem be resumed. Unlike Suspend
// (which operates on the caller's own workitem), Resume targets another
// workitem by ID — typically a child that the caller previously suspended.
func (c *Client) Resume(ctx context.Context, workitemID string) error {
	_, err := c.session.Operator.ResumeWorkitem(ctx, &flowv1.ResumeWorkitemRequest{
		WorkitemId: workitemID,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: resume failed: %w", err)
	}
	return nil
}

// GetArtefact retrieves the latest version of the named artefact.
func (c *Client) GetArtefact(ctx context.Context, artefactID string) (*flowv1.GetArtefactResponse, error) {
	resp, err := c.session.Archivist.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: c.session.workitemID,
		ArtefactId: artefactID,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get artefact failed: %w", err)
	}
	return resp, nil
}

// StoreArtefact stores content as a named artefact. The Sidecar will compute
// the content hash — the SDK does not need to supply it. Returns the response
// containing the version_hash and whether this was a new version.
func (c *Client) StoreArtefact(
	ctx context.Context, artefactID, governedArtefact string, content []byte,
) (*flowv1.StoreArtefactResponse, error) {
	resp, err := c.session.Archivist.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       c.session.workitemID,
		ArtefactId:       artefactID,
		GovernedArtefact: governedArtefact,
		Content:          content,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: store artefact failed: %w", err)
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Stamp Convenience Methods
// ---------------------------------------------------------------------------

// StampArtefact applies a named governance stamp to the current (head)
// version of the specified artefact. The Sidecar injects cryptographic
// identity (signature, cert_chain) — the SDK does not supply these.
func (c *Client) StampArtefact(
	ctx context.Context, artefactID, stampName string,
) (*flowv1.StampArtefactResponse, error) {
	resp, err := c.session.Archivist.StampArtefact(ctx, &flowv1.StampArtefactRequest{
		WorkitemId: c.session.workitemID,
		ArtefactId: artefactID,
		StampName:  stampName,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: stamp artefact failed: %w", err)
	}
	return resp, nil
}

// GetStamps returns all stamps on the current version of the specified artefact.
func (c *Client) GetStamps(ctx context.Context, artefactID string) ([]*flowv1.Stamp, error) {
	resp, err := c.session.Archivist.GetStamps(ctx, &flowv1.GetStampsRequest{
		WorkitemId: c.session.workitemID,
		ArtefactId: artefactID,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get stamps failed: %w", err)
	}
	return resp.GetStamps(), nil
}

// HasStamp checks whether the named stamp exists on the current version
// of the specified artefact.
func (c *Client) HasStamp(ctx context.Context, artefactID, stampName string) (bool, error) {
	resp, err := c.session.Archivist.HasStamp(ctx, &flowv1.HasStampRequest{
		WorkitemId: c.session.workitemID,
		ArtefactId: artefactID,
		StampName:  stampName,
	})
	if err != nil {
		return false, fmt.Errorf("flow sdk: has stamp failed: %w", err)
	}
	return resp.GetExists(), nil
}

// ---------------------------------------------------------------------------
// Feedback Convenience Methods
// ---------------------------------------------------------------------------

// AddFeedback creates a new feedback item on the specified artefact.
// The feedback starts in NEW state. The canWontFix parameter gates whether
// the refiner may refuse this feedback with a justification (true = refusal
// permitted, false = must action). Returns the generated feedback ID.
func (c *Client) AddFeedback(
	ctx context.Context, artefactID string, canWontFix bool, message string,
) (string, error) {
	resp, err := c.session.Archivist.AddFeedback(ctx, &flowv1.AddFeedbackRequest{
		WorkitemId: c.session.workitemID,
		ArtefactId: artefactID,
		CanWontFix: canWontFix,
		Message:    message,
	})
	if err != nil {
		return "", fmt.Errorf("flow sdk: add feedback failed: %w", err)
	}
	return resp.GetFeedbackId(), nil
}

// GetFeedback returns all feedback items for the specified artefact.
func (c *Client) GetFeedback(ctx context.Context, artefactID string) ([]*flowv1.FeedbackItem, error) {
	resp, err := c.session.Archivist.GetFeedback(ctx, &flowv1.GetFeedbackRequest{
		WorkitemId: c.session.workitemID,
		ArtefactId: artefactID,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get feedback failed: %w", err)
	}
	return resp.GetFeedbackItems(), nil
}

// HasUnresolvedFeedback returns true if any feedback for the artefact
// is not in RESOLVED state.
func (c *Client) HasUnresolvedFeedback(ctx context.Context, artefactID string) (bool, error) {
	resp, err := c.session.Archivist.HasUnresolvedFeedback(ctx, &flowv1.HasUnresolvedFeedbackRequest{
		WorkitemId: c.session.workitemID,
		ArtefactId: artefactID,
	})
	if err != nil {
		return false, fmt.Errorf("flow sdk: has unresolved feedback failed: %w", err)
	}
	return resp.GetHasUnresolved(), nil
}

// ResolveFeedback transitions feedback from NEW/REJECTED to ACTIONED,
// indicating the fix has been applied.
func (c *Client) ResolveFeedback(ctx context.Context, feedbackID, message string) error {
	_, err := c.session.Archivist.ResolveFeedback(ctx, &flowv1.ResolveFeedbackRequest{
		WorkitemId: c.session.workitemID,
		FeedbackId: feedbackID,
		Message:    message,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: resolve feedback failed: %w", err)
	}
	return nil
}

// RefuseFeedback transitions feedback from NEW/REJECTED to WONT_FIX,
// indicating the refining node refuses to fix the issue. A structured
// justification is required — either a Citation (referencing existing
// laws) or a NovelArgument (new reasoning).
func (c *Client) RefuseFeedback(ctx context.Context, feedbackID string, justification *flowv1.Justification) error {
	_, err := c.session.Archivist.RefuseFeedback(ctx, &flowv1.RefuseFeedbackRequest{
		WorkitemId:    c.session.workitemID,
		FeedbackId:    feedbackID,
		Justification: justification,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: refuse feedback failed: %w", err)
	}
	return nil
}

// AcceptFix transitions feedback from ACTIONED to RESOLVED, indicating
// the reviewer accepts the applied fix.
func (c *Client) AcceptFix(ctx context.Context, feedbackID string) error {
	_, err := c.session.Archivist.AcceptFix(ctx, &flowv1.AcceptFixRequest{
		WorkitemId: c.session.workitemID,
		FeedbackId: feedbackID,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: accept fix failed: %w", err)
	}
	return nil
}

// RejectFix transitions feedback from ACTIONED to REJECTED, indicating
// the reviewer finds the applied fix inadequate. The message explains why
// the fix is insufficient so the refining node can try again.
func (c *Client) RejectFix(ctx context.Context, feedbackID, message string) error {
	_, err := c.session.Archivist.RejectFix(ctx, &flowv1.RejectFixRequest{
		WorkitemId: c.session.workitemID,
		FeedbackId: feedbackID,
		Message:    message,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: reject fix failed: %w", err)
	}
	return nil
}

// AcceptRefusal transitions feedback from WONT_FIX to RESOLVED, indicating
// the reviewer accepts the refiner's justification for refusing the feedback.
func (c *Client) AcceptRefusal(ctx context.Context, feedbackID string) error {
	_, err := c.session.Archivist.AcceptRefusal(ctx, &flowv1.AcceptRefusalRequest{
		WorkitemId: c.session.workitemID,
		FeedbackId: feedbackID,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: accept refusal failed: %w", err)
	}
	return nil
}

// RejectRefusal transitions feedback from WONT_FIX to REJECTED, indicating
// the reviewer finds the refiner's justification unjustified. The message
// explains why the refusal is not acceptable.
func (c *Client) RejectRefusal(ctx context.Context, feedbackID, message string) error {
	_, err := c.session.Archivist.RejectRefusal(ctx, &flowv1.RejectRefusalRequest{
		WorkitemId: c.session.workitemID,
		FeedbackId: feedbackID,
		Message:    message,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: reject refusal failed: %w", err)
	}
	return nil
}

// GetFeedbackDepth returns the current history depth (number of transitions)
// for the specified feedback item.
func (c *Client) GetFeedbackDepth(ctx context.Context, feedbackID string) (int32, error) {
	resp, err := c.session.Archivist.GetFeedbackDepth(ctx, &flowv1.GetFeedbackDepthRequest{
		WorkitemId: c.session.workitemID,
		FeedbackId: feedbackID,
	})
	if err != nil {
		return 0, fmt.Errorf("flow sdk: get feedback depth failed: %w", err)
	}
	return resp.GetDepth(), nil
}

// DeadlockFeedback transitions feedback from any non-resolved,
// non-deadlocked state to DEADLOCKED. Called by the gate node when
// feedback depth exceeds the configured threshold.
func (c *Client) DeadlockFeedback(
	ctx context.Context, feedbackID string,
) (*flowv1.FeedbackItem, error) {
	resp, err := c.session.Archivist.DeadlockFeedback(ctx, &flowv1.DeadlockFeedbackRequest{
		WorkitemId: c.session.workitemID,
		FeedbackId: feedbackID,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: deadlock feedback failed: %w", err)
	}
	return resp.GetUpdatedItem(), nil
}

// ---------------------------------------------------------------------------
// Topology Convenience Methods
// ---------------------------------------------------------------------------

// GetFlowTopology returns the Flow topology visible to the calling node.
// Requires READ:flow capability. The Sidecar injects node identity; the
// Operator resolves the calling node's outputs, all peer nodes with
// capabilities, and the bound exit contract (if exit-bound).
func (c *Client) GetFlowTopology(ctx context.Context) (*flowv1.GetFlowTopologyResponse, error) {
	resp, err := c.session.Operator.GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get flow topology failed: %w", err)
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Librarian Convenience Methods
// ---------------------------------------------------------------------------

// QueryLaws returns all laws matching the filter.
// Pass empty strings for all laws. Pass governedArtefact for scoped+global.
// Pass governedArtefact+repType for further filtering.
func (c *Client) QueryLaws(ctx context.Context, governedArtefact, representationType string) ([]*flowv1.Law, error) {
	var filter *flowv1.LawFilter
	if governedArtefact != "" || representationType != "" {
		filter = &flowv1.LawFilter{
			GovernedArtefact:   governedArtefact,
			RepresentationType: representationType,
		}
	}
	resp, err := c.session.Librarian.QueryLaws(ctx, &flowv1.QueryLawsRequest{
		Filter: filter,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: query laws failed: %w", err)
	}
	return resp.GetLaws(), nil
}

// Cite records usage of one or more laws.
func (c *Client) Cite(ctx context.Context, lawIDs ...string) error {
	_, err := c.session.Librarian.Cite(ctx, &flowv1.CiteRequest{
		LawIds: lawIDs,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: cite failed: %w", err)
	}
	return nil
}

// RecordFindingProto creates a Tier 1 Finding. This is the old flat method
// retained for backward compatibility during migration. It takes a context
// and returns the law ID string. Prefer the new RecordFinding entry method
// that does not require a context parameter.
// Deprecated: Use RecordFinding without ctx instead.
func (c *Client) RecordFindingProto(
	ctx context.Context, goal string, appliesTo []string, representations []*flowv1.Representation,
) (string, error) {
	resp, err := c.session.Librarian.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            goal,
		AppliesTo:       appliesTo,
		Representations: representations,
	})
	if err != nil {
		return "", fmt.Errorf("flow sdk: record finding failed: %w", err)
	}
	return resp.GetLawId(), nil
}

// ---------------------------------------------------------------------------
// LawGroup Convenience Methods
// ---------------------------------------------------------------------------

// GetLawGroup returns the evaluation contract for a named law group.
// If the group has no stored entry, the Librarian returns a built-in
// default {mode:"bundle", passes:1}.
func (c *Client) GetLawGroup(ctx context.Context, groupName string) (*LawGroup, error) {
	resp, err := c.session.Librarian.GetLawGroup(ctx, &flowv1.GetLawGroupRequest{GroupName: groupName})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get law group failed: %w", err)
	}
	return newLawGroup(
		resp.GetGroup().GetName(),
		GroupMode(resp.GetGroup().GetMode()),
		resp.GetGroup().GetPasses(),
		c.session.Librarian,
	), nil
}

// ListLawGroups returns all stored law groups.
// Built-in defaults for groups without entries are NOT included.
// Returns an empty slice if no groups are stored.
func (c *Client) ListLawGroups(ctx context.Context) ([]*LawGroup, error) {
	resp, err := c.session.Librarian.ListLawGroups(ctx, &flowv1.ListLawGroupsRequest{})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: list law groups failed: %w", err)
	}
	groups := resp.GetGroups()
	out := make([]*LawGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, newLawGroup(
			g.GetName(),
			GroupMode(g.GetMode()),
			g.GetPasses(),
			c.session.Librarian,
		))
	}
	return out, nil
}

// QueryLawsByGroup returns all laws matching the governed artefact and group.
func (c *Client) QueryLawsByGroup(ctx context.Context, governedArtefact, groupName string) ([]*flowv1.Law, error) {
	filter := &flowv1.LawFilter{
		GovernedArtefact: governedArtefact,
		Group:            groupName,
	}
	resp, err := c.session.Librarian.QueryLaws(ctx, &flowv1.QueryLawsRequest{Filter: filter})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: query laws by group failed: %w", err)
	}
	return resp.GetLaws(), nil
}

// PublishAuditEvent marshals the payload to JSON and publishes it to the
// Event Bus on the "audit" channel. This is a best-effort operation:
// callers should log the error but not fail work execution.
// The workitemID and flowNamespace are set on the FlowEvent for standard
// event labelling (spec R11). Pass empty strings if unavailable.
func (c *Client) PublishAuditEvent(
	ctx context.Context, eventType string, payload any, workitemID, flowNamespace string,
) error {
	if c.session.EventBus == nil {
		return fmt.Errorf("flow sdk: publish audit event requires Event Bus connection (set EVENT_BUS_ADDRESS)")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("flow sdk: marshal audit payload: %w", err)
	}
	// ponytail: using time-based ID instead of randid (not available in SDK module).
	_, err = c.session.EventBus.Publish(ctx, &flowv1.PublishRequest{
		Channel: "audit",
		Event: &flowv1.FlowEvent{
			EventId:       fmt.Sprintf("%x", time.Now().UnixNano()),
			EventType:     eventType,
			WorkitemId:    workitemID,
			FlowNamespace: flowNamespace,
			Timestamp:     timestamppb.Now(),
			Payload:       raw,
		},
	})
	if err != nil {
		return fmt.Errorf("flow sdk: publish audit event failed: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Telemetry Convenience Methods
// ---------------------------------------------------------------------------

// RecordTelemetry emits a custom telemetry event through the Sidecar to the
// Event Bus. The eventType identifies the event kind (use the "foundry."
// namespace prefix). The payload must be JSON-serializable and at most 64 KB.
// The Sidecar wraps the event in a standard envelope with identity context.
//
// Telemetry emission is non-blocking from the caller's perspective; however,
// the gRPC call itself is synchronous. Delivery failures are returned as
// errors but should not fail work execution.
func (c *Client) RecordTelemetry(ctx context.Context, eventType string, payload []byte) error {
	_, err := c.session.Sidecar.RecordTelemetry(ctx, &flowv1.RecordTelemetryRequest{
		EventType: eventType,
		Payload:   payload,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: record telemetry failed: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// LinkRuling Convenience Method
// ---------------------------------------------------------------------------

// LinkRuling atomically links a judiciary ruling to a deadlocked feedback
// item, transitioning it to the specified terminal state and enabling the
// contempt guard. The feedback must be in DEADLOCKED state and must not
// already have a linked ruling. The targetState must be WONT_FIX or REJECTED.
func (c *Client) LinkRuling(
	ctx context.Context, feedbackID, lawID string, targetState flowv1.FeedbackState,
) (*flowv1.FeedbackItem, error) {
	resp, err := c.session.Archivist.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId:  c.session.workitemID,
		FeedbackId:  feedbackID,
		LawId:       lawID,
		TargetState: targetState,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: link ruling failed: %w", err)
	}
	return resp.GetUpdatedItem(), nil
}

// ---------------------------------------------------------------------------
// QueryFriction Convenience Method
// ---------------------------------------------------------------------------

// QueryFriction returns aggregated friction data from the Friction Ledger.
// Used by judiciary nodes to gather evidence for hearings.
func (c *Client) QueryFriction(
	ctx context.Context, filter *flowv1.FrictionFilter,
) ([]*flowv1.FrictionAggregate, error) {
	resp, err := c.session.FrictionLedger.QueryFriction(ctx, &flowv1.QueryFrictionRequest{
		Filter: filter,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: query friction failed: %w", err)
	}
	return resp.GetFrictionAggregates(), nil
}

// GetLawProto returns the full law object by identifier from the Librarian.
// This is the old flat method retained for backward compatibility during
// migration. It takes a context and returns the proto type. Prefer the new
// GetLaw entry method that does not require a context parameter and returns
// the domain *Law type.
// Deprecated: Use GetLaw without ctx instead.
func (c *Client) GetLawProto(ctx context.Context, lawID string) (*flowv1.Law, error) {
	resp, err := c.session.Librarian.GetLaw(ctx, &flowv1.GetLawRequest{
		LawId: lawID,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get law failed: %w", err)
	}
	return resp.GetLaw(), nil
}

// ---------------------------------------------------------------------------
// Child Workitem Convenience Methods
// ---------------------------------------------------------------------------

// CreateChildWorkitem creates a new child Workitem under the caller's
// current parent Workitem. The child starts in Pending state with no
// assignee. Use the returned ChildWorkitem handle to store artefacts
// and route the child to a target node.
func (c *Client) CreateChildWorkitem(ctx context.Context) (*ChildWorkitem, error) {
	resp, err := c.session.Operator.CreateChildWorkitem(ctx, &flowv1.CreateChildWorkitemRequest{})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: create child workitem failed: %w", err)
	}
	return &ChildWorkitem{
		id:      resp.GetChildWorkitemId(),
		session: c.session,
	}, nil
}

// GetChildren returns the status of all child Workitems created by the
// caller's current Workitem.
func (c *Client) GetChildren(ctx context.Context) ([]ChildWorkitemStatus, error) {
	resp, err := c.session.Operator.GetChildren(ctx, &flowv1.GetChildrenRequest{})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get children failed: %w", err)
	}
	children := make([]ChildWorkitemStatus, 0, len(resp.GetChildren()))
	for _, ch := range resp.GetChildren() {
		children = append(children, ChildWorkitemStatus{
			WorkitemID:      ch.GetWorkitemId(),
			Phase:           ch.GetPhase(),
			CurrentAssignee: ch.GetCurrentAssignee(),
			Artefacts:       ch.GetArtefacts(),
		})
	}
	return children, nil
}

// GetChildArtefact retrieves the latest version of the named artefact from
// the specified child Workitem. The child must be in Completed state and
// must belong to the caller's current Workitem (parent-child validated by
// the Archivist and Sidecar).
func (c *Client) GetChildArtefact(
	ctx context.Context, childWorkitemID, artefactID string,
) (*flowv1.GetArtefactResponse, error) {
	resp, err := c.session.Archivist.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId:       c.session.workitemID,
		ArtefactId:       artefactID,
		TargetWorkitemId: childWorkitemID,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get child artefact failed: %w", err)
	}
	return resp, nil
}

// ListChildArtefacts returns all artefact references from the specified
// child Workitem. The child must be in Completed state and must belong
// to the caller's current Workitem.
func (c *Client) ListChildArtefacts(
	ctx context.Context, childWorkitemID string,
) ([]*flowv1.ArtefactRef, error) {
	resp, err := c.session.Archivist.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId:       c.session.workitemID,
		TargetWorkitemId: childWorkitemID,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: list child artefacts failed: %w", err)
	}
	return resp.GetArtefactRefs(), nil
}

// WatchChildren opens a streaming subscription to the Event Bus on the
// "workitem" channel, filtered by parent_workitem_id matching the caller's
// current Workitem. It returns a channel that receives ChildLifecycleEvent
// values for each phase transition of any child Workitem.
//
// The returned channel is closed when the context is cancelled, the stream
// ends, or an error occurs. Errors are not surfaced through the channel;
// the caller should use context cancellation for lifecycle management.
//
// Requires a direct Event Bus connection (set EVENT_BUS_ADDRESS or use
// WithEventBusAddress). Returns an error if the EventBus client is nil.
func (c *Client) WatchChildren(ctx context.Context) (<-chan ChildLifecycleEvent, error) {
	if c.session.EventBus == nil {
		return nil, fmt.Errorf("flow sdk: watch children requires Event Bus connection (set EVENT_BUS_ADDRESS)")
	}

	stream, err := c.session.EventBus.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: "workitem",
		Filter: &flowv1.SubscribeFilter{
			EventType: "workitem.phase_changed",
			MatchLabels: []*flowv1.Label{
				{Key: "parent_workitem_id", Value: c.session.workitemID},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: watch children subscribe failed: %w", err)
	}

	ch := make(chan ChildLifecycleEvent, 16)
	go func() {
		defer close(ch)
		for {
			evt, recvErr := stream.Recv()
			if recvErr != nil {
				return
			}
			event := ChildLifecycleEvent{
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
			select {
			case ch <- event:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

// ---------------------------------------------------------------------------
// Interceptor — injects workitem context into every outgoing call
// ---------------------------------------------------------------------------

// workitemContextInterceptor attaches x-flow-workitem-id to every outgoing
// gRPC call. This value is used by the Sidecar's identity injection
// interceptor as a session lookup key. The Sidecar overwrites all identity
// metadata (flow_namespace, workitem_id, node_id) with authoritative values from
// the active assignment session before forwarding to upstream services.
// See: specs/05-reference/grpc-api.md#identity-injection
func workitemContextInterceptor(workitemID string) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		if workitemID != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, metadataKeyWorkitemID, workitemID)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
