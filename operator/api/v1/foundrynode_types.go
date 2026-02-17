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

// FoundryNodeSpec defines the desired state of FoundryNode.
// The FoundryNode CRD defines node-local behaviour, permission envelope, and routing topology.
type FoundryNodeSpec struct {
	// image is the container image for the node.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// outputs are the named routing outputs. Each output maps a name to a target node.
	// +optional
	Outputs []Output `json:"outputs,omitempty"`

	// capabilities are the capability grant strings.
	// Grammar: VERB:RESOURCE[/QUALIFIER].
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`

	// entry is the name of the entry contract this node is bound to.
	// Must reference a key in the FoundryFlow's entryContracts.
	// +optional
	Entry string `json:"entry,omitempty"`

	// exit is the name of the exit contract this node is bound to.
	// Must reference a key in the FoundryFlow's exitContracts. Grants complete() eligibility.
	// +optional
	Exit string `json:"exit,omitempty"`

	// timeout is the inactivity timeout for assignments to this node.
	// Cannot exceed governancePolicy.maxTimeout on the FoundryFlow.
	// Falls back to governancePolicy.defaultTimeout if unset.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// concurrency is the maximum concurrent Workitem assignments per pod.
	// Default 1. Value 0 means unlimited.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	Concurrency *int32 `json:"concurrency,omitempty"`

	// storage defines volume mounts and deployment strategy.
	// Presence of persistent volumes triggers StatefulSet deployment;
	// otherwise ReplicaSet (default).
	// +optional
	Storage *StorageConfig `json:"storage,omitempty"`
}

// Output defines a named routing output mapping to a target node.
type Output struct {
	// name is the output channel name. Referenced by route_to_output instructions.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// target is the target node name. Must reference an existing FoundryNode in the namespace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Target string `json:"target"`
}

// StorageConfig defines volume mounts and deployment strategy for a node.
type StorageConfig struct {
	// volumes are the volume mount declarations.
	// Injected into both node and Sidecar containers.
	// +optional
	Volumes []VolumeMount `json:"volumes,omitempty"`

	// deploymentStrategy is the deployment strategy: ReplicaSet (default) or StatefulSet.
	// +optional
	// +kubebuilder:validation:Enum=ReplicaSet;StatefulSet
	// +kubebuilder:default="ReplicaSet"
	DeploymentStrategy string `json:"deploymentStrategy,omitempty"`
}

// VolumeMount defines a volume mount declaration for node storage.
type VolumeMount struct {
	// name is the name of the volume mount.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// mountPath is the path where the volume is mounted in the container.
	// +kubebuilder:validation:Required
	MountPath string `json:"mountPath"`

	// size is the requested storage size (e.g. "1Gi", "500Mi").
	// +optional
	Size string `json:"size,omitempty"`
}

// FoundryNodeStatus defines the observed state of FoundryNode.
type FoundryNodeStatus struct {
	// conditions represent the current state of the FoundryNode resource.
	// Standard Kubernetes conditions for reconciliation health.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced

// FoundryNode is the Schema for the foundrynodes API.
// It defines node-local behaviour, permission envelope, and routing topology.
type FoundryNode struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of FoundryNode
	// +required
	Spec FoundryNodeSpec `json:"spec"`

	// status defines the observed state of FoundryNode
	// +optional
	Status FoundryNodeStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// FoundryNodeList contains a list of FoundryNode
type FoundryNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []FoundryNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FoundryNode{}, &FoundryNodeList{})
}
