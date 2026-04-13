package main

import (
	"context"
	"strings"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Tests -- Manifest builder (slice 13.5.1)
// ---------------------------------------------------------------------------

func TestBuildManifest_ReadsArtefactsAndBuildsTransferManifest(t *testing.T) {
	// Manifest builder reads Workitem artefacts via Archivist and builds
	// a TransferManifest.
	arSpy := &spyArchivist{
		listArtefacts: []*flowv1.ArtefactRef{
			{Id: "art-1", GovernedArtefact: "petition"},
		},
		artefactContents: map[string][]byte{
			"petition": []byte("petition-content"),
		},
	}

	cfg := &embassyConfig{
		FederationIdentity: "local-flow",
	}

	manifest, content, err := buildExportManifest(
		context.Background(),
		arSpy,
		"wi-export-001",
		importTypeLawPetition,
		"authority-flow",
		cfg,
	)
	if err != nil {
		t.Fatalf("buildExportManifest() returned error: %v", err)
	}
	if manifest == nil {
		t.Fatal("expected non-nil manifest")
	}
	if len(content) == 0 {
		t.Fatal("expected non-empty content map")
	}
}

func TestBuildManifest_IncludesImportTypeSourceTargetTransferIDExpiry(t *testing.T) {
	// Manifest includes import_type, source_flow, target_flow,
	// transfer_id (generated UUID), and expires_at.
	arSpy := &spyArchivist{
		listArtefacts: []*flowv1.ArtefactRef{
			{Id: "art-1", GovernedArtefact: "petition"},
		},
		artefactContents: map[string][]byte{
			"petition": []byte("petition-content"),
		},
	}

	cfg := &embassyConfig{
		FederationIdentity: "local-flow",
	}

	manifest, _, err := buildExportManifest(
		context.Background(),
		arSpy,
		"wi-export-002",
		importTypeLawPetition,
		"authority-flow",
		cfg,
	)
	if err != nil {
		t.Fatalf("buildExportManifest() returned error: %v", err)
	}

	if manifest.GetImportType() != importTypeLawPetition {
		t.Fatalf("expected import_type %q, got %q", importTypeLawPetition, manifest.GetImportType())
	}
	if manifest.GetSourceFlow() != "local-flow" {
		t.Fatalf("expected source_flow 'local-flow', got %q", manifest.GetSourceFlow())
	}
	if manifest.GetTargetFlow() != "authority-flow" {
		t.Fatalf("expected target_flow 'authority-flow', got %q", manifest.GetTargetFlow())
	}
	if manifest.GetTransferId() == "" {
		t.Fatal("expected non-empty transfer_id (UUID)")
	}
	// Transfer ID should look like a UUID (contains hyphens).
	if !strings.Contains(manifest.GetTransferId(), "-") {
		t.Fatalf("transfer_id does not look like a UUID: %q", manifest.GetTransferId())
	}
	if manifest.GetExpiresAt() == nil {
		t.Fatal("expected non-nil expires_at")
	}
	// Expiry should be in the future.
	if manifest.GetExpiresAt().AsTime().Before(time.Now()) {
		t.Fatal("expected expires_at to be in the future")
	}
}

func TestBuildManifest_IncludesArtefactManifestEntries(t *testing.T) {
	// Manifest includes ArtefactManifest entries with digest, size, and
	// representation metadata.
	petitionContent := []byte("petition-content")
	evidenceContent := []byte("evidence-content")

	arSpy := &spyArchivist{
		listArtefacts: []*flowv1.ArtefactRef{
			{Id: "art-1", GovernedArtefact: "petition"},
			{Id: "art-2", GovernedArtefact: "evidence"},
		},
		artefactContents: map[string][]byte{
			"petition": petitionContent,
			"evidence": evidenceContent,
		},
	}

	cfg := &embassyConfig{
		FederationIdentity: "local-flow",
	}

	manifest, _, err := buildExportManifest(
		context.Background(),
		arSpy,
		"wi-export-003",
		importTypeLawPetition,
		"authority-flow",
		cfg,
	)
	if err != nil {
		t.Fatalf("buildExportManifest() returned error: %v", err)
	}

	if len(manifest.GetArtefacts()) != 2 {
		t.Fatalf("expected 2 artefact manifests, got %d", len(manifest.GetArtefacts()))
	}

	// Check first artefact.
	art0 := manifest.GetArtefacts()[0]
	if art0.GetGovernedArtefact() != "petition" {
		t.Fatalf("expected governed_artefact 'petition', got %q", art0.GetGovernedArtefact())
	}
	if art0.GetDigest() == "" {
		t.Fatal("expected non-empty digest for petition artefact")
	}
	if art0.GetSizeBytes() != int64(len(petitionContent)) {
		t.Fatalf("expected size_bytes %d, got %d", len(petitionContent), art0.GetSizeBytes())
	}

	// Check second artefact.
	art1 := manifest.GetArtefacts()[1]
	if art1.GetGovernedArtefact() != "evidence" {
		t.Fatalf("expected governed_artefact 'evidence', got %q", art1.GetGovernedArtefact())
	}
	if art1.GetDigest() == "" {
		t.Fatal("expected non-empty digest for evidence artefact")
	}
	if art1.GetSizeBytes() != int64(len(evidenceContent)) {
		t.Fatalf("expected size_bytes %d, got %d", len(evidenceContent), art1.GetSizeBytes())
	}
}

func TestBuildManifest_IncludesLocalStampsAsForeignStamps(t *testing.T) {
	// Manifest includes local stamps as ForeignStamp entries.
	arSpy := &spyArchivist{
		listArtefacts: []*flowv1.ArtefactRef{
			{Id: "art-1", GovernedArtefact: "petition"},
		},
		artefactContents: map[string][]byte{
			"petition": []byte("petition-content"),
		},
		stamps: map[string][]*flowv1.Stamp{
			"petition": {
				{Name: "approval", ApplyingNode: "clerk-sort", ContentHash: "hash-1"},
				{Name: "review", ApplyingNode: "clerk-appraise", ContentHash: "hash-2"},
			},
		},
	}

	cfg := &embassyConfig{
		FederationIdentity: "local-flow",
	}

	manifest, _, err := buildExportManifest(
		context.Background(),
		arSpy,
		"wi-export-004",
		importTypeLawPetition,
		"authority-flow",
		cfg,
	)
	if err != nil {
		t.Fatalf("buildExportManifest() returned error: %v", err)
	}

	if len(manifest.GetArtefacts()) != 1 {
		t.Fatalf("expected 1 artefact manifest, got %d", len(manifest.GetArtefacts()))
	}
	art := manifest.GetArtefacts()[0]
	if len(art.GetForeignStamps()) != 2 {
		t.Fatalf("expected 2 foreign stamps, got %d", len(art.GetForeignStamps()))
	}

	stampNames := map[string]bool{}
	for _, fs := range art.GetForeignStamps() {
		stampNames[fs.GetStampName()] = true
		if fs.GetIssuer() != "local-flow" {
			t.Fatalf("expected stamp issuer 'local-flow', got %q", fs.GetIssuer())
		}
	}
	if !stampNames["approval"] {
		t.Fatal("expected 'approval' foreign stamp")
	}
	if !stampNames["review"] {
		t.Fatal("expected 'review' foreign stamp")
	}
}
