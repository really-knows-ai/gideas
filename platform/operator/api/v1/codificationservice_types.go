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

// CodificationServiceSpec defines the desired state of CodificationService.
// A CodificationService is a specialised Flow Support Service that translates
// law goals into formal representations. Each instance produces exactly one
// representation type, declared via outputFormat.
// The provided capability is always "encode" — the Operator enforces this implicitly.
type CodificationServiceSpec struct {
	ServiceSpecBase `json:",inline"`

	// outputFormat is the MIME type of the representation this service produces
	// (e.g. application/smt-lib, application/rego, application/python).
	// Exactly one output format per service instance.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	OutputFormat string `json:"outputFormat"`
}

// CodificationServiceStatus defines the observed state of CodificationService.
type CodificationServiceStatus struct {
	ServiceStatusBase `json:",inline"`
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

// The accessor methods below are thin delegators to the shared
// ServiceSpecBase/ServiceStatusBase accessors; they exist only to satisfy the
// ServiceObject/statusUpdater/ServiceWithOutput interfaces on the CRD type.
func (c *CodificationService) GetSpecImage() string       { return c.Spec.GetSpecImage() }
func (c *CodificationService) GetSpecMinReplicas() *int32 { return c.Spec.GetSpecMinReplicas() }
func (c *CodificationService) GetSpecDeploymentStrategy() string {
	return c.Spec.GetSpecDeploymentStrategy()
}
func (c *CodificationService) GetSpecOutputFormat() string { return c.Spec.OutputFormat }
func (c *CodificationService) GetSpecResources() *corev1.ResourceRequirements {
	return c.Spec.GetSpecResources()
}
func (c *CodificationService) GetSpecStorage() *StorageConfig { return c.Spec.GetSpecStorage() }

func (c *CodificationService) GetPhase() string            { return c.Status.GetPhase() }
func (c *CodificationService) SetPhase(p string)           { c.Status.SetPhase(p) }
func (c *CodificationService) GetAvailableReplicas() int32 { return c.Status.GetAvailableReplicas() }
func (c *CodificationService) SetAvailableReplicas(r int32) {
	c.Status.SetAvailableReplicas(r)
}
func (c *CodificationService) GetConditions() []metav1.Condition { return c.Status.GetConditions() }
func (c *CodificationService) SetConditions(cs []metav1.Condition) {
	c.Status.SetConditions(cs)
}

func init() {
	SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(GroupVersion, &CodificationService{}, &CodificationServiceList{})
		return nil
	})
}
