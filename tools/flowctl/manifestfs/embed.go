// Package manifestfs provides embedded Foundry Flow platform manifests
// (CRDs and operator deployment resources) for `flowctl init`.
//
// The embedded files are copies of auto-generated Kubebuilder manifests:
//   - CRDs from platform/operator/config/crd/bases/
//   - Operator manifests from platform/operator/config/manager/ and config/rbac/
//
// Namespace-scoped resources have been rewritten from "system" to "foundry-system".
// Run `make flowctl-manifests` to refresh every embedded copy from its source:
// the target regenerates all of crd/ (from config/crd/bases/) and all of
// operator/ (deployment.yaml and namespace.yaml from config/manager/manager.yaml,
// role.yaml, rolebinding.yaml, and serviceaccount.yaml from config/rbac/).
//
//go:generate rm -f crd/*.yaml operator/*.yaml
//go:generate cp ../../../platform/operator/config/crd/bases/*.yaml crd/
//go:generate sed '1,/^---$/d; s/namespace: system/namespace: foundry-system/' ../../../platform/operator/config/manager/manager.yaml > operator/deployment.yaml
//go:generate sed -n '1,/^---$/{/^---$/d;s/name: system/name: foundry-system/;p}' ../../../platform/operator/config/manager/manager.yaml > operator/namespace.yaml
//go:generate cp ../../../platform/operator/config/rbac/role.yaml operator/role.yaml
//go:generate sed 's/namespace: system/namespace: foundry-system/' ../../../platform/operator/config/rbac/role_binding.yaml > operator/rolebinding.yaml
//go:generate sed 's/namespace: system/namespace: foundry-system/' ../../../platform/operator/config/rbac/service_account.yaml > operator/serviceaccount.yaml
package manifestfs

import (
	"embed"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

// Manifests contains all embedded Foundry Flow YAML manifests.
//
//go:embed crd/*.yaml operator/*.yaml
var Manifests embed.FS

// OperatorPodLabelSelector identifies the deployed operator pod. It is sourced
// from the app.kubernetes.io/name label on the pod template of the embedded
// operator deployment.yaml (a copy of the deployed manager.yaml, see the
// //go:generate line above), so flowctl's operator pod identification can never
// silently drift from the manifest it bootstraps.
var OperatorPodLabelSelector = operatorPodLabelSelector()

// operatorPodLabelSelector parses the operator pod label out of the embedded
// deployment manifest at package init. It panics on failure: a manifest that
// cannot be read or lacks the label is a packaging error caught at build/test
// time, not a runtime condition.
func operatorPodLabelSelector() string {
	data, err := Manifests.ReadFile("operator/deployment.yaml")
	if err != nil {
		panic(fmt.Sprintf("manifestfs: read embedded operator deployment: %v", err))
	}
	var dep appsv1.Deployment
	if err := yaml.Unmarshal(data, &dep); err != nil {
		panic(fmt.Sprintf("manifestfs: parse embedded operator deployment: %v", err))
	}
	label := dep.Spec.Template.Labels["app.kubernetes.io/name"]
	if label == "" {
		panic("manifestfs: operator deployment pod template is missing the app.kubernetes.io/name label")
	}
	return "app.kubernetes.io/name=" + label
}
