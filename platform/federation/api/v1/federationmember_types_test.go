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

const (
	testFlowAlpha    = "flow-alpha"
	testScopeEdu     = "education"
	testEndpointPort = ":50059"
)

func TestFederationMemberRegistersInScheme(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme failed: %v", err)
	}

	gvk := schema.GroupVersionKind{
		Group:   "federation.gideas.io",
		Version: "v1",
		Kind:    "FederationMember",
	}

	obj, err := s.New(gvk)
	if err != nil {
		t.Fatalf("scheme.New(%v) failed: %v", gvk, err)
	}

	if _, ok := obj.(*FederationMember); !ok {
		t.Errorf("expected *FederationMember, got %T", obj)
	}
}

func TestFederationMemberListRegistersInScheme(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme failed: %v", err)
	}

	gvk := schema.GroupVersionKind{
		Group:   "federation.gideas.io",
		Version: "v1",
		Kind:    "FederationMemberList",
	}

	obj, err := s.New(gvk)
	if err != nil {
		t.Fatalf("scheme.New(%v) failed: %v", gvk, err)
	}

	if _, ok := obj.(*FederationMemberList); !ok {
		t.Errorf("expected *FederationMemberList, got %T", obj)
	}
}

func TestFederationMemberDeepCopy(t *testing.T) {
	now := metav1.Now()
	original := &FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "member-001",
			Namespace: "federation-ns",
			Labels: map[string]string{
				"role": "publisher",
			},
		},
		Spec: FederationMemberSpec{
			FlowIdentity:    testFlowAlpha,
			EmbassyEndpoint: testFlowAlpha + "-embassy" + testEndpointPort,
			StateRefs:       []string{"state-qld", "state-nsw"},
			PublisherRoles: []PublisherRoleSpec{
				{Scope: testScopeEdu, Level: "state"},
				{Scope: "health", Level: "federation"},
			},
		},
		Status: FederationMemberStatus{
			JoinedAt: &now,
			Conditions: []metav1.Condition{
				{
					Type:   "Ready",
					Status: metav1.ConditionTrue,
					Reason: "Valid",
				},
			},
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
	if copied.Spec.FlowIdentity != original.Spec.FlowIdentity {
		t.Errorf("Spec.FlowIdentity = %q, want %q", copied.Spec.FlowIdentity, original.Spec.FlowIdentity)
	}
	if copied.Spec.EmbassyEndpoint != original.Spec.EmbassyEndpoint {
		t.Errorf("Spec.EmbassyEndpoint = %q, want %q", copied.Spec.EmbassyEndpoint, original.Spec.EmbassyEndpoint)
	}
	if len(copied.Spec.StateRefs) != 2 {
		t.Fatalf("len(Spec.StateRefs) = %d, want 2", len(copied.Spec.StateRefs))
	}
	if copied.Spec.StateRefs[0] != "state-qld" {
		t.Errorf("Spec.StateRefs[0] = %q, want %q", copied.Spec.StateRefs[0], "state-qld")
	}
	if len(copied.Spec.PublisherRoles) != 2 {
		t.Fatalf("len(Spec.PublisherRoles) = %d, want 2", len(copied.Spec.PublisherRoles))
	}
	if copied.Spec.PublisherRoles[0].Scope != testScopeEdu {
		t.Errorf("Spec.PublisherRoles[0].Scope = %q, want %q", copied.Spec.PublisherRoles[0].Scope, testScopeEdu)
	}
	if copied.Spec.PublisherRoles[0].Level != "state" {
		t.Errorf("Spec.PublisherRoles[0].Level = %q, want %q", copied.Spec.PublisherRoles[0].Level, "state")
	}
	if copied.Status.JoinedAt == nil {
		t.Fatal("Status.JoinedAt is nil after deep copy")
	}
	if len(copied.Status.Conditions) != 1 {
		t.Fatalf("len(Status.Conditions) = %d, want 1", len(copied.Status.Conditions))
	}
	if copied.Status.Conditions[0].Type != "Ready" {
		t.Errorf("Status.Conditions[0].Type = %q, want %q", copied.Status.Conditions[0].Type, "Ready")
	}

	// Verify deep independence — mutating the copy must not affect the original.
	copied.Labels["role"] = "subscriber"
	if original.Labels["role"] != "publisher" {
		t.Error("mutating copy's Labels affected the original")
	}

	copied.Spec.FlowIdentity = "flow-beta"
	if original.Spec.FlowIdentity != testFlowAlpha {
		t.Error("mutating copy's Spec.FlowIdentity affected the original")
	}

	copied.Spec.StateRefs[0] = "state-vic"
	if original.Spec.StateRefs[0] != "state-qld" {
		t.Error("mutating copy's Spec.StateRefs affected the original")
	}

	copied.Spec.PublisherRoles[0].Scope = "finance"
	if original.Spec.PublisherRoles[0].Scope != testScopeEdu {
		t.Error("mutating copy's Spec.PublisherRoles affected the original")
	}

	copied.Status.Conditions[0].Reason = "Invalid"
	if original.Status.Conditions[0].Reason != "Valid" {
		t.Error("mutating copy's Status.Conditions affected the original")
	}
}

func TestFederationMemberDeepCopyObject(t *testing.T) {
	original := &FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name: "member-002",
		},
		Spec: FederationMemberSpec{
			FlowIdentity:    "flow-gamma",
			EmbassyEndpoint: "flow-gamma-embassy:50059",
		},
	}

	obj := original.DeepCopyObject()
	if obj == nil {
		t.Fatal("DeepCopyObject returned nil")
	}

	copied, ok := obj.(*FederationMember)
	if !ok {
		t.Fatalf("expected *FederationMember, got %T", obj)
	}

	if copied.Spec.FlowIdentity != original.Spec.FlowIdentity {
		t.Errorf("Spec.FlowIdentity = %q, want %q", copied.Spec.FlowIdentity, original.Spec.FlowIdentity)
	}
}

func TestFederationMemberListDeepCopy(t *testing.T) {
	original := &FederationMemberList{
		Items: []FederationMember{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "member-a"},
				Spec: FederationMemberSpec{
					FlowIdentity:    testFlowAlpha,
					EmbassyEndpoint: "alpha" + testEndpointPort,
					StateRefs:       []string{"state-qld"},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "member-b"},
				Spec: FederationMemberSpec{
					FlowIdentity:    "flow-beta",
					EmbassyEndpoint: "beta:50059",
					PublisherRoles: []PublisherRoleSpec{
						{Scope: testScopeEdu, Level: "state"},
					},
				},
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

	if copied.Items[0].Spec.FlowIdentity != testFlowAlpha {
		t.Errorf("Items[0].Spec.FlowIdentity = %q, want %q", copied.Items[0].Spec.FlowIdentity, testFlowAlpha)
	}

	if copied.Items[1].Spec.FlowIdentity != "flow-beta" {
		t.Errorf("Items[1].Spec.FlowIdentity = %q, want %q", copied.Items[1].Spec.FlowIdentity, "flow-beta")
	}

	// Verify deep independence.
	copied.Items[0].Spec.FlowIdentity = "flow-gamma"
	if original.Items[0].Spec.FlowIdentity != testFlowAlpha {
		t.Error("mutating copy's Items affected the original")
	}

	copied.Items[1].Spec.PublisherRoles[0].Scope = "health"
	if original.Items[1].Spec.PublisherRoles[0].Scope != testScopeEdu {
		t.Error("mutating copy's Items PublisherRoles affected the original")
	}
}

func TestNewTestSchemeIncludesFederationMember(t *testing.T) {
	s := NewTestScheme()

	gvk := schema.GroupVersionKind{
		Group:   "federation.gideas.io",
		Version: "v1",
		Kind:    "FederationMember",
	}

	if _, err := s.New(gvk); err != nil {
		t.Errorf("NewTestScheme does not include FederationMember: %v", err)
	}
}
