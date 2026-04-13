/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestFederationStateRegistersInScheme(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme failed: %v", err)
	}

	gvk := schema.GroupVersionKind{
		Group:   "federation.gideas.io",
		Version: "v1",
		Kind:    "FederationState",
	}

	obj, err := s.New(gvk)
	if err != nil {
		t.Fatalf("scheme.New(%v) failed: %v", gvk, err)
	}

	if _, ok := obj.(*FederationState); !ok {
		t.Errorf("expected *FederationState, got %T", obj)
	}
}

func TestFederationStateListRegistersInScheme(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme failed: %v", err)
	}

	gvk := schema.GroupVersionKind{
		Group:   "federation.gideas.io",
		Version: "v1",
		Kind:    "FederationStateList",
	}

	obj, err := s.New(gvk)
	if err != nil {
		t.Fatalf("scheme.New(%v) failed: %v", gvk, err)
	}

	if _, ok := obj.(*FederationStateList); !ok {
		t.Errorf("expected *FederationStateList, got %T", obj)
	}
}

func TestFederationStateDeepCopy(t *testing.T) {
	original := &FederationState{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "state-001",
			Namespace: "federation-ns",
			Labels: map[string]string{
				"tier": "state",
			},
		},
		Spec: FederationStateSpec{
			Name: "Queensland",
		},
	}

	copied := original.DeepCopy()
	if copied == nil {
		t.Fatal("DeepCopy returned nil")
	}

	// Verify field values were copied.
	if copied.Name != original.Name {
		t.Errorf("Name = %q, want %q", copied.Name, original.Name)
	}
	if copied.Namespace != original.Namespace {
		t.Errorf("Namespace = %q, want %q", copied.Namespace, original.Namespace)
	}
	if copied.Spec.Name != original.Spec.Name {
		t.Errorf("Spec.Name = %q, want %q", copied.Spec.Name, original.Spec.Name)
	}

	// Verify deep independence — mutating the copy must not affect the original.
	copied.Labels["tier"] = "federation"
	if original.Labels["tier"] != "state" {
		t.Error("mutating copy's Labels affected the original")
	}

	copied.Spec.Name = "Victoria"
	if original.Spec.Name != "Queensland" {
		t.Error("mutating copy's Spec.Name affected the original")
	}
}

func TestFederationStateDeepCopyObject(t *testing.T) {
	original := &FederationState{
		ObjectMeta: metav1.ObjectMeta{
			Name: "state-002",
		},
		Spec: FederationStateSpec{
			Name: "New South Wales",
		},
	}

	obj := original.DeepCopyObject()
	if obj == nil {
		t.Fatal("DeepCopyObject returned nil")
	}

	copied, ok := obj.(*FederationState)
	if !ok {
		t.Fatalf("expected *FederationState, got %T", obj)
	}

	if copied.Spec.Name != original.Spec.Name {
		t.Errorf("Spec.Name = %q, want %q", copied.Spec.Name, original.Spec.Name)
	}
}

func TestFederationStateListDeepCopy(t *testing.T) {
	original := &FederationStateList{
		Items: []FederationState{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "state-a"},
				Spec:       FederationStateSpec{Name: "Alpha"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "state-b"},
				Spec:       FederationStateSpec{Name: "Beta"},
			},
		},
	}

	copied := original.DeepCopy()
	if copied == nil {
		t.Fatal("DeepCopy returned nil")
	}

	if len(copied.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(copied.Items))
	}

	if copied.Items[0].Spec.Name != "Alpha" {
		t.Errorf("Items[0].Spec.Name = %q, want %q", copied.Items[0].Spec.Name, "Alpha")
	}

	// Verify deep independence.
	copied.Items[0].Spec.Name = "Gamma"
	if original.Items[0].Spec.Name != "Alpha" {
		t.Error("mutating copy's Items affected the original")
	}
}

func TestNewTestSchemeIncludesFederationState(t *testing.T) {
	s := NewTestScheme()

	gvk := schema.GroupVersionKind{
		Group:   "federation.gideas.io",
		Version: "v1",
		Kind:    "FederationState",
	}

	if _, err := s.New(gvk); err != nil {
		t.Errorf("NewTestScheme does not include FederationState: %v", err)
	}
}
