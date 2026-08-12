/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	flowv1gen "github.com/foundry/flow/gen/flow/v1"
)

// ProxyServer implements CartographerServiceServer for the Operator's gRPC proxy.
// It authorizes incoming requests via TokenReview + SubjectAccessReview and forwards
// to the registered Cartographer instance.
type ProxyServer struct {
	flowv1gen.UnimplementedCartographerServiceServer
	routingTable       *ProxyRoutingTable
	k8sClient          client.Client
	authCache          *authCache
	dialer             func(ctx context.Context, endpoint string) (CartographerClient, error)
	operatorSigningKey []byte
}

// NewProxyServer creates a new ProxyServer.
func NewProxyServer(rt *ProxyRoutingTable, k8sClient client.Client, dialer func(ctx context.Context, endpoint string) (CartographerClient, error), operatorSigningKey []byte) *ProxyServer {
	return &ProxyServer{
		routingTable: rt,
		k8sClient:    k8sClient,
		// ponytail: the proxy auth cache TTL is a fixed 30s irrespective of the
		// CAPABILITY_STALENESS_WINDOW the operator forwards to the Cartographer
		// (foundrygraph_infra.go). That env bounds how long the *Cartographer* honours a
		// signed request capability; this TTL bounds how long the *operator* caches a
		// positive TokenReview/SAR authz decision. The two are intentionally decoupled, but
		// the coupling risk is: if an operator deploys CAPABILITY_STALENESS_WINDOW < 30s to
		// shorten the freshness window, this 30s proxy cache does not honour that intent, so
		// a revoked identity can keep a cached authorization for up to 30s. Accepted because
		// the proxy also does a fresh SAR per grant; wiring the TTL to the env would require
		// threading reconciler config into the proxy server constructor.
		authCache:          newAuthCache(30 * time.Second),
		dialer:             dialer,
		operatorSigningKey: operatorSigningKey,
	}
}

// proxyUnimplemented returns a standard error for RPCs not available through the proxy.
func (s *ProxyServer) proxyUnimplemented(method string) error {
	return status.Error(codes.Unimplemented, method+" is not available through the Operator proxy; use the SDK directly")
}

// defaultGraphName is the conventional singleton FoundryGraph name (SPEC R1: the
// singleton is conventionally named "flow-graph"; other components reference this
// conventional name).
const defaultGraphName = "flow-graph"

// extractRoutingMetadata reads x-flow-namespace and x-flow-graph-name from gRPC metadata.
func (s *ProxyServer) extractRoutingMetadata(ctx context.Context) (namespace, name string, err error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", "", status.Error(codes.InvalidArgument, "missing gRPC metadata")
	}

	ns := md.Get("x-flow-namespace")
	if len(ns) == 0 || ns[0] == "" {
		return "", "", status.Error(codes.InvalidArgument, "missing routing namespace (x-flow-namespace)")
	}

	graphName := defaultGraphName // SPEC R1 singleton convention
	if gn := md.Get("x-flow-graph-name"); len(gn) > 0 && gn[0] != "" {
		graphName = gn[0]
	}

	return ns[0], graphName, nil
}

// authorize performs TokenReview + SubjectAccessReview for the caller. The proxy only
// ever authorizes the read capability the CLI path needs, so the verb is fixed to "get"
// (SPEC Graph Export Flow step 3: SubjectAccessReview for READ:graph/entity/* against
// the foundrygraphs resource).
func (s *ProxyServer) authorize(ctx context.Context, namespace, name string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing gRPC metadata")
	}

	authHeaders := md.Get("authorization")
	if len(authHeaders) == 0 {
		return status.Error(codes.Unauthenticated, "missing authorization header")
	}

	token := strings.TrimPrefix(authHeaders[0], "Bearer ")
	const verb = "get"

	// Check auth cache.
	if s.authCache.Get(s.authCache.key(token, namespace, name, verb)) {
		return nil
	}

	// TokenReview
	tr := &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token},
	}
	if err := s.k8sClient.Create(ctx, tr); err != nil {
		return status.Errorf(codes.Unavailable, "authentication request failed: %v", err)
	}
	if !tr.Status.Authenticated {
		return status.Error(codes.Unauthenticated, "invalid token")
	}

	// SubjectAccessReview — include the caller's full identity (groups, UID) from the
	// TokenReview so group-based RBAC (the common way `get` on foundrygraphs is granted)
	// matches; omitting Groups would yield spurious PermissionDenied.
	sar := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   tr.Status.User.Username,
			Groups: tr.Status.User.Groups,
			UID:    tr.Status.User.UID,
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: namespace,
				Group:     "flow.foundry.io",
				Resource:  "foundrygraphs",
				Verb:      verb,
				Name:      name,
			},
		},
	}
	if err := s.k8sClient.Create(ctx, sar); err != nil {
		return status.Errorf(codes.Unavailable, "authorization request failed: %v", err)
	}
	if !sar.Status.Allowed {
		return status.Error(codes.PermissionDenied, "not authorized")
	}

	// Cache positive result.
	s.authCache.Set(s.authCache.key(token, namespace, name, verb))
	return nil
}

// signedCapabilities holds capability metadata for forwarding requests.
type signedCapabilities struct {
	capabilities string
	signedBy     string // always "operator"
	signedAt     string // Unix timestamp as string
	signature    string // base64-encoded Ed25519 signature
}

// signCapabilities creates capability-signed gRPC metadata for the forwarding path.
// fail closed: if the operator signing key is missing or not an Ed25519 private key,
// it returns an error rather than forwarding an empty (invalid) signature. An unsigned
// capability must never be forwarded, because the Cartographer would reject it and the
// caller would be left with a request we cannot authoritatively authorize.
func (s *ProxyServer) signCapabilities(capabilities string) (*signedCapabilities, error) {
	now := strconv.FormatInt(time.Now().Unix(), 10)
	if len(s.operatorSigningKey) != ed25519.PrivateKeySize {
		return nil, status.Error(codes.Internal, "operator signing key has wrong length; refusing to forward unsigned capabilities")
	}
	payload := capabilities + "|" + now
	sig := ed25519.Sign(ed25519.PrivateKey(s.operatorSigningKey), []byte(payload))
	return &signedCapabilities{
		capabilities: capabilities,
		signedBy:     "operator",
		signedAt:     now,
		signature:    base64.StdEncoding.EncodeToString(sig),
	}, nil
}

// authCache is a short-TTL positive-result cache for auth decisions.
type authCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]time.Time // key → expiry timestamp
}

func newAuthCache(ttl time.Duration) *authCache {
	return &authCache{
		ttl:     ttl,
		entries: make(map[string]time.Time),
	}
}

func (c *authCache) Get(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	expiry, ok := c.entries[key]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(c.entries, key)
		return false
	}
	return true
}

func (c *authCache) Set(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = time.Now().Add(c.ttl)
}

func (c *authCache) key(token, ns, name, verb string) string {
	// ponytail: pipe delimiter assumes none of the four fields contain a literal pipe. If
	// one did (e.g. a token value containing '|'), distinct (token, ns, name, verb) tuples
	// could hash to the same cache key, so a cached positive authz decision for one identity
	// could be served for a different identity for up to the 30s TTL. This fails closed — a
	// collision only ever grants access that was granted to some other identity, never
	// revokes — but it is a real, if unlikely, boundary. Namespaces/verbs are operator/API
	// normalised and pipe-free; only the raw Bearer token is user-supplied. Upgrade path:
	// switch to a struct key or a length-prefixed field encoding (e.g. fmt of each field with
	// an explicit length) so a pipe in a value cannot alias another tuple.
	h := sha256.Sum256([]byte(token + "|" + ns + "|" + name + "|" + verb))
	return fmt.Sprintf("%x", h)
}

// --- Exported RPC methods ---

// ExportGraph is proxied for CLI usage (flowctl graph export).
func (s *ProxyServer) ExportGraph(req *flowv1gen.ExportGraphRequest, stream flowv1gen.CartographerService_ExportGraphServer) error {
	ns, name, err := s.extractRoutingMetadata(stream.Context())
	if err != nil {
		return err
	}

	// Authorize BEFORE the routing-table lookup (SPEC Graph Export Flow step 3 precedes step 4).
	// authorize needs only the namespace/name/verb, never the routing endpoint, so it can run
	// safely ahead of Lookup. This closes an existence oracle: if the lookup ran first, an
	// unauthenticated/unprivileged caller could distinguish "no such (namespace, graph-name)"
	// (Unavailable) from "registered" (proceed to auth). With auth-first, an unauthorized caller
	// gets Unauthenticated/PermissionDenied regardless of whether the graph is registered.
	if err := s.authorize(stream.Context(), ns, name); err != nil {
		return err
	}

	endpoint, ok := s.routingTable.Lookup(ns, name)
	if !ok {
		return status.Error(codes.Unavailable, "graph "+ns+"/"+name+" unavailable")
	}

	// Inject capability metadata signed by the operator.
	caps, err := s.signCapabilities("READ:graph/entity/*")
	if err != nil {
		return err
	}
	ctx := metadata.AppendToOutgoingContext(stream.Context(),
		"x-flow-capabilities", caps.capabilities,
		"x-flow-capabilities-signed-by", caps.signedBy,
		"x-flow-capabilities-signed-at", caps.signedAt,
		"x-flow-capabilities-signature", caps.signature,
	)

	// grpc.NewClient connects lazily, so the 10s dial timeout below does not by itself
	// bound the transport connection — the actual connect happens when the stream RPC is
	// initiated. Bound ONLY the dial/connect with the short deadline, then establish and
	// stream on the capability-injected caller ctx below. grpc-go binds a client stream's
	// lifetime to the context passed to the stream RPC, so passing the dial deadline to
	// ExportGraph would cut any export that streams longer than the window mid-stream
	// (surfacing as the SPEC R11 INTERNAL case). The dial deadline stays scoped to the
	// connect so an unreachable/blackholed upstream still fails fast before the stream is
	// established, while a connected stream that outlives the dial window is not cut.
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cc, err := s.dialer(dialCtx, endpoint)
	if err != nil {
		return status.Errorf(codes.Unavailable, "cannot connect to cartographer: %v", err)
	}
	defer func() { _ = cc.Close() }()

	// Establish and stream on the caller's ctx (the capability-injected outgoing context),
	// NOT the dial deadline: a stream that outlives the 10s dial window must not be cut
	// mid-stream. A broken upstream that surfaces after establishment is the SPEC R11
	// INTERNAL case, not a dial-timeout Unavailable.
	clientStream, err := cc.ExportGraph(ctx, req)
	if err != nil {
		return status.Errorf(codes.Unavailable, "cannot start export stream: %v", err)
	}

	// A pre-stream rejection — the Cartographer returns INVALID_ARGUMENT (unsupported
	// format) or RESOURCE_EXHAUSTED (buffer allocation) BEFORE sending any chunk (SPEC
	// error table rows "Unsupported export format" and "ExportGraph buffer allocation
	// failure", both "no data sent"); those statuses arrive at the proxy's first Recv
	// with no chunk forwarded, so pass them through verbatim and let the documented CLI
	// error codes surface (the sidecar relay preserves upstream statuses identically).
	// Once at least one chunk has been forwarded, any failure — a transport-level break
	// (Unavailable), a non-conforming upstream status (e.g. DataLoss), or a raw error —
	// is a genuine mid-stream failure (partial data may already have been sent) and
	// maps to INTERNAL per the SPEC error table row "ExportGraph mid-stream failure".
	var sentAny bool
	for {
		chunk, err := clientStream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			if !sentAny {
				if st, ok := status.FromError(err); ok {
					if c := st.Code(); c == codes.InvalidArgument || c == codes.ResourceExhausted {
						return err
					}
				}
			}
			return status.Errorf(codes.Internal, "export stream failed: %v", err)
		}
		sentAny = true
		if err := stream.Send(chunk); err != nil {
			// SPEC error table: a downstream stream break during export is the same
			// mid-stream failure (partial data may already have been sent) → INTERNAL.
			return status.Errorf(codes.Internal, "export stream failed: %v", err)
		}
	}
}

// --- Service-facing RPCs (excluded from proxy) ---

func (s *ProxyServer) ApplySchema(ctx context.Context, req *flowv1gen.ApplySchemaRequest) (*flowv1gen.ApplySchemaResponse, error) {
	return nil, s.proxyUnimplemented("ApplySchema")
}

func (s *ProxyServer) WipeGraph(ctx context.Context, req *flowv1gen.WipeGraphRequest) (*flowv1gen.WipeGraphResponse, error) {
	return nil, s.proxyUnimplemented("WipeGraph")
}

func (s *ProxyServer) HealthCheck(ctx context.Context, req *flowv1gen.HealthCheckRequest) (*flowv1gen.HealthCheckResponse, error) {
	return nil, s.proxyUnimplemented("HealthCheck")
}

// --- Node-facing read path (excluded from proxy) ---

func (s *ProxyServer) ExecuteCypher(ctx context.Context, req *flowv1gen.ExecuteCypherRequest) (*flowv1gen.ExecuteCypherResponse, error) {
	return nil, s.proxyUnimplemented("ExecuteCypher")
}

func (s *ProxyServer) SearchNeighbors(ctx context.Context, req *flowv1gen.SearchNeighborsRequest) (*flowv1gen.SearchNeighborsResponse, error) {
	return nil, s.proxyUnimplemented("SearchNeighbors")
}

func (s *ProxyServer) FullTextSearch(ctx context.Context, req *flowv1gen.FullTextSearchRequest) (*flowv1gen.FullTextSearchResponse, error) {
	return nil, s.proxyUnimplemented("FullTextSearch")
}

func (s *ProxyServer) ListEntities(ctx context.Context, req *flowv1gen.ListEntitiesRequest) (*flowv1gen.ListEntitiesResponse, error) {
	return nil, s.proxyUnimplemented("ListEntities")
}

// --- Node-facing write path (excluded from proxy) ---

func (s *ProxyServer) CreateEntity(ctx context.Context, req *flowv1gen.CreateEntityRequest) (*flowv1gen.CreateEntityResponse, error) {
	return nil, s.proxyUnimplemented("CreateEntity")
}

func (s *ProxyServer) UpdateEntity(ctx context.Context, req *flowv1gen.UpdateEntityRequest) (*flowv1gen.UpdateEntityResponse, error) {
	return nil, s.proxyUnimplemented("UpdateEntity")
}

func (s *ProxyServer) DeleteEntity(ctx context.Context, req *flowv1gen.DeleteEntityRequest) (*flowv1gen.DeleteEntityResponse, error) {
	return nil, s.proxyUnimplemented("DeleteEntity")
}

func (s *ProxyServer) CreateEdge(ctx context.Context, req *flowv1gen.CreateEdgeRequest) (*flowv1gen.CreateEdgeResponse, error) {
	return nil, s.proxyUnimplemented("CreateEdge")
}

func (s *ProxyServer) DeleteEdge(ctx context.Context, req *flowv1gen.DeleteEdgeRequest) (*flowv1gen.DeleteEdgeResponse, error) {
	return nil, s.proxyUnimplemented("DeleteEdge")
}

// --- Transaction lifecycle (excluded from proxy) ---

func (s *ProxyServer) BeginTransaction(ctx context.Context, req *flowv1gen.BeginTransactionRequest) (*flowv1gen.BeginTransactionResponse, error) {
	return nil, s.proxyUnimplemented("BeginTransaction")
}

func (s *ProxyServer) CommitTransaction(ctx context.Context, req *flowv1gen.CommitTransactionRequest) (*flowv1gen.CommitTransactionResponse, error) {
	return nil, s.proxyUnimplemented("CommitTransaction")
}

func (s *ProxyServer) RollbackTransaction(ctx context.Context, req *flowv1gen.RollbackTransactionRequest) (*flowv1gen.RollbackTransactionResponse, error) {
	return nil, s.proxyUnimplemented("RollbackTransaction")
}

func (s *ProxyServer) RefreshTransaction(ctx context.Context, req *flowv1gen.RefreshTransactionRequest) (*flowv1gen.RefreshTransactionResponse, error) {
	return nil, s.proxyUnimplemented("RefreshTransaction")
}

func (s *ProxyServer) GetTransactionDiff(ctx context.Context, req *flowv1gen.GetTransactionDiffRequest) (*flowv1gen.GetTransactionDiffResponse, error) {
	return nil, s.proxyUnimplemented("GetTransactionDiff")
}

func (s *ProxyServer) ExtendTimeout(ctx context.Context, req *flowv1gen.ExtendTimeoutRequest) (*flowv1gen.ExtendTimeoutResponse, error) {
	return nil, s.proxyUnimplemented("ExtendTimeout")
}
