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

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	flowv1gen "github.com/gideas/flow/gen/flow/v1"
	flowv1 "github.com/gideas/flow/operator/api/v1"
	"github.com/gideas/flow/operator/internal/controller/dispatcher"
	"github.com/gideas/flow/operator/internal/controller/scheduler"
	"github.com/gideas/flow/pkg/eventbus"
	"github.com/gideas/flow/pkg/randid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Workitem lifecycle phase constants.
const (
	wiPhasePending   = "Pending"
	wiPhaseRunning   = "Running"
	wiPhaseRouting   = "Routing"
	wiPhaseSuspended = "Suspended"
	wiPhaseCompleted = "Completed"
	// wiPhaseFailed uses the package-level phaseFailed from foundrynode_controller.go.

	// childCheckInterval is the requeue interval for a Running parent
	// that has non-terminal children. The Operator re-checks periodically
	// to resume normal timeout enforcement once all children finish.
	childCheckInterval = 30 * time.Second

	// suspendCheckInterval is the requeue interval for re-evaluating
	// a Suspended workitem's resume condition. Used when neither timeout
	// nor condition has been met yet.
	suspendCheckInterval = 15 * time.Second
)

// nowFunc is a function variable for the current time.
// Tests can override this to produce deterministic timestamps.
var nowFunc = metav1.Now

// WorkitemReconciler reconciles a Workitem object.
//
// It enforces the spec-defined guard evaluation order for routing transitions:
//  1. Instruction shape validity
//  2. Routing target or exit eligibility validity (including exit contract validation)
//  3. Timeout and thrash guard compliance
//  4. Lifecycle transition application
type WorkitemReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// ArtefactQuerier is used by the scheduler to validate exit contracts
	// against artefact state in the Archivist. May be nil in tests or when
	// the Archivist is not yet available (contract validation is skipped).
	ArtefactQuerier func(ctx context.Context, workitemID string, governedArtefacts []string) ([]scheduler.ArtefactState, error)

	// Auditor publishes lifecycle events to the Event Bus via async submit.
	// nil-safe: audit publishing degrades gracefully.
	Auditor *eventbus.AsyncPublisher
}

// publishAudit submits an audit event for a workitem lifecycle transition via
// the async publisher. If the publisher is nil, audit publishing is silently
// disabled.
func (r *WorkitemReconciler) publishAudit(eventType string, workitemName string, attrs map[string]string) {
	if r.Auditor == nil {
		return
	}
	r.Auditor.Submit(&flowv1gen.PublishRequest{
		Channel: "audit",
		Event: &flowv1gen.FlowEvent{
			EventId:    randid.NewRandomID(),
			EventType:  eventType,
			WorkitemId: workitemName,
			Timestamp:  timestamppb.Now(),
			Attributes: attrs,
		},
	})
}

// publishLifecycle emits a workitem.phase_changed event on the "workitem"
// Event Bus channel. Labels carry the filtering-relevant dimensions
// (workitem_id, phase, node_id, and optionally parent_workitem_id).
// The flow_id is placed in attributes as non-filtered context.
func (r *WorkitemReconciler) publishLifecycle(workitem *flowv1.Workitem, phase string, nodeID string) {
	if r.Auditor == nil {
		return
	}

	labels := []*flowv1gen.Label{
		{Key: "workitem_id", Value: workitem.Name},
		{Key: "phase", Value: phase},
		{Key: "node_id", Value: nodeID},
	}

	if workitem.Status.ParentWorkitemID != "" {
		labels = append(labels, &flowv1gen.Label{
			Key:   "parent_workitem_id",
			Value: workitem.Status.ParentWorkitemID,
		})
	}

	attrs := map[string]string{
		"flow_namespace": workitem.Namespace,
	}

	r.Auditor.Submit(&flowv1gen.PublishRequest{
		Channel: "workitem",
		Event: &flowv1gen.FlowEvent{
			EventId:    randid.NewRandomID(),
			EventType:  "workitem.phase_changed",
			WorkitemId: workitem.Name,
			Timestamp:  timestamppb.Now(),
			Labels:     labels,
			Attributes: attrs,
		},
	})
}

// +kubebuilder:rbac:groups=flow.gideas.io,resources=workitems,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=flow.gideas.io,resources=workitems/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=flow.gideas.io,resources=workitems/finalizers,verbs=update
// +kubebuilder:rbac:groups=flow.gideas.io,resources=foundrynodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=flow.gideas.io,resources=foundryflows,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// The Workitem reconciler handles three phases:
//
//  1. Pending — The Workitem has a currentAssignee but no Pod is processing it.
//     The reconciler increments the thrash counter, records assignedAt, and uses
//     the Dispatcher to push the assignment via gRPC, then transitions to Running.
//
//  2. Running — The Workitem is assigned to a Pod. The reconciler checks for
//     timeout expiry and requeues with the remaining timeout duration.
//
//  3. Routing — The gRPC server has written a routingInstruction and set the
//     phase to Routing. The reconciler executes the full guard pipeline
//     (instruction validity, target resolution, contract validation, thrash guard)
//     to determine the next destination.
func (r *WorkitemReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the Workitem instance.
	var workitem flowv1.Workitem
	if err := r.Get(ctx, req.NamespacedName, &workitem); err != nil {
		// Workitem was deleted — nothing to reconcile.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Log the current state for observability.
	log.Info("Reconciling Workitem",
		"name", workitem.Name,
		"namespace", workitem.Namespace,
		"phase", workitem.Status.Phase,
		"assignee", workitem.Status.CurrentAssignee,
	)

	switch workitem.Status.Phase {
	case wiPhasePending:
		return r.reconcilePending(ctx, &workitem)
	case wiPhaseRunning:
		return r.reconcileRunning(ctx, req, &workitem)
	case wiPhaseRouting:
		return r.reconcileRouting(ctx, req, &workitem)
	case wiPhaseSuspended:
		return r.reconcileSuspended(ctx, &workitem)
	default:
		return ctrl.Result{}, nil
	}
}

// resolveFlow fetches the singleton FoundryFlow CRD in the Workitem's
// namespace. The one-namespace-one-flow invariant (enforced by B.1) means
// we list FoundryFlows in the namespace and expect exactly one.
func (r *WorkitemReconciler) resolveFlow(ctx context.Context, workitem *flowv1.Workitem) (*flowv1.FoundryFlow, error) {
	var flows flowv1.FoundryFlowList
	if err := r.List(ctx, &flows, client.InNamespace(workitem.Namespace)); err != nil {
		return nil, fmt.Errorf("list FoundryFlows: %w", err)
	}
	if len(flows.Items) != 1 {
		return nil, fmt.Errorf("expected exactly 1 FoundryFlow in namespace %q, found %d", workitem.Namespace, len(flows.Items))
	}
	return &flows.Items[0], nil
}

// resolveTimeout returns the effective timeout duration for the given node
// assignment, applying the flow's governance policy rules:
//   - Node-specific timeout takes precedence if set and within maxTimeout.
//   - Falls back to defaultTimeout.
//   - Node timeout is capped at maxTimeout.
func resolveTimeout(node *flowv1.FoundryNode, flow *flowv1.FoundryFlow) time.Duration {
	policy := flow.Spec.GovernancePolicy
	defaultTimeout := policy.DefaultTimeout.Duration
	maxTimeout := policy.MaxTimeout.Duration

	if node != nil && node.Spec.Timeout != nil {
		nodeTimeout := node.Spec.Timeout.Duration
		if nodeTimeout > maxTimeout {
			return maxTimeout
		}
		return nodeTimeout
	}

	return defaultTimeout
}

// reconcilePending handles Workitems in the Pending phase.
// It increments the thrash counter, records assignedAt, and uses the
// Dispatcher to discover a ready Pod and push the assignment.
//
// To prevent duplicate dispatches in a tight reconcile loop, this function
// transitions the Workitem to "Running" BEFORE dispatching. This acts as
// an optimistic lock: only the first reconcile to successfully claim the
// transition will proceed to dispatch work.
func (r *WorkitemReconciler) reconcilePending(ctx context.Context, workitem *flowv1.Workitem) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Guard: must have an assignee to dispatch to.
	if workitem.Status.CurrentAssignee == "" {
		log.Info("Workitem is Pending but has no assignee, skipping",
			"name", workitem.Name,
		)
		return ctrl.Result{}, nil
	}

	assignee := workitem.Status.CurrentAssignee

	// Resolve the FoundryFlow for thrash guard enforcement.
	flow, err := r.resolveFlow(ctx, workitem)
	if err != nil {
		log.Error(err, "Failed to resolve FoundryFlow",
			"name", workitem.Name,
		)
		return ctrl.Result{}, err
	}

	// Increment the thrash counter for the target node.
	if workitem.Status.ThrashCounters == nil {
		workitem.Status.ThrashCounters = make(map[string]int32)
	}
	workitem.Status.ThrashCounters[assignee]++

	// Check thrash guard BEFORE dispatching. If the budget is already
	// exceeded, fail the Workitem immediately instead of wasting a dispatch.
	var aggregate int32
	for _, count := range workitem.Status.ThrashCounters {
		aggregate += count
	}
	if aggregate > flow.Spec.GovernancePolicy.MaxVisits {
		log.Info("Thrash budget exceeded, failing Workitem",
			"name", workitem.Name,
			"aggregate", aggregate,
			"maxVisits", flow.Spec.GovernancePolicy.MaxVisits,
		)
		r.publishAudit("audit.workitem.failed", workitem.Name, map[string]string{
			"action": "failed",
			"reason": "THRASH_BUDGET_EXCEEDED",
		})
		r.publishLifecycle(workitem, phaseFailed, assignee)
		return r.failWorkitem(ctx, workitem, "THRASH_BUDGET_EXCEEDED")
	}

	log.Info("Dispatching Workitem",
		"name", workitem.Name,
		"assignee", assignee,
		"thrashCounter", workitem.Status.ThrashCounters[assignee],
	)

	// Record the assignment timestamp and claim the workitem by
	// transitioning to Running BEFORE dispatching. This prevents
	// duplicate dispatches: if two reconciles race, only one will
	// succeed at this status update.
	now := nowFunc()
	workitem.Status.Phase = wiPhaseRunning
	workitem.Status.AssignedAt = &now
	if err := r.Status().Update(ctx, workitem); err != nil {
		// Conflict means another reconcile already claimed it — that's fine.
		log.Info("Could not claim Workitem (likely already claimed)",
			"name", workitem.Name,
			"error", err.Error(),
		)
		return ctrl.Result{}, nil
	}

	// Use the Dispatcher to push the assignment.
	d := dispatcher.New(r.Client, workitem.Namespace)

	_, err = d.Assign(
		ctx,
		assignee,
		workitem.Name, // workitem_id
		workitem.Status.Metadata,
	)
	if err != nil {
		log.Error(err, "Failed to assign Workitem to pod",
			"name", workitem.Name,
			"assignee", assignee,
		)
		// Revert to Pending so it can be retried.
		var fresh flowv1.Workitem
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(workitem), &fresh); getErr == nil {
			if fresh.Status.Phase == wiPhaseRunning {
				fresh.Status.Phase = wiPhasePending
				fresh.Status.AssignedAt = nil
				_ = r.Status().Update(ctx, &fresh)
			}
		}
		return ctrl.Result{}, err
	}

	log.Info("Assigned Workitem successfully",
		"name", workitem.Name,
		"assignee", assignee,
	)

	r.publishAudit("audit.workitem.running", workitem.Name, map[string]string{
		"action":   "running",
		"assignee": assignee,
	})
	r.publishLifecycle(workitem, wiPhaseRunning, assignee)

	// Requeue after the timeout period to check for inactivity timeout.
	var node flowv1.FoundryNode
	nodeKey := k8stypes.NamespacedName{Namespace: workitem.Namespace, Name: assignee}
	timeout := flow.Spec.GovernancePolicy.DefaultTimeout.Duration
	if getErr := r.Get(ctx, nodeKey, &node); getErr == nil {
		timeout = resolveTimeout(&node, flow)
	}

	if timeout > 0 {
		return ctrl.Result{RequeueAfter: timeout}, nil
	}

	return ctrl.Result{}, nil
}

// reconcileRunning checks for timeout expiry on Running Workitems.
// If the assignment has exceeded the configured timeout, the Workitem
// transitions to Failed with TIMEOUT_EXCEEDED.
func (r *WorkitemReconciler) reconcileRunning(ctx context.Context, req ctrl.Request, workitem *flowv1.Workitem) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Resolve the FoundryFlow for timeout configuration.
	flow, err := r.resolveFlow(ctx, workitem)
	if err != nil {
		log.Error(err, "Failed to resolve FoundryFlow for timeout check",
			"name", workitem.Name,
		)
		return ctrl.Result{}, err
	}

	// Resolve the effective timeout for this node.
	var node flowv1.FoundryNode
	nodeKey := k8stypes.NamespacedName{Namespace: req.Namespace, Name: workitem.Status.CurrentAssignee}
	timeout := flow.Spec.GovernancePolicy.DefaultTimeout.Duration
	if getErr := r.Get(ctx, nodeKey, &node); getErr == nil {
		timeout = resolveTimeout(&node, flow)
	}

	// If no timeout configured (zero duration), skip timeout enforcement.
	if timeout <= 0 {
		return ctrl.Result{}, nil
	}

	// If this Workitem has non-terminal children, skip timeout enforcement.
	// A parent waiting for children to complete is not stuck — the Sidecar
	// pauses the inactivity timer while AwaitChildren blocks.
	if hasActiveChildren, checkErr := r.hasNonTerminalChildren(ctx, req.Namespace, workitem.Name); checkErr != nil {
		log.Error(checkErr, "Failed to check child Workitems", "name", workitem.Name)
		// On error, fall through to normal timeout logic (fail-open for timeout).
	} else if hasActiveChildren {
		log.Info("Skipping timeout — Workitem has non-terminal children",
			"name", workitem.Name,
			"assignee", workitem.Status.CurrentAssignee,
		)
		return ctrl.Result{RequeueAfter: childCheckInterval}, nil
	}

	// Check timeout expiry.
	if workitem.Status.AssignedAt != nil {
		elapsed := nowFunc().Sub(workitem.Status.AssignedAt.Time)
		if elapsed >= timeout {
			log.Info("Workitem assignment timed out",
				"name", workitem.Name,
				"assignee", workitem.Status.CurrentAssignee,
				"elapsed", elapsed,
				"timeout", timeout,
			)
			r.publishAudit("audit.workitem.failed", workitem.Name, map[string]string{
				"action": "failed",
				"reason": "TIMEOUT_EXCEEDED",
			})
			r.publishLifecycle(workitem, phaseFailed, workitem.Status.CurrentAssignee)
			return r.failWorkitem(ctx, workitem, "TIMEOUT_EXCEEDED")
		}

		// Requeue for the remaining timeout window.
		remaining := timeout - elapsed
		return ctrl.Result{RequeueAfter: remaining}, nil
	}

	// No assignedAt — requeue after the full timeout to check again.
	return ctrl.Result{RequeueAfter: timeout}, nil
}

// reconcileRouting handles Workitems in the Routing phase.
// It executes the full guard pipeline via the scheduler.
func (r *WorkitemReconciler) reconcileRouting(ctx context.Context, req ctrl.Request, workitem *flowv1.Workitem) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Guard: a routing instruction must be present.
	if workitem.Status.RoutingInstruction == nil {
		log.Error(
			fmt.Errorf("missing routing instruction"),
			"Workitem is in Routing phase but has no routing instruction",
			"name", workitem.Name,
		)
		return ctrl.Result{}, nil
	}

	log.Info("Routing instruction detected",
		"name", workitem.Name,
		"routing_type", workitem.Status.RoutingInstruction.Type,
		"routing_target", workitem.Status.RoutingInstruction.Target,
	)

	// Resolve the FoundryFlow for guard evaluation.
	flow, err := r.resolveFlow(ctx, workitem)
	if err != nil {
		log.Error(err, "Failed to resolve FoundryFlow for routing",
			"name", workitem.Name,
		)
		return ctrl.Result{}, err
	}

	// Execute the scheduling decision with full guard pipeline.
	sched := scheduler.New(r.Client, req.Namespace)
	sched.Querier = r.ArtefactQuerier

	result, err := sched.CalculateNextStep(
		ctx,
		workitem.Status.CurrentAssignee,
		*workitem.Status.RoutingInstruction,
		workitem,
		flow,
	)
	if err != nil {
		// Check if this is a guard failure that should fail the Workitem.
		var guardErr *scheduler.GuardError
		if errors.As(err, &guardErr) {
			// Terminal guard failures: thrash budget exceeded transitions
			// the Workitem to Failed. Other guard errors (INVALID_ROUTE,
			// EXIT_NOT_BOUND, CONTRACT_VIOLATION) are rejected — the
			// Workitem stays in its current state and the error is returned
			// to the caller.
			if guardErr.Code == "THRASH_BUDGET_EXCEEDED" {
				log.Info("Guard failure, failing Workitem",
					"name", workitem.Name,
					"code", guardErr.Code,
					"message", guardErr.Message,
				)
				r.publishAudit("audit.workitem.failed", workitem.Name, map[string]string{
					"action": "failed",
					"reason": guardErr.Code,
				})
				r.publishLifecycle(workitem, phaseFailed, workitem.Status.CurrentAssignee)
				return r.failWorkitem(ctx, workitem, guardErr.Code)
			}

			log.Error(err, "Guard failure, rejecting routing instruction",
				"name", workitem.Name,
				"code", guardErr.Code,
			)
		} else {
			log.Error(err, "Failed to calculate next step",
				"name", workitem.Name,
				"assignee", workitem.Status.CurrentAssignee,
			)
		}
		// Return the error so the controller retries with backoff.
		return ctrl.Result{}, err
	}

	previousAssignee := workitem.Status.CurrentAssignee

	// Re-fetch the Workitem to get the latest resourceVersion before writing.
	var fresh flowv1.Workitem
	if err := r.Get(ctx, req.NamespacedName, &fresh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// If the workitem has already moved past Routing, skip this update.
	if fresh.Status.Phase != wiPhaseRouting {
		log.Info("Workitem already advanced past Routing, skipping",
			"name", workitem.Name,
			"currentPhase", fresh.Status.Phase,
		)
		return ctrl.Result{}, nil
	}

	// Apply the transition on the fresh copy.
	fresh.Status.RoutingInstruction = nil // Clear to prevent re-processing.
	fresh.Status.AssignedAt = nil         // Clear for next assignment.

	if result.Phase == wiPhaseSuspended {
		// Suspend: keep the current assignee (re-dispatched on resume),
		// record the suspend timestamp, condition, and timeout.
		now := nowFunc()
		fresh.Status.Phase = wiPhaseSuspended
		// CurrentAssignee is preserved — the workitem resumes on the same node type.
		fresh.Status.SuspendedAt = &now
		fresh.Status.ResumeCondition = result.SuspendCondition
		fresh.Status.ResumeTimeout = result.SuspendTimeout

		if err := r.Status().Update(ctx, &fresh); err != nil {
			log.Error(err, "Failed to update Workitem status to Suspended",
				"name", workitem.Name,
			)
			return ctrl.Result{}, err
		}

		log.Info("Workitem suspended",
			"name", workitem.Name,
			"assignee", previousAssignee,
			"condition", result.SuspendCondition,
			"timeout", result.SuspendTimeout,
		)
		r.publishAudit("audit.workitem.suspended", workitem.Name, map[string]string{
			"action":    "suspended",
			"assignee":  previousAssignee,
			"condition": result.SuspendCondition,
			"timeout":   result.SuspendTimeout,
		})
		r.publishLifecycle(workitem, wiPhaseSuspended, previousAssignee)

		// If a resume timeout is configured, requeue to enforce it.
		if result.SuspendTimeout != "" {
			if d, parseErr := time.ParseDuration(result.SuspendTimeout); parseErr == nil && d > 0 {
				return ctrl.Result{RequeueAfter: d}, nil
			}
		}

		return ctrl.Result{}, nil
	}

	// Non-suspend transitions: route or complete.
	fresh.Status.Phase = result.Phase
	fresh.Status.CurrentAssignee = result.NextAssignee

	// Persist the status update.
	if err := r.Status().Update(ctx, &fresh); err != nil {
		log.Error(err, "Failed to update Workitem status",
			"name", workitem.Name,
		)
		return ctrl.Result{}, err
	}

	if result.Phase == wiPhaseCompleted {
		log.Info("Workitem completed",
			"name", workitem.Name,
			"lastNode", previousAssignee,
		)
		r.publishAudit("audit.workitem.completed", workitem.Name, map[string]string{
			"action":    "completed",
			"last_node": previousAssignee,
		})
		r.publishLifecycle(workitem, wiPhaseCompleted, previousAssignee)
	} else {
		log.Info("Moving Workitem",
			"name", workitem.Name,
			"from", previousAssignee,
			"to", result.NextAssignee,
		)
		r.publishAudit("audit.workitem.routed", workitem.Name, map[string]string{
			"action": "routed",
			"from":   previousAssignee,
			"to":     result.NextAssignee,
		})
		r.publishLifecycle(workitem, wiPhasePending, result.NextAssignee)
	}

	return ctrl.Result{}, nil
}

// reconcileSuspended handles Workitems in the Suspended phase.
// It enforces two resume triggers:
//  1. Timeout: if suspendedAt + resumeTimeout has elapsed, fail with SUSPEND_TIMEOUT_EXCEEDED.
//  2. CEL condition: if resumeCondition is set, evaluate it against child workitem states.
//     If the condition evaluates to true, transition to Pending (resume on same node).
//
// If neither trigger fires, the reconciler requeues for periodic re-evaluation.
func (r *WorkitemReconciler) reconcileSuspended(ctx context.Context, workitem *flowv1.Workitem) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// --- 1. Timeout enforcement ---
	if workitem.Status.SuspendedAt != nil && workitem.Status.ResumeTimeout != "" {
		timeout, parseErr := time.ParseDuration(workitem.Status.ResumeTimeout)
		if parseErr != nil {
			log.Error(parseErr, "Invalid resume timeout, failing Workitem",
				"name", workitem.Name,
				"resumeTimeout", workitem.Status.ResumeTimeout,
			)
			r.publishAudit("audit.workitem.failed", workitem.Name, map[string]string{
				"action": "failed",
				"reason": "SUSPEND_TIMEOUT_EXCEEDED",
			})
			r.publishLifecycle(workitem, phaseFailed, workitem.Status.CurrentAssignee)
			return r.failWorkitem(ctx, workitem, "SUSPEND_TIMEOUT_EXCEEDED")
		}

		elapsed := nowFunc().Sub(workitem.Status.SuspendedAt.Time)
		if elapsed >= timeout {
			log.Info("Suspend timeout exceeded, failing Workitem",
				"name", workitem.Name,
				"elapsed", elapsed,
				"timeout", timeout,
			)
			r.publishAudit("audit.workitem.failed", workitem.Name, map[string]string{
				"action": "failed",
				"reason": "SUSPEND_TIMEOUT_EXCEEDED",
			})
			r.publishLifecycle(workitem, phaseFailed, workitem.Status.CurrentAssignee)
			return r.failWorkitem(ctx, workitem, "SUSPEND_TIMEOUT_EXCEEDED")
		}
	}

	// --- 2. Child completion check ---
	// A suspended workitem with a resume condition resumes when all its
	// children reach a terminal phase (Completed or Failed).
	if workitem.Status.ResumeCondition != "" {
		hasActive, checkErr := r.hasNonTerminalChildren(ctx, workitem.Namespace, workitem.Name)
		if checkErr != nil {
			log.Error(checkErr, "Failed to check children for resume",
				"name", workitem.Name,
			)
			return ctrl.Result{RequeueAfter: suspendCheckInterval}, nil
		}
		if !hasActive {
			log.Info("All children terminal, resuming Workitem",
				"name", workitem.Name,
				"assignee", workitem.Status.CurrentAssignee,
			)
			return r.resumeWorkitem(ctx, workitem)
		}
	}

	// --- 3. Requeue for periodic re-evaluation ---
	// If a timeout is set, requeue at the earlier of suspendCheckInterval
	// or the remaining timeout. Otherwise just use the check interval.
	requeue := suspendCheckInterval
	if workitem.Status.SuspendedAt != nil && workitem.Status.ResumeTimeout != "" {
		if timeout, parseErr := time.ParseDuration(workitem.Status.ResumeTimeout); parseErr == nil {
			remaining := timeout - nowFunc().Sub(workitem.Status.SuspendedAt.Time)
			if remaining > 0 && remaining < requeue {
				requeue = remaining
			}
		}
	}

	return ctrl.Result{RequeueAfter: requeue}, nil
}

// resumeWorkitem transitions a Suspended workitem back to Pending for
// re-dispatch to the same node type. Clears suspend-related fields.
func (r *WorkitemReconciler) resumeWorkitem(ctx context.Context, workitem *flowv1.Workitem) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Re-fetch for latest resourceVersion.
	var fresh flowv1.Workitem
	if err := r.Get(ctx, client.ObjectKeyFromObject(workitem), &fresh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Only resume if still Suspended.
	if fresh.Status.Phase != wiPhaseSuspended {
		return ctrl.Result{}, nil
	}

	assignee := fresh.Status.CurrentAssignee
	fresh.Status.Phase = wiPhasePending
	fresh.Status.SuspendedAt = nil
	fresh.Status.ResumeCondition = ""
	fresh.Status.ResumeTimeout = ""

	if err := r.Status().Update(ctx, &fresh); err != nil {
		log.Error(err, "Failed to resume Workitem",
			"name", workitem.Name,
		)
		return ctrl.Result{}, err
	}

	log.Info("Workitem resumed",
		"name", workitem.Name,
		"assignee", assignee,
	)
	r.publishAudit("audit.workitem.resumed", workitem.Name, map[string]string{
		"action":   "resumed",
		"assignee": assignee,
	})
	r.publishLifecycle(workitem, wiPhasePending, assignee)

	return ctrl.Result{}, nil
}

// failWorkitem transitions a Workitem to the Failed phase with a structured
// failure reason.
func (r *WorkitemReconciler) failWorkitem(ctx context.Context, workitem *flowv1.Workitem, reason string) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Re-fetch for latest resourceVersion.
	var fresh flowv1.Workitem
	if err := r.Get(ctx, client.ObjectKeyFromObject(workitem), &fresh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Only transition if not already terminal.
	if fresh.Status.Phase == wiPhaseCompleted || fresh.Status.Phase == phaseFailed {
		return ctrl.Result{}, nil
	}

	fresh.Status.Phase = phaseFailed
	fresh.Status.FailureReason = reason
	fresh.Status.RoutingInstruction = nil
	fresh.Status.AssignedAt = nil

	if err := r.Status().Update(ctx, &fresh); err != nil {
		log.Error(err, "Failed to transition Workitem to Failed",
			"name", workitem.Name,
			"reason", reason,
		)
		return ctrl.Result{}, err
	}

	log.Info("Workitem failed",
		"name", workitem.Name,
		"reason", reason,
	)

	return ctrl.Result{}, nil
}

// hasNonTerminalChildren checks whether the given Workitem has any child
// Workitems that are still in a non-terminal phase (i.e. not Completed or
// Failed). This is used by reconcileRunning to skip timeout enforcement
// for parents that are waiting on children.
func (r *WorkitemReconciler) hasNonTerminalChildren(ctx context.Context, namespace, parentName string) (bool, error) {
	var childList flowv1.WorkitemList
	if err := r.List(ctx, &childList,
		client.InNamespace(namespace),
		client.MatchingLabels{"flow.gideas.io/parent": parentName},
	); err != nil {
		return false, fmt.Errorf("failed to list child workitems: %w", err)
	}

	// No children at all — not a fan-out parent.
	if len(childList.Items) == 0 {
		return false, nil
	}

	for i := range childList.Items {
		phase := childList.Items[i].Status.Phase
		if phase != wiPhaseCompleted && phase != phaseFailed {
			return true, nil
		}
	}
	return false, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkitemReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&flowv1.Workitem{}).
		Named("workitem").
		WithOptions(controller.Options{MaxConcurrentReconciles: 10}).
		Complete(r)
}
