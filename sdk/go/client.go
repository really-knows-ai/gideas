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
	Sidecar   flowv1.SidecarServiceClient
	Operator  flowv1.OperatorServiceClient
	Archivist flowv1.ArchivistServiceClient
	Librarian flowv1.LibrarianServiceClient
	Monitor   flowv1.FlowMonitorServiceClient
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
		conn:       conn,
		workitemID: workitemID,
		Sidecar:    flowv1.NewSidecarServiceClient(conn),
		Operator:   flowv1.NewOperatorServiceClient(conn),
		Archivist:  flowv1.NewArchivistServiceClient(conn),
		Librarian:  flowv1.NewLibrarianServiceClient(conn),
		Monitor:    flowv1.NewFlowMonitorServiceClient(conn),
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
// Monitor Convenience Methods
// ---------------------------------------------------------------------------

// RecordTelemetry emits a custom telemetry event through the Sidecar to the
// Flow Monitor. The eventType identifies the event kind (use the "foundry."
// namespace prefix). The payload must be JSON-serializable and at most 64 KB.
// The Sidecar wraps the event in a standard envelope with identity context.
//
// Telemetry emission is non-blocking from the caller's perspective; however,
// the gRPC call itself is synchronous. Delivery failures are returned as
// errors but should not fail work execution.
func (c *Client) RecordTelemetry(ctx context.Context, eventType string, payload []byte) error {
	_, err := c.Monitor.RecordTelemetry(ctx, &flowv1.RecordTelemetryRequest{
		EventType: eventType,
		Payload:   payload,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: record telemetry failed: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Interceptor — injects workitem context into every outgoing call
// ---------------------------------------------------------------------------

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
