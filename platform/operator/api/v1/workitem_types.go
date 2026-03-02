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

// WorkitemStatus defines the state of a Workitem.
// The Workitem CRD is a pure control-plane state machine for a unit of work.
// It carries lifecycle state, assignment ownership, routing outcomes, and loop-detection counters.
// The Operator is the sole mutator. Nodes interact through SDK abstractions, not CRD field paths.
type WorkitemStatus struct {
	// phase is the current lifecycle state.
	// Routing is a transitional state set by the gRPC server when a
	// SubmitResult is received, signalling the reconciler to process
	// the routing instruction.
	// +optional
	// +kubebuilder:validation:Enum=Pending;Running;Routing;Suspended;Completed;Failed
	Phase string `json:"phase,omitempty"`

	// currentAssignee is the node currently processing this Workitem.
	// Empty when Pending.
	// +optional
	CurrentAssignee string `json:"currentAssignee,omitempty"`

	// parentWorkitemID is the ID of the parent Workitem that spawned this child.
	// Set by the Operator when a child Workitem is created via CreateChildWorkitem.
	// Empty for root Workitems. Immutable once set.
	// The label flow.gideas.io/parent is also set for efficient querying.
	// +optional
	ParentWorkitemID string `json:"parentWorkitemID,omitempty"`

	// routingInstruction is the most recent routing outcome submitted by the assigned node.
	// +optional
	RoutingInstruction *RoutingInstruction `json:"routingInstruction,omitempty"`

	// thrashCounters are per-node visit counts. Hidden from nodes.
	// The Thrash Guard triggers when the aggregate sum exceeds governancePolicy.maxVisits.
	// +optional
	ThrashCounters map[string]int32 `json:"thrashCounters,omitempty"`

	// metadata contains caller-supplied key-value pairs from CreateWorkitemRequest.
	// Stored on the Workitem CRD and propagated through to the handler via
	// WorkitemContext.Metadata. Entry-bound nodes use this to pass context
	// (e.g. law_id) from the entry loop to the handler, which may run on a
	// different replica. Immutable once set.
	// +optional
	Metadata map[string]string `json:"metadata,omitempty"`

	// assignedAt records when the current assignment began.
	// Set when the Workitem transitions to Running. Used for timeout enforcement.
	// +optional
	AssignedAt *metav1.Time `json:"assignedAt,omitempty"`

	// failureReason records why the Workitem transitioned to Failed.
	// Stable error codes from the error catalogue (e.g. THRASH_BUDGET_EXCEEDED,
	// TIMEOUT_EXCEEDED, CONTRACT_VIOLATION, INVALID_ROUTE).
	// +optional
	FailureReason string `json:"failureReason,omitempty"`

	// completionReason distinguishes success from cancellation on Completed workitems.
	// Empty or "success" means normal completion. "cancelled" means HITL cancelled.
	// Stored for audit and filterable in CEL suspend conditions.
	// +optional
	CompletionReason string `json:"completionReason,omitempty"`

	// resumeCondition is the CEL expression that must evaluate to true for the
	// Operator to auto-resume a Suspended workitem. Empty means manual Resume() required.
	// +optional
	ResumeCondition string `json:"resumeCondition,omitempty"`

	// suspendedAt records when the Workitem entered the Suspended phase.
	// Used for timeout enforcement.
	// +optional
	SuspendedAt *metav1.Time `json:"suspendedAt,omitempty"`

	// resumeTimeout is the maximum duration a workitem may remain Suspended.
	// Serialized as a Go duration string (e.g. "336h"). If the condition is not
	// met before suspendedAt + resumeTimeout, the workitem transitions to Failed.
	// +optional
	ResumeTimeout string `json:"resumeTimeout,omitempty"`
}

// RoutingInstruction represents a routing outcome submitted by the assigned node.
type RoutingInstruction struct {
	// type is the routing instruction type: route_to_output, route_to, complete, or suspend.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=route_to_output;route_to;complete;suspend
	Type string `json:"type"`

	// target is the output name (for route_to_output) or node name (for route_to).
	// Empty for complete and suspend.
	// +optional
	Target string `json:"target,omitempty"`

	// completionReason is set when type is "complete". Distinguishes success from
	// cancellation. Empty means success (default, backward compatible).
	// +optional
	CompletionReason string `json:"completionReason,omitempty"`

	// suspendCondition is set when type is "suspend". CEL expression for auto-resume.
	// Empty means manual Resume() required.
	// +optional
	SuspendCondition string `json:"suspendCondition,omitempty"`

	// suspendTimeout is set when type is "suspend". Duration string for timeout.
	// +optional
	SuspendTimeout string `json:"suspendTimeout,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Assignee",type=string,JSONPath=".status.currentAssignee"

// Workitem is the Schema for the workitems API.
// The Workitem CRD has no spec block. It is created by the Operator
// and all mutable state lives in status.
type Workitem struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// status defines the state of the Workitem. All fields are Operator-managed.
	// +optional
	Status WorkitemStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// WorkitemList contains a list of Workitem
type WorkitemList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Workitem `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Workitem{}, &WorkitemList{})
}
