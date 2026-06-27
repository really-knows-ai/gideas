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

// LawSpec defines the desired state of Law.
// The Law object is managed by the Librarian. Laws are versioned by content hash;
// any mutation to any part of the law produces a new version.
type LawSpec struct {
	// goal is the plain-language statement of what the law enforces, stops, or ensures.
	// The law's identity.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Goal string `json:"goal"`

	// representations are one or more typed expressions of the goal.
	// At least one representation is required.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Representations []Representation `json:"representations"`

	// tier is the law tier: 1 (Finding), 2 (Ruling), 3 (Local Statute),
	// 4 (State Constitution), 5 (Federal Accord).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=5
	Tier int32 `json:"tier"`

	// appliesTo lists governed artefact kinds this law applies to.
	// Empty means global — applies to all kinds in the Flow.
	// +optional
	AppliesTo []string `json:"appliesTo,omitempty"`

	// Group is the law group name. Must match the metadata.name of a LawGroup CRD
	// or be empty (defaults to "default").
	// +optional
	Group string `json:"group,omitempty"`
}

// Representation is a typed expression of a law's goal.
type Representation struct {
	// type is the MIME type identifying the representation format
	// (e.g. text/markdown, application/smt-lib, application/python, application/rego).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`

	// content is the representation payload.
	// +kubebuilder:validation:Required
	Content string `json:"content"`
}

// LawStatus defines the observed state of Law.
type LawStatus struct {
	// version is the content hash of the current law version.
	// Any mutation to spec produces a new hash.
	// +optional
	Version string `json:"version,omitempty"`

	// conditions represent the current state of the Law resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Tier",type=integer,JSONPath=".spec.tier"
// +kubebuilder:printcolumn:name="Group",type=string,JSONPath=".spec.group"
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=".status.version"

// Law is the Schema for the laws API.
// Laws are managed by the Librarian and represent enforceable rules within a Flow.
type Law struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Law
	// +required
	Spec LawSpec `json:"spec"`

	// status defines the observed state of Law
	// +optional
	Status LawStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// LawList contains a list of Law
type LawList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Law `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Law{}, &LawList{})
}
