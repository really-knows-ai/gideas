package flow

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Normalize strips cluster-assigned and server-managed metadata from an
// unstructured resource:
//   - .metadata.namespace
//   - .metadata.resourceVersion
//   - .metadata.uid
//   - .metadata.creationTimestamp
//   - .metadata.managedFields
//   - .status
//
// Modification is in-place. Returns the same object for chaining.
func Normalize(obj *unstructured.Unstructured) *unstructured.Unstructured {
	unstructured.RemoveNestedField(obj.Object, "metadata", "namespace")
	unstructured.RemoveNestedField(obj.Object, "metadata", "resourceVersion")
	unstructured.RemoveNestedField(obj.Object, "metadata", "uid")
	unstructured.RemoveNestedField(obj.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(obj.Object, "status")
	return obj
}
