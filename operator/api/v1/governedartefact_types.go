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

// GovernedArtefactSpec defines the desired state of GovernedArtefact.
// The GovernedArtefact CRD registers an artefact kind and declares its stamp vocabulary.
type GovernedArtefactSpec struct {
	// kind is the artefact kind identifier (e.g. "petition-draft", "haiku").
	// Unique within the Flow namespace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`

	// stamps are the stamp vocabulary — the set of stamp names meaningful for this kind
	// (e.g. ["linter", "security-review", "approval"]).
	// Entry and exit contracts select required stamps from this vocabulary.
	// +optional
	Stamps []string `json:"stamps,omitempty"`
}

// GovernedArtefactStatus defines the observed state of GovernedArtefact.
type GovernedArtefactStatus struct {
	// conditions represent the current state of the GovernedArtefact resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced

// GovernedArtefact is the Schema for the governedartefacts API.
// It registers an artefact kind and declares its stamp vocabulary.
type GovernedArtefact struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of GovernedArtefact
	// +required
	Spec GovernedArtefactSpec `json:"spec"`

	// status defines the observed state of GovernedArtefact
	// +optional
	Status GovernedArtefactStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// GovernedArtefactList contains a list of GovernedArtefact
type GovernedArtefactList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []GovernedArtefact `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GovernedArtefact{}, &GovernedArtefactList{})
}
