package flow

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRewrite_InjectsNamespace(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ConfigMap",
		"metadata": map[string]interface{}{
			"name": "test",
		},
	}}
	Rewrite(obj, "my-flow")
	ns, _, _ := unstructured.NestedString(obj.Object, "metadata", "namespace")
	if ns != "my-flow" {
		t.Errorf("namespace = %q, want %q", ns, "my-flow")
	}
}

func TestRewrite_OverwritesExistingNamespace(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ConfigMap",
		"metadata": map[string]interface{}{
			"name":      "test",
			"namespace": "old",
		},
	}}
	Rewrite(obj, "new-ns")
	ns, _, _ := unstructured.NestedString(obj.Object, "metadata", "namespace")
	if ns != "new-ns" {
		t.Errorf("namespace = %q, want %q", ns, "new-ns")
	}
}

func TestRewrite_RenamesFoundryFlow(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "flow.foundry.io/v1",
		"kind":       "FoundryFlow",
		"metadata": map[string]interface{}{
			"name": "original-name",
		},
	}}
	Rewrite(obj, "my-flow")
	name, _, _ := unstructured.NestedString(obj.Object, "metadata", "name")
	if name != "my-flow" {
		t.Errorf("name = %q, want %q", name, "my-flow")
	}
}

func TestRewrite_DoesNotRenameNonFoundryFlow(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "flow.foundry.io/v1",
		"kind":       "FoundryNode",
		"metadata": map[string]interface{}{
			"name": "original-node",
		},
	}}
	Rewrite(obj, "my-flow")
	name, _, _ := unstructured.NestedString(obj.Object, "metadata", "name")
	if name != "original-node" {
		t.Errorf("name = %q, want %q", name, "original-node")
	}
}

func TestRewrite_RewritesFlowNameLabelOnFoundryNode(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "flow.foundry.io/v1",
		"kind":       "FoundryNode",
		"metadata": map[string]interface{}{
			"name": "forge",
		},
	}}
	Rewrite(obj, "my-flow")
	labels := obj.GetLabels()
	if labels == nil {
		t.Fatal("labels should not be nil")
	}
	if labels["flow.foundry.io/flow-name"] != "my-flow" {
		t.Errorf("flow-name label = %q, want %q", labels["flow.foundry.io/flow-name"], "my-flow")
	}
}

func TestRewrite_DoesNotRewriteLabelOnNonFoundryNode(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name": "test",
			"labels": map[string]interface{}{
				"existing": "value",
			},
		},
	}}
	Rewrite(obj, "my-flow")
	labels := obj.GetLabels()
	if labels["existing"] != "value" {
		t.Errorf("existing label changed: %v", labels)
	}
	if _, ok := labels["flow.foundry.io/flow-name"]; ok {
		t.Error("flow-name label should not be set on non-FoundryNode")
	}
}

func TestRewrite_DoesNotRewriteGovernedArtefactName(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "flow.foundry.io/v1",
		"kind":       "GovernedArtefact",
		"metadata": map[string]interface{}{
			"name": "haiku",
		},
	}}
	Rewrite(obj, "my-flow")
	name, _, _ := unstructured.NestedString(obj.Object, "metadata", "name")
	if name != "haiku" {
		t.Errorf("GovernedArtefact name = %q, want %q", name, "haiku")
	}
}

func TestRewrite_DoesNotRewriteContractKeys(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "flow.foundry.io/v1",
		"kind":       "FoundryFlow",
		"metadata": map[string]interface{}{
			"name": "original",
		},
		"spec": map[string]interface{}{
			"entryContracts": map[string]interface{}{
				"standard-entry": "haiku",
			},
			"exitContracts": map[string]interface{}{
				"standard-exit": "petition",
			},
		},
	}}
	Rewrite(obj, "my-flow")
	entry, _, _ := unstructured.NestedMap(obj.Object, "spec", "entryContracts")
	if entry["standard-entry"] != "haiku" {
		t.Errorf("entryContract key value changed: %v", entry)
	}
}

func TestRewrite_DoesNotRewriteTreatyRemoteName(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "flow.foundry.io/v1",
		"kind":       "Treaty",
		"metadata": map[string]interface{}{
			"name": "cross-flow-treaty",
		},
		"spec": map[string]interface{}{
			"remoteName": "other-flow",
		},
	}}
	Rewrite(obj, "my-flow")
	remote, _, _ := unstructured.NestedString(obj.Object, "spec", "remoteName")
	if remote != "other-flow" {
		t.Errorf("spec.remoteName = %q, want %q", remote, "other-flow")
	}
}

func TestRewrite_FoundryNodeNamePreserved(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "flow.foundry.io/v1",
		"kind":       "FoundryNode",
		"metadata": map[string]interface{}{
			"name": "forge-node",
		},
	}}
	Rewrite(obj, "my-flow")
	name, _, _ := unstructured.NestedString(obj.Object, "metadata", "name")
	if name != "forge-node" {
		t.Errorf("FoundryNode name = %q, want %q", name, "forge-node")
	}
}

func TestRewrite_FoundryNodeOutputsTargetPreserved(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "flow.foundry.io/v1",
		"kind":       "FoundryNode",
		"metadata": map[string]interface{}{
			"name": "forge",
		},
		"spec": map[string]interface{}{
			"outputs": []interface{}{
				map[string]interface{}{"target": "sort", "type": "workitem"},
				map[string]interface{}{"target": "archive", "type": "workitem"},
			},
		},
	}}
	Rewrite(obj, "my-flow")
	outputs, _, _ := unstructured.NestedSlice(obj.Object, "spec", "outputs")
	if len(outputs) != 2 {
		t.Fatalf("expected 2 outputs, got %d", len(outputs))
	}
	for i, out := range outputs {
		o := out.(map[string]interface{})
		if o["target"] == "my-flow" {
			t.Errorf("outputs[%d].target was rewritten to %q", i, o["target"])
		}
	}
}

func TestRewrite_ConfigMapDataPreserved(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name": "test-config",
		},
		"data": map[string]interface{}{
			"key1": "value1",
			"key2": "value2",
		},
	}}
	Rewrite(obj, "my-flow")
	data, _, _ := unstructured.NestedMap(obj.Object, "data")
	if data["key1"] != "value1" {
		t.Errorf("data['key1'] = %q, want %q", data["key1"], "value1")
	}
	if data["key2"] != "value2" {
		t.Errorf("data['key2'] = %q, want %q", data["key2"], "value2")
	}
}
