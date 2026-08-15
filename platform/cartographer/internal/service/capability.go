package service

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"slices"
	"strconv"
	"time"

	"github.com/foundry/flow/cartographer/internal/uuidutil"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// contextKey is an unexported type for context keys to avoid collisions.
type contextKey string

const capabilitiesContextKey contextKey = "capabilities"

// Capabilities holds the parsed, already-verified capability set from the
// gRPC metadata.
type Capabilities struct {
	Caps     []string
	SignedBy string
}

// CapabilityVerifier verifies Ed25519-signed capability attestations in
// gRPC metadata at ingress.
type CapabilityVerifier struct {
	operatorKey     ed25519.PublicKey
	sidecarKey      ed25519.PublicKey
	stalenessWindow time.Duration // negative disables staleness check
}

// NewCapabilityVerifier creates a verifier from the two Ed25519 public keys
// and the anti-replay staleness window. Passing a negative stalenessWindow
// disables the staleness check.
func NewCapabilityVerifier(
	operatorKey, sidecarKey ed25519.PublicKey,
	stalenessWindow time.Duration,
) *CapabilityVerifier {
	return &CapabilityVerifier{
		operatorKey:     operatorKey,
		sidecarKey:      sidecarKey,
		stalenessWindow: stalenessWindow,
	}
}

// VerifyInterceptor is a grpc.UnaryServerInterceptor that verifies the
// Ed25519 capability signature on every RPC at ingress. If the metadata is
// absent (system-to-system Operator call), the interceptor passes the request
// through unmodified — capability-requiring handlers then deny it with
// PERMISSION_DENIED (errCapabilityDenied) because no capabilities were
// extracted, while RPCs that perform no capability checks (ApplySchema,
// WipeGraph, HealthCheck) proceed. If present but unverifiable (bad signature,
// stale timestamp, unknown signer), the interceptor returns PERMISSION_DENIED
// before the handler runs.
func (v *CapabilityVerifier) VerifyInterceptor(
	ctx context.Context, req any,
	info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
) (any, error) {
	ctx, err := v.verify(ctx)
	if err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

// VerifyStreamInterceptor is a grpc.StreamServerInterceptor that verifies the
// Ed25519 capability signature on every streaming RPC at ingress.
func (v *CapabilityVerifier) VerifyStreamInterceptor(
	srv any, ss grpc.ServerStream,
	info *grpc.StreamServerInfo, handler grpc.StreamHandler,
) error {
	ctx, err := v.verify(ss.Context())
	if err != nil {
		return err
	}
	return handler(srv, &wrappedServerStream{ServerStream: ss, ctx: ctx})
}

// wrappedServerStream is a thin wrapper that overrides Context() to return
// the enriched context with stored capabilities.
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}

// verify extracts capability metadata from context, verifies the Ed25519
// signature, checks staleness, and stores the verified Capabilities in the
// context. If metadata is absent, it returns the context unchanged (pass-through
// for system-to-system Operator RPCs).
func (v *CapabilityVerifier) verify(ctx context.Context) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx, nil
	}

	capsList := md.Get(flowmeta.MetadataKeyCapabilities)
	if len(capsList) == 0 {
		// No capabilities metadata = system-to-system call (Operator).
		return ctx, nil
	}
	if len(flowmeta.NormalizeCapabilities(capsList[0])) == 0 {
		// Present-but-empty capabilities metadata (empty or whitespace-only):
		// the request claims a capability attestation but carries no capability
		// entries. Fail closed like any other unverifiable attestation instead
		// of reclassifying the request as a system-to-system pass-through that
		// skips signature and staleness verification (interceptor contract
		// above).
		return nil, errInvalidCapabilitySignature()
	}
	capsStr := capsList[0]

	sigList := md.Get(flowmeta.MetadataKeyCapabilitiesSignature)
	if len(sigList) == 0 || sigList[0] == "" {
		return nil, errInvalidCapabilitySignature()
	}
	sigB64 := sigList[0]

	signedByList := md.Get(flowmeta.MetadataKeyCapabilitiesSignedBy)
	if len(signedByList) == 0 || signedByList[0] == "" {
		return nil, errCapabilitySignedByUnrecognized("")
	}
	signedBy := signedByList[0]

	signedAtList := md.Get(flowmeta.MetadataKeyCapabilitiesSignedAt)
	if len(signedAtList) == 0 || signedAtList[0] == "" {
		// Missing/empty signed-at is the stale-capability trigger (SPEC error
		// table "Stale capability signature (anti-replay)": missing, malformed,
		// or expired).
		return nil, errStaleCapability()
	}
	signedAtStr := signedAtList[0]

	// Select verification key.
	var verificationKey ed25519.PublicKey
	switch signedBy {
	case flowmeta.SignerIdentityOperator:
		verificationKey = v.operatorKey
	case flowmeta.SignerIdentitySidecar:
		verificationKey = v.sidecarKey
	default:
		return nil, errCapabilitySignedByUnrecognized(signedBy)
	}

	// Decode signature.
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, errInvalidCapabilitySignature()
	}

	// Verify signature.
	payload := []byte(capsStr + "|" + signedAtStr)
	if !ed25519.Verify(verificationKey, payload, sig) {
		return nil, errInvalidCapabilitySignature()
	}

	// Staleness check (anti-replay).
	if v.stalenessWindow >= 0 {
		signedAt, err := strconv.ParseInt(signedAtStr, 10, 64)
		if err != nil {
			// Malformed (unparseable) signed-at is the stale-capability trigger
			// (SPEC error table "Stale capability signature (anti-replay)").
			return nil, errStaleCapability()
		}
		elapsed := time.Since(time.Unix(signedAt, 0))
		// Two-sided staleness (SPEC error table "Stale capability signature
		// (anti-replay)"): a future-dated signed-at (elapsed < 0) is as stale
		// as one past the window — a captured attestation replayed with a
		// forged future timestamp must never outlive the anti-replay window.
		if elapsed < 0 || elapsed > v.stalenessWindow {
			return nil, errStaleCapability()
		}
	}

	// Store verified capabilities in context. The shared NormalizeCapabilities
	// helper (pkg/metadata) trims each comma-separated entry and drops empty
	// entries, matching the sibling capability gates (Sidecar nodeCapabilities
	// and SDK CheckCapability) so the authoritative exact-match checks here
	// agree with those gates on the same capability string (SPEC R3 /
	// Capability Authorisation Chain).
	caps := &Capabilities{
		Caps:     flowmeta.NormalizeCapabilities(capsStr),
		SignedBy: signedBy,
	}
	ctx = StoreCapabilitiesInContext(ctx, caps)
	return ctx, nil
}

// StoreCapabilitiesInContext stores the verified capability set in the context.
func StoreCapabilitiesInContext(ctx context.Context, caps *Capabilities) context.Context {
	return context.WithValue(ctx, capabilitiesContextKey, caps)
}

// ExtractCapabilities returns the parsed, already-verified capability set
// from the context. Returns nil, nil when metadata is absent (system-to-system
// Operator call).
func ExtractCapabilities(ctx context.Context) (*Capabilities, error) {
	caps, ok := ctx.Value(capabilitiesContextKey).(*Capabilities)
	if !ok || caps == nil {
		return nil, nil
	}
	return caps, nil
}

// CheckSpecificType checks that caps contains a capability matching
// "<prefix>:graph/entity/<entityType>".
func (v *CapabilityVerifier) CheckSpecificType(caps *Capabilities, requiredCapPrefix string, entityType string) error {
	if caps == nil {
		return errCapabilityDenied(requiredCapPrefix + ":graph/entity/" + entityType)
	}
	required := requiredCapPrefix + ":graph/entity/" + entityType
	if slices.Contains(caps.Caps, required) {
		return nil
	}
	return errCapabilityDenied(required)
}

// CheckWildcard checks that caps contains a capability matching
// "<requiredCapPrefix>:graph/entity/*". Returns errWildcardMissing if
// the wildcard is absent (handler branching sentinel, NOT a gRPC error).
func (v *CapabilityVerifier) CheckWildcard(caps *Capabilities, requiredCapPrefix string) error {
	if caps == nil {
		return errWildcardMissing
	}
	required := requiredCapPrefix + ":graph/entity/*"
	if slices.Contains(caps.Caps, required) {
		return nil
	}
	return errWildcardMissing
}

func isValidUUID(s string) bool {
	return uuidutil.Validate(s) == nil
}

// validateTxID checks that txID is a valid UUID v4 if non-empty.
func validateTxID(txID string) error {
	if txID == "" {
		return nil
	}
	if !isValidUUID(txID) {
		return errInvalidTransactionIDFormat(txID)
	}
	return nil
}

// checkEntityCap implements Mode 1 + Mode 2 capability checking for entity
// operations. It first checks for a specific type (<prefix>:graph/entity/<type>),
// then falls back to the wildcard (<prefix>:graph/entity/*).
func (s *CartographerServer) checkEntityCap(ctx context.Context, prefix, entityType string) error {
	return s.checkCap(ctx, prefix+":graph/entity/"+entityType, prefix+":graph/entity/*")
}

// checkTxCap checks that the caller holds the exact required transaction
// capability (e.g. "WRITE:graph/tx" or "READ:graph/tx").
func (s *CartographerServer) checkTxCap(ctx context.Context, required string) error {
	return s.checkCap(ctx, required)
}

// checkWildcardEntityCap checks that the caller holds the wildcard entity
// capability (<prefix>:graph/entity/*). It uses already-verified capabilities
// from the context (stored by the ingress interceptor verify()).
func (s *CartographerServer) checkWildcardEntityCap(ctx context.Context, prefix string) error {
	return s.checkCap(ctx, prefix+":graph/entity/*")
}

// checkCap is the shared capability gate behind checkEntityCap, checkTxCap,
// and checkWildcardEntityCap: it denies PERMISSION_DENIED (errCapabilityDenied
// naming the primary required capability — the first accepted string) unless
// the caller's verified capabilities contain at least one of the accepted
// capability strings. It uses already-verified capabilities from the context
// (stored by the ingress interceptor verify()).
func (s *CartographerServer) checkCap(ctx context.Context, accepted ...string) error {
	required := accepted[0]
	caps, err := ExtractCapabilities(ctx)
	if err != nil {
		return err
	}
	if caps == nil {
		return errCapabilityDenied(required)
	}
	for _, candidate := range accepted {
		if slices.Contains(caps.Caps, candidate) {
			return nil
		}
	}
	return errCapabilityDenied(required)
}
