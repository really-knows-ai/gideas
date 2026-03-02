// Package service implements the Sidecar's gRPC service handlers.
package service

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const (
	// MetadataKeyNamespace is the gRPC metadata key for the namespace
	// (flow identity boundary). Replaces the former x-flow-flow-id.
	MetadataKeyNamespace = "x-flow-namespace"

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
// incoming metadata with authoritative identity and capability fields.
//
// Per spec (specs/05-reference/grpc-api.md#identity-injection), the Sidecar
// is the sole authority for runtime attribution on node-originated requests.
// Nodes cannot override or spoof these fields. The interceptor operates in
// two modes:
//
//  1. **Session mode**: When x-flow-workitem-id is present and a matching
//     session exists, the interceptor injects x-flow-namespace (from the
//     Sidecar's environment), x-flow-workitem-id, x-flow-node-id, and
//     x-flow-capabilities from the active session.
//
//  2. **Entry-bound fallback**: When no workitem session is found but the
//     Sidecar has namespace and nodeID configured, it injects
//     x-flow-namespace, x-flow-node-id, and x-flow-capabilities (but NOT
//     x-flow-workitem-id). This enables entry-bound node calls such as
//     CreateWorkitem before any assignment exists.
//
// The namespace and nodeID are Sidecar-level constants provided at startup.
// The capabilities string comes from the FLOW_CAPABILITIES environment
// variable set by the Operator.
func IdentityInterceptor(resolver SessionResolver, namespace, nodeID, capabilities string) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			md = metadata.MD{}
		}

		// Try session-based enrichment first.
		vals := md.Get(MetadataKeyWorkitemID)
		if len(vals) > 0 {
			identity := resolver.LookupSession(vals[0])
			if identity != nil {
				slog.Debug("Identity interceptor: injecting session identity",
					"namespace", namespace,
					"workitem_id", identity.WorkitemID,
					"node_id", identity.NodeID,
					"capabilities", capabilities,
					"method", info.FullMethod,
				)

				enriched := md.Copy()
				enriched.Set(MetadataKeyNamespace, namespace)
				enriched.Set(MetadataKeyWorkitemID, identity.WorkitemID)
				enriched.Set(MetadataKeyNodeID, identity.NodeID)
				enriched.Set(MetadataKeyCapabilities, capabilities)

				ctx = metadata.NewIncomingContext(ctx, enriched)
				return handler(ctx, req)
			}
		}

		// Entry-bound fallback: no active workitem session, but the
		// Sidecar knows its namespace and node identity.
		if namespace != "" && nodeID != "" {
			slog.Debug("Identity interceptor: entry-bound fallback",
				"namespace", namespace,
				"node_id", nodeID,
				"capabilities", capabilities,
				"method", info.FullMethod,
			)

			enriched := md.Copy()
			enriched.Set(MetadataKeyNamespace, namespace)
			enriched.Set(MetadataKeyNodeID, nodeID)
			enriched.Set(MetadataKeyCapabilities, capabilities)
			// Do NOT set x-flow-workitem-id — no active assignment.

			ctx = metadata.NewIncomingContext(ctx, enriched)
			return handler(ctx, req)
		}

		return handler(ctx, req)
	}
}
