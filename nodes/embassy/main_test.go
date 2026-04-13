package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---------------------------------------------------------------------------
// Tests -- Embassy scaffold: entry function starts without error
// ---------------------------------------------------------------------------

func TestWatchInbound_StartsEmbassyServer(t *testing.T) {
	// The entry function should start an Embassy gRPC server and block
	// until context is cancelled. We verify it doesn't return an error
	// when cancelled.
	opSpy := &spyOperator{returnID: "wi-1"}
	ebSpy := &spyEventBus{}
	ec := setupEntryTestClient(t, opSpy, ebSpy)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- watchInbound(ctx, ec)
	}()

	// Give the server time to start, then cancel.
	cancel()

	err := <-errCh
	if err != nil && err != context.Canceled {
		t.Fatalf("watchInbound() returned unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests -- Embassy handler: stub RPCs return Unimplemented
// ---------------------------------------------------------------------------

func TestEmbassyHandler_PreflightManifest_NilConfigRejects(t *testing.T) {
	// A handler without config (nil cfg) rejects all manifests because
	// import type resolution cannot proceed without a registry.
	handler := &embassyHandler{}
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	resp, err := embClient.PreflightManifest(context.Background(), &flowv1.TransferManifest{
		ImportType: "law-petition",
	}, "")
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("expected rejection when handler has nil config")
	}
	if resp.GetRejectionReason() == "" {
		t.Fatal("expected non-empty rejection reason")
	}
}

func TestEmbassyHandler_StreamPackage_NoManifestRejectsWithInvalidArgument(t *testing.T) {
	handler := &embassyHandler{}
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	_, err = embClient.StreamPackage(context.Background(), []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Content{Content: []byte("data")}},
	})

	if err == nil {
		t.Fatal("expected error from StreamPackage, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", st.Code())
	}
}

func TestEmbassyHandler_ExportPackage_Unimplemented(t *testing.T) {
	handler := &embassyHandler{}
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	stream, err := embClient.ExportPackage(context.Background(), "wi-1", "law-petition")
	if err != nil {
		// Error on opening the stream — check for Unimplemented.
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("expected gRPC status error, got: %v", err)
		}
		if st.Code() != codes.Unimplemented {
			t.Fatalf("expected Unimplemented, got %v", st.Code())
		}
		return
	}

	// The server-streaming error may surface on first Recv.
	_, err = stream.Recv()
	if err == nil || err == io.EOF {
		t.Fatal("expected error from ExportPackage Recv, got nil or EOF")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %v", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Tests -- Embassy preflight: import type resolution (slice 13.2.1)
// ---------------------------------------------------------------------------

func TestPreflightManifest_LawPetitionResolvesViaSystemImportTypes(t *testing.T) {
	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	resp, err := embClient.PreflightManifest(context.Background(), &flowv1.TransferManifest{
		ImportType: "law-petition",
		SourceFlow: "remote-flow",
		TargetFlow: "local-flow",
	}, "")
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected manifest accepted, got rejection: %s", resp.GetRejectionReason())
	}
}

func TestPreflightManifest_FlowAuthoredImportTypeResolves(t *testing.T) {
	handler := newTestHandler(t, testHandlerOpts{
		flowImportTypes: map[string]flowImportTypeSpec{
			"external-submission": {
				Node: "intake-triage",
				RequireForeignStamps: map[string][]string{
					"submission": {"approval"},
				},
			},
		},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	resp, err := embClient.PreflightManifest(context.Background(), &flowv1.TransferManifest{
		ImportType: "external-submission",
		SourceFlow: "remote-flow",
		TargetFlow: "local-flow",
	}, "")
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected manifest accepted for flow-authored type, got rejection: %s", resp.GetRejectionReason())
	}
}

func TestPreflightManifest_UnknownImportTypeRejected(t *testing.T) {
	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	resp, err := embClient.PreflightManifest(context.Background(), &flowv1.TransferManifest{
		ImportType: "nonexistent-type",
		SourceFlow: "remote-flow",
		TargetFlow: "local-flow",
	}, "")
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("expected manifest rejected for unknown import type")
	}
	if resp.GetRejectionReason() == "" {
		t.Fatal("expected non-empty rejection reason")
	}
}

func TestPreflightManifest_GeneratesTransferID(t *testing.T) {
	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	resp, err := embClient.PreflightManifest(context.Background(), &flowv1.TransferManifest{
		ImportType: "law-petition",
		SourceFlow: "remote-flow",
		TargetFlow: "local-flow",
	}, "")
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if resp.GetTransferId() == "" {
		t.Fatal("expected non-empty transfer_id in response")
	}

	// Verify a second call generates a different transfer_id.
	resp2, err := embClient.PreflightManifest(context.Background(), &flowv1.TransferManifest{
		ImportType: "law-petition",
		SourceFlow: "remote-flow",
		TargetFlow: "local-flow",
	}, "")
	if err != nil {
		t.Fatalf("second PreflightManifest() returned error: %v", err)
	}
	if resp2.GetTransferId() == resp.GetTransferId() {
		t.Fatalf("expected unique transfer_ids, both were %q", resp.GetTransferId())
	}
}

// ---------------------------------------------------------------------------
// Tests -- Embassy preflight: trust source validation (slice 13.2.2)
// ---------------------------------------------------------------------------

func TestPreflightManifest_FederationTrustAcceptsMember(t *testing.T) {
	// Federation trust: a manifest from a federation member with valid
	// identity is accepted (federation trust is implicit — no treaty
	// constraints).
	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
		federationID:     "local-flow",
		federationStates: []string{"state-a"},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	resp, err := embClient.PreflightManifest(context.Background(), &flowv1.TransferManifest{
		ImportType: "law-petition",
		SourceFlow: "remote-federation-member",
		TargetFlow: "local-flow",
	}, "") // empty treaty_name → federation trust
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected federation member manifest accepted, got rejection: %s", resp.GetRejectionReason())
	}
}

func TestPreflightManifest_TreatyTrustAcceptsAllowedImportType(t *testing.T) {
	// Treaty trust: a manifest with a treaty name resolves against the
	// treaty's allowed import types. When the type is allowed, accepted.
	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
		treaties: map[string]treatyConfig{
			"partner-treaty": {
				AllowedImportTypes: []string{"law-petition"},
				AllowedSubjects:    []string{"spiffe://partner/embassy"},
			},
		},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	resp, err := embClient.PreflightManifest(context.Background(), &flowv1.TransferManifest{
		ImportType: "law-petition",
		SourceFlow: "partner-flow",
		TargetFlow: "local-flow",
		Signature:  &flowv1.ManifestSignature{Subject: "spiffe://partner/embassy"},
	}, "partner-treaty")
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected treaty manifest accepted, got rejection: %s", resp.GetRejectionReason())
	}
}

func TestPreflightManifest_TreatyTrustRejectsDisallowedImportType(t *testing.T) {
	// Treaty trust: import type not in AllowedImportTypes → rejected.
	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
		flowImportTypes: map[string]flowImportTypeSpec{
			"external-submission": {Node: "intake"},
		},
		treaties: map[string]treatyConfig{
			"partner-treaty": {
				AllowedImportTypes: []string{"law-petition"},
			},
		},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	resp, err := embClient.PreflightManifest(context.Background(), &flowv1.TransferManifest{
		ImportType: "external-submission",
		SourceFlow: "partner-flow",
		TargetFlow: "local-flow",
	}, "partner-treaty")
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("expected treaty manifest rejected for disallowed import type")
	}
	if resp.GetRejectionReason() == "" {
		t.Fatal("expected non-empty rejection reason")
	}
}

func TestPreflightManifest_TreatyTrustRejectsDisallowedSubject(t *testing.T) {
	// Treaty trust: subject not in AllowedSubjects → rejected.
	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
		treaties: map[string]treatyConfig{
			"partner-treaty": {
				AllowedSubjects: []string{"spiffe://partner/embassy"},
			},
		},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	resp, err := embClient.PreflightManifest(context.Background(), &flowv1.TransferManifest{
		ImportType: "law-petition",
		SourceFlow: "rogue-flow",
		TargetFlow: "local-flow",
		Signature:  &flowv1.ManifestSignature{Subject: "spiffe://rogue/embassy"},
	}, "partner-treaty")
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("expected treaty manifest rejected for disallowed subject")
	}
	if resp.GetRejectionReason() == "" {
		t.Fatal("expected non-empty rejection reason")
	}
}

func TestPreflightManifest_ExpiredManifestRejected(t *testing.T) {
	// A manifest with an expired expires_at → rejected regardless of
	// trust source.
	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	// Set expires_at to the past.
	pastTime := timestamppb.New(time.Now().Add(-1 * time.Hour))
	resp, err := embClient.PreflightManifest(context.Background(), &flowv1.TransferManifest{
		ImportType: "law-petition",
		SourceFlow: "remote-flow",
		TargetFlow: "local-flow",
		ExpiresAt:  pastTime,
	}, "")
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("expected expired manifest to be rejected")
	}
	if resp.GetRejectionReason() == "" {
		t.Fatal("expected non-empty rejection reason for expired manifest")
	}
}

// ---------------------------------------------------------------------------
// Tests -- Embassy preflight: foreign stamp verification (slice 13.2.3)
// ---------------------------------------------------------------------------

func TestPreflightManifest_AllRequiredForeignStampsPresent(t *testing.T) {
	// A flow-authored import type that requires foreign stamps.
	// Manifest has all required stamps → accepted.
	handler := newTestHandler(t, testHandlerOpts{
		flowImportTypes: map[string]flowImportTypeSpec{
			"external-submission": {
				Node: "intake-triage",
				RequireForeignStamps: map[string][]string{
					"submission": {"approval", "review"},
				},
			},
		},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	resp, err := embClient.PreflightManifest(context.Background(), &flowv1.TransferManifest{
		ImportType: "external-submission",
		SourceFlow: "remote-flow",
		TargetFlow: "local-flow",
		Artefacts: []*flowv1.ArtefactManifest{
			{
				GovernedArtefact: "submission",
				Digest:           "sha256:abc",
				SizeBytes:        100,
				ForeignStamps: []*flowv1.ForeignStamp{
					{StampName: "approval", Issuer: "remote-flow"},
					{StampName: "review", Issuer: "remote-flow"},
				},
			},
		},
	}, "")
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected manifest accepted with all required stamps, got rejection: %s", resp.GetRejectionReason())
	}
}

func TestPreflightManifest_MissingRequiredForeignStampRejected(t *testing.T) {
	// A flow-authored import type requires "approval" and "review" stamps.
	// Manifest only has "approval" → rejected with reason listing "review".
	handler := newTestHandler(t, testHandlerOpts{
		flowImportTypes: map[string]flowImportTypeSpec{
			"external-submission": {
				Node: "intake-triage",
				RequireForeignStamps: map[string][]string{
					"submission": {"approval", "review"},
				},
			},
		},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	resp, err := embClient.PreflightManifest(context.Background(), &flowv1.TransferManifest{
		ImportType: "external-submission",
		SourceFlow: "remote-flow",
		TargetFlow: "local-flow",
		Artefacts: []*flowv1.ArtefactManifest{
			{
				GovernedArtefact: "submission",
				Digest:           "sha256:abc",
				SizeBytes:        100,
				ForeignStamps: []*flowv1.ForeignStamp{
					{StampName: "approval", Issuer: "remote-flow"},
					// "review" stamp is missing
				},
			},
		},
	}, "")
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("expected manifest rejected for missing required stamp")
	}
	reason := resp.GetRejectionReason()
	if reason == "" {
		t.Fatal("expected non-empty rejection reason")
	}
	// The rejection reason should mention the missing stamp name.
	if !strings.Contains(reason, "review") {
		t.Fatalf("expected rejection reason to mention missing stamp 'review', got: %s", reason)
	}
}

func TestPreflightManifest_ExtraUnrequiredStampsAccepted(t *testing.T) {
	// Manifest has extra stamps beyond what is required → accepted.
	// Extra stamps are provenance only and should not cause rejection.
	handler := newTestHandler(t, testHandlerOpts{
		flowImportTypes: map[string]flowImportTypeSpec{
			"external-submission": {
				Node: "intake-triage",
				RequireForeignStamps: map[string][]string{
					"submission": {"approval"},
				},
			},
		},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	resp, err := embClient.PreflightManifest(context.Background(), &flowv1.TransferManifest{
		ImportType: "external-submission",
		SourceFlow: "remote-flow",
		TargetFlow: "local-flow",
		Artefacts: []*flowv1.ArtefactManifest{
			{
				GovernedArtefact: "submission",
				Digest:           "sha256:abc",
				SizeBytes:        100,
				ForeignStamps: []*flowv1.ForeignStamp{
					{StampName: "approval", Issuer: "remote-flow"},
					{StampName: "extra-provenance", Issuer: "remote-flow"},
					{StampName: "another-extra", Issuer: "remote-flow"},
				},
			},
		},
	}, "")
	if err != nil {
		t.Fatalf("PreflightManifest() returned error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected manifest accepted with extra stamps, got rejection: %s", resp.GetRejectionReason())
	}
}

// ---------------------------------------------------------------------------
// Tests -- Embassy handler: handleExport stub
// ---------------------------------------------------------------------------

func TestHandleExport_MissingImportTypeReturnsError(t *testing.T) {
	// processExport should return an error when import_type metadata is
	// missing from the workitem context.
	wctx := &flowv1.WorkitemContext{
		FlowNamespace: "test-ns",
		WorkitemId:    "wi-export-001",
		NodeId:        "embassy",
		Metadata:      map[string]string{},
	}

	spy := &handlerSpy{}
	client := newHandlerTestClient(t, spy)

	deps := &exportDeps{
		cfg: &embassyConfig{FederationIdentity: "local-flow"},
	}

	err := processExport(context.Background(), client, wctx, deps)
	if err == nil {
		t.Fatal("expected processExport to return error when import_type is missing")
	}
}

// ---------------------------------------------------------------------------
// Tests -- Embassy stager: package staging (slice 13.3.1)
// ---------------------------------------------------------------------------

func TestStager_AcceptsManifestChunkAndStoresIt(t *testing.T) {
	stager := newEmbassyStager()
	manifest := &flowv1.TransferManifest{
		ImportType: "law-petition",
		SourceFlow: "remote-flow",
		TargetFlow: "local-flow",
		TransferId: "tx-1",
	}

	err := stager.StageManifest(context.Background(), manifest)
	if err != nil {
		t.Fatalf("StageManifest() returned error: %v", err)
	}

	staged, err := stager.Complete(context.Background())
	if err != nil {
		t.Fatalf("Complete() returned error: %v", err)
	}
	if staged.Manifest == nil {
		t.Fatal("expected staged manifest to be non-nil")
	}
	if staged.Manifest.GetTransferId() != "tx-1" {
		t.Fatalf("expected transfer_id 'tx-1', got %q", staged.Manifest.GetTransferId())
	}
}

func TestStager_AcceptsContentChunksAndAccumulatesThem(t *testing.T) {
	stager := newEmbassyStager()
	manifest := &flowv1.TransferManifest{
		ImportType: "law-petition",
		TransferId: "tx-2",
	}

	if err := stager.StageManifest(context.Background(), manifest); err != nil {
		t.Fatalf("StageManifest() returned error: %v", err)
	}

	chunk1 := &flowv1.PackageChunk{Chunk: &flowv1.PackageChunk_Content{Content: []byte("part-1")}}
	chunk2 := &flowv1.PackageChunk{Chunk: &flowv1.PackageChunk_Content{Content: []byte("part-2")}}

	if err := stager.StageChunk(context.Background(), chunk1); err != nil {
		t.Fatalf("StageChunk(1) returned error: %v", err)
	}
	if err := stager.StageChunk(context.Background(), chunk2); err != nil {
		t.Fatalf("StageChunk(2) returned error: %v", err)
	}

	staged, err := stager.Complete(context.Background())
	if err != nil {
		t.Fatalf("Complete() returned error: %v", err)
	}
	if len(staged.Chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(staged.Chunks))
	}
}

func TestStager_AcceptsTrailerChunkWithPackageDigest(t *testing.T) {
	stager := newEmbassyStager()
	manifest := &flowv1.TransferManifest{
		ImportType: "law-petition",
		TransferId: "tx-3",
	}

	if err := stager.StageManifest(context.Background(), manifest); err != nil {
		t.Fatalf("StageManifest() returned error: %v", err)
	}

	content := &flowv1.PackageChunk{Chunk: &flowv1.PackageChunk_Content{Content: []byte("payload")}}
	trailer := &flowv1.PackageChunk{
		Chunk: &flowv1.PackageChunk_Trailer{
			Trailer: &flowv1.PackageTrailer{PackageDigest: "sha256:abcdef"},
		},
	}

	if err := stager.StageChunk(context.Background(), content); err != nil {
		t.Fatalf("StageChunk(content) returned error: %v", err)
	}
	if err := stager.StageChunk(context.Background(), trailer); err != nil {
		t.Fatalf("StageChunk(trailer) returned error: %v", err)
	}

	staged, err := stager.Complete(context.Background())
	if err != nil {
		t.Fatalf("Complete() returned error: %v", err)
	}
	// Trailer should be stored as one of the chunks.
	if len(staged.Chunks) != 2 {
		t.Fatalf("expected 2 chunks (content + trailer), got %d", len(staged.Chunks))
	}
	// Verify we can access the trailer digest from the staged package.
	lastChunk := staged.Chunks[len(staged.Chunks)-1]
	if lastChunk.GetTrailer() == nil {
		t.Fatal("expected last chunk to be a trailer")
	}
	if lastChunk.GetTrailer().GetPackageDigest() != "sha256:abcdef" {
		t.Fatalf("expected package digest 'sha256:abcdef', got %q", lastChunk.GetTrailer().GetPackageDigest())
	}
}

func TestStager_CompleteReturnsEmbassyStagedPackageWithManifestAndChunks(t *testing.T) {
	stager := newEmbassyStager()
	manifest := &flowv1.TransferManifest{
		ImportType: "law-petition",
		SourceFlow: "remote-flow",
		TargetFlow: "local-flow",
		TransferId: "tx-4",
		Artefacts: []*flowv1.ArtefactManifest{
			{GovernedArtefact: "petition", Digest: "sha256:aaa", SizeBytes: 50},
		},
	}

	if err := stager.StageManifest(context.Background(), manifest); err != nil {
		t.Fatalf("StageManifest() returned error: %v", err)
	}

	chunk := &flowv1.PackageChunk{Chunk: &flowv1.PackageChunk_Content{Content: []byte("data")}}
	if err := stager.StageChunk(context.Background(), chunk); err != nil {
		t.Fatalf("StageChunk() returned error: %v", err)
	}

	staged, err := stager.Complete(context.Background())
	if err != nil {
		t.Fatalf("Complete() returned error: %v", err)
	}
	if staged.Manifest == nil {
		t.Fatal("expected staged manifest to be non-nil")
	}
	if staged.Manifest.GetImportType() != "law-petition" {
		t.Fatalf("expected import_type 'law-petition', got %q", staged.Manifest.GetImportType())
	}
	if len(staged.Chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(staged.Chunks))
	}
}

func TestStager_EmptyChunkStreamErrorsOnComplete(t *testing.T) {
	stager := newEmbassyStager()

	// No manifest, no chunks — Complete should fail.
	_, err := stager.Complete(context.Background())
	if err == nil {
		t.Fatal("expected error from Complete() on empty chunk stream")
	}
}

// ---------------------------------------------------------------------------
// Tests -- Embassy stager: digest verification (slice 13.3.2)
// ---------------------------------------------------------------------------

func TestVerifyDigests_MatchingTrailerDigestPasses(t *testing.T) {
	content := []byte("hello world")
	digest := computeSHA256(content)

	staged := &flow.EmbassyStagedPackage{
		Manifest: &flowv1.TransferManifest{
			ImportType: "law-petition",
			TransferId: "tx-verify-1",
		},
		Chunks: []*flowv1.PackageChunk{
			{Chunk: &flowv1.PackageChunk_Content{Content: content}},
			{Chunk: &flowv1.PackageChunk_Trailer{
				Trailer: &flowv1.PackageTrailer{PackageDigest: digest},
			}},
		},
	}

	err := verifyPackageDigests(staged)
	if err != nil {
		t.Fatalf("verifyPackageDigests() returned error: %v", err)
	}
}

func TestVerifyDigests_MismatchedTrailerDigestErrors(t *testing.T) {
	content := []byte("hello world")

	staged := &flow.EmbassyStagedPackage{
		Manifest: &flowv1.TransferManifest{
			ImportType: "law-petition",
			TransferId: "tx-verify-2",
		},
		Chunks: []*flowv1.PackageChunk{
			{Chunk: &flowv1.PackageChunk_Content{Content: content}},
			{Chunk: &flowv1.PackageChunk_Trailer{
				Trailer: &flowv1.PackageTrailer{
					PackageDigest: "sha256:wrong",
				},
			}},
		},
	}

	err := verifyPackageDigests(staged)
	if err == nil {
		t.Fatal("expected error for mismatched trailer digest")
	}
}

func TestVerifyDigests_PerArtefactDigestMatchPasses(t *testing.T) {
	content := []byte("artefact-payload")
	digest := computeSHA256(content)

	staged := &flow.EmbassyStagedPackage{
		Manifest: &flowv1.TransferManifest{
			ImportType: "law-petition",
			TransferId: "tx-verify-3",
			Artefacts: []*flowv1.ArtefactManifest{
				{
					GovernedArtefact: "petition",
					Digest:           digest,
					SizeBytes:        int64(len(content)),
				},
			},
		},
		Chunks: []*flowv1.PackageChunk{
			{Chunk: &flowv1.PackageChunk_Content{Content: content}},
		},
	}

	err := verifyPackageDigests(staged)
	if err != nil {
		t.Fatalf("verifyPackageDigests() returned error: %v", err)
	}
}

func TestVerifyDigests_PerArtefactDigestMismatchErrors(t *testing.T) {
	content := []byte("artefact-payload")

	staged := &flow.EmbassyStagedPackage{
		Manifest: &flowv1.TransferManifest{
			ImportType: "law-petition",
			TransferId: "tx-verify-4",
			Artefacts: []*flowv1.ArtefactManifest{
				{
					GovernedArtefact: "petition",
					Digest:           "sha256:wrong-artefact-digest",
					SizeBytes:        int64(len(content)),
				},
			},
		},
		Chunks: []*flowv1.PackageChunk{
			{Chunk: &flowv1.PackageChunk_Content{Content: content}},
		},
	}

	err := verifyPackageDigests(staged)
	if err == nil {
		t.Fatal("expected error for mismatched artefact digest")
	}
}

// ---------------------------------------------------------------------------
// Tests -- Embassy StreamPackage handler wiring (slice 13.3.3)
// ---------------------------------------------------------------------------

func TestStreamPackage_ValidManifestContentTrailerReturnsSuccess(t *testing.T) {
	content := []byte("petition-content")
	contentDigest := computeSHA256(content)
	packageDigest := computeSHA256(content)

	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	chunks := []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{
			Manifest: &flowv1.TransferManifest{
				ImportType: "law-petition",
				SourceFlow: "remote-flow",
				TargetFlow: "local-flow",
				TransferId: "tx-stream-1",
				Artefacts: []*flowv1.ArtefactManifest{
					{
						GovernedArtefact: "petition",
						Digest:           contentDigest,
						SizeBytes:        int64(len(content)),
					},
				},
			},
		}},
		{Chunk: &flowv1.PackageChunk_Content{Content: content}},
		{Chunk: &flowv1.PackageChunk_Trailer{
			Trailer: &flowv1.PackageTrailer{
				PackageDigest: packageDigest,
			},
		}},
	}

	resp, err := embClient.StreamPackage(context.Background(), chunks)
	if err != nil {
		t.Fatalf("StreamPackage() returned error: %v", err)
	}
	if resp.GetWorkitemId() == "" {
		t.Fatal("expected non-empty workitem_id in response")
	}
	if resp.GetErrorReason() != "" {
		t.Fatalf(
			"expected no error_reason, got %q",
			resp.GetErrorReason(),
		)
	}
}

func TestStreamPackage_FailedDigestVerificationReturnsError(t *testing.T) {
	content := []byte("petition-content")

	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	chunks := []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{
			Manifest: &flowv1.TransferManifest{
				ImportType: "law-petition",
				SourceFlow: "remote-flow",
				TargetFlow: "local-flow",
				TransferId: "tx-stream-bad",
			},
		}},
		{Chunk: &flowv1.PackageChunk_Content{Content: content}},
		{Chunk: &flowv1.PackageChunk_Trailer{
			Trailer: &flowv1.PackageTrailer{
				PackageDigest: "sha256:wrong-digest",
			},
		}},
	}

	_, err = embClient.StreamPackage(context.Background(), chunks)
	if err == nil {
		t.Fatal("expected error from StreamPackage with bad digest")
	}
}

func TestStreamPackage_UnknownImportTypeReturnsError(t *testing.T) {
	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	chunks := []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{
			Manifest: &flowv1.TransferManifest{
				ImportType: "unknown-type",
				SourceFlow: "remote-flow",
				TargetFlow: "local-flow",
				TransferId: "tx-stream-unknown",
			},
		}},
		{Chunk: &flowv1.PackageChunk_Content{
			Content: []byte("payload"),
		}},
	}

	_, err = embClient.StreamPackage(context.Background(), chunks)
	if err == nil {
		t.Fatal(
			"expected error from StreamPackage with unknown import type",
		)
	}
}

// ---------------------------------------------------------------------------
// Tests -- Embassy materialiser: workitem creation and artefact unpacking
// (slice 13.4.1)
// ---------------------------------------------------------------------------

func TestStreamPackage_MaterialiserCreatesWorkitem(t *testing.T) {
	content := []byte("petition-content")
	contentDigest := computeSHA256(content)
	packageDigest := computeSHA256(content)

	opSpy := &spyOperator{returnID: "wi-import-001"}
	arSpy := &spyArchivist{}

	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
		operatorSpy:  opSpy,
		archivistSpy: arSpy,
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	chunks := []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{
			Manifest: &flowv1.TransferManifest{
				ImportType: "law-petition",
				SourceFlow: "remote-flow",
				TargetFlow: "local-flow",
				TransferId: "tx-mat-1",
				Artefacts: []*flowv1.ArtefactManifest{
					{
						GovernedArtefact: "petition",
						Digest:           contentDigest,
						SizeBytes:        int64(len(content)),
					},
				},
			},
		}},
		{Chunk: &flowv1.PackageChunk_Content{Content: content}},
		{Chunk: &flowv1.PackageChunk_Trailer{
			Trailer: &flowv1.PackageTrailer{PackageDigest: packageDigest},
		}},
	}

	resp, err := embClient.StreamPackage(context.Background(), chunks)
	if err != nil {
		t.Fatalf("StreamPackage() returned error: %v", err)
	}

	// Materialiser should have created a workitem.
	if resp.GetWorkitemId() != "wi-import-001" {
		t.Fatalf("expected workitem_id 'wi-import-001', got %q", resp.GetWorkitemId())
	}

	opSpy.mu.Lock()
	defer opSpy.mu.Unlock()
	if len(opSpy.calls) != 1 {
		t.Fatalf("expected 1 CreateWorkitem call, got %d", len(opSpy.calls))
	}
}

func TestStreamPackage_MaterialiserStoresArtefacts(t *testing.T) {
	content := []byte("petition-content")
	contentDigest := computeSHA256(content)
	packageDigest := computeSHA256(content)

	opSpy := &spyOperator{returnID: "wi-import-002"}
	arSpy := &spyArchivist{}

	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
		operatorSpy:  opSpy,
		archivistSpy: arSpy,
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	chunks := []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{
			Manifest: &flowv1.TransferManifest{
				ImportType: "law-petition",
				SourceFlow: "remote-flow",
				TargetFlow: "local-flow",
				TransferId: "tx-mat-2",
				Artefacts: []*flowv1.ArtefactManifest{
					{
						GovernedArtefact: "petition",
						Digest:           contentDigest,
						SizeBytes:        int64(len(content)),
					},
				},
			},
		}},
		{Chunk: &flowv1.PackageChunk_Content{Content: content}},
		{Chunk: &flowv1.PackageChunk_Trailer{
			Trailer: &flowv1.PackageTrailer{PackageDigest: packageDigest},
		}},
	}

	_, err = embClient.StreamPackage(context.Background(), chunks)
	if err != nil {
		t.Fatalf("StreamPackage() returned error: %v", err)
	}

	arSpy.mu.Lock()
	defer arSpy.mu.Unlock()
	if len(arSpy.storedArtefacts) != 1 {
		t.Fatalf("expected 1 StoreArtefact call, got %d", len(arSpy.storedArtefacts))
	}
	stored := arSpy.storedArtefacts[0]
	if stored.GetWorkitemId() != "wi-import-002" {
		t.Fatalf("expected workitem_id 'wi-import-002', got %q", stored.GetWorkitemId())
	}
	if stored.GetGovernedArtefact() != "petition" {
		t.Fatalf("expected governed_artefact 'petition', got %q", stored.GetGovernedArtefact())
	}
	if string(stored.GetContent()) != string(content) {
		t.Fatalf("expected artefact content to match")
	}
}

func TestStreamPackage_WorkitemMetadataIncludesImportFields(t *testing.T) {
	content := []byte("petition-content")
	contentDigest := computeSHA256(content)
	packageDigest := computeSHA256(content)

	opSpy := &spyOperator{returnID: "wi-import-003"}
	arSpy := &spyArchivist{}

	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
		operatorSpy:  opSpy,
		archivistSpy: arSpy,
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	chunks := []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{
			Manifest: &flowv1.TransferManifest{
				ImportType: "law-petition",
				SourceFlow: "remote-flow",
				TargetFlow: "local-flow",
				TransferId: "tx-mat-3",
				Artefacts: []*flowv1.ArtefactManifest{
					{
						GovernedArtefact: "petition",
						Digest:           contentDigest,
						SizeBytes:        int64(len(content)),
					},
				},
			},
		}},
		{Chunk: &flowv1.PackageChunk_Content{Content: content}},
		{Chunk: &flowv1.PackageChunk_Trailer{
			Trailer: &flowv1.PackageTrailer{PackageDigest: packageDigest},
		}},
	}

	_, err = embClient.StreamPackage(context.Background(), chunks)
	if err != nil {
		t.Fatalf("StreamPackage() returned error: %v", err)
	}

	opSpy.mu.Lock()
	defer opSpy.mu.Unlock()
	if len(opSpy.calls) != 1 {
		t.Fatalf("expected 1 CreateWorkitem call, got %d", len(opSpy.calls))
	}
	meta := opSpy.calls[0].GetMetadata()
	if meta["import_type"] != "law-petition" {
		t.Fatalf("expected metadata import_type='law-petition', got %q", meta["import_type"])
	}
	if meta["source_flow"] != "remote-flow" {
		t.Fatalf("expected metadata source_flow='remote-flow', got %q", meta["source_flow"])
	}
	if meta["transfer_id"] != "tx-mat-3" {
		t.Fatalf("expected metadata transfer_id='tx-mat-3', got %q", meta["transfer_id"])
	}
}

// ---------------------------------------------------------------------------
// Tests -- Embassy materialiser: naturalisation stamps (slice 13.4.2)
// ---------------------------------------------------------------------------

func TestStreamPackage_NaturalisationAppliesImportedStamps(t *testing.T) {
	// For each verified required foreign stamp, materialiser applies an
	// "imported-<stamp>" local attestation.
	content := []byte("submission-content")
	contentDigest := computeSHA256(content)
	packageDigest := computeSHA256(content)

	opSpy := &spyOperator{returnID: "wi-nat-001"}
	arSpy := &spyArchivist{}
	autoNat := true

	handler := newTestHandler(t, testHandlerOpts{
		flowImportTypes: map[string]flowImportTypeSpec{
			"external-submission": {
				Node: "intake-triage",
				RequireForeignStamps: map[string][]string{
					"submission": {"approval", "review"},
				},
			},
		},
		naturalisation: &naturalisationConfig{
			AutoNaturalise: &autoNat,
		},
		operatorSpy:  opSpy,
		archivistSpy: arSpy,
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	chunks := []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{
			Manifest: &flowv1.TransferManifest{
				ImportType: "external-submission",
				SourceFlow: "remote-flow",
				TargetFlow: "local-flow",
				TransferId: "tx-nat-1",
				Artefacts: []*flowv1.ArtefactManifest{
					{
						GovernedArtefact: "submission",
						Digest:           contentDigest,
						SizeBytes:        int64(len(content)),
						ForeignStamps: []*flowv1.ForeignStamp{
							{StampName: "approval", Issuer: "remote-flow"},
							{StampName: "review", Issuer: "remote-flow"},
						},
					},
				},
			},
		}},
		{Chunk: &flowv1.PackageChunk_Content{Content: content}},
		{Chunk: &flowv1.PackageChunk_Trailer{
			Trailer: &flowv1.PackageTrailer{PackageDigest: packageDigest},
		}},
	}

	_, err = embClient.StreamPackage(context.Background(), chunks)
	if err != nil {
		t.Fatalf("StreamPackage() returned error: %v", err)
	}

	arSpy.mu.Lock()
	defer arSpy.mu.Unlock()

	// Should have 2 stamp calls: "imported-approval" and "imported-review".
	if len(arSpy.stampedCalls) != 2 {
		t.Fatalf("expected 2 StampArtefact calls, got %d", len(arSpy.stampedCalls))
	}

	stampNames := map[string]bool{}
	for _, call := range arSpy.stampedCalls {
		stampNames[call.GetStampName()] = true
		if call.GetWorkitemId() != "wi-nat-001" {
			t.Fatalf("expected stamp workitem_id 'wi-nat-001', got %q", call.GetWorkitemId())
		}
		if call.GetArtefactId() != "submission" {
			t.Fatalf("expected stamp artefact_id 'submission', got %q", call.GetArtefactId())
		}
	}
	if !stampNames["imported-approval"] {
		t.Fatal("expected 'imported-approval' stamp to be applied")
	}
	if !stampNames["imported-review"] {
		t.Fatal("expected 'imported-review' stamp to be applied")
	}
}

func TestStreamPackage_NaturalisationPreservesForeignStamps(t *testing.T) {
	// Foreign stamps remain attached as provenance — stored artefact
	// content is unchanged. We verify artefact content is stored as-is.
	content := []byte("submission-content")
	contentDigest := computeSHA256(content)
	packageDigest := computeSHA256(content)

	opSpy := &spyOperator{returnID: "wi-nat-002"}
	arSpy := &spyArchivist{}
	autoNat := true

	handler := newTestHandler(t, testHandlerOpts{
		flowImportTypes: map[string]flowImportTypeSpec{
			"external-submission": {
				Node: "intake-triage",
				RequireForeignStamps: map[string][]string{
					"submission": {"approval"},
				},
			},
		},
		naturalisation: &naturalisationConfig{
			AutoNaturalise: &autoNat,
		},
		operatorSpy:  opSpy,
		archivistSpy: arSpy,
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	chunks := []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{
			Manifest: &flowv1.TransferManifest{
				ImportType: "external-submission",
				SourceFlow: "remote-flow",
				TargetFlow: "local-flow",
				TransferId: "tx-nat-2",
				Artefacts: []*flowv1.ArtefactManifest{
					{
						GovernedArtefact: "submission",
						Digest:           contentDigest,
						SizeBytes:        int64(len(content)),
						ForeignStamps: []*flowv1.ForeignStamp{
							{StampName: "approval", Issuer: "remote-flow"},
							{StampName: "extra-provenance", Issuer: "remote-flow"},
						},
					},
				},
			},
		}},
		{Chunk: &flowv1.PackageChunk_Content{Content: content}},
		{Chunk: &flowv1.PackageChunk_Trailer{
			Trailer: &flowv1.PackageTrailer{PackageDigest: packageDigest},
		}},
	}

	_, err = embClient.StreamPackage(context.Background(), chunks)
	if err != nil {
		t.Fatalf("StreamPackage() returned error: %v", err)
	}

	arSpy.mu.Lock()
	defer arSpy.mu.Unlock()

	// Artefact content stored as-is (foreign stamps do not modify content).
	if len(arSpy.storedArtefacts) != 1 {
		t.Fatalf("expected 1 StoreArtefact call, got %d", len(arSpy.storedArtefacts))
	}
	if string(arSpy.storedArtefacts[0].GetContent()) != string(content) {
		t.Fatal("expected artefact content to be stored unmodified")
	}
}

func TestStreamPackage_NaturalisationAppliesRequireLocalStamps(t *testing.T) {
	// If naturalisation config has requireLocalStamps, those are applied.
	content := []byte("petition-content")
	contentDigest := computeSHA256(content)
	packageDigest := computeSHA256(content)

	opSpy := &spyOperator{returnID: "wi-nat-003"}
	arSpy := &spyArchivist{}
	autoNat := true

	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
		naturalisation: &naturalisationConfig{
			AutoNaturalise:     &autoNat,
			RequireLocalStamps: []string{"embassy-verified", "intake-cleared"},
		},
		operatorSpy:  opSpy,
		archivistSpy: arSpy,
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	chunks := []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{
			Manifest: &flowv1.TransferManifest{
				ImportType: "law-petition",
				SourceFlow: "remote-flow",
				TargetFlow: "local-flow",
				TransferId: "tx-nat-3",
				Artefacts: []*flowv1.ArtefactManifest{
					{
						GovernedArtefact: "petition",
						Digest:           contentDigest,
						SizeBytes:        int64(len(content)),
					},
				},
			},
		}},
		{Chunk: &flowv1.PackageChunk_Content{Content: content}},
		{Chunk: &flowv1.PackageChunk_Trailer{
			Trailer: &flowv1.PackageTrailer{PackageDigest: packageDigest},
		}},
	}

	_, err = embClient.StreamPackage(context.Background(), chunks)
	if err != nil {
		t.Fatalf("StreamPackage() returned error: %v", err)
	}

	arSpy.mu.Lock()
	defer arSpy.mu.Unlock()

	// Should have 2 stamp calls for requireLocalStamps.
	if len(arSpy.stampedCalls) != 2 {
		t.Fatalf("expected 2 StampArtefact calls for requireLocalStamps, got %d", len(arSpy.stampedCalls))
	}

	stampNames := map[string]bool{}
	for _, call := range arSpy.stampedCalls {
		stampNames[call.GetStampName()] = true
	}
	if !stampNames["embassy-verified"] {
		t.Fatal("expected 'embassy-verified' stamp")
	}
	if !stampNames["intake-cleared"] {
		t.Fatal("expected 'intake-cleared' stamp")
	}
}

func TestStreamPackage_NaturalisationAutoNaturaliseFalseSkipsImportedStamps(t *testing.T) {
	// If autoNaturalise is false, no imported-* stamps are applied.
	content := []byte("submission-content")
	contentDigest := computeSHA256(content)
	packageDigest := computeSHA256(content)

	opSpy := &spyOperator{returnID: "wi-nat-004"}
	arSpy := &spyArchivist{}
	autoNat := false

	handler := newTestHandler(t, testHandlerOpts{
		flowImportTypes: map[string]flowImportTypeSpec{
			"external-submission": {
				Node: "intake-triage",
				RequireForeignStamps: map[string][]string{
					"submission": {"approval"},
				},
			},
		},
		naturalisation: &naturalisationConfig{
			AutoNaturalise: &autoNat,
		},
		operatorSpy:  opSpy,
		archivistSpy: arSpy,
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	chunks := []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{
			Manifest: &flowv1.TransferManifest{
				ImportType: "external-submission",
				SourceFlow: "remote-flow",
				TargetFlow: "local-flow",
				TransferId: "tx-nat-4",
				Artefacts: []*flowv1.ArtefactManifest{
					{
						GovernedArtefact: "submission",
						Digest:           contentDigest,
						SizeBytes:        int64(len(content)),
						ForeignStamps: []*flowv1.ForeignStamp{
							{StampName: "approval", Issuer: "remote-flow"},
						},
					},
				},
			},
		}},
		{Chunk: &flowv1.PackageChunk_Content{Content: content}},
		{Chunk: &flowv1.PackageChunk_Trailer{
			Trailer: &flowv1.PackageTrailer{PackageDigest: packageDigest},
		}},
	}

	_, err = embClient.StreamPackage(context.Background(), chunks)
	if err != nil {
		t.Fatalf("StreamPackage() returned error: %v", err)
	}

	arSpy.mu.Lock()
	defer arSpy.mu.Unlock()

	// No stamps should be applied when autoNaturalise is false.
	if len(arSpy.stampedCalls) != 0 {
		t.Fatalf("expected 0 StampArtefact calls when autoNaturalise=false, got %d", len(arSpy.stampedCalls))
	}
}

// ---------------------------------------------------------------------------
// Tests -- Embassy materialiser: intake routing (slice 13.4.3)
// ---------------------------------------------------------------------------

func TestStreamPackage_LawPetitionRoutesToPetitionIntake(t *testing.T) {
	// Built-in law-petition import type: Workitem is routed to the
	// platform-owned petition intake path ("petition-intake").
	content := []byte("petition-content")
	contentDigest := computeSHA256(content)
	packageDigest := computeSHA256(content)

	opSpy := &spyOperator{returnID: "wi-route-001"}
	arSpy := &spyArchivist{}

	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
		operatorSpy:  opSpy,
		archivistSpy: arSpy,
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	chunks := []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{
			Manifest: &flowv1.TransferManifest{
				ImportType: "law-petition",
				SourceFlow: "remote-flow",
				TargetFlow: "local-flow",
				TransferId: "tx-route-1",
				Artefacts: []*flowv1.ArtefactManifest{
					{
						GovernedArtefact: "petition",
						Digest:           contentDigest,
						SizeBytes:        int64(len(content)),
					},
				},
			},
		}},
		{Chunk: &flowv1.PackageChunk_Content{Content: content}},
		{Chunk: &flowv1.PackageChunk_Trailer{
			Trailer: &flowv1.PackageTrailer{PackageDigest: packageDigest},
		}},
	}

	_, err = embClient.StreamPackage(context.Background(), chunks)
	if err != nil {
		t.Fatalf("StreamPackage() returned error: %v", err)
	}

	opSpy.mu.Lock()
	defer opSpy.mu.Unlock()

	if len(opSpy.submitCalls) != 1 {
		t.Fatalf("expected 1 SubmitResult call, got %d", len(opSpy.submitCalls))
	}
	submit := opSpy.submitCalls[0]
	if submit.GetWorkitemId() != "wi-route-001" {
		t.Fatalf("expected SubmitResult workitem_id 'wi-route-001', got %q", submit.GetWorkitemId())
	}
	route := submit.GetRoute()
	if route == nil {
		t.Fatal("expected SubmitResult to have RouteAction")
	}
	if route.GetTarget() != "petition-intake" {
		t.Fatalf("expected route target 'petition-intake', got %q", route.GetTarget())
	}
}

func TestStreamPackage_FlowAuthoredImportTypeRoutesToConfiguredNode(t *testing.T) {
	// Flow-authored import type: Workitem is routed to the configured
	// node value from import type spec.
	content := []byte("submission-content")
	contentDigest := computeSHA256(content)
	packageDigest := computeSHA256(content)

	opSpy := &spyOperator{returnID: "wi-route-002"}
	arSpy := &spyArchivist{}

	handler := newTestHandler(t, testHandlerOpts{
		flowImportTypes: map[string]flowImportTypeSpec{
			"external-submission": {
				Node: "intake-triage",
			},
		},
		operatorSpy:  opSpy,
		archivistSpy: arSpy,
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	chunks := []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{
			Manifest: &flowv1.TransferManifest{
				ImportType: "external-submission",
				SourceFlow: "remote-flow",
				TargetFlow: "local-flow",
				TransferId: "tx-route-2",
				Artefacts: []*flowv1.ArtefactManifest{
					{
						GovernedArtefact: "submission",
						Digest:           contentDigest,
						SizeBytes:        int64(len(content)),
					},
				},
			},
		}},
		{Chunk: &flowv1.PackageChunk_Content{Content: content}},
		{Chunk: &flowv1.PackageChunk_Trailer{
			Trailer: &flowv1.PackageTrailer{PackageDigest: packageDigest},
		}},
	}

	_, err = embClient.StreamPackage(context.Background(), chunks)
	if err != nil {
		t.Fatalf("StreamPackage() returned error: %v", err)
	}

	opSpy.mu.Lock()
	defer opSpy.mu.Unlock()

	if len(opSpy.submitCalls) != 1 {
		t.Fatalf("expected 1 SubmitResult call, got %d", len(opSpy.submitCalls))
	}
	route := opSpy.submitCalls[0].GetRoute()
	if route == nil {
		t.Fatal("expected SubmitResult to have RouteAction")
	}
	if route.GetTarget() != "intake-triage" {
		t.Fatalf("expected route target 'intake-triage', got %q", route.GetTarget())
	}
}

func TestStreamPackage_UnknownImportTypePostPreflightErrors(t *testing.T) {
	// Unknown import type at StreamPackage stage (should not happen
	// post-preflight, but defensive guard): returns error.
	handler := newTestHandler(t, testHandlerOpts{
		systemImportTypes: map[string]systemImportType{
			"law-petition": {BuiltIn: true},
		},
		operatorSpy:  &spyOperator{returnID: "wi-route-err"},
		archivistSpy: &spyArchivist{},
	})
	addr := startEmbassyTestServer(t, handler)

	embClient, err := flow.NewEmbassyClientForTest(addr)
	if err != nil {
		t.Fatalf("NewEmbassyClientForTest() failed: %v", err)
	}
	defer func() { _ = embClient.Close() }()

	// Send a manifest with an unknown import type.
	chunks := []*flowv1.PackageChunk{
		{Chunk: &flowv1.PackageChunk_Manifest{
			Manifest: &flowv1.TransferManifest{
				ImportType: "unknown-type",
				SourceFlow: "remote-flow",
				TargetFlow: "local-flow",
				TransferId: "tx-route-err",
			},
		}},
		{Chunk: &flowv1.PackageChunk_Content{Content: []byte("data")}},
	}

	_, err = embClient.StreamPackage(context.Background(), chunks)
	if err == nil {
		t.Fatal("expected error from StreamPackage with unknown import type")
	}
}
