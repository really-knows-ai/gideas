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

// ServiceSpecBase carries the spec fields shared by every service CRD
// (CodificationService, FlowSupportService). The ServiceObject accessors live
// here exactly once; each service CRD embeds this type in its Spec and its
// accessor methods delegate to the promoted methods.
type ServiceSpecBase struct {
	// image is the container image for the service.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

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

func (s *ServiceSpecBase) GetSpecImage() string              { return s.Image }
func (s *ServiceSpecBase) GetSpecMinReplicas() *int32        { return s.MinReplicas }
func (s *ServiceSpecBase) GetSpecDeploymentStrategy() string { return s.DeploymentStrategy }
func (s *ServiceSpecBase) GetSpecResources() *corev1.ResourceRequirements {
	return s.Resources
}
func (s *ServiceSpecBase) GetSpecStorage() *StorageConfig { return s.Storage }

// ServiceStatusBase carries the status fields shared by every service CRD.
// The statusUpdater accessors live here exactly once; each service CRD embeds
// this type in its Status and its accessor methods delegate to the promoted
// methods.
type ServiceStatusBase struct {
	// phase is the service state: Initialising, Ready, Degraded, Stopped.
	// +optional
	// +kubebuilder:validation:Enum=Initialising;Ready;Degraded;Stopped
	Phase string `json:"phase,omitempty"`

	// availableReplicas is the current number of ready replicas.
	// +optional
	AvailableReplicas int32 `json:"availableReplicas,omitempty"`

	// conditions represent the current state of the resource.
	// Standard Kubernetes conditions.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

func (s *ServiceStatusBase) GetPhase() string            { return s.Phase }
func (s *ServiceStatusBase) SetPhase(p string)           { s.Phase = p }
func (s *ServiceStatusBase) GetAvailableReplicas() int32 { return s.AvailableReplicas }
func (s *ServiceStatusBase) SetAvailableReplicas(r int32) {
	s.AvailableReplicas = r
}
func (s *ServiceStatusBase) GetConditions() []metav1.Condition { return s.Conditions }
func (s *ServiceStatusBase) SetConditions(cs []metav1.Condition) {
	s.Conditions = cs
}
