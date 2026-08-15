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
	"k8s.io/apimachinery/pkg/runtime"
)

// FlowSupportServiceSpec defines the desired state of FlowSupportService.
// The FlowSupportService CRD declares an optional, Flow-Engineering-Team-deployed service container.
type FlowSupportServiceSpec struct {
	ServiceSpecBase `json:",inline"`

	// providesCapabilities are the capability names this service exposes
	// (e.g. ["encode"]). Nodes consume these via USE:support/<service>/<capability>
	// grants on their FoundryNode capabilities field.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	ProvidesCapabilities []string `json:"providesCapabilities"`
}

// FlowSupportServiceStatus defines the observed state of FlowSupportService.
type FlowSupportServiceStatus struct {
	ServiceStatusBase `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=".status.availableReplicas"

// FlowSupportService is the Schema for the flowsupportservices API.
// It declares an optional, Flow-Engineering-Team-deployed service container.
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

// The accessor methods below are thin delegators to the shared
// ServiceSpecBase/ServiceStatusBase accessors; they exist only to satisfy the
// ServiceObject/statusUpdater interfaces on the CRD type.
func (c *FlowSupportService) GetSpecImage() string       { return c.Spec.GetSpecImage() }
func (c *FlowSupportService) GetSpecMinReplicas() *int32 { return c.Spec.GetSpecMinReplicas() }
func (c *FlowSupportService) GetSpecDeploymentStrategy() string {
	return c.Spec.GetSpecDeploymentStrategy()
}
func (c *FlowSupportService) GetSpecResources() *corev1.ResourceRequirements {
	return c.Spec.GetSpecResources()
}
func (c *FlowSupportService) GetSpecStorage() *StorageConfig { return c.Spec.GetSpecStorage() }

func (c *FlowSupportService) GetPhase() string            { return c.Status.GetPhase() }
func (c *FlowSupportService) SetPhase(p string)           { c.Status.SetPhase(p) }
func (c *FlowSupportService) GetAvailableReplicas() int32 { return c.Status.GetAvailableReplicas() }
func (c *FlowSupportService) SetAvailableReplicas(r int32) {
	c.Status.SetAvailableReplicas(r)
}
func (c *FlowSupportService) GetConditions() []metav1.Condition { return c.Status.GetConditions() }
func (c *FlowSupportService) SetConditions(cs []metav1.Condition) {
	c.Status.SetConditions(cs)
}

func init() {
	SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(GroupVersion, &FlowSupportService{}, &FlowSupportServiceList{})
		return nil
	})
}
