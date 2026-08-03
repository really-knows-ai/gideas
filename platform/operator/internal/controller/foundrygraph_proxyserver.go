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
	"log/slog"
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
	port               int
	authCache          *authCache
	dialer             func(ctx context.Context, endpoint string) (CartographerClient, error)
	operatorSigningKey []byte
}

// NewProxyServer creates a new ProxyServer.
func NewProxyServer(rt *ProxyRoutingTable, k8sClient client.Client, port int, dialer func(ctx context.Context, endpoint string) (CartographerClient, error), operatorSigningKey []byte) *ProxyServer {
	return &ProxyServer{
		routingTable:       rt,
		k8sClient:          k8sClient,
		port:               port,
		authCache:          newAuthCache(30 * time.Second),
		dialer:             dialer,
		operatorSigningKey: operatorSigningKey,
	}
}

// proxyUnimplemented returns a standard error for RPCs not available through the proxy.
func (s *ProxyServer) proxyUnimplemented(method string) error {
	return status.Error(codes.Unimplemented, method+" is not available through the Operator proxy; use the SDK directly")
}

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

	graphName := "flow-graph" // default per SPEC R1 singleton convention
	if gn := md.Get("x-flow-graph-name"); len(gn) > 0 && gn[0] != "" {
		graphName = gn[0]
	}

	return ns[0], graphName, nil
}

// authorize performs TokenReview + SubjectAccessReview for the caller.
func (s *ProxyServer) authorize(ctx context.Context, namespace, name, verb string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing gRPC metadata")
	}

	authHeaders := md.Get("authorization")
	if len(authHeaders) == 0 {
		return status.Error(codes.Unauthenticated, "missing authorization header")
	}

	token := strings.TrimPrefix(authHeaders[0], "Bearer ")

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

	// SubjectAccessReview
	sar := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User: tr.Status.User.Username,
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
func (s *ProxyServer) signCapabilities(capabilities string) *signedCapabilities {
	now := strconv.FormatInt(time.Now().Unix(), 10)
	if len(s.operatorSigningKey) != ed25519.PrivateKeySize {
		slog.Warn("operator signing key has wrong length, returning unsigned capabilities",
			"expected", ed25519.PrivateKeySize,
			"got", len(s.operatorSigningKey),
		)
		return &signedCapabilities{
			capabilities: capabilities,
			signedBy:     "operator",
			signedAt:     now,
			signature:    "",
		}
	}
	payload := capabilities + "|" + now
	sig := ed25519.Sign(ed25519.PrivateKey(s.operatorSigningKey), []byte(payload))
	return &signedCapabilities{
		capabilities: capabilities,
		signedBy:     "operator",
		signedAt:     now,
		signature:    base64.StdEncoding.EncodeToString(sig),
	}
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
	// ponytail: pipe delimiter assumes none of the four fields contain literal pipe.
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

	endpoint, ok := s.routingTable.Lookup(ns, name)
	if !ok {
		return status.Error(codes.Unavailable, "graph "+ns+"/"+name+" unavailable")
	}

	if err := s.authorize(stream.Context(), ns, name, "get"); err != nil {
		return err
	}

	// Inject capability metadata signed by the operator.
	caps := s.signCapabilities("READ:graph/entity/*")
	ctx := metadata.AppendToOutgoingContext(stream.Context(),
		"x-flow-capabilities", caps.capabilities,
		"x-flow-capabilities-signed-by", caps.signedBy,
		"x-flow-capabilities-signed-at", caps.signedAt,
		"x-flow-capabilities-signature", caps.signature,
	)

	dialCtx, cancel := context.WithTimeout(stream.Context(), 10*time.Second)
	defer cancel()

	client, err := s.dialer(dialCtx, endpoint)
	if err != nil {
		return status.Errorf(codes.Unavailable, "cannot connect to cartographer: %v", err)
	}
	defer client.Close()

	clientStream, err := client.ExportGraph(ctx, req)
	if err != nil {
		return status.Errorf(codes.Unavailable, "cannot start export stream: %v", err)
	}

	for {
		chunk, err := clientStream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return status.Errorf(codes.Unavailable, "export stream failed: %v", err)
		}
		if err := stream.Send(chunk); err != nil {
			return status.Errorf(codes.Canceled, "client cancelled: %v", err)
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

// --- Administrative path (excluded from proxy) ---

func (s *ProxyServer) PullFromRemote(ctx context.Context, req *flowv1gen.PullFromRemoteRequest) (*flowv1gen.PullFromRemoteResponse, error) {
	return nil, s.proxyUnimplemented("PullFromRemote")
}
