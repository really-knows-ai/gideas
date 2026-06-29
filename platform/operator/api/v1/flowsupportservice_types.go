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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FlowSupportServiceSpec defines the desired state of FlowSupportService.
// The FlowSupportService CRD declares an optional, Flow-Architect-deployed service container.
type FlowSupportServiceSpec struct {
	// image is the container image for the Support Service.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// providesCapabilities are the capability names this service exposes
	// (e.g. ["encode"]). Nodes consume these via USE:support/<service>/<capability>
	// grants on their FoundryNode capabilities field.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	ProvidesCapabilities []string `json:"providesCapabilities"`

	// deploymentStrategy is the deployment strategy: ReplicaSet (default) or StatefulSet.
	// +optional
	// +kubebuilder:validation:Enum=ReplicaSet;StatefulSet
	// +kubebuilder:default="ReplicaSet"
	DeploymentStrategy string `json:"deploymentStrategy,omitempty"`

	// minReplicas is the minimum replica count. Default 0, allowing scale-to-zero.
	// +optional
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	MinReplicas *int32 `json:"minReplicas,omitempty"`

	// storage defines volume mounts and PVC declarations.
	// +optional
	Storage *StorageConfig `json:"storage,omitempty"`

	// resources defines CPU and memory resource limits and requests.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// FlowSupportServiceStatus defines the observed state of FlowSupportService.
type FlowSupportServiceStatus struct {
	// phase is the service state: Initialising, Ready, Degraded, Stopped.
	// +optional
	// +kubebuilder:validation:Enum=Initialising;Ready;Degraded;Stopped
	Phase string `json:"phase,omitempty"`

	// availableReplicas is the current number of ready replicas.
	// +optional
	AvailableReplicas int32 `json:"availableReplicas,omitempty"`

	// conditions represent the current state of the FlowSupportService resource.
	// Standard Kubernetes conditions.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=".status.availableReplicas"

// FlowSupportService is the Schema for the flowsupportservices API.
// It declares an optional, Flow-Architect-deployed service container.
type FlowSupportService struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of FlowSupportService
	// +required
	Spec FlowSupportServiceSpec `json:"spec"`

	// status defines the observed state of FlowSupportService
	// +optional
	Status FlowSupportServiceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// FlowSupportServiceList contains a list of FlowSupportService
type FlowSupportServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []FlowSupportService `json:"items"`
}

func (c *FlowSupportService) GetSpecImage() string                           { return c.Spec.Image }
func (c *FlowSupportService) GetSpecMinReplicas() *int32                     { return c.Spec.MinReplicas }
func (c *FlowSupportService) GetSpecDeploymentStrategy() string              { return c.Spec.DeploymentStrategy }
func (c *FlowSupportService) GetSpecResources() *corev1.ResourceRequirements { return c.Spec.Resources }
func (c *FlowSupportService) GetSpecStorage() *StorageConfig                 { return c.Spec.Storage }

func (c *FlowSupportService) GetPhase() string                    { return c.Status.Phase }
func (c *FlowSupportService) SetPhase(p string)                   { c.Status.Phase = p }
func (c *FlowSupportService) GetAvailableReplicas() int32         { return c.Status.AvailableReplicas }
func (c *FlowSupportService) SetAvailableReplicas(r int32)        { c.Status.AvailableReplicas = r }
func (c *FlowSupportService) GetConditions() []metav1.Condition   { return c.Status.Conditions }
func (c *FlowSupportService) SetConditions(cs []metav1.Condition) { c.Status.Conditions = cs }

func init() {
	SchemeBuilder.Register(&FlowSupportService{}, &FlowSupportServiceList{})
}
