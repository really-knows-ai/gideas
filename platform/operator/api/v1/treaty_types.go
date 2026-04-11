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

// TreatySpec defines the desired state of Treaty.
// The Treaty CRD defines a directed trust policy for cross-flow collaboration
// between non-sibling Flows.
type TreatySpec struct {
	// remoteName is the identifier of the remote Flow.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	RemoteName string `json:"remoteName"`

	// direction is the trust direction: import (this Flow receives from remote)
	// or export (this Flow sends to remote). Bidirectional exchange requires two Treaty CRDs.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=import;export
	Direction string `json:"direction"`

	// caCert is the PEM-encoded CA certificate of the remote Flow's trust root.
	// Used for chain verification of imported stamps and packages.
	// +kubebuilder:validation:Required
	CACert string `json:"caCert"`

	// allowedSubjects are the permitted identity subjects on imported certificates.
	// If empty, all subjects under the CA are accepted.
	// +optional
	AllowedSubjects []string `json:"allowedSubjects,omitempty"`

	// allowedImportTypes constrains which of the receiving Flow's published
	// crossFlow.importTypes the remote Flow may use. If empty, all published
	// import types are permitted.
	// +optional
	AllowedImportTypes []string `json:"allowedImportTypes,omitempty"`

	// maxBundleSize is the maximum size of export/import bundles.
	// +optional
	MaxBundleSize string `json:"maxBundleSize,omitempty"`
}

// TreatyStatus defines the observed state of Treaty.
type TreatyStatus struct {
	// conditions represent the current state of the Treaty resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Remote",type=string,JSONPath=".spec.remoteName"
// +kubebuilder:printcolumn:name="Direction",type=string,JSONPath=".spec.direction"

// Treaty is the Schema for the treaties API.
// It defines a directed trust policy for cross-flow collaboration.
type Treaty struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Treaty
	// +required
	Spec TreatySpec `json:"spec"`

	// status defines the observed state of Treaty
	// +optional
	Status TreatyStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TreatyList contains a list of Treaty
type TreatyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Treaty `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Treaty{}, &TreatyList{})
}
