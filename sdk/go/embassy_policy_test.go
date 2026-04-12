package flow

import "testing"

func TestEmbassyTrustPolicyAllowsBuiltInImportType(t *testing.T) {
	t.Parallel()

	policy := EmbassyTrustPolicy{Source: EmbassyTrustSourceFederation}
	if err := ValidateEmbassyTrustPolicy(
		policy,
		EmbassyImportRequest{ImportType: builtInLawPetitionImportType},
		DefaultSystemImportTypes(),
		nil,
	); err != nil {
		t.Fatalf("expected built-in import type to validate, got %v", err)
	}
}

func TestEmbassyTrustPolicyAllowsCustomImportType(t *testing.T) {
	t.Parallel()

	policy := EmbassyTrustPolicy{Source: EmbassyTrustSourceFederation}
	if err := ValidateEmbassyTrustPolicy(
		policy,
		EmbassyImportRequest{ImportType: "external-submission"},
		DefaultSystemImportTypes(),
		map[string]EmbassyFlowImportTypeSpec{"external-submission": {Node: "intake"}},
	); err != nil {
		t.Fatalf("expected custom import type to validate, got %v", err)
	}
}

func TestEmbassyTrustPolicyEnforcesAllowedImportTypes(t *testing.T) {
	t.Parallel()

	policy := EmbassyTrustPolicy{
		Source:             EmbassyTrustSourceTreaty,
		AllowedImportTypes: []string{"law-petition"},
	}
	err := ValidateEmbassyTrustPolicy(
		policy,
		EmbassyImportRequest{ImportType: "external-submission"},
		DefaultSystemImportTypes(),
		map[string]EmbassyFlowImportTypeSpec{"external-submission": {Node: "intake"}},
	)
	if err == nil {
		t.Fatal("expected custom import type to be rejected by allowedImportTypes")
	}
}

func TestEmbassyTrustPolicyEnforcesAllowedSubjects(t *testing.T) {
	t.Parallel()

	policy := EmbassyTrustPolicy{
		Source:          EmbassyTrustSourceTreaty,
		AllowedSubjects: []string{"spiffe://remote/embassy"},
	}
	err := ValidateEmbassyTrustPolicy(
		policy,
		EmbassyImportRequest{ImportType: builtInLawPetitionImportType, Subject: "spiffe://other/embassy"},
		DefaultSystemImportTypes(),
		nil,
	)
	if err == nil {
		t.Fatal("expected subject to be rejected by allowedSubjects")
	}
}

func TestEmbassyTrustPolicyEnforcesMaxBundleSize(t *testing.T) {
	t.Parallel()

	policy := EmbassyTrustPolicy{
		Source:             EmbassyTrustSourceTreaty,
		MaxBundleSizeBytes: 4,
	}
	err := ValidateEmbassyTrustPolicy(
		policy,
		EmbassyImportRequest{ImportType: builtInLawPetitionImportType, BundleSizeBytes: 5},
		DefaultSystemImportTypes(),
		nil,
	)
	if err == nil {
		t.Fatal("expected bundle size to be rejected by maxBundleSize")
	}
}

func TestResolveEmbassyTrustSourcePrefersTreatyWhenConfigured(t *testing.T) {
	t.Parallel()

	if got := ResolveEmbassyTrustSource(true); got != EmbassyTrustSourceTreaty {
		t.Fatalf("expected treaty trust source, got %q", got)
	}
	if got := ResolveEmbassyTrustSource(false); got != EmbassyTrustSourceFederation {
		t.Fatalf("expected federation trust source, got %q", got)
	}
}
