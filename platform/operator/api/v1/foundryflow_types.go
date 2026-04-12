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

	// governancePolicy defines governance thresholds and timers for this Flow.
	// +kubebuilder:validation:Required
	GovernancePolicy GovernancePolicy `json:"governancePolicy"`

	// eventBusConfig defines Event Bus per-channel retention settings.
	// When omitted, the Event Bus uses its built-in defaults.
	// +optional
	EventBusConfig *EventBusConfig `json:"eventBusConfig,omitempty"`

	// suspension defines suspension timeout policy for this Flow.
	// Controls how long workitems may remain in the Suspended phase.
	// +optional
	Suspension *SuspensionConfig `json:"suspension,omitempty"`

	// crossFlow defines cross-flow trust and naturalisation settings.
	// +optional
	CrossFlow *CrossFlowConfig `json:"crossFlow,omitempty"`

	// nodeGroups defines sub-topology boundaries within the Flow.
	// Each key is a group name; each value defines the group's entry/exit contracts and member nodes.
	// Nodes inside a group are routing-isolated: internal routing stays within the group,
	// and external routing enters only through entry-bound nodes.
	// +optional
	NodeGroups map[string]NodeGroup `json:"nodeGroups,omitempty"`
}

// NodeGroup defines a sub-topology boundary within a Flow.
// A NodeGroup has its own entry and exit contracts and a set of member nodes.
// Work enters a NodeGroup by routing to a specific entry-bound node within the group.
// The group's entry contract is validated by the Operator, same as Flow-level entry contracts.
type NodeGroup struct {
	// entryContracts are the named entry contracts for this group.
	// Validated when a root Workitem is routed to an entry-bound node in the group.
	// +optional
	EntryContracts map[string]Contract `json:"entryContracts,omitempty"`

	// exitContracts are the named exit contracts for this group.
	// Validated when an exit-bound node in the group calls Complete().
	// +optional
	ExitContracts map[string]Contract `json:"exitContracts,omitempty"`

	// nodes is the list of FoundryNode names that belong to this group.
	// Each node can belong to at most one group.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Nodes []string `json:"nodes"`
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
	RetentionPolicy *WorkitemRetentionPolicy `json:"retentionPolicy,omitempty"`
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

// WorkitemRetentionPolicy defines retention duration for terminal Workitems before garbage collection.
type WorkitemRetentionPolicy struct {
	// maxAge is the maximum age of terminal Workitems before garbage collection.
	// +optional
	MaxAge *metav1.Duration `json:"maxAge,omitempty"`
}

// SuspensionConfig defines suspension timeout policy for a Flow.
// Controls how long workitems may remain in the Suspended phase.
// Every suspension has a timeout. If the resume condition is not met before
// the deadline, the workitem transitions to Failed. There are no truly
// indefinite suspensions.
type SuspensionConfig struct {
	// maxSuspendTimeout is the hard cap on suspension duration.
	// The Operator rejects suspensions exceeding this value.
	// Defaults to 336h (2 weeks) if not specified.
	// +optional
	MaxSuspendTimeout *metav1.Duration `json:"maxSuspendTimeout,omitempty"`

	// defaultSuspendTimeout is applied when a node calls Suspend() without
	// specifying a timeout via WithTimeout(). Defaults to maxSuspendTimeout
	// if not specified.
	// +optional
	DefaultSuspendTimeout *metav1.Duration `json:"defaultSuspendTimeout,omitempty"`
}

// EventBusConfig defines Event Bus configuration for a Flow.
// The Operator passes these settings as env vars to the Event Bus Deployment.
type EventBusConfig struct {
	// retention defines per-channel retention windows for the Event Bus.
	// Each key is a channel name (e.g. "telemetry", "audit", "friction", "workitem").
	// +optional
	Retention EventBusRetention `json:"retention,omitempty"`
}

// EventBusRetention is a map of channel name to retention policy.
// Each key is a channel name (e.g. "telemetry", "audit", "friction", "workitem").
// Adding a new channel requires only a new map entry, not a code change.
type EventBusRetention map[string]EventBusRetentionPolicy

// EventBusRetentionPolicy defines retention limits for a single Event Bus channel.
// Both duration-based and size-based limits are supported.
// When both are specified, the Event Bus evicts when either limit is exceeded.
type EventBusRetentionPolicy struct {
	// duration is the retention window for this channel.
	// Go duration string (e.g. "24h", "168h").
	// +optional
	Duration string `json:"duration,omitempty"`

	// size is the maximum total payload size for this channel.
	// Byte-count string with unit suffix (e.g. "100MB", "1GB").
	// +optional
	Size string `json:"size,omitempty"`
}

// CrossFlowConfig defines cross-flow trust and naturalisation settings.
type CrossFlowConfig struct {
	// federationCA is the PEM-encoded Federation CA certificate.
	// Present when the Flow operates within a Federation.
	// +optional
	FederationCA string `json:"federationCA,omitempty"`

	// importTypes publishes the Flow's flow-authored cross-flow import types.
	// Built-in system import types are always present/configured by the platform
	// and are not declared in this map. Each flow-authored import type maps to an
	// entry-bound node and optional per-artefact foreign stamp requirements.
	// +optional
	ImportTypes map[string]ImportTypeSpec `json:"importTypes,omitempty"`

	// naturalisation defines the policy for naturalising imported artefacts and stamps
	// at treaty boundaries.
	// +optional
	Naturalisation *NaturalisationConfig `json:"naturalisation,omitempty"`

	// federation defines the Flow's federation membership, identity, state assignments,
	// and publisher roles. When set, the Flow participates in a Federation.
	// +optional
	Federation *FederationConfig `json:"federation,omitempty"`
}

// FederationConfig defines a Flow's federation membership and identity.
type FederationConfig struct {
	// identity is the Flow's unique identity within the Federation.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Identity string `json:"identity"`

	// states is the list of federation-defined state names this Flow belongs to.
	// States are organisational groups; sibling relationships derive from shared membership.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	States []string `json:"states"`

	// publisherRoles defines the authority publisher roles for this Flow.
	// Each role specifies a domain scope and whether the authority is state-level
	// or federation-level.
	// +optional
	PublisherRoles []FederationPublisherRole `json:"publisherRoles,omitempty"`

	// federationEndpoint is the address of the Federation service.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	FederationEndpoint string `json:"federationEndpoint"`
}

// FederationPublisherRole defines a single publisher authority role.
type FederationPublisherRole struct {
	// scope is the domain this role covers (e.g. "security", "compliance").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Scope string `json:"scope"`

	// level is the authority level: "state" for state-level publication (Tier 4)
	// or "federation" for federation-wide publication (Tier 5).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=state;federation
	Level string `json:"level"`
}

// ImportTypeSpec defines one flow-authored cross-flow import type.
type ImportTypeSpec struct {
	// node is the name of the FoundryNode that receives imported Workitems for
	// this import type. Must reference an existing entry-bound node.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Node string `json:"node"`

	// requireForeignStamps defines per-governed-artefact foreign stamp
	// requirements that the Embassy must verify before materialising the import.
	// +optional
	RequireForeignStamps Contract `json:"requireForeignStamps,omitempty"`
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
