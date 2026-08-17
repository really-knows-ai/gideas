// Package service implements the Archivist gRPC server.
package service

import (
	"context"
	"strings"

	flowmeta "github.com/foundry/flow/pkg/metadata"
	flow "github.com/foundry/flow/sdk/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// checkCapability enforces deny-by-default capability gating for
// node-originated requests. System-to-system calls (no x-flow-node-id)
// pass through unconditionally.
//
// The gate reuses the shared flow.CheckCapability (normalize + exact/wildcard
// match) and adds the archivist's scoped-grant-satisfies-broad rule: a scoped
// READ grant (READ:artefact/haiku) also satisfies a broad READ requirement
// (READ:artefact), because nodes declare scoped artefact reads.
//
// "Capability enforcement is performed by the owning service."
func checkCapability(ctx context.Context, required string) error {
	if err := flow.CheckCapability(ctx, required); err == nil {
		return nil
	} else if hasScopedGrant(ctx, required) && strings.HasPrefix(required, "READ:") {
		return nil
	} else {
		return err
	}
}

// hasScopedGrant reports whether the node's capability metadata contains a
// grant that is a scoped prefix of the requirement, e.g. READ:artefact/haiku
// for the requirement READ:artefact.
func hasScopedGrant(ctx context.Context, required string) bool {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}
	for _, raw := range md.Get(flowmeta.MetadataKeyCapabilities) {
		for _, cap := range flowmeta.NormalizeCapabilities(raw) {
			if strings.HasPrefix(cap, required+"/") {
				return true
			}
		}
	}
	return false
}

// checkCapabilityAny checks that at least one of the required capabilities is
// present. Used for operations like StoreArtefact where either a broad
// WRITE:artefact or a scoped WRITE:artefact/<name> grant suffices.
func checkCapabilityAny(ctx context.Context, required ...string) error {
	for _, r := range required {
		if err := flow.CheckCapability(ctx, r); err == nil {
			return nil
		}
	}
	return status.Errorf(codes.PermissionDenied,
		"CAPABILITY_DENIED: missing required capability (one of %v)", required)
}
