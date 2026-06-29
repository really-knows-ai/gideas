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

// CodificationServiceSpec defines the desired state of CodificationService.
// A CodificationService is a specialised Flow Support Service that translates
// law goals into formal representations. Each instance produces exactly one
// representation type, declared via outputFormat.
// The provided capability is always "encode" — the Operator enforces this implicitly.
type CodificationServiceSpec struct {
	// image is the container image for the Codification Service.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// outputFormat is the MIME type of the representation this service produces
	// (e.g. application/smt-lib, application/rego, application/python).
	// Exactly one output format per service instance.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	OutputFormat string `json:"outputFormat"`

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

// CodificationServiceStatus defines the observed state of CodificationService.
type CodificationServiceStatus struct {
	// phase is the service state: Initialising, Ready, Degraded, Stopped.
	// +optional
	// +kubebuilder:validation:Enum=Initialising;Ready;Degraded;Stopped
	Phase string `json:"phase,omitempty"`

	// availableReplicas is the current number of ready replicas.
	// +optional
	AvailableReplicas int32 `json:"availableReplicas,omitempty"`

	// conditions represent the current state of the CodificationService resource.
	// Standard Kubernetes conditions.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="OutputFormat",type=string,JSONPath=".spec.outputFormat"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"

// CodificationService is the Schema for the codificationservices API.
// It declares a Codification Service that translates law goals into formal representations.
type CodificationService struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of CodificationService
	// +required
	Spec CodificationServiceSpec `json:"spec"`

	// status defines the observed state of CodificationService
	// +optional
	Status CodificationServiceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// CodificationServiceList contains a list of CodificationService
type CodificationServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []CodificationService `json:"items"`
}

func (c *CodificationService) GetSpecImage() string              { return c.Spec.Image }
func (c *CodificationService) GetSpecMinReplicas() *int32        { return c.Spec.MinReplicas }
func (c *CodificationService) GetSpecDeploymentStrategy() string { return c.Spec.DeploymentStrategy }
func (c *CodificationService) GetSpecOutputFormat() string       { return c.Spec.OutputFormat }
func (c *CodificationService) GetSpecResources() *corev1.ResourceRequirements {
	return c.Spec.Resources
}
func (c *CodificationService) GetSpecStorage() *StorageConfig { return c.Spec.Storage }

func (c *CodificationService) GetPhase() string                    { return c.Status.Phase }
func (c *CodificationService) SetPhase(p string)                   { c.Status.Phase = p }
func (c *CodificationService) GetAvailableReplicas() int32         { return c.Status.AvailableReplicas }
func (c *CodificationService) SetAvailableReplicas(r int32)        { c.Status.AvailableReplicas = r }
func (c *CodificationService) GetConditions() []metav1.Condition   { return c.Status.Conditions }
func (c *CodificationService) SetConditions(cs []metav1.Condition) { c.Status.Conditions = cs }

func init() {
	SchemeBuilder.Register(&CodificationService{}, &CodificationServiceList{})
}
