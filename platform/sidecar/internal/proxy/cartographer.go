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
	flow "github.com/foundry/flow/sdk/go"
	"github.com/foundry/flow/sidecar/internal/service"
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
// incoming metadata. Returns nil when the request is not node-originated (no
// x-flow-node-id), in which case the proxy passes the request through and the
// Cartographer's ingress verifier is the security boundary.
func nodeCapabilities(ctx context.Context) []string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil
	}
	if len(md.Get(service.MetadataKeyNodeID)) == 0 {
		return nil
	}
	var caps []string
	for _, c := range md.Get(service.MetadataKeyCapabilities) {
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
// unknown). Empty when no type is resolvable.
func entityTypeFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("entity_type")
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
// hold <verb>:<resource> (a wildcard grant matches via flow.MatchCapability)
// or the request is rejected with PERMISSION_DENIED.
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
		if flow.MatchCapability(c, required) {
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

// checkReadByType enforces the READ gate for read RPCs whose entity type is
// known from the request body: a specific type is a mode-1 check, an omitted
// type (all-types search) is a mode-2 wildcard best-effort check (SPEC R3).
func (p *CartographerProxy) checkReadByType(ctx context.Context, entityType string) error {
	if entityType != "" {
		return p.checkCapability(ctx, "READ", "graph/entity/"+entityType, true)
	}
	return p.checkCapability(ctx, "READ", "graph/entity/*", false)
}

// checkWriteByType enforces the WRITE gate for write RPCs whose entity type is
// known (mode 1) or unresolvable (mode 2 wildcard, no block).
func (p *CartographerProxy) checkWriteByType(ctx context.Context, entityType string) error {
	if entityType == "" || entityType == "*" {
		return p.checkCapability(ctx, "WRITE", "graph/entity/*", false)
	}
	return p.checkCapability(ctx, "WRITE", "graph/entity/"+entityType, true)
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
// from the request body (mode 1); an omitted type is a wildcard best-effort
// check (mode 2) with the Cartographer authoritative (SPEC R3).
func (p *CartographerProxy) SearchNeighbors(
	ctx context.Context, req *flowv1.SearchNeighborsRequest,
) (*flowv1.SearchNeighborsResponse, error) {
	if err := p.checkReadByType(ctx, req.GetEntityType()); err != nil {
		return nil, err
	}
	return p.client.SearchNeighbors(ctx, req)
}

// FullTextSearch validates the READ grant against the requested entity type
// from the request body (mode 1); an omitted type is a mode-2 wildcard
// best-effort check (SPEC R3).
func (p *CartographerProxy) FullTextSearch(
	ctx context.Context, req *flowv1.FullTextSearchRequest,
) (*flowv1.FullTextSearchResponse, error) {
	if err := p.checkReadByType(ctx, req.GetEntityType()); err != nil {
		return nil, err
	}
	return p.client.FullTextSearch(ctx, req)
}

// ListEntities validates the READ grant against the requested entity type from
// the request body (mode 1); an omitted type is a mode-2 wildcard best-effort
// check (SPEC R3).
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
// resolved from its local ID-to-type mapping (mode 1, entity_type metadata);
// an unresolvable type falls back to the wildcard best-effort check (mode 2)
// with the Cartographer authoritative.
func (p *CartographerProxy) UpdateEntity(
	ctx context.Context, req *flowv1.UpdateEntityRequest,
) (*flowv1.UpdateEntityResponse, error) {
	if err := p.checkWriteByType(ctx, entityTypeFromMetadata(ctx)); err != nil {
		return nil, err
	}
	return p.client.UpdateEntity(ctx, req)
}

// DeleteEntity validates the WRITE grant against the entity type the SDK
// resolved from its local ID-to-type mapping (mode 1); an unresolvable type
// falls back to the wildcard best-effort check (mode 2).
func (p *CartographerProxy) DeleteEntity(
	ctx context.Context, req *flowv1.DeleteEntityRequest,
) (*flowv1.DeleteEntityResponse, error) {
	if err := p.checkWriteByType(ctx, entityTypeFromMetadata(ctx)); err != nil {
		return nil, err
	}
	return p.client.DeleteEntity(ctx, req)
}

// CreateEdge validates the WRITE grant against the source entity type the SDK
// resolved (mode 1, entity_type metadata); an unresolvable source type falls
// back to the wildcard best-effort check (mode 2).
func (p *CartographerProxy) CreateEdge(
	ctx context.Context, req *flowv1.CreateEdgeRequest,
) (*flowv1.CreateEdgeResponse, error) {
	if err := p.checkWriteByType(ctx, entityTypeFromMetadata(ctx)); err != nil {
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
	for {
		resp, err := upstream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}
