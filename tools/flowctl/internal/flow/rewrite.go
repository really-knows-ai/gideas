package flow

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Rewrite mutates an unstructured object for installation into the target
// namespace. It performs:
//   - .metadata.namespace = targetNamespace (sets even if absent)
//   - If kind is "FoundryFlow": .metadata.name = targetNamespace
//   - If kind is "FoundryNode": .metadata.labels["flow.gideas.io/flow-name"] = targetNamespace
//
// Fields that are NEVER rewritten (passed through unchanged):
//   - GovernedArtefact .metadata.name
//   - FoundryNode .metadata.name
//   - FoundryNode .spec.outputs[].target
//   - Treaty .spec.remoteName
//   - ConfigMap .data values
//   - entryContracts / exitContracts map keys
//
// Modification is in-place. Returns the same object for chaining.
func Rewrite(obj *unstructured.Unstructured, targetNamespace string) *unstructured.Unstructured {
	// Inject namespace (creates metadata map if missing)
	unstructured.SetNestedField(obj.Object, targetNamespace, "metadata", "namespace")

	// Rewrite FoundryFlow name
	if obj.GetKind() == "FoundryFlow" {
		unstructured.SetNestedField(obj.Object, targetNamespace, "metadata", "name")
	}

	// Rewrite FoundryNode flow-name label
	if obj.GetKind() == "FoundryNode" {
		labels := obj.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels["flow.gideas.io/flow-name"] = targetNamespace
		obj.SetLabels(labels)
	}

	return obj
}
