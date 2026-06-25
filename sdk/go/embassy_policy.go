package flow

import (
	"fmt"
	"slices"
)

// EmbassyTrustSource identifies which trust topology governs a transfer.
type EmbassyTrustSource string

const (
	EmbassyTrustSourceFederation EmbassyTrustSource = "federation"
	EmbassyTrustSourceTreaty     EmbassyTrustSource = "treaty"
)

// EmbassyTrustPolicy captures the parity checks shared by federation and treaty
// exchange. Treaty-specific fields are enforced only for treaty trust.
type EmbassyTrustPolicy struct {
	Source             EmbassyTrustSource
	AllowedImportTypes []string
	AllowedSubjects    []string
	MaxBundleSizeBytes int64
}

// EmbassyImportRequest is the trust-relevant subset of an inbound transfer.
type EmbassyImportRequest struct {
	ImportType      string
	Subject         string
	BundleSizeBytes int64
}

// ResolveEmbassyTrustSource chooses the active trust topology.
func ResolveEmbassyTrustSource(hasTreaty bool) EmbassyTrustSource {
	if hasTreaty {
		return EmbassyTrustSourceTreaty
	}
	return EmbassyTrustSourceFederation
}

// ValidateEmbassyTrustPolicy enforces import-type, subject, and bundle-size
// parity checks across federation and treaty exchange.
func ValidateEmbassyTrustPolicy(
	policy EmbassyTrustPolicy,
	req EmbassyImportRequest,
	system map[string]EmbassyResolvedImportType,
	flowImportTypes map[string]EmbassyFlowImportTypeSpec,
) error {
	if _, ok := ResolveEmbassyImportType(req.ImportType, system, flowImportTypes); !ok {
		return fmt.Errorf("unknown import type %q", req.ImportType)
	}

	if policy.Source != EmbassyTrustSourceTreaty {
		return nil
	}

	if len(policy.AllowedImportTypes) > 0 && !slices.Contains(policy.AllowedImportTypes, req.ImportType) {
		return fmt.Errorf("import type %q is not allowed by treaty policy", req.ImportType)
	}

	if len(policy.AllowedSubjects) > 0 && !slices.Contains(policy.AllowedSubjects, req.Subject) {
		return fmt.Errorf("subject %q is not allowed by treaty policy", req.Subject)
	}

	if policy.MaxBundleSizeBytes > 0 && req.BundleSizeBytes > policy.MaxBundleSizeBytes {
		return fmt.Errorf("bundle size %d exceeds treaty maxBundleSize %d", req.BundleSizeBytes, policy.MaxBundleSizeBytes)
	}

	return nil
}
