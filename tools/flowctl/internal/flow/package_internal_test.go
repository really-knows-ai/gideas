package flow

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func makeConventionObj(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "flow.gideas.io/v1",
			"kind":       "FoundryFlow",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
		},
	}
}

func TestCheckFlowConvention_violation(t *testing.T) {
	obj := makeConventionObj("my-flow", "wrong-ns")
	err := checkFlowConvention(obj, "my-flow")
	if err == nil {
		t.Fatal("expected error for convention violation")
	}
	if err.Error() != "convention violation: flow `my-flow` must reside in namespace `my-flow`" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestCheckFlowConvention_ok(t *testing.T) {
	obj := makeConventionObj("my-flow", "my-flow")
	err := checkFlowConvention(obj, "my-flow")
	if err != nil {
		t.Errorf("expected nil, got: %v", err)
	}
}
