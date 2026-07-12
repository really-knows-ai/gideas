// Package manifestfs provides embedded Foundry Flow platform manifests
// (CRDs and operator deployment resources) for `flowctl init`.
//
// The embedded files are copies of auto-generated Kubebuilder manifests:
//   - CRDs from platform/operator/config/crd/bases/
//   - Operator manifests from platform/operator/config/manager/ and config/rbac/
//
// Namespace-scoped resources have been rewritten from "system" to "operator-system".
// Run `make flowctl-manifests` to refresh these copies.
//
//go:generate cp ../../platform/operator/config/crd/bases/*.yaml crd/
//go:generate cp ../../platform/operator/config/manager/manager.yaml operator/deployment.yaml
package manifestfs

import "embed"

// Manifests contains all embedded Foundry Flow YAML manifests.
//
//go:embed crd/*.yaml operator/*.yaml
var Manifests embed.FS
