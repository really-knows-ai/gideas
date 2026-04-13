package main

import (
	"context"
	"fmt"

	flow "github.com/gideas/flow/sdk/go"
)

// importTypeLawPetition is the built-in system import type for cross-flow
// law petitions. It is always present and not user-configurable.
const importTypeLawPetition = "law-petition"

// exportTarget holds the resolved authority Flow and its Embassy endpoint
// for an outbound cross-flow transfer.
type exportTarget struct {
	AuthorityFlowIdentity string
	EmbassyEndpoint       string
}

// resolveExportTarget resolves the target authority for an outbound export
// via the Federation service. For law-petition exports, it calls
// GetPetitionTarget with the given scope to discover which authority Flow
// should receive the petition and its Embassy endpoint.
func resolveExportTarget(
	ctx context.Context,
	fedClient *flow.FederationClient,
	importType string,
	scope string,
) (*exportTarget, error) {
	if importType != importTypeLawPetition {
		return nil, fmt.Errorf(
			"embassy target: unsupported import type %q for federation target resolution",
			importType,
		)
	}

	pt, err := fedClient.GetPetitionTarget(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("embassy target: resolve petition target for scope %q: %w", scope, err)
	}

	return &exportTarget{
		AuthorityFlowIdentity: pt.AuthorityFlowIdentity,
		EmbassyEndpoint:       pt.EmbassyEndpoint,
	}, nil
}
