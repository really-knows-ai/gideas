// Package flow provides a high-level Go SDK for Foundry Flow nodes.
//
// The Client wraps the generated gRPC service stubs and handles connection
// management, workitem context injection, and convenience methods for common
// operations. All calls are routed through the in-pod Sidecar.
package flow

import (
	"context"
	"fmt"
	"os"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
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
)

// ClientOption configures the Client.
type ClientOption func(*clientConfig)

type clientConfig struct {
	sidecarAddr string
}

// WithSidecarAddress overrides the default Sidecar gRPC address.
func WithSidecarAddress(addr string) ClientOption {
	return func(c *clientConfig) {
		c.sidecarAddr = addr
	}
}

// Client is the primary SDK entry point for Foundry Flow nodes.
// It wraps the generated gRPC clients and provides convenience methods.
type Client struct {
	conn       *grpc.ClientConn
	workitemID string

	// Raw gRPC service clients, exposed for advanced use.
	Sidecar        flowv1.SidecarServiceClient
	Operator       flowv1.OperatorServiceClient
	Archivist      flowv1.ArchivistServiceClient
	Librarian      flowv1.LibrarianServiceClient
	FrictionLedger flowv1.FrictionLedgerServiceClient
	Jury           flowv1.JuryServiceClient
	Clerk          flowv1.ClerkServiceClient
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

	workitemID := os.Getenv(EnvWorkitemID)

	conn, err := grpc.NewClient(
		cfg.sidecarAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(workitemContextInterceptor(workitemID)),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"flow sdk: failed to connect to sidecar at %s: %w (is the sidecar running?)",
			cfg.sidecarAddr, err,
		)
	}

	return &Client{
		conn:           conn,
		workitemID:     workitemID,
		Sidecar:        flowv1.NewSidecarServiceClient(conn),
		Operator:       flowv1.NewOperatorServiceClient(conn),
		Archivist:      flowv1.NewArchivistServiceClient(conn),
		Librarian:      flowv1.NewLibrarianServiceClient(conn),
		FrictionLedger: flowv1.NewFrictionLedgerServiceClient(conn),
		Jury:           flowv1.NewJuryServiceClient(conn),
		Clerk:          flowv1.NewClerkServiceClient(conn),
	}, nil
}

// Close releases the underlying gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// WorkitemID returns the workitem ID read from the environment at init time.
func (c *Client) WorkitemID() string {
	return c.workitemID
}

// ---------------------------------------------------------------------------
// Convenience Methods
// ---------------------------------------------------------------------------

// Heartbeat sends an explicit heartbeat to the Sidecar, resetting the
// inactivity timer. Returns the acknowledged flag.
func (c *Client) Heartbeat(ctx context.Context) (bool, error) {
	resp, err := c.Sidecar.Heartbeat(ctx, &flowv1.HeartbeatRequest{
		WorkitemId: c.workitemID,
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
	_, err := c.Sidecar.PauseTimer(ctx, &flowv1.PauseTimerRequest{
		WorkitemId: c.workitemID,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: pause timer failed: %w", err)
	}
	return nil
}

// ResumeTimer resumes the Sidecar's inactivity timer after a PauseTimer call.
// The timer resets to the full timeout window on resume.
func (c *Client) ResumeTimer(ctx context.Context) error {
	_, err := c.Sidecar.ResumeTimer(ctx, &flowv1.ResumeTimerRequest{
		WorkitemId: c.workitemID,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: resume timer failed: %w", err)
	}
	return nil
}

// Complete submits a routing instruction to the Operator via the Sidecar,
// signalling that the node has finished processing. The routing type
// ROUTING_TYPE_COMPLETE is used with the given target (which can be empty
// for a simple completion).
func (c *Client) Complete(ctx context.Context, target string) (bool, error) {
	resp, err := c.Operator.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: c.workitemID,
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_COMPLETE,
			Target: target,
		},
	})
	if err != nil {
		return false, fmt.Errorf("flow sdk: complete failed: %w", err)
	}
	return resp.GetAccepted(), nil
}

// RouteToOutput submits a routing instruction that routes the workitem through
// the named output channel of the current node. The Operator resolves the
// output name to the target node defined in the FoundryNode CRD.
func (c *Client) RouteToOutput(ctx context.Context, outputName string) (bool, error) {
	resp, err := c.Operator.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: c.workitemID,
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO_OUTPUT,
			Target: outputName,
		},
	})
	if err != nil {
		return false, fmt.Errorf("flow sdk: route to output failed: %w", err)
	}
	return resp.GetAccepted(), nil
}

// GetArtefact retrieves the latest version of the named artefact.
func (c *Client) GetArtefact(ctx context.Context, artefactID string) (*flowv1.GetArtefactResponse, error) {
	resp, err := c.Archivist.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: c.workitemID,
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
	resp, err := c.Archivist.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       c.workitemID,
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
	resp, err := c.Archivist.StampArtefact(ctx, &flowv1.StampArtefactRequest{
		WorkitemId: c.workitemID,
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
	resp, err := c.Archivist.GetStamps(ctx, &flowv1.GetStampsRequest{
		WorkitemId: c.workitemID,
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
	resp, err := c.Archivist.HasStamp(ctx, &flowv1.HasStampRequest{
		WorkitemId: c.workitemID,
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
// The feedback starts in NEW state. Returns the generated feedback ID.
func (c *Client) AddFeedback(
	ctx context.Context, artefactID string, severity flowv1.Severity, message string,
) (string, error) {
	resp, err := c.Archivist.AddFeedback(ctx, &flowv1.AddFeedbackRequest{
		WorkitemId: c.workitemID,
		ArtefactId: artefactID,
		Severity:   severity,
		Message:    message,
	})
	if err != nil {
		return "", fmt.Errorf("flow sdk: add feedback failed: %w", err)
	}
	return resp.GetFeedbackId(), nil
}

// GetFeedback returns all feedback items for the specified artefact.
func (c *Client) GetFeedback(ctx context.Context, artefactID string) ([]*flowv1.FeedbackItem, error) {
	resp, err := c.Archivist.GetFeedback(ctx, &flowv1.GetFeedbackRequest{
		WorkitemId: c.workitemID,
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
	resp, err := c.Archivist.HasUnresolvedFeedback(ctx, &flowv1.HasUnresolvedFeedbackRequest{
		WorkitemId: c.workitemID,
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
	_, err := c.Archivist.ResolveFeedback(ctx, &flowv1.ResolveFeedbackRequest{
		WorkitemId: c.workitemID,
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
	_, err := c.Archivist.RefuseFeedback(ctx, &flowv1.RefuseFeedbackRequest{
		WorkitemId:    c.workitemID,
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
	_, err := c.Archivist.AcceptFix(ctx, &flowv1.AcceptFixRequest{
		WorkitemId: c.workitemID,
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
	_, err := c.Archivist.RejectFix(ctx, &flowv1.RejectFixRequest{
		WorkitemId: c.workitemID,
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
	_, err := c.Archivist.AcceptRefusal(ctx, &flowv1.AcceptRefusalRequest{
		WorkitemId: c.workitemID,
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
	_, err := c.Archivist.RejectRefusal(ctx, &flowv1.RejectRefusalRequest{
		WorkitemId: c.workitemID,
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
	resp, err := c.Archivist.GetFeedbackDepth(ctx, &flowv1.GetFeedbackDepthRequest{
		WorkitemId: c.workitemID,
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
	resp, err := c.Archivist.DeadlockFeedback(ctx, &flowv1.DeadlockFeedbackRequest{
		WorkitemId: c.workitemID,
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
	resp, err := c.Operator.GetFlowTopology(ctx, &flowv1.GetFlowTopologyRequest{})
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
	resp, err := c.Librarian.QueryLaws(ctx, &flowv1.QueryLawsRequest{
		Filter: filter,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: query laws failed: %w", err)
	}
	return resp.GetLaws(), nil
}

// Cite records usage of one or more laws.
func (c *Client) Cite(ctx context.Context, lawIDs ...string) error {
	_, err := c.Librarian.Cite(ctx, &flowv1.CiteRequest{
		LawIds: lawIDs,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: cite failed: %w", err)
	}
	return nil
}

// RecordFinding creates a Tier 1 Finding.
func (c *Client) RecordFinding(
	ctx context.Context, goal string, appliesTo []string, representations []*flowv1.Representation,
) (string, error) {
	resp, err := c.Librarian.RecordFinding(ctx, &flowv1.RecordFindingRequest{
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
	_, err := c.Sidecar.RecordTelemetry(ctx, &flowv1.RecordTelemetryRequest{
		EventType: eventType,
		Payload:   payload,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: record telemetry failed: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Jury Convenience Methods
// ---------------------------------------------------------------------------

// Deliberate conducts a multi-agent deliberation via the Jury service.
// The caller frames the question and assembles evidence; the Jury empanels
// jurors, runs voting rounds, and returns the verdict.
func (c *Client) Deliberate(
	ctx context.Context,
	question, evidence string,
	allowedOutcomes []string,
	strategy flowv1.ConsensusStrategy,
	maxRounds, jurySize int32,
) (*flowv1.DeliberateResponse, error) {
	resp, err := c.Jury.Deliberate(ctx, &flowv1.DeliberateRequest{
		Question:          question,
		Evidence:          evidence,
		AllowedOutcomes:   allowedOutcomes,
		ConsensusStrategy: strategy,
		MaxRounds:         maxRounds,
		JurySize:          jurySize,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: deliberate failed: %w", err)
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Clerk Convenience Methods
// ---------------------------------------------------------------------------

// DraftLaw requests the Clerk to draft a law from a jury verdict and persist
// it via the Librarian. For retire verdicts, retires the law. For demote
// verdicts, writes the law at a lower tier.
func (c *Client) DraftLaw(
	ctx context.Context,
	verdict *flowv1.DeliberateResponse,
	goal string,
	tier int32,
	appliesTo []string,
) (*flowv1.DraftLawResponse, error) {
	resp, err := c.Clerk.DraftLaw(ctx, &flowv1.DraftLawRequest{
		Verdict:   verdict,
		Goal:      goal,
		Tier:      tier,
		AppliesTo: appliesTo,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: draft law failed: %w", err)
	}
	return resp, nil
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
	resp, err := c.Archivist.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId:  c.workitemID,
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
	resp, err := c.FrictionLedger.QueryFriction(ctx, &flowv1.QueryFrictionRequest{
		Filter: filter,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: query friction failed: %w", err)
	}
	return resp.GetFrictionAggregates(), nil
}

// ---------------------------------------------------------------------------
// GetLaw Convenience Method
// ---------------------------------------------------------------------------

// GetLaw returns the full law object by identifier from the Librarian.
// Used by judiciary nodes for hearing evidence retrieval.
func (c *Client) GetLaw(ctx context.Context, lawID string) (*flowv1.Law, error) {
	resp, err := c.Librarian.GetLaw(ctx, &flowv1.GetLawRequest{
		LawId: lawID,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get law failed: %w", err)
	}
	return resp.GetLaw(), nil
}

// ---------------------------------------------------------------------------
// Interceptor — injects workitem context into every outgoing call
// ---------------------------------------------------------------------------

// workitemContextInterceptor attaches x-flow-workitem-id to every outgoing
// gRPC call. This value is used by the Sidecar's identity injection
// interceptor as a session lookup key. The Sidecar overwrites all identity
// metadata (flow_id, workitem_id, node_id) with authoritative values from
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
