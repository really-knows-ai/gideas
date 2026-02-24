// Package service implements the Sidecar's gRPC service handlers.
package service

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const (
	// MetadataKeyFlowID is the gRPC metadata key for the flow identity.
	MetadataKeyFlowID = "x-flow-flow-id"

	// MetadataKeyWorkitemID is the gRPC metadata key for the workitem identity.
	MetadataKeyWorkitemID = "x-flow-workitem-id"

	// MetadataKeyNodeID is the gRPC metadata key for the node identity.
	MetadataKeyNodeID = "x-flow-node-id"

	// MetadataKeyCapabilities is the gRPC metadata key carrying the
	// comma-separated capability grants for the node. Injected by the
	// Sidecar from the FLOW_CAPABILITIES environment variable (set by the
	// Operator during pod construction). Owning services read this header
	// to enforce capability-gated access.
	MetadataKeyCapabilities = "x-flow-capabilities"
)

// SessionResolver looks up active assignment sessions by workitem ID.
// This interface decouples the interceptor from the concrete SidecarServer.
type SessionResolver interface {
	LookupSession(workitemID string) *SessionIdentity
}

// IdentityInterceptor returns a gRPC unary server interceptor that enriches
// incoming metadata with authoritative identity and capability fields from
// the active assignment session.
//
// Per spec (specs/05-reference/grpc-api.md#identity-injection), the Sidecar
// is the sole authority for runtime attribution on node-originated requests.
// Nodes cannot override or spoof these fields. The interceptor:
//
//  1. Reads x-flow-workitem-id from the incoming metadata (SDK-supplied).
//  2. Looks up the active session to retrieve flow_id and node_id.
//  3. Overwrites/injects x-flow-flow-id, x-flow-workitem-id,
//     x-flow-node-id, and x-flow-capabilities in the incoming metadata
//     so that all downstream proxy handlers (via propagateMetadata)
//     forward authoritative identity and capability context.
//
// The capabilities string is provided at Sidecar startup (from the
// FLOW_CAPABILITIES environment variable set by the Operator). It is
// injected on every enriched request so that owning services can enforce
// deny-by-default capability checks.
//
// If no session is found for the workitem_id (e.g. SidecarService or
// Operator-facing RPCs that don't originate from node code), the context
// is passed through unmodified.
func IdentityInterceptor(resolver SessionResolver, capabilities string) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return handler(ctx, req)
		}

		// Extract the SDK-supplied workitem_id from incoming metadata.
		vals := md.Get(MetadataKeyWorkitemID)
		if len(vals) == 0 {
			return handler(ctx, req)
		}
		workitemID := vals[0]

		// Look up the active session for this workitem.
		identity := resolver.LookupSession(workitemID)
		if identity == nil {
			return handler(ctx, req)
		}

		slog.Debug("Identity interceptor: injecting session identity",
			"flow_id", identity.FlowID,
			"workitem_id", identity.WorkitemID,
			"node_id", identity.NodeID,
			"capabilities", capabilities,
			"method", info.FullMethod,
		)

		// Clone the metadata and overwrite identity fields with
		// authoritative values from the session. This prevents node
		// code from spoofing identity or capabilities.
		enriched := md.Copy()
		enriched.Set(MetadataKeyFlowID, identity.FlowID)
		enriched.Set(MetadataKeyWorkitemID, identity.WorkitemID)
		enriched.Set(MetadataKeyNodeID, identity.NodeID)
		enriched.Set(MetadataKeyCapabilities, capabilities)

		ctx = metadata.NewIncomingContext(ctx, enriched)
		return handler(ctx, req)
	}
}
