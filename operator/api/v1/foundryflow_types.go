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
)

// Contract is a map of governed artefact name to required stamp names.
// Each key is a governed artefact name (the metadata.name of a GovernedArtefact CR).
// Each value is a list of required stamp names.
// A governed artefact name with an empty list means artefacts of that type must be present but no stamps are required.
// Empty map means no artefact requirements.
type Contract map[string][]string

// FoundryFlowSpec defines the desired state of FoundryFlow.
// The FoundryFlow CRD defines the executable shape of a Flow. The Operator
// reconciles this resource as the source of truth for all flow-wide behavioural semantics.
type FoundryFlowSpec struct {
	// entryContracts are the named entry contracts. Each contract is a map of governed artefact name
	// to required stamp names. At least one entry contract must be defined.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinProperties=1
	EntryContracts map[string]Contract `json:"entryContracts"`

	// exitContracts are the named exit contracts. Each contract is a map of governed artefact name
	// to required stamp names.
	// +kubebuilder:validation:Required
	ExitContracts map[string]Contract `json:"exitContracts"`

	// importNode is the name of the FoundryNode that receives cross-flow imported Workitems.
	// Must reference an existing entry-bound node. If absent, cross-flow import is disabled.
	// +optional
	ImportNode string `json:"importNode,omitempty"`

	// governancePolicy defines governance thresholds and timers for this Flow.
	// +kubebuilder:validation:Required
	GovernancePolicy GovernancePolicy `json:"governancePolicy"`

	// eventBusConfig defines Event Bus per-channel retention settings.
	// When omitted, the Event Bus uses its built-in defaults.
	// +optional
	EventBusConfig *EventBusConfig `json:"eventBusConfig,omitempty"`

	// crossFlow defines cross-flow trust and naturalisation settings.
	// +optional
	CrossFlow *CrossFlowConfig `json:"crossFlow,omitempty"`
}

// GovernancePolicy defines governance thresholds and timers for a Flow.
type GovernancePolicy struct {
	// maxVisits is the Thrash Guard budget. When the aggregate visit count across all nodes
	// exceeds this value, the Workitem fails with THRASH_BUDGET_EXCEEDED.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	MaxVisits int32 `json:"maxVisits"`

	// defaultTimeout is the default inactivity timeout for node assignments.
	// Used as the fallback when no node-specific timeout is set in FoundryNode.
	// +kubebuilder:validation:Required
	DefaultTimeout metav1.Duration `json:"defaultTimeout"`

	// maxTimeout is the maximum inactivity timeout for node assignments.
	// No node-specific timeout can exceed this value. Must be >= defaultTimeout.
	// +kubebuilder:validation:Required
	MaxTimeout metav1.Duration `json:"maxTimeout"`

	// frictionThresholds are per-tier friction thresholds that trigger review hearings.
	// +optional
	FrictionThresholds *FrictionThresholds `json:"frictionThresholds,omitempty"`

	// reviewTTLs are per-tier time-to-live durations that trigger review hearings.
	// +optional
	ReviewTTLs *ReviewTTLs `json:"reviewTTLs,omitempty"`

	// retentionPolicy defines retention duration for terminal Workitems before garbage collection.
	// +optional
	RetentionPolicy *RetentionPolicy `json:"retentionPolicy,omitempty"`
}

// FrictionThresholds defines per-tier friction thresholds that trigger review hearings.
// When a law's accumulated friction crosses its tier's configured threshold,
// the Librarian triggers a review hearing. Values are decimal quantities (e.g. "1.5", "0.75").
type FrictionThresholds struct {
	// tier1 is the accumulated friction threshold for Tier 1 laws (Findings).
	// +optional
	Tier1 *resource.Quantity `json:"tier1,omitempty"`

	// tier2 is the accumulated friction threshold for Tier 2 laws (Rulings).
	// +optional
	Tier2 *resource.Quantity `json:"tier2,omitempty"`

	// tier3 is the accumulated friction threshold for Tier 3 laws (Local Statutes).
	// +optional
	Tier3 *resource.Quantity `json:"tier3,omitempty"`

	// tier4 is the accumulated friction threshold for Tier 4 laws (State Constitutions).
	// +optional
	Tier4 *resource.Quantity `json:"tier4,omitempty"`

	// tier5 is the accumulated friction threshold for Tier 5 laws (Federal Accords).
	// +optional
	Tier5 *resource.Quantity `json:"tier5,omitempty"`
}

// ReviewTTLs defines per-tier time-to-live durations that trigger review hearings.
// When a law's age exceeds its tier's configured TTL, the Librarian triggers a review hearing.
type ReviewTTLs struct {
	// tier1 is the time-to-live for Tier 1 laws (Findings).
	// +optional
	Tier1 *metav1.Duration `json:"tier1,omitempty"`

	// tier2 is the time-to-live for Tier 2 laws (Rulings).
	// +optional
	Tier2 *metav1.Duration `json:"tier2,omitempty"`

	// tier3 is the time-to-live for Tier 3 laws (Local Statutes).
	// +optional
	Tier3 *metav1.Duration `json:"tier3,omitempty"`

	// tier4 is the time-to-live for Tier 4 laws (State Constitutions).
	// +optional
	Tier4 *metav1.Duration `json:"tier4,omitempty"`

	// tier5 is the time-to-live for Tier 5 laws (Federal Accords).
	// +optional
	Tier5 *metav1.Duration `json:"tier5,omitempty"`
}

// RetentionPolicy defines retention duration for terminal Workitems before garbage collection.
type RetentionPolicy struct {
	// maxAge is the maximum age of terminal Workitems before garbage collection.
	// +optional
	MaxAge *metav1.Duration `json:"maxAge,omitempty"`
}

// EventBusConfig defines Event Bus configuration for a Flow.
// The Operator passes these settings as env vars to the Event Bus Deployment.
type EventBusConfig struct {
	// retention defines per-channel retention windows for the Event Bus.
	// +optional
	Retention EventBusRetention `json:"retention,omitempty"`
}

// EventBusRetention defines per-channel retention windows.
// Both duration-based and size-based limits are supported.
// When both are specified, the Event Bus evicts when either limit is exceeded.
type EventBusRetention struct {
	// telemetryDuration is the retention window for the telemetry channel.
	// Go duration string (e.g. "24h", "168h").
	// +optional
	TelemetryDuration string `json:"telemetryDuration,omitempty"`

	// telemetrySize is the maximum total payload size for the telemetry channel.
	// Byte-count string with unit suffix (e.g. "100MB", "1GB").
	// +optional
	TelemetrySize string `json:"telemetrySize,omitempty"`

	// auditDuration is the retention window for the audit channel.
	// Go duration string (e.g. "168h").
	// +optional
	AuditDuration string `json:"auditDuration,omitempty"`

	// auditSize is the maximum total payload size for the audit channel.
	// Byte-count string with unit suffix (e.g. "1GB").
	// +optional
	AuditSize string `json:"auditSize,omitempty"`

	// frictionDuration is the retention window for the friction channel.
	// Go duration string (e.g. "72h").
	// +optional
	FrictionDuration string `json:"frictionDuration,omitempty"`

	// frictionSize is the maximum total payload size for the friction channel.
	// Byte-count string with unit suffix (e.g. "100MB").
	// +optional
	FrictionSize string `json:"frictionSize,omitempty"`
}

// CrossFlowConfig defines cross-flow trust and naturalisation settings.
type CrossFlowConfig struct {
	// stateRootCA is the PEM-encoded State Root CA certificate.
	// Present when the Flow operates under a Governance Flow.
	// +optional
	StateRootCA string `json:"stateRootCA,omitempty"`

	// naturalisation defines the policy for naturalising imported artefacts and stamps
	// at treaty boundaries.
	// +optional
	Naturalisation *NaturalisationConfig `json:"naturalisation,omitempty"`
}

// NaturalisationConfig defines the policy for naturalising imported artefacts and stamps.
type NaturalisationConfig struct {
	// autoNaturalise controls whether imported stamps from sibling Flows are automatically
	// authoritative after chain verification. Default true for sibling Flows.
	// +optional
	// +kubebuilder:default=true
	AutoNaturalise *bool `json:"autoNaturalise,omitempty"`

	// requireLocalStamps is a list of local stamp names that must be applied to imported
	// artefacts during naturalisation at treaty boundaries.
	// +optional
	RequireLocalStamps []string `json:"requireLocalStamps,omitempty"`
}

// FoundryFlowStatus defines the observed state of FoundryFlow.
type FoundryFlowStatus struct {
	// phase is the reconciliation state: Initialising, Ready, Degraded, Failed.
	// +optional
	// +kubebuilder:validation:Enum=Initialising;Ready;Degraded;Failed
	Phase string `json:"phase,omitempty"`

	// conditions represent the current state of the FoundryFlow resource.
	// Standard Kubernetes conditions for reconciliation health.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"

// FoundryFlow is the Schema for the foundryflows API.
// It defines the executable shape of a Flow including entry/exit contracts,
// governance policy, and cross-flow configuration.
type FoundryFlow struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of FoundryFlow
	// +required
	Spec FoundryFlowSpec `json:"spec"`

	// status defines the observed state of FoundryFlow
	// +optional
	Status FoundryFlowStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// FoundryFlowList contains a list of FoundryFlow
type FoundryFlowList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []FoundryFlow `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FoundryFlow{}, &FoundryFlowList{})
}
