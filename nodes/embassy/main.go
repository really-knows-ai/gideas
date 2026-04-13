// Embassy is the cross-flow boundary node for the Foundry Flow platform.
//
// It runs as a persistent entry node (watcher-style StartEntry process) with
// two responsibilities:
//
//   - Inbound import: runs an Embassy gRPC server that receives signed
//     manifests and streamed packages from remote Embassies.
//   - Outbound export: handles locally-created Workitems routed to Embassy for
//     cross-flow transfer.
//
// Architecture:
//   - Entry function: starts the Embassy gRPC server for inbound transfers.
//   - Handler: processes outbound export Workitems.
//
// Uses the SDK StartEntry pattern: the entry function and handler server run
// concurrently, with shared-nothing semantics between them.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

const (
	// defaultInboundPort is the default gRPC port for the Embassy inbound server.
	defaultInboundPort = "50059"

	// envInboundPort overrides the default Embassy inbound gRPC port.
	envInboundPort = "EMBASSY_INBOUND_PORT"
)

func main() {
	slog.Info("embassy: starting")
	if err := flow.StartEntry(watchInbound, handleExport); err != nil {
		slog.Error("embassy: failed", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Entry function — starts the Embassy gRPC server for inbound transfers
// ---------------------------------------------------------------------------

// watchInbound is the entry function. It starts an Embassy gRPC server
// that serves EmbassyService RPCs for remote callers and blocks until
// the context is cancelled.
func watchInbound(ctx context.Context, _ *flow.EntryClient) error {
	port := defaultInboundPort
	if envPort := os.Getenv(envInboundPort); envPort != "" {
		port = envPort
	}

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("embassy: load config: %w", err)
	}

	handler := &embassyHandler{cfg: cfg}

	// Connect to the Sidecar for materialisation (CreateWorkitem, StoreArtefact).
	sidecarAddr := os.Getenv(flow.EnvSidecarAddress)
	if sidecarAddr == "" {
		sidecarAddr = flow.DefaultSidecarAddress
	}
	sidecarConn, err := grpc.NewClient(
		sidecarAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("embassy: connect to sidecar at %s: %w", sidecarAddr, err)
	}
	defer func() { _ = sidecarConn.Close() }()
	handler.operator = flowv1.NewOperatorServiceClient(sidecarConn)
	handler.archivist = flowv1.NewArchivistServiceClient(sidecarConn)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return fmt.Errorf("embassy: failed to listen on port %s: %w", port, err)
	}

	srv := grpc.NewServer()
	flowv1.RegisterEmbassyServiceServer(srv, flow.NewEmbassyServer(handler))
	reflection.Register(srv)

	// Shut down the server when context is cancelled.
	go func() {
		<-ctx.Done()
		slog.Info("embassy: context cancelled, stopping inbound server")
		srv.GracefulStop()
	}()

	slog.Info("embassy: inbound server listening", "address", lis.Addr().String())
	if err := srv.Serve(lis); err != nil {
		// If context was cancelled, the error is expected.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("embassy: inbound server error: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Embassy inbound handler — implements EmbassyServiceHandler
// ---------------------------------------------------------------------------

// embassyHandler implements flow.EmbassyServiceHandler.
// It holds a loaded config to perform import-type resolution, trust
// validation, and foreign stamp checking during preflight.
//
// For materialisation (creating local Workitems and storing artefacts from
// accepted inbound transfers), the handler holds operator and archivist gRPC
// clients connected through the Sidecar.
type embassyHandler struct {
	cfg       *embassyConfig
	operator  flowv1.OperatorServiceClient
	archivist flowv1.ArchivistServiceClient
}

// sdkSystemImportTypes converts the config-loaded system import types to the
// SDK type map used by flow.ResolveEmbassyImportType.
func (h *embassyHandler) sdkSystemImportTypes() map[string]flow.EmbassyResolvedImportType {
	if h.cfg == nil {
		return nil
	}
	out := make(map[string]flow.EmbassyResolvedImportType, len(h.cfg.SystemImportTypes))
	for name, sit := range h.cfg.SystemImportTypes {
		out[name] = flow.EmbassyResolvedImportType{
			Name:    name,
			BuiltIn: sit.BuiltIn,
		}
	}
	return out
}

// sdkFlowImportTypes converts the config-loaded flow import types to the SDK
// type map used by flow.ResolveEmbassyImportType.
func (h *embassyHandler) sdkFlowImportTypes() map[string]flow.EmbassyFlowImportTypeSpec {
	if h.cfg == nil {
		return nil
	}
	out := make(map[string]flow.EmbassyFlowImportTypeSpec, len(h.cfg.FlowImportTypes))
	for name, fit := range h.cfg.FlowImportTypes {
		out[name] = flow.EmbassyFlowImportTypeSpec{
			Node:                 fit.Node,
			RequireForeignStamps: fit.RequireForeignStamps,
		}
	}
	return out
}

func (h *embassyHandler) PreflightManifest(
	_ context.Context, req *flowv1.PreflightManifestRequest,
) (*flowv1.PreflightManifestResponse, error) {
	manifest := req.GetManifest()
	importTypeName := manifest.GetImportType()

	// --- Import type resolution ---
	system := h.sdkSystemImportTypes()
	flowTypes := h.sdkFlowImportTypes()

	resolvedType, resolved := flow.ResolveEmbassyImportType(importTypeName, system, flowTypes)
	if !resolved {
		return &flowv1.PreflightManifestResponse{
			Accepted:        false,
			RejectionReason: fmt.Sprintf("unknown import type %q", importTypeName),
		}, nil
	}

	// --- Expiry check ---
	if manifest.GetExpiresAt() != nil {
		expiresAt := manifest.GetExpiresAt().AsTime()
		if time.Now().After(expiresAt) {
			return &flowv1.PreflightManifestResponse{
				Accepted:        false,
				RejectionReason: fmt.Sprintf("manifest expired at %s", expiresAt.Format(time.RFC3339)),
			}, nil
		}
	}

	// --- Trust source validation ---
	treatyName := req.GetTreatyName()
	hasTreaty := treatyName != ""
	trustSource := flow.ResolveEmbassyTrustSource(hasTreaty)

	if hasTreaty {
		// Build treaty trust policy from config.
		policy, err := h.buildTreatyPolicy(treatyName)
		if err != nil {
			return &flowv1.PreflightManifestResponse{
				Accepted:        false,
				RejectionReason: err.Error(),
			}, nil
		}

		subject := manifest.GetSignature().GetSubject()
		importReq := flow.EmbassyImportRequest{
			ImportType: importTypeName,
			Subject:    subject,
		}

		if err := flow.ValidateEmbassyTrustPolicy(policy, importReq, system, flowTypes); err != nil {
			return &flowv1.PreflightManifestResponse{
				Accepted:        false,
				RejectionReason: fmt.Sprintf("trust policy violation (%s): %v", trustSource, err),
			}, nil
		}
	}
	// Federation trust: implicit — if import type resolves and manifest
	// is not expired, the transfer is accepted. mTLS authenticates the
	// channel; fine-grained authorization is done at the transport layer.

	// --- Foreign stamp verification ---
	if reason := verifyForeignStamps(resolvedType, manifest.GetArtefacts()); reason != "" {
		return &flowv1.PreflightManifestResponse{
			Accepted:        false,
			RejectionReason: reason,
		}, nil
	}

	// Generate a transfer_id for the accepted transfer.
	transferID := uuid.New().String()

	return &flowv1.PreflightManifestResponse{
		Accepted:   true,
		TransferId: transferID,
	}, nil
}

// verifyForeignStamps checks that each manifest artefact has all required
// foreign stamps specified by the resolved import type's
// requireForeignStamps config. Returns an empty string if all stamps are
// present, or a descriptive rejection reason if any are missing.
func verifyForeignStamps(
	resolved flow.EmbassyResolvedImportType,
	artefacts []*flowv1.ArtefactManifest,
) string {
	if resolved.Spec == nil || len(resolved.Spec.RequireForeignStamps) == 0 {
		return ""
	}

	var missing []string
	for _, art := range artefacts {
		artName := art.GetGovernedArtefact()
		requiredStamps, ok := resolved.Spec.RequireForeignStamps[artName]
		if !ok {
			continue
		}

		// Build a set of stamp names present on this artefact.
		present := make(map[string]bool, len(art.GetForeignStamps()))
		for _, fs := range art.GetForeignStamps() {
			present[fs.GetStampName()] = true
		}

		for _, required := range requiredStamps {
			if !present[required] {
				missing = append(missing, fmt.Sprintf("%s:%s", artName, required))
			}
		}
	}

	if len(missing) > 0 {
		return fmt.Sprintf("missing required foreign stamps: %v", missing)
	}
	return ""
}

// buildTreatyPolicy constructs an EmbassyTrustPolicy from the named treaty
// in the handler's config.
func (h *embassyHandler) buildTreatyPolicy(treatyName string) (flow.EmbassyTrustPolicy, error) {
	if h.cfg == nil || h.cfg.Treaties == nil {
		return flow.EmbassyTrustPolicy{}, fmt.Errorf("treaty %q not found (no treaties configured)", treatyName)
	}
	tc, ok := h.cfg.Treaties[treatyName]
	if !ok {
		return flow.EmbassyTrustPolicy{}, fmt.Errorf("treaty %q not found", treatyName)
	}
	return flow.EmbassyTrustPolicy{
		Source:             flow.EmbassyTrustSourceTreaty,
		AllowedImportTypes: tc.AllowedImportTypes,
		AllowedSubjects:    tc.AllowedSubjects,
		MaxBundleSizeBytes: tc.MaxBundleSizeBytes,
	}, nil
}

func (h *embassyHandler) StreamPackage(
	ctx context.Context, chunks []*flowv1.PackageChunk,
) (*flowv1.StreamPackageResponse, error) {
	// --- Extract manifest from chunk stream ---
	manifest := extractManifest(chunks)
	if manifest == nil {
		return nil, status.Error(
			codes.InvalidArgument,
			"embassy: StreamPackage: no manifest in chunk stream",
		)
	}

	// --- Resolve import type ---
	importTypeName := manifest.GetImportType()
	system := h.sdkSystemImportTypes()
	flowTypes := h.sdkFlowImportTypes()

	resolvedType, resolved := flow.ResolveEmbassyImportType(
		importTypeName, system, flowTypes,
	)
	if !resolved {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"embassy: StreamPackage: unknown import type %q",
			importTypeName,
		)
	}

	// --- Stage chunks ---
	stager := newEmbassyStager()
	if err := stager.StageManifest(ctx, manifest); err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"embassy: StreamPackage: stage manifest: %v", err,
		)
	}
	for _, chunk := range chunks {
		// Skip the manifest chunk (already staged above).
		if chunk.GetManifest() != nil {
			continue
		}
		if err := stager.StageChunk(ctx, chunk); err != nil {
			return nil, status.Errorf(
				codes.Internal,
				"embassy: StreamPackage: stage chunk: %v", err,
			)
		}
	}

	staged, err := stager.Complete(ctx)
	if err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"embassy: StreamPackage: complete staging: %v", err,
		)
	}

	// --- Verify digests ---
	if err := verifyPackageDigests(staged); err != nil {
		return nil, status.Errorf(
			codes.DataLoss,
			"embassy: StreamPackage: %v", err,
		)
	}

	// --- Materialise: create Workitem and unpack artefacts ---
	if h.operator == nil || h.archivist == nil {
		// No sidecar connections — cannot materialise. Return a
		// placeholder (preserves backward compat with pre-13.4 tests
		// that do not inject operator/archivist spies).
		return &flowv1.StreamPackageResponse{
			WorkitemId: fmt.Sprintf(
				"staged-%s", manifest.GetTransferId(),
			),
		}, nil
	}

	mat := &embassyMaterializer{
		operator:       h.operator,
		archivist:      h.archivist,
		naturalisation: h.cfg.Naturalisation,
	}

	return mat.MaterializeImport(ctx, resolvedType, staged)
}

func (h *embassyHandler) ExportPackage(
	_ context.Context, _ *flowv1.ExportPackageRequest,
) ([]*flowv1.PackageChunk, error) {
	return nil, status.Error(codes.Unimplemented, "embassy: ExportPackage not implemented")
}

// ---------------------------------------------------------------------------
// Handler — processes outbound export Workitems
// ---------------------------------------------------------------------------

// embassyDialerFunc is a function that connects to a remote Embassy at
// the given address. Used for dependency injection in tests.
type embassyDialerFunc func(addr string) (*flow.EmbassyClient, error)

// exportDeps bundles the dependencies that processExport needs beyond
// the flow.Client. This struct enables test injection of spies without
// full gRPC wiring.
type exportDeps struct {
	cfg           *embassyConfig
	archivist     archivistReader
	fedClient     *flow.FederationClient
	embassyDialer embassyDialerFunc
}

// handleExport is the SDK handler entry point for export Workitems.
// It creates an SDK client and delegates to processExport.
func handleExport(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("embassy: handler: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("embassy: handler: load config: %w", err)
	}

	fedClient, err := flow.NewFederationClient()
	if err != nil {
		return fmt.Errorf("embassy: handler: create federation client: %w", err)
	}
	defer func() { _ = fedClient.Close() }()

	// The archivist reader wraps the sidecar client via gRPC. We use
	// a thin adapter so the archivistReader interface is satisfied.
	ar := &sidecarArchivistAdapter{client: client}

	deps := &exportDeps{
		cfg:       cfg,
		archivist: ar,
		fedClient: fedClient,
		embassyDialer: func(addr string) (*flow.EmbassyClient, error) {
			return flow.NewEmbassyClient(flow.WithEmbassyAddress(addr))
		},
	}

	return processExport(ctx, client, wctx, deps)
}

// sidecarArchivistAdapter adapts the SDK Client's archivist methods to
// the archivistReader interface so processExport can use the same code
// path as tests.
type sidecarArchivistAdapter struct {
	client *flow.Client
}

func (a *sidecarArchivistAdapter) ListArtefacts(
	ctx context.Context, req *flowv1.ListArtefactsRequest,
) (*flowv1.ListArtefactsResponse, error) {
	return a.client.Archivist.ListArtefacts(ctx, req)
}

func (a *sidecarArchivistAdapter) GetArtefact(
	ctx context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	return a.client.Archivist.GetArtefact(ctx, req)
}

func (a *sidecarArchivistAdapter) GetStamps(
	ctx context.Context, req *flowv1.GetStampsRequest,
) (*flowv1.GetStampsResponse, error) {
	return a.client.Archivist.GetStamps(ctx, req)
}

// processExport performs the core export handler logic.
//
// Steps:
//  1. Read workitem metadata for import_type and scope.
//  2. Build a transfer manifest from the workitem's artefacts.
//  3. Resolve the target authority Flow via Federation.
//  4. Connect to the remote Embassy.
//  5. Preflight the manifest — if rejected, fail.
//  6. Stream the package — if fails, fail.
//  7. Complete the local workitem.
func processExport(
	ctx context.Context,
	client *flow.Client,
	wctx *flowv1.WorkitemContext,
	deps *exportDeps,
) error {
	workitemID := wctx.GetWorkitemId()
	metadata := wctx.GetMetadata()

	importType := metadata["import_type"]
	if importType == "" {
		return fmt.Errorf("embassy export: workitem %s missing import_type metadata", workitemID)
	}
	scope := metadata["scope"]

	// --- 1. Resolve target via Federation ---
	target, err := resolveExportTarget(ctx, deps.fedClient, importType, scope)
	if err != nil {
		return fmt.Errorf("embassy export: %w", err)
	}

	// --- 2. Build manifest from workitem artefacts ---
	manifest, contentMap, err := buildExportManifest(
		ctx, deps.archivist, workitemID, importType, target.AuthorityFlowIdentity, deps.cfg,
	)
	if err != nil {
		return fmt.Errorf("embassy export: %w", err)
	}

	// --- 3. Connect to remote Embassy ---
	var remoteClient *flow.EmbassyClient
	if deps.embassyDialer != nil {
		remoteClient, err = deps.embassyDialer(target.EmbassyEndpoint)
	} else {
		remoteClient, err = flow.NewEmbassyClient(
			flow.WithEmbassyAddress(target.EmbassyEndpoint),
		)
	}
	if err != nil {
		return fmt.Errorf("embassy export: connect to remote embassy at %s: %w",
			target.EmbassyEndpoint, err)
	}
	// Only close the client if we dialled it ourselves (not a test-injected
	// static client). The staticEmbassyDialer returns a pre-existing client
	// whose lifecycle is managed by the test.
	if deps.embassyDialer == nil {
		defer func() { _ = remoteClient.Close() }()
	}

	// --- 4. Preflight ---
	preflightResp, err := remoteClient.PreflightManifest(ctx, manifest, "")
	if err != nil {
		return fmt.Errorf("embassy export: preflight manifest: %w", err)
	}
	if !preflightResp.GetAccepted() {
		return fmt.Errorf("embassy export: preflight rejected: %s",
			preflightResp.GetRejectionReason())
	}

	// --- 5. Stream the package ---
	chunks := buildPackageChunks(manifest, contentMap)
	_, err = remoteClient.StreamPackage(ctx, chunks)
	if err != nil {
		return fmt.Errorf("embassy export: stream package: %w", err)
	}

	// --- 6. Complete the local workitem ---
	if _, err := client.Complete(ctx); err != nil {
		return fmt.Errorf("embassy export: complete workitem: %w", err)
	}

	slog.Info("embassy: export completed",
		"workitem_id", workitemID,
		"import_type", importType,
		"target_flow", target.AuthorityFlowIdentity,
		"transfer_id", manifest.GetTransferId(),
	)

	return nil
}

// buildPackageChunks constructs the ordered PackageChunk stream for an
// export transfer: manifest header, content chunks (one per artefact),
// and a trailer with the package digest.
func buildPackageChunks(
	manifest *flowv1.TransferManifest,
	contentMap map[string][]byte,
) []*flowv1.PackageChunk {
	artefacts := manifest.GetArtefacts()
	// 1 manifest header + N content chunks + 1 trailer.
	chunks := make([]*flowv1.PackageChunk, 0, len(artefacts)+2)
	chunks = append(chunks, &flowv1.PackageChunk{
		Chunk: &flowv1.PackageChunk_Manifest{Manifest: manifest},
	})

	// Content chunks in manifest artefact order.
	allContent := make([]byte, 0, len(artefacts)*1024)
	for _, art := range artefacts {
		content := contentMap[art.GetGovernedArtefact()]
		allContent = append(allContent, content...)
		chunks = append(chunks, &flowv1.PackageChunk{
			Chunk: &flowv1.PackageChunk_Content{Content: content},
		})
	}

	// Trailer with package digest.
	chunks = append(chunks, &flowv1.PackageChunk{
		Chunk: &flowv1.PackageChunk_Trailer{
			Trailer: &flowv1.PackageTrailer{
				PackageDigest: computeSHA256(allContent),
			},
		},
	})

	return chunks
}
