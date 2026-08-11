// Package service implements the Sidecar's gRPC service handlers.
package service

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strconv"
	"time"

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

	// MetadataKeyCapabilitiesSignature is the gRPC metadata key carrying the
	// base64-encoded Ed25519 signature over
	// "{x-flow-capabilities}|{x-flow-capabilities-signed-at}" (SPEC R3 /
	// Capability Authorisation Chain). Injected by the Sidecar alongside
	// MetadataKeyCapabilities so the Cartographer can verify the attested
	// capabilities on ingress.
	MetadataKeyCapabilitiesSignature = "x-flow-capabilities-signature"

	// MetadataKeyCapabilitiesSignedBy is the gRPC metadata key selecting the
	// verification key at the Cartographer ("sidecar" or "operator").
	MetadataKeyCapabilitiesSignedBy = "x-flow-capabilities-signed-by"

	// MetadataKeyCapabilitiesSignedAt is the gRPC metadata key carrying the
	// Unix timestamp (seconds) used for the anti-replay staleness check.
	MetadataKeyCapabilitiesSignedAt = "x-flow-capabilities-signed-at"

	// signerIdentitySidecar is the value carried in MetadataKeyCapabilitiesSignedBy
	// for capabilities signed by the Sidecar's shared signing key.
	signerIdentitySidecar = "sidecar"
)

// IdentityInterceptor returns a gRPC unary server interceptor that enriches
// incoming metadata with authoritative identity and capability fields.
//
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
// Whenever x-flow-capabilities is injected and a signingKey is configured,
// the interceptor also appends x-flow-capabilities-signature (Ed25519 over
// "{capabilities}|{unix-seconds}"), x-flow-capabilities-signed-by=sidecar,
// and x-flow-capabilities-signed-at so the Cartographer can verify the
// attestation on ingress (SPEC R3 / Capability Authorisation Chain). A
// configured signing key that is not a valid Ed25519 private key fails the
// RPC (fail closed — an unsigned capability attestation would be rejected by
// the Cartographer anyway).
//
// The namespace and nodeID are Sidecar-level constants provided at startup.
// The capabilities string comes from the FLOW_CAPABILITIES environment
// variable set by the Operator.
func IdentityInterceptor(
	server *SidecarServer, namespace, nodeID, capabilities string, signingKey ed25519.PrivateKey,
) grpc.UnaryServerInterceptor {
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

		enriched, err := enrichIdentity(server, namespace, nodeID, capabilities, signingKey, md, info.FullMethod)
		if err != nil {
			return nil, err
		}
		if enriched == nil {
			return handler(ctx, req)
		}
		ctx = metadata.NewIncomingContext(ctx, enriched)
		return handler(ctx, req)
	}
}

// IdentityStreamInterceptor is the streaming variant of IdentityInterceptor.
// It enriches the incoming metadata of server-streaming RPCs (e.g. the
// Cartographer's ExportGraph) so streamed requests carry the same identity
// and signed capability metadata as unary ones.
func IdentityStreamInterceptor(
	server *SidecarServer, namespace, nodeID, capabilities string, signingKey ed25519.PrivateKey,
) grpc.StreamServerInterceptor {
	return func(
		srv any, ss grpc.ServerStream,
		info *grpc.StreamServerInfo, handler grpc.StreamHandler,
	) error {
		md, ok := metadata.FromIncomingContext(ss.Context())
		if !ok {
			md = metadata.MD{}
		}

		enriched, err := enrichIdentity(server, namespace, nodeID, capabilities, signingKey, md, info.FullMethod)
		if err != nil {
			return err
		}
		if enriched == nil {
			return handler(srv, ss)
		}
		ctx := metadata.NewIncomingContext(ss.Context(), enriched)
		return handler(srv, &identityWrappedStream{ServerStream: ss, ctx: ctx})
	}
}

// identityWrappedStream overrides Context() so the stream handler observes
// the identity-enriched incoming metadata.
type identityWrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the identity-enriched context.
func (w *identityWrappedStream) Context() context.Context { return w.ctx }

// enrichIdentity applies the Sidecar's authoritative identity and capability
// metadata to the incoming metadata, signing the capabilities when a signing
// key is configured. Returns (nil, nil) when no enrichment applies (no
// session and no entry-bound identity), in which case the caller passes the
// request through unmodified.
func enrichIdentity(
	server *SidecarServer, namespace, nodeID, capabilities string, signingKey ed25519.PrivateKey,
	md metadata.MD, method string,
) (metadata.MD, error) {
	// Try session-based enrichment first.
	vals := md.Get(MetadataKeyWorkitemID)
	if len(vals) > 0 {
		identity := server.LookupSession(vals[0])
		if identity != nil {
			slog.Info("Identity interceptor: injecting session identity",
				"namespace", namespace,
				"workitem_id", identity.WorkitemID,
				"node_id", identity.NodeID,
				"capabilities", capabilities,
				"method", method,
			)

			enriched := md.Copy()
			enriched.Set(MetadataKeyNamespace, namespace)
			enriched.Set(MetadataKeyWorkitemID, identity.WorkitemID)
			enriched.Set(MetadataKeyNodeID, identity.NodeID)
			enriched.Set(MetadataKeyCapabilities, capabilities)
			return signCapabilities(enriched, capabilities, signingKey)
		}
		slog.Info("Identity interceptor: no session found for workitem",
			"requested_workitem_id", vals[0],
			"method", method,
		)
	}

	// Entry-bound fallback: no active workitem session, but the
	// Sidecar knows its namespace and node identity.
	if namespace != "" && nodeID != "" {
		slog.Info("Identity interceptor: entry-bound fallback",
			"namespace", namespace,
			"node_id", nodeID,
			"capabilities", capabilities,
			"method", method,
		)

		enriched := md.Copy()
		enriched.Set(MetadataKeyNamespace, namespace)
		enriched.Set(MetadataKeyNodeID, nodeID)
		enriched.Set(MetadataKeyCapabilities, capabilities)
		// Do NOT set x-flow-workitem-id — no active assignment.
		return signCapabilities(enriched, capabilities, signingKey)
	}

	return nil, nil
}

// signCapabilities appends the Ed25519 capability-attestation metadata
// (x-flow-capabilities-signature/-signed-by/-signed-at) to md whenever a
// signing key is configured (SPEC R3 / Capability Authorisation Chain). The
// signed payload is the UTF-8 encoding of "{capabilities}|{unix-seconds}".
//
// A configured signing key that is not a valid Ed25519 private key fails
// closed: the unsigned request must never be forwarded, because the
// Cartographer would reject it and the caller would be left with a request
// the Sidecar cannot authoritatively attest.
func signCapabilities(md metadata.MD, capabilities string, signingKey ed25519.PrivateKey) (metadata.MD, error) {
	if len(signingKey) == 0 {
		return md, nil // No signing key configured — legacy mode, no attestation.
	}
	if len(signingKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf(
			"identity interceptor: sidecar signing key has wrong length %d (want %d); refusing to forward unsigned capabilities",
			len(signingKey), ed25519.PrivateKeySize,
		)
	}
	signedAt := strconv.FormatInt(time.Now().Unix(), 10)
	payload := []byte(capabilities + "|" + signedAt)
	sig := ed25519.Sign(signingKey, payload)
	md.Set(MetadataKeyCapabilitiesSignature, base64.StdEncoding.EncodeToString(sig))
	md.Set(MetadataKeyCapabilitiesSignedBy, signerIdentitySidecar)
	md.Set(MetadataKeyCapabilitiesSignedAt, signedAt)
	return md, nil
}
