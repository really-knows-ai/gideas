package main

import (
	"context"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Tests -- Remote Embassy connection and transfer (slice 13.5.3)
// ---------------------------------------------------------------------------

// exportTestEnv bundles all the spies, clients, and helpers for an export
// integration test. Extract setup to avoid duplication across tests that
// vary only in assertions.
type exportTestEnv struct {
	localSpy *handlerSpy
	client   *flow.Client
	deps     *exportDeps
}

// setupSuccessfulExportEnv creates a full export pipeline backed by spy
// servers: a remote Embassy that accepts everything, a Federation spy that
// resolves the target, and a local archivist with a single petition artefact.
func setupSuccessfulExportEnv(t *testing.T, remoteWIID string) *exportTestEnv {
	t.Helper()

	remoteHandler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
		operatorSpy:  &spyOperator{returnID: remoteWIID},
		archivistSpy: &spyArchivist{},
	})
	remoteAddr := startEmbassyTestServer(t, remoteHandler)

	remoteClient, err := flow.NewEmbassyClientForTest(remoteAddr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	t.Cleanup(func() { _ = remoteClient.Close() })

	fedSpy := &spyFederation{
		authorityFlowIdentity: "authority-flow",
		embassyEndpoint:       remoteAddr,
	}
	fedAddr := startFederationSpy(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	t.Cleanup(func() { _ = fedClient.Close() })

	localArchivist := &spyArchivist{
		listArtefacts: []*flowv1.ArtefactRef{
			{Id: "art-1", GovernedArtefact: "petition"},
		},
		artefactContents: map[string][]byte{
			"petition": []byte("petition-content"),
		},
	}

	localSpy := &handlerSpy{}
	client := newHandlerTestClient(t, localSpy)

	deps := &exportDeps{
		cfg:           &embassyConfig{FederationIdentity: "local-flow"},
		archivist:     localArchivist,
		fedClient:     fedClient,
		embassyDialer: staticEmbassyDialer(remoteClient),
	}

	return &exportTestEnv{
		localSpy: localSpy,
		client:   client,
		deps:     deps,
	}
}

func TestProcessExport_ConnectsToRemoteEmbassyAndTransfers(t *testing.T) {
	// Export handler connects to remote Embassy via EmbassyClient.
	// Sends manifest via PreflightManifest → accepted, streams package
	// via StreamPackage → success, calls Complete().
	env := setupSuccessfulExportEnv(t, "wi-remote-001")

	wctx := &flowv1.WorkitemContext{
		WorkitemId: "wi-export-001",
		Metadata: map[string]string{
			"import_type": "law-petition",
			"scope":       "security",
		},
	}

	err := processExport(context.Background(), env.client, wctx, env.deps)
	if err != nil {
		t.Fatalf("processExport() returned error: %v", err)
	}

	env.localSpy.mu.Lock()
	defer env.localSpy.mu.Unlock()
	if env.localSpy.completedCount != 1 {
		t.Fatalf("expected 1 Complete call, got %d", env.localSpy.completedCount)
	}
}

func TestProcessExport_PreflightRejectedFailsWithRejectionReason(t *testing.T) {
	// Sends manifest via PreflightManifest → rejected, export fails
	// with rejection reason.

	// Remote Embassy that rejects everything (nil config rejects).
	remoteHandler := &embassyHandler{}
	remoteAddr := startEmbassyTestServer(t, remoteHandler)

	remoteClient, err := flow.NewEmbassyClientForTest(remoteAddr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = remoteClient.Close() }()

	fedSpy := &spyFederation{
		authorityFlowIdentity: "authority-flow",
		embassyEndpoint:       remoteAddr,
	}
	fedAddr := startFederationSpy(t, fedSpy)
	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	localArchivist := &spyArchivist{
		listArtefacts: []*flowv1.ArtefactRef{
			{Id: "art-1", GovernedArtefact: "petition"},
		},
		artefactContents: map[string][]byte{
			"petition": []byte("petition-content"),
		},
	}

	localSpy := &handlerSpy{}
	client := newHandlerTestClient(t, localSpy)

	cfg := &embassyConfig{
		FederationIdentity: "local-flow",
	}

	deps := &exportDeps{
		cfg:           cfg,
		archivist:     localArchivist,
		fedClient:     fedClient,
		embassyDialer: staticEmbassyDialer(remoteClient),
	}

	wctx := &flowv1.WorkitemContext{
		WorkitemId: "wi-export-rejected",
		Metadata: map[string]string{
			"import_type": "law-petition",
			"scope":       "security",
		},
	}

	err = processExport(context.Background(), client, wctx, deps)
	if err == nil {
		t.Fatal("expected error when preflight is rejected")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected error to contain 'rejected', got: %v", err)
	}

	// Should NOT have called Complete.
	localSpy.mu.Lock()
	defer localSpy.mu.Unlock()
	if localSpy.completedCount != 0 {
		t.Fatalf("expected 0 Complete calls on rejection, got %d", localSpy.completedCount)
	}
}

func TestProcessExport_SuccessfulTransferCallsComplete(t *testing.T) {
	// On successful transfer, calls client.Complete(ctx) on the local
	// Workitem. Uses the shared env helper — the key assertion here is
	// that Complete() is called exactly once.
	env := setupSuccessfulExportEnv(t, "wi-remote-002")

	wctx := &flowv1.WorkitemContext{
		WorkitemId: "wi-export-success",
		Metadata: map[string]string{
			"import_type": "law-petition",
			"scope":       "security",
		},
	}

	err := processExport(context.Background(), env.client, wctx, env.deps)
	if err != nil {
		t.Fatalf("processExport() returned error: %v", err)
	}

	env.localSpy.mu.Lock()
	defer env.localSpy.mu.Unlock()
	if env.localSpy.completedCount != 1 {
		t.Fatalf("expected 1 Complete call on success, got %d", env.localSpy.completedCount)
	}
}

func TestProcessExport_TransferFailureReturnsError(t *testing.T) {
	// On transfer failure, returns error (workitem fails).
	// We simulate failure by having the federation return an error.
	fedSpy := &spyFederation{
		returnErr: status.Error(codes.NotFound, "no authority for scope"),
	}
	fedAddr := startFederationSpy(t, fedSpy)
	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	localArchivist := &spyArchivist{
		listArtefacts: []*flowv1.ArtefactRef{
			{Id: "art-1", GovernedArtefact: "petition"},
		},
		artefactContents: map[string][]byte{
			"petition": []byte("petition-content"),
		},
	}

	localSpy := &handlerSpy{}
	client := newHandlerTestClient(t, localSpy)

	cfg := &embassyConfig{
		FederationIdentity: "local-flow",
	}

	deps := &exportDeps{
		cfg:           cfg,
		archivist:     localArchivist,
		fedClient:     fedClient,
		embassyDialer: nil, // Should not get this far
	}

	wctx := &flowv1.WorkitemContext{
		WorkitemId: "wi-export-fail",
		Metadata: map[string]string{
			"import_type": "law-petition",
			"scope":       "security",
		},
	}

	err = processExport(context.Background(), client, wctx, deps)
	if err == nil {
		t.Fatal("expected error when federation returns error")
	}

	// Should NOT have called Complete.
	localSpy.mu.Lock()
	defer localSpy.mu.Unlock()
	if localSpy.completedCount != 0 {
		t.Fatalf("expected 0 Complete calls on failure, got %d", localSpy.completedCount)
	}
}

// staticEmbassyDialer returns an embassyDialerFunc that always returns
// the given pre-connected EmbassyClient, ignoring the address argument.
func staticEmbassyDialer(c *flow.EmbassyClient) embassyDialerFunc {
	return func(_ string) (*flow.EmbassyClient, error) {
		return c, nil
	}
}
