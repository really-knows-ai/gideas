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

// LawGroupSpec defines the desired state of LawGroup.
type LawGroupSpec struct {
	// mode is the evaluation mode for this law group.
	// "bundle" evaluates all laws in the group as a single unit.
	// "law-by-law" evaluates each law as its own unit.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=bundle;law-by-law
	Mode string `json:"mode"`

	// passes is the number of evaluation passes per unit per appraiser.
	// Must be at least 1.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	Passes int32 `json:"passes"`
}

// LawGroupStatus defines the observed state of LawGroup.
type LawGroupStatus struct {
	// conditions represent the current state of the LawGroup resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=".spec.mode"
// +kubebuilder:printcolumn:name="Passes",type=integer,JSONPath=".spec.passes"

// LawGroup is the Schema for the lawgroups API.
// A LawGroup defines the evaluation contract (mode and passes) for a named
// group of laws. An implicit "default" group exists with {mode: "bundle", passes: 1};
// creating a LawGroup CRD named "default" overrides these built-in defaults.
type LawGroup struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of LawGroup
	// +required
	Spec LawGroupSpec `json:"spec"`

	// status defines the observed state of LawGroup
	// +optional
	Status LawGroupStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// LawGroupList contains a list of LawGroup.
type LawGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []LawGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LawGroup{}, &LawGroupList{})
}
