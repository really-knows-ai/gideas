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

// FederationStateSpec defines the desired state of FederationState.
// A FederationState declares a federation-defined organisational group.
// Flows are assigned to states via FederationMember.spec.stateRefs.
type FederationStateSpec struct {
	// name is the human-readable display name for this state.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// FederationStateStatus defines the observed state of FederationState.
type FederationStateStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="DISPLAY_NAME",type=string,JSONPath=".spec.name"

// FederationState is the Schema for the federationstates API.
// It declares a federation-defined organisational group of Flows.
type FederationState struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of FederationState
	// +required
	Spec FederationStateSpec `json:"spec"`

	// status defines the observed state of FederationState
	// +optional
	Status FederationStateStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// FederationStateList contains a list of FederationState
type FederationStateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []FederationState `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FederationState{}, &FederationStateList{})
}
