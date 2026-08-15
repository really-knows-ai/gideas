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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Port",type=integer,JSONPath=".status.endpoint.port"
// +kubebuilder:printcolumn:name="Storage",type=string,JSONPath=".status.storageSize"

// FoundryGraph is the Schema for the foundrygraphs API.
// It defines a graph database instance managed by the Cartographer service.
type FoundryGraph struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of FoundryGraph
	// +required
	Spec FoundryGraphSpec `json:"spec"`

	// status defines the observed state of FoundryGraph
	// +optional
	Status FoundryGraphStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// FoundryGraphList contains a list of FoundryGraph.
type FoundryGraphList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []FoundryGraph `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(GroupVersion, &FoundryGraph{}, &FoundryGraphList{})
		return nil
	})
}

// FoundryGraphSpec defines the desired state.
type FoundryGraphSpec struct {
	// EntityTypes is the list of entity types in the graph.
	// +optional
	EntityTypes []EntityTypeSpec `json:"entityTypes,omitempty"`
	// EdgeTypes is the list of edge types in the graph.
	// +optional
	EdgeTypes []EdgeTypeSpec `json:"edgeTypes,omitempty"`
	// Storage is the storage configuration for the graph.
	// +optional
	Storage *StorageSpec `json:"storage,omitempty"`
	// Versioning is the versioning configuration for the graph.
	// +optional
	Versioning *VersioningSpec `json:"versioning,omitempty"`
}

// EntityTypeSpec defines an entity type within the graph.
type EntityTypeSpec struct {
	// name is the entity type name. Must be a valid Cypher identifier.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:Pattern="^[a-zA-Z_][a-zA-Z0-9_]*$"
	Name string `json:"name"`
	// Properties is the list of properties for this entity type.
	// +optional
	Properties []PropertySpec `json:"properties,omitempty"`
	// EnableVectorIndex enables vector indexing for this entity type.
	// +kubebuilder:default=false
	// +optional
	EnableVectorIndex bool `json:"enableVectorIndex,omitempty"`
	// Rules defines the connection rules for this entity type.
	// +optional
	Rules []ConnectionRule `json:"rules,omitempty"`
}

// EdgeTypeSpec defines an edge type within the graph.
type EdgeTypeSpec struct {
	// name is the edge type name. Must be a valid Cypher identifier.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:Pattern="^[a-zA-Z_][a-zA-Z0-9_]*$"
	Name string `json:"name"`
	// Properties is the list of properties for this edge type.
	// +optional
	Properties []PropertySpec `json:"properties,omitempty"`
}

// PropertySpec defines a property within an entity or edge type.
type PropertySpec struct {
	// name is the property name. Must be a valid Cypher identifier.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:Pattern="^[a-zA-Z_][a-zA-Z0-9_]*$"
	Name string `json:"name"`
	// type is the property type. Only "string" is supported in v1.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=string
	Type string `json:"type"`
	// Required marks this property as required.
	// +kubebuilder:default=false
	// +optional
	Required bool `json:"required,omitempty"`
}

// ConnectionRule defines a rule for edge connections between entity types.
type ConnectionRule struct {
	// canConnectTo is the list of entity type names this rule permits connections to.
	// +optional
	CanConnectTo []string `json:"canConnectTo,omitempty"`
	// using is the list of edge type names this rule permits.
	// +optional
	Using []string `json:"using,omitempty"`
}

// StorageSpec defines the storage configuration for the graph.
type StorageSpec struct {
	// Size is the requested storage size for the graph.
	// +kubebuilder:default="1Gi"
	Size *resource.Quantity `json:"size,omitempty"`
}

// VersioningSpec defines the versioning configuration for the graph.
type VersioningSpec struct {
	// TransactionTimeout is the default timeout for transactions.
	// +kubebuilder:default="30m"
	TransactionTimeout *metav1.Duration `json:"transactionTimeout,omitempty"`
	// Remote is the remote repository configuration.
	Remote *RemoteConfig `json:"remote,omitempty"`
}

// RemoteConfig defines the remote repository configuration.
type RemoteConfig struct {
	// URL is the remote repository URL.
	URL string `json:"url,omitempty"`
	// Auth is the authentication configuration for the remote repository.
	Auth *RemoteAuth `json:"auth,omitempty"`
	// PullOnInit enables pulling from the remote repository on initialization.
	// +kubebuilder:default=false
	PullOnInit bool `json:"pullOnInit,omitempty"`
}

// RemoteAuth defines the authentication configuration for the remote repository.
type RemoteAuth struct {
	// SecretRef is the name of the secret containing authentication credentials.
	SecretRef string `json:"secretRef,omitempty"`
}

// FoundryGraphStatus defines the observed state.
// The Conditions field is not shown in SPEC R1's status YAML block (which
// shows only endpoint and storageSize for brevity), but the SPEC prose states "The
// Operator also sets conditions on the status to reflect intermediate states" (§R1).
// The Conditions field is added here to make the Go struct complete — controller-runtime
// standard status conditions are required for the Operator to report intermediate states
// like "destructive schema change blocked by open transactions".
type FoundryGraphStatus struct {
	// Endpoint is the gRPC endpoint of the Cartographer service.
	Endpoint EndpointInfo `json:"endpoint,omitempty"`
	// StorageSize is the actual storage allocation of the PVC.
	StorageSize *resource.Quantity `json:"storageSize,omitempty"`
	// conditions represent the current state of the FoundryGraph resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// EndpointInfo defines the endpoint information for the Cartographer service.
type EndpointInfo struct {
	// Host is the hostname of the Cartographer service.
	Host string `json:"host,omitempty"`
	// Port is the gRPC port of the Cartographer service.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`
}
