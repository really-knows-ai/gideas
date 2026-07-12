package flow

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestNormalize_StripsNamespace(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"namespace": "test",
		},
	}}
	Normalize(obj)
	if _, found, _ := unstructured.NestedString(obj.Object, "metadata", "namespace"); found {
		t.Error("namespace should be removed")
	}
}

func TestNormalize_StripsResourceVersion(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"resourceVersion": "123",
		},
	}}
	Normalize(obj)
	if _, found, _ := unstructured.NestedString(obj.Object, "metadata", "resourceVersion"); found {
		t.Error("resourceVersion should be removed")
	}
}

func TestNormalize_StripsUID(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"uid": "abc",
		},
	}}
	Normalize(obj)
	if _, found, _ := unstructured.NestedString(obj.Object, "metadata", "uid"); found {
		t.Error("uid should be removed")
	}
}

func TestNormalize_StripsCreationTimestamp(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"creationTimestamp": "2024-01-01T00:00:00Z",
		},
	}}
	Normalize(obj)
	if _, found, _ := unstructured.NestedString(obj.Object, "metadata", "creationTimestamp"); found {
		t.Error("creationTimestamp should be removed")
	}
}

func TestNormalize_StripsStatus(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"phase": "Running",
		},
	}}
	Normalize(obj)
	if _, found, _ := unstructured.NestedMap(obj.Object, "status"); found {
		t.Error("status should be removed")
	}
}

func TestNormalize_StripsAllFields(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"namespace":         "ns",
			"resourceVersion":   "1",
			"uid":               "u",
			"creationTimestamp": "t",
			"managedFields":     []interface{}{map[string]interface{}{"manager": "kube-controller", "operation": "Update"}},
		},
		"status": map[string]interface{}{"phase": "Running"},
	}}
	Normalize(obj)
	foundFields := []string{}
	for _, field := range []string{"namespace", "resourceVersion", "uid", "creationTimestamp", "managedFields"} {
		if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "metadata", field); found {
			foundFields = append(foundFields, field)
		}
	}
	if _, found, _ := unstructured.NestedMap(obj.Object, "status"); found {
		foundFields = append(foundFields, "status")
	}
	if len(foundFields) > 0 {
		t.Errorf("expected all 6 fields removed, still found: %v", foundFields)
	}
}

func TestNormalize_NoopOnCleanObject(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name": "test",
		},
		"data": map[string]interface{}{
			"key": "value",
		},
	}}
	original := obj.DeepCopy()
	Normalize(obj)
	if obj.Object["apiVersion"] != original.Object["apiVersion"] {
		t.Error("apiVersion should be preserved")
	}
	if obj.Object["kind"] != original.Object["kind"] {
		t.Error("kind should be preserved")
	}
}

func TestNormalize_PreservesOtherMetadata(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"namespace": "ns",
			"labels": map[string]interface{}{
				"foo": "bar",
			},
		},
	}}
	Normalize(obj)
	if _, found, _ := unstructured.NestedString(obj.Object, "metadata", "namespace"); found {
		t.Error("namespace should be removed")
	}
	labels, found, _ := unstructured.NestedMap(obj.Object, "metadata", "labels")
	if !found {
		t.Fatal("labels should be preserved")
	}
	if labels["foo"] != "bar" {
		t.Errorf("labels['foo'] = %q, want %q", labels["foo"], "bar")
	}
}

func TestNormalize_PreservesSpec(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"foo": "bar",
		},
		"status": map[string]interface{}{
			"phase": "Running",
		},
	}}
	Normalize(obj)
	spec, found, _ := unstructured.NestedMap(obj.Object, "spec")
	if !found {
		t.Fatal("spec should be preserved")
	}
	if spec["foo"] != "bar" {
		t.Errorf("spec['foo'] = %q, want %q", spec["foo"], "bar")
	}
	if _, found, _ := unstructured.NestedMap(obj.Object, "status"); found {
		t.Error("status should be removed")
	}
}

func TestNormalize_StripsManagedFields(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"managedFields": []interface{}{
				map[string]interface{}{
					"manager":   "kube-controller",
					"operation": "Update",
				},
			},
		},
	}}
	Normalize(obj)
	if _, found, _ := unstructured.NestedSlice(obj.Object, "metadata", "managedFields"); found {
		t.Error("managedFields should be removed")
	}
}
