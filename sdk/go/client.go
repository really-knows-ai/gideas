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
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/durationpb"
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
	workitemID   string
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

// WithWorkitemID overrides the current workitem ID read from FLOW_WORKITEM_ID.
func WithWorkitemID(id string) ClientOption {
	return func(c *clientConfig) {
		c.workitemID = id
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

// ---------------------------------------------------------------------------
// Entry-Point Methods
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
// Options (used by Workitem domain methods)
// ---------------------------------------------------------------------------

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
