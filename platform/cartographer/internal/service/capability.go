package service

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

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
func NewCapabilityVerifier(operatorKey, sidecarKey ed25519.PublicKey, stalenessWindow time.Duration) *CapabilityVerifier {
	return &CapabilityVerifier{
		operatorKey:     operatorKey,
		sidecarKey:      sidecarKey,
		stalenessWindow: stalenessWindow,
	}
}

// VerifyInterceptor is a grpc.UnaryServerInterceptor that verifies the
// Ed25519 capability signature on every RPC at ingress. If the metadata is
// absent (system-to-system Operator call), the interceptor passes the request
// through — the handler then skips capability checks. If present but
// unverifiable (bad signature, stale timestamp, unknown signer), the
// interceptor returns PERMISSION_DENIED before the handler runs.
func (v *CapabilityVerifier) VerifyInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	ctx, err := v.verify(ctx)
	if err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

// VerifyStreamInterceptor is a grpc.StreamServerInterceptor that verifies the
// Ed25519 capability signature on every streaming RPC at ingress.
func (v *CapabilityVerifier) VerifyStreamInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
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

	capsList := md.Get(MetadataKeyCapabilities)
	if len(capsList) == 0 || capsList[0] == "" {
		// No capabilities metadata = system-to-system call (Operator).
		return ctx, nil
	}
	capsStr := capsList[0]

	sigList := md.Get(MetadataKeyCapabilitiesSignature)
	if len(sigList) == 0 || sigList[0] == "" {
		return nil, errInvalidCapabilitySignature()
	}
	sigB64 := sigList[0]

	signedByList := md.Get(MetadataKeyCapabilitiesSignedBy)
	if len(signedByList) == 0 || signedByList[0] == "" {
		return nil, errCapabilitySignedByUnrecognized("")
	}
	signedBy := signedByList[0]

	signedAtList := md.Get(MetadataKeyCapabilitiesSignedAt)
	if len(signedAtList) == 0 || signedAtList[0] == "" {
		return nil, errInvalidCapabilitySignature()
	}
	signedAtStr := signedAtList[0]

	// Select verification key.
	var verificationKey ed25519.PublicKey
	switch signedBy {
	case "operator":
		verificationKey = v.operatorKey
	case "sidecar":
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
			return nil, errInvalidCapabilitySignature()
		}
		elapsed := time.Since(time.Unix(signedAt, 0))
		if elapsed > v.stalenessWindow {
			return nil, errStaleCapability()
		}
	}

	// Store verified capabilities in context.
	caps := &Capabilities{
		Caps:     strings.Split(capsStr, ","),
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
	for _, c := range caps.Caps {
		if c == required {
			return nil
		}
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
	for _, c := range caps.Caps {
		if c == required {
			return nil
		}
	}
	return errWildcardMissing
}

// CheckCapability combines extract + verify + match in one call for handlers
// that don't need Mode 1/Mode 2 branching.
// impl-note: This re-implements the full verification flow (reading metadata,
// selecting verifier key, decoding signature, verifying Ed25519, parsing
// capabilities) independently of the interceptor-based flow in VerifyInterceptor.
// Notably it does not include the staleness check (anti-replay) that the
// interceptor performs, so error semantics differ: a stale capability that
// the interceptor would reject with PERMISSION_DENIED may be accepted here.
// Production handlers should rely on ExtractCapabilities after the interceptor
// has run; this method exists as a defence-in-depth path for test contexts
// that bypass the interceptor.
func (v *CapabilityVerifier) CheckCapability(ctx context.Context, required string) error {
	// Re-verify signature from metadata (defence-in-depth for tests).
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return errCapabilityDenied(required)
	}

	capsList := md.Get(MetadataKeyCapabilities)
	if len(capsList) == 0 || capsList[0] == "" {
		return errCapabilityDenied(required)
	}
	capsStr := capsList[0]

	sigList := md.Get(MetadataKeyCapabilitiesSignature)
	if len(sigList) == 0 || sigList[0] == "" {
		return errInvalidCapabilitySignature()
	}
	sigB64 := sigList[0]

	signedByList := md.Get(MetadataKeyCapabilitiesSignedBy)
	if len(signedByList) == 0 || signedByList[0] == "" {
		return errCapabilityDenied(required)
	}
	signedBy := signedByList[0]

	signedAtList := md.Get(MetadataKeyCapabilitiesSignedAt)
	if len(signedAtList) == 0 || signedAtList[0] == "" {
		return errInvalidCapabilitySignature()
	}
	signedAtStr := signedAtList[0]

	var verificationKey ed25519.PublicKey
	switch signedBy {
	case "operator":
		verificationKey = v.operatorKey
	case "sidecar":
		verificationKey = v.sidecarKey
	default:
		return errCapabilityDenied(required)
	}

	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return errInvalidCapabilitySignature()
	}

	payload := []byte(capsStr + "|" + signedAtStr)
	if !ed25519.Verify(verificationKey, payload, sig) {
		return errInvalidCapabilitySignature()
	}

	// Check required capability.
	caps := strings.Split(capsStr, ",")
	for _, c := range caps {
		if c == required {
			return nil
		}
	}
	return errCapabilityDenied(required)
}

// checkTxCap is a helper for checking WRITE:graph/tx or READ:graph/tx capabilities.
func (v *CapabilityVerifier) checkTxCap(ctx context.Context, required string) error {
	caps, err := ExtractCapabilities(ctx)
	if err != nil {
		return err
	}
	if caps == nil {
		return errCapabilityDenied(required)
	}
	for _, c := range caps.Caps {
		if c == required {
			return nil
		}
	}
	return errCapabilityDenied(required)
}

// checkEntityCap is a helper for checking entity-level capabilities
// (Mode 1 — specific type).
// impl-note: Checks only the exact "<prefix>:graph/entity/<entityType>" match
// via CheckSpecificType. Does not fall back to checking
// "<prefix>:graph/entity/*" as a wildcard. Every caller must independently
// implement wildcard fallback logic when the entity type may be unknown or a
// broad capability is acceptable.
func (v *CapabilityVerifier) checkEntityCap(ctx context.Context, prefix, entityType string) error {
	caps, err := ExtractCapabilities(ctx)
	if err != nil {
		return err
	}
	if caps == nil {
		return errCapabilityDenied(prefix + ":graph/entity/" + entityType)
	}
	return v.CheckSpecificType(caps, prefix, entityType)
}

// isValidUUID returns true if s looks like a UUID v4 (basic format check).
// ponytail: Only validates hex characters and dash positions — does not verify
// the UUID v4 version nibble (position 14 must be '4') or variant bits
// (position 19 must be 8/9/a/b). Non-v4 UUIDs (v1, v2, v3, v5) pass
// validation. Consequences: (1) SPEC violation — non-v4 UUIDs are accepted
// despite SPEC requiring "valid UUID v4" for entity, edge, and transaction IDs
// (INVALID_ARGUMENT per SPEC error table). (2) v1 UUIDs encode creation
// timestamps, leaking temporal information about ID generation; v3/v5 UUIDs
// are deterministic from namespace/name inputs, enabling inference of inputs
// by enumerating observed IDs. (3) Per-version uniqueness and collision
// characteristics differ — while collision probability is negligible for all
// UUID versions, environments that audit or validate for v4 compliance would
// report false positives on accepted non-v4 IDs. Deployment-context risk:
// caller-supplied entity/edge IDs and proxy-forwarded transaction IDs are the
// primary external source; the Cartographer's own auto-generation produces
// correct UUID v4. Upgrade path: add s[14] == '4' version nibble check and
// s[19] variant check accepting '8','9','a','A','b','B' (both cases — the
// hex check accepts A-F).
func isValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
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

// branchForTx returns the LadybugDB branch name for a transaction.
func branchForTx(txID string) string {
	if txID == "" {
		return ""
	}
	return txID
}
