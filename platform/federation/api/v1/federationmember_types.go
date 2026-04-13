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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PublisherRoleSpec defines a publisher authority role for a federation member.
type PublisherRoleSpec struct {
	// scope is the domain or division this publisher role covers.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Scope string `json:"scope"`

	// level is the authority level: "state" or "federation".
	// State-level publishers produce Tier 4 laws; federation-level produce Tier 5.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=state;federation
	Level string `json:"level"`
}

// FederationMemberSpec defines the desired state of FederationMember.
// A FederationMember tracks a Flow's membership in the federation, including
// its embassy endpoint, state assignments, and publisher authority roles.
type FederationMemberSpec struct {
	// flowIdentity is the unique identity of the Flow in the federation.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	FlowIdentity string `json:"flowIdentity"`

	// embassyEndpoint is the gRPC address of the Flow's Embassy service.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	EmbassyEndpoint string `json:"embassyEndpoint"`

	// stateRefs is a list of FederationState names this member belongs to.
	// Sibling relationships derive from shared state membership.
	// +optional
	StateRefs []string `json:"stateRefs,omitempty"`

	// publisherRoles defines the publisher authority roles for this member.
	// Each role authorises publishing laws at a given scope and level.
	// +optional
	PublisherRoles []PublisherRoleSpec `json:"publisherRoles,omitempty"`
}

// FederationMemberStatus defines the observed state of FederationMember.
type FederationMemberStatus struct {
	// conditions represent the latest available observations of the member's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// joinedAt is the timestamp when the member first joined the federation.
	// Set on first reconcile and preserved on subsequent reconciles.
	// +optional
	JoinedAt *metav1.Time `json:"joinedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="FLOW_IDENTITY",type=string,JSONPath=".spec.flowIdentity"
// +kubebuilder:printcolumn:name="EMBASSY_ENDPOINT",type=string,JSONPath=".spec.embassyEndpoint"
// +kubebuilder:printcolumn:name="STATES",type=string,JSONPath=".spec.stateRefs",priority=1
// +kubebuilder:printcolumn:name="ROLES",type=string,JSONPath=".spec.publisherRoles",priority=1

// FederationMember is the Schema for the federationmembers API.
// It tracks a Flow's membership in the federation, including its embassy
// endpoint, state assignments, and publisher authority roles.
type FederationMember struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of FederationMember
	// +required
	Spec FederationMemberSpec `json:"spec"`

	// status defines the observed state of FederationMember
	// +optional
	Status FederationMemberStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// FederationMemberList contains a list of FederationMember
type FederationMemberList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []FederationMember `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FederationMember{}, &FederationMemberList{})
}
