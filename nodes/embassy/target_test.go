package main

import (
	"context"
	"testing"

	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Tests -- Target resolution via Federation (slice 13.5.2)
// ---------------------------------------------------------------------------

func TestResolveExportTarget_LawPetitionCallsGetPetitionTarget(t *testing.T) {
	// For law-petition export: calls FederationClient.GetPetitionTarget(scope)
	// to resolve the authority Flow.
	fedSpy := &spyFederation{
		authorityFlowIdentity: "authority-flow",
		embassyEndpoint:       "authority-embassy:50059",
	}
	fedAddr := startFederationSpy(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	target, err := resolveExportTarget(
		context.Background(),
		fedClient,
		"law-petition",
		"security",
	)
	if err != nil {
		t.Fatalf("resolveExportTarget() returned error: %v", err)
	}

	if target.AuthorityFlowIdentity != "authority-flow" {
		t.Fatalf("expected authority_flow_identity 'authority-flow', got %q", target.AuthorityFlowIdentity)
	}
	if target.EmbassyEndpoint != "authority-embassy:50059" {
		t.Fatalf("expected embassy_endpoint 'authority-embassy:50059', got %q", target.EmbassyEndpoint)
	}

	fedSpy.mu.Lock()
	defer fedSpy.mu.Unlock()
	if len(fedSpy.calls) != 1 {
		t.Fatalf("expected 1 GetPetitionTarget call, got %d", len(fedSpy.calls))
	}
	if fedSpy.calls[0].GetScope() != "security" {
		t.Fatalf("expected scope 'security', got %q", fedSpy.calls[0].GetScope())
	}
}

func TestResolveExportTarget_ReturnsAuthorityEndpoint(t *testing.T) {
	// Returns target authority's Embassy endpoint and Flow identity.
	fedSpy := &spyFederation{
		authorityFlowIdentity: "law-authority",
		embassyEndpoint:       "law-authority.svc:50059",
	}
	fedAddr := startFederationSpy(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	target, err := resolveExportTarget(
		context.Background(),
		fedClient,
		"law-petition",
		"architecture",
	)
	if err != nil {
		t.Fatalf("resolveExportTarget() returned error: %v", err)
	}

	if target.AuthorityFlowIdentity != "law-authority" {
		t.Fatalf("expected authority 'law-authority', got %q", target.AuthorityFlowIdentity)
	}
	if target.EmbassyEndpoint != "law-authority.svc:50059" {
		t.Fatalf("expected endpoint 'law-authority.svc:50059', got %q", target.EmbassyEndpoint)
	}
}

func TestResolveExportTarget_FederationErrorFailsWithDescriptiveError(t *testing.T) {
	// Error from Federation (no authority found) → export fails with
	// descriptive error.
	fedSpy := &spyFederation{
		returnErr: status.Error(codes.NotFound, "no authority for scope 'unknown-scope'"),
	}
	fedAddr := startFederationSpy(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	_, err = resolveExportTarget(
		context.Background(),
		fedClient,
		"law-petition",
		"unknown-scope",
	)
	if err == nil {
		t.Fatal("expected error when Federation returns NotFound")
	}
}
