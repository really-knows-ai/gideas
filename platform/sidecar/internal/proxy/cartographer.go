// Package proxy implements forwarding handlers that relay gRPC calls
// from the Sidecar to the real cluster services.
package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowmeta "github.com/foundry/flow/pkg/metadata"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// CartographerProxy implements flowv1.CartographerServiceServer by forwarding
// the node-facing graph RPCs to the real Cartographer gRPC endpoint, enforcing
// the SPEC R3 capability model at the Sidecar (Capability Authorisation
// Chain):
//
//   - mode 1 (specific entity type known): the caller must hold
//     <verb>:graph/entity/<type> (a <verb>:graph/entity/* grant also matches)
//     or the request is rejected with PERMISSION_DENIED.
//   - mode 2 (type unknown / wildcard-resolvable): the caller's
//     <verb>:graph/entity/* grant is matched best-effort (logged, attested)
//     but never blocks — the Cartographer is the authoritative per-type
//     enforcer.
//   - fixed requirements (transaction RPCs, Sync, ExportGraph): the caller
//     must hold the exact required capability or the request is rejected.
//
// The identity interceptor (service.IdentityInterceptor) injects the signed
// capability metadata (x-flow-capabilities[-signature|-signed-by|-signed-at])
// into every request before the proxy sees it; this proxy only decides
// whether the attested capabilities permit the RPC to be forwarded.
type CartographerProxy struct {
	flowv1.UnimplementedCartographerServiceServer
	client flowv1.CartographerServiceClient
	conn   *grpc.ClientConn
}

// NewCartographerProxy dials the Cartographer gRPC endpoint and returns a
// proxy handler ready to be registered on the Sidecar's gRPC server.
func NewCartographerProxy(cartographerAddr string) (*CartographerProxy, error) {
	conn, err := dialService(cartographerAddr)
	if err != nil {
		return nil, err
	}

	return &CartographerProxy{
		client: flowv1.NewCartographerServiceClient(conn),
		conn:   conn,
	}, nil
}

// Close releases the underlying gRPC connection to the Cartographer.
func (p *CartographerProxy) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// nodeCapabilities returns the node's attested capability grants from the
// incoming metadata. Returns nil only when the request is not node-originated
// (no x-flow-node-id), in which case the proxy passes the request through and
// the Cartographer's ingress verifier is the security boundary. Node-originated
// requests always return a non-nil slice — empty when the node holds no grants —
// so an empty grant set is denied by mode-1 checks instead of being mistaken
// for a system-to-system call.
func nodeCapabilities(ctx context.Context) []string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil
	}
	if len(md.Get(flowmeta.MetadataKeyNodeID)) == 0 {
		return nil
	}
	caps := []string{}
	for _, c := range md.Get(flowmeta.MetadataKeyCapabilities) {
		for cap := range strings.SplitSeq(c, ",") {
			if cap = strings.TrimSpace(cap); cap != "" {
				caps = append(caps, cap)
			}
		}
	}
	return caps
}

// entityTypeFromMetadata returns the first entity_type metadata value attached
// by the SDK (SPEC R3: the SDK annotates the resolved entity type, or "*" when
// the ID-to-type mapping is unknown or TTL-stale). Returns "" when no type is
// resolvable.
func entityTypeFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(flowmeta.MetadataKeyEntityType)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// checkCapability gates a Cartographer RPC on the caller's attested
// capabilities (SPEC R3 / Capability Authorisation Chain).
//
// resource is the full required capability path ("graph/entity/Component",
// "graph/entity/*", or "graph/tx").
//
// block=true implements mode 1 or a fixed exact requirement: the caller must
// hold <verb>:<resource> or the request is rejected with PERMISSION_DENIED.
// Matching follows the Cartographer's authoritative exact-string semantics
// (grantMatches): a grant equals the requirement exactly, or is the literal
// "<verb>:graph/entity/*" wildcard satisfying a type-specific requirement —
// filepath metacharacters such as "Comp*" or "Compon?nt" are literal, never
// wildcards.
// block=false implements mode 2: the wildcard grant is matched best-effort
// and the outcome logged, but the request always passes through — the
// Cartographer is the authoritative per-type enforcer.
//
// Non-node-originated requests (system-to-system) pass through unchecked.
func (p *CartographerProxy) checkCapability(ctx context.Context, verb, resource string, block bool) error {
	caps := nodeCapabilities(ctx)
	if caps == nil {
		return nil
	}
	required := verb + ":" + resource
	for _, c := range caps {
		if grantMatches(c, required) {
			slog.Debug("Sidecar: Cartographer capability granted",
				"capability", c, "required", required)
			return nil
		}
	}
	if !block {
		// Mode 2: wildcard best-effort — log + attest, do not block.
		slog.Info("Sidecar: Cartographer capability check (mode 2) wildcard best-effort — attest only",
			"required", required)
		return nil
	}
	slog.Warn("Sidecar: Cartographer capability denied",
		"required", required)
	return status.Errorf(codes.PermissionDenied,
		"CAPABILITY_DENIED: missing required capability %q", required)
}

// grantMatches reports whether the held capability grant satisfies the
// required capability under the Cartographer's authoritative exact-string
// semantics (SPEC R3 / Capability Authorisation Chain): a grant matches only
// when it equals the requirement exactly, or is the literal wildcard
// "<verb>:graph/entity/*" while the requirement is a type-specific
// "<verb>:graph/entity/<type>" (SPEC R3:241-242 — the wildcard authorises all
// types). Only a full-segment literal "*" is a wildcard: a grant such as
// "WRITE:graph/entity/Comp*" or "WRITE:graph/entity/Compon?nt" is treated as
// a literal string and never matches, mirroring the Cartographer's
// CheckSpecificType/CheckWildcard slices.Contains gates so the two gates of
// the chain can never silently disagree on the same grant.
func grantMatches(held, required string) bool {
	if held == required {
		return true
	}
	prefix, entityType, ok := strings.Cut(required, ":graph/entity/")
	if !ok || entityType == "*" {
		return false
	}
	return held == prefix+":graph/entity/*"
}

// checkReadByType enforces the READ gate for read RPCs whose entity type is
// known from the request body (SPEC R3:262): a specific type is a mode-1
// check against <verb>:graph/entity/<type>; an omitted type is an all-types
// search and is also a mode-1 check against <verb>:graph/entity/* — a per-type
// grant cannot authorise it (a wildcard grant matches via grantMatches).
func (p *CartographerProxy) checkReadByType(ctx context.Context, entityType string) error {
	if entityType != "" {
		return p.checkCapability(ctx, "READ", "graph/entity/"+entityType, true)
	}
	return p.checkCapability(ctx, "READ", "graph/entity/*", true)
}

// checkWriteByType enforces the WRITE gate for write RPCs whose entity type is
// known from the request body (CreateEntity) or resolved by the SDK into
// entity_type metadata (UpdateEntity, DeleteEntity, CreateEdge; SPEC R3:252):
// a specific type is a mode-1 check against WRITE:graph/entity/<type>; an
// omitted or wildcard type is the all-types mode-2 check against
// WRITE:graph/entity/* — best-effort, never blocks, with the Cartographer
// authoritative on ingress.
func (p *CartographerProxy) checkWriteByType(ctx context.Context, entityType string) error {
	if entityType == "" || entityType == "*" {
		return p.checkCapability(ctx, "WRITE", "graph/entity/*", false)
	}
	return p.checkCapability(ctx, "WRITE", "graph/entity/"+entityType, true)
}

// checkWriteMetadataType enforces the WRITE gate for write RPCs whose entity
// type is derived from the SDK's local ID-to-type mapping and carried in
// entity_type metadata (UpdateEntity, DeleteEntity, CreateEdge; SPEC R3:252 /
// Capability Authorisation Chain). The SDK annotates the specific resolved
// type only when its TTL-fresh mapping knows the ID (the entity was created or
// fetched through the same SDK client); unknown or stale (TTL-expired) IDs are
// annotated "*". A specific type is the chain's mode-1 case: the Sidecar
// validates the caller against WRITE:graph/entity/<type> and blocks with
// PERMISSION_DENIED when it is lacking. A "*" or absent annotation is the
// mode-2 case: the WRITE:graph/entity/* wildcard check is best-effort — it
// never blocks — and the Cartographer performs the authoritative type-specific
// check on ingress as the correctness net.
func (p *CartographerProxy) checkWriteMetadataType(ctx context.Context) error {
	return p.checkWriteByType(ctx, entityTypeFromMetadata(ctx))
}

// ---------------------------------------------------------------------------
// Read path (node-facing)
// ---------------------------------------------------------------------------

// ExecuteCypher is always mode 2 at the Sidecar (SPEC:734): the entity types
// referenced by the statement are unknowable until it is parsed, so the
// wildcard READ grant is checked best-effort and the authoritative per-type
// check happens at the Cartographer from its own server-side parse.
func (p *CartographerProxy) ExecuteCypher(
	ctx context.Context, req *flowv1.ExecuteCypherRequest,
) (*flowv1.ExecuteCypherResponse, error) {
	if err := p.checkCapability(ctx, "READ", "graph/entity/*", false); err != nil {
		return nil, err
	}
	return p.client.ExecuteCypher(ctx, req)
}

// SearchNeighbors validates the READ grant against the requested entity type
// from the request body (mode 1); an omitted type is an all-types search that
// requires READ:graph/entity/* (SPEC R3:262).
func (p *CartographerProxy) SearchNeighbors(
	ctx context.Context, req *flowv1.SearchNeighborsRequest,
) (*flowv1.SearchNeighborsResponse, error) {
	if err := p.checkReadByType(ctx, req.GetEntityType()); err != nil {
		return nil, err
	}
	return p.client.SearchNeighbors(ctx, req)
}

// FullTextSearch validates the READ grant against the requested entity type
// from the request body (mode 1); an omitted type is an all-types search that
// requires READ:graph/entity/* (SPEC R3:262).
func (p *CartographerProxy) FullTextSearch(
	ctx context.Context, req *flowv1.FullTextSearchRequest,
) (*flowv1.FullTextSearchResponse, error) {
	if err := p.checkReadByType(ctx, req.GetEntityType()); err != nil {
		return nil, err
	}
	return p.client.FullTextSearch(ctx, req)
}

// ListEntities validates the READ grant against the requested entity type from
// the request body (mode 1); an omitted type is an all-types search that
// requires READ:graph/entity/* (SPEC R3:262).
func (p *CartographerProxy) ListEntities(
	ctx context.Context, req *flowv1.ListEntitiesRequest,
) (*flowv1.ListEntitiesResponse, error) {
	if err := p.checkReadByType(ctx, req.GetEntityType()); err != nil {
		return nil, err
	}
	return p.client.ListEntities(ctx, req)
}

// ---------------------------------------------------------------------------
// Write path (node-facing)
// ---------------------------------------------------------------------------

// CreateEntity validates the WRITE grant against the entity type from the
// request body (mode 1).
func (p *CartographerProxy) CreateEntity(
	ctx context.Context, req *flowv1.CreateEntityRequest,
) (*flowv1.CreateEntityResponse, error) {
	if err := p.checkWriteByType(ctx, req.GetEntityType()); err != nil {
		return nil, err
	}
	return p.client.CreateEntity(ctx, req)
}

// UpdateEntity validates the WRITE grant against the entity type the SDK
// resolved from its local ID-to-type mapping (SPEC R3:252 / Capability
// Authorisation Chain). A specific resolved type is a mode-1 check that blocks
// with PERMISSION_DENIED when the caller lacks WRITE:graph/entity/<type>; an
// unknown or stale ID is annotated "*" and falls back to the mode-2 wildcard
// best-effort check, with the Cartographer authoritative on ingress.
func (p *CartographerProxy) UpdateEntity(
	ctx context.Context, req *flowv1.UpdateEntityRequest,
) (*flowv1.UpdateEntityResponse, error) {
	if err := p.checkWriteMetadataType(ctx); err != nil {
		return nil, err
	}
	return p.client.UpdateEntity(ctx, req)
}

// DeleteEntity validates the WRITE grant against the entity type the SDK
// resolved from its local ID-to-type mapping (SPEC R3:252 / Capability
// Authorisation Chain). A specific resolved type is a mode-1 check that blocks
// with PERMISSION_DENIED when the caller lacks WRITE:graph/entity/<type>; an
// unknown or stale ID is annotated "*" and falls back to the mode-2 wildcard
// best-effort check, with the Cartographer authoritative on ingress.
func (p *CartographerProxy) DeleteEntity(
	ctx context.Context, req *flowv1.DeleteEntityRequest,
) (*flowv1.DeleteEntityResponse, error) {
	if err := p.checkWriteMetadataType(ctx); err != nil {
		return nil, err
	}
	return p.client.DeleteEntity(ctx, req)
}

// CreateEdge validates the WRITE grant against the source entity type the SDK
// resolved from its local ID-to-type mapping (SPEC R3:249-250, R3:252 /
// Capability Authorisation Chain). A specific resolved type is a mode-1 check
// that blocks with PERMISSION_DENIED when the caller lacks
// WRITE:graph/entity/<type>; an unknown or stale source ID is annotated "*"
// and falls back to the mode-2 wildcard best-effort check, with the
// Cartographer authoritative on ingress.
func (p *CartographerProxy) CreateEdge(
	ctx context.Context, req *flowv1.CreateEdgeRequest,
) (*flowv1.CreateEdgeResponse, error) {
	if err := p.checkWriteMetadataType(ctx); err != nil {
		return nil, err
	}
	return p.client.CreateEdge(ctx, req)
}

// DeleteEdge is always mode 2: the SDK annotates entity_type=* (SPEC R3) and
// the Sidecar's wildcard check is best-effort — the Cartographer validates
// WRITE:graph/entity/<source-type> authoritatively on ingress (SPEC R7).
func (p *CartographerProxy) DeleteEdge(
	ctx context.Context, req *flowv1.DeleteEdgeRequest,
) (*flowv1.DeleteEdgeResponse, error) {
	if err := p.checkCapability(ctx, "WRITE", "graph/entity/*", false); err != nil {
		return nil, err
	}
	return p.client.DeleteEdge(ctx, req)
}

// ---------------------------------------------------------------------------
// Transaction path (node-facing)
// ---------------------------------------------------------------------------

// BeginTransaction requires WRITE:graph/tx (SPEC R3).
func (p *CartographerProxy) BeginTransaction(
	ctx context.Context, req *flowv1.BeginTransactionRequest,
) (*flowv1.BeginTransactionResponse, error) {
	if err := p.checkCapability(ctx, "WRITE", "graph/tx", true); err != nil {
		return nil, err
	}
	return p.client.BeginTransaction(ctx, req)
}

// CommitTransaction requires WRITE:graph/tx (SPEC R3).
func (p *CartographerProxy) CommitTransaction(
	ctx context.Context, req *flowv1.CommitTransactionRequest,
) (*flowv1.CommitTransactionResponse, error) {
	if err := p.checkCapability(ctx, "WRITE", "graph/tx", true); err != nil {
		return nil, err
	}
	return p.client.CommitTransaction(ctx, req)
}

// RollbackTransaction requires WRITE:graph/tx (SPEC R3).
func (p *CartographerProxy) RollbackTransaction(
	ctx context.Context, req *flowv1.RollbackTransactionRequest,
) (*flowv1.RollbackTransactionResponse, error) {
	if err := p.checkCapability(ctx, "WRITE", "graph/tx", true); err != nil {
		return nil, err
	}
	return p.client.RollbackTransaction(ctx, req)
}

// RefreshTransaction requires WRITE:graph/tx (SPEC R3).
func (p *CartographerProxy) RefreshTransaction(
	ctx context.Context, req *flowv1.RefreshTransactionRequest,
) (*flowv1.RefreshTransactionResponse, error) {
	if err := p.checkCapability(ctx, "WRITE", "graph/tx", true); err != nil {
		return nil, err
	}
	return p.client.RefreshTransaction(ctx, req)
}

// GetTransactionDiff requires READ:graph/tx (SPEC R3).
func (p *CartographerProxy) GetTransactionDiff(
	ctx context.Context, req *flowv1.GetTransactionDiffRequest,
) (*flowv1.GetTransactionDiffResponse, error) {
	if err := p.checkCapability(ctx, "READ", "graph/tx", true); err != nil {
		return nil, err
	}
	return p.client.GetTransactionDiff(ctx, req)
}

// ExtendTimeout requires WRITE:graph/tx (SPEC R3).
func (p *CartographerProxy) ExtendTimeout(
	ctx context.Context, req *flowv1.ExtendTimeoutRequest,
) (*flowv1.ExtendTimeoutResponse, error) {
	if err := p.checkCapability(ctx, "WRITE", "graph/tx", true); err != nil {
		return nil, err
	}
	return p.client.ExtendTimeout(ctx, req)
}

// ---------------------------------------------------------------------------
// Administrative path (node-facing)
// ---------------------------------------------------------------------------

// Sync requires WRITE:graph/entity/* (SPEC R3).
func (p *CartographerProxy) Sync(
	ctx context.Context, req *flowv1.SyncRequest,
) (*flowv1.SyncResponse, error) {
	if err := p.checkCapability(ctx, "WRITE", "graph/entity/*", true); err != nil {
		return nil, err
	}
	return p.client.Sync(ctx, req)
}

// ExportGraph requires READ:graph/entity/* (SPEC R3). It relays the
// server-streaming response chunk by chunk to the caller.
func (p *CartographerProxy) ExportGraph(
	req *flowv1.ExportGraphRequest, stream grpc.ServerStreamingServer[flowv1.ExportGraphResponse],
) error {
	if err := p.checkCapability(stream.Context(), "READ", "graph/entity/*", true); err != nil {
		return err
	}
	upstream, err := p.client.ExportGraph(stream.Context(), req)
	if err != nil {
		return err
	}
	var sentAny bool
	for {
		resp, err := upstream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			// SPEC error table: "Unsupported export format" → INVALID_ARGUMENT and
			// "ExportGraph buffer allocation failure" → RESOURCE_EXHAUSTED are
			// pre-stream rejections ("no data sent"): the Cartographer returns them
			// BEFORE any chunk, so they arrive at the relay's first Recv with no
			// chunk forwarded. An upstream transport Unavailable with no chunk
			// forwarded is the same no-data-sent, stream-establishment condition —
			// the Cartographer could not be reached — which the operator proxy
			// surfaces as UNAVAILABLE ("cannot start export stream"); the lazy
			// grpc.NewClient dial delivers it on the first Recv instead. Pass all
			// three through verbatim — preserving the upstream status code and
			// message — so an SDK caller (graph.ExportGraph("bogus"), which
			// performs no local format validation) receives the documented error,
			// matching the operator proxy. Once at least one chunk has been
			// forwarded, any failure — a transport-level break (Unavailable), a
			// non-conforming upstream status, or a raw error — is a genuine
			// mid-stream failure (partial data may already have been sent), so
			// surface it as INTERNAL rather than the raw upstream status.
			if !sentAny {
				if st, ok := status.FromError(err); ok {
					if c := st.Code(); c == codes.InvalidArgument || c == codes.ResourceExhausted || c == codes.Unavailable {
						return err
					}
				}
			}
			return status.Errorf(codes.Internal, "export stream failed: %v", err)
		}
		sentAny = true
		if err := stream.Send(resp); err != nil {
			// SPEC error table: a downstream stream break during export is the same
			// mid-stream failure (partial data may already have been sent) → INTERNAL,
			// matching the operator proxy and the Cartographer service handler.
			return status.Errorf(codes.Internal, "export stream failed: %v", err)
		}
	}
}
