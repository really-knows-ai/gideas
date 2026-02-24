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
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	flowv1gen "github.com/gideas/flow/gen/flow/v1"
	flowv1 "github.com/gideas/flow/operator/api/v1"
	"github.com/gideas/flow/operator/internal/controller/dispatcher"
	"github.com/gideas/flow/operator/internal/controller/scheduler"
	"github.com/gideas/flow/pkg/eventbus"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Workitem lifecycle phase constants.
const (
	wiPhasePending   = "Pending"
	wiPhaseRunning   = "Running"
	wiPhaseRouting   = "Routing"
	wiPhaseCompleted = "Completed"
	// wiPhaseFailed uses the package-level phaseFailed from foundrynode_controller.go.
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
	ArtefactQuerier scheduler.ArtefactQuerier

	// Auditor publishes lifecycle events to the Event Bus via async submit.
	// nil-safe: audit publishing degrades gracefully.
	Auditor *eventbus.AsyncPublisher
}

// publishAudit submits an audit event for a workitem lifecycle transition via
// the async publisher. If the publisher is nil, audit publishing is silently
// disabled.
func (r *WorkitemReconciler) publishAudit(_ context.Context, eventType string, workitemName string, attrs map[string]string) {
	if r.Auditor == nil {
		return
	}
	r.Auditor.Submit(&flowv1gen.PublishRequest{
		Channel: "audit",
		Event: &flowv1gen.FlowEvent{
			EventId:    newWIAuditID(),
			EventType:  eventType,
			WorkitemId: workitemName,
			Timestamp:  timestamppb.Now(),
			Attributes: attrs,
		},
	})
}

// newWIAuditID returns a random hex-encoded identifier for audit events.
func newWIAuditID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return fmt.Sprintf("%x", b)
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
	default:
		return ctrl.Result{}, nil
	}
}

// resolveFlow fetches the FoundryFlow CRD that owns this Workitem.
// It looks up the flow name from the Workitem's flow label, falling back
// to listing flows in the namespace.
func (r *WorkitemReconciler) resolveFlow(ctx context.Context, workitem *flowv1.Workitem) (*flowv1.FoundryFlow, error) {
	// Try the label first (set by CreateWorkitem/ImportWorkitem).
	flowName := ""
	if workitem.Labels != nil {
		flowName = workitem.Labels["flow.gideas.io/flow"]
	}

	if flowName != "" {
		var flow flowv1.FoundryFlow
		key := types.NamespacedName{Namespace: workitem.Namespace, Name: flowName}
		if err := r.Get(ctx, key, &flow); err != nil {
			return nil, fmt.Errorf("failed to fetch FoundryFlow %q: %w", flowName, err)
		}
		return &flow, nil
	}

	// Fallback: find the first flow in the namespace.
	var flowList flowv1.FoundryFlowList
	if err := r.List(ctx, &flowList, client.InNamespace(workitem.Namespace)); err != nil {
		return nil, fmt.Errorf("failed to list FoundryFlows: %w", err)
	}
	if len(flowList.Items) == 0 {
		return nil, fmt.Errorf("no FoundryFlow found in namespace %q", workitem.Namespace)
	}
	return &flowList.Items[0], nil
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
		r.publishAudit(ctx, "audit.workitem.failed", workitem.Name, map[string]string{
			"action": "failed",
			"reason": "THRASH_BUDGET_EXCEEDED",
		})
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
		workitem.Namespace, // flow_id placeholder
		workitem.Name,      // workitem_id
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

	r.publishAudit(ctx, "audit.workitem.running", workitem.Name, map[string]string{
		"action":   "running",
		"assignee": assignee,
	})

	// Requeue after the timeout period to check for inactivity timeout.
	var node flowv1.FoundryNode
	nodeKey := types.NamespacedName{Namespace: workitem.Namespace, Name: assignee}
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
	nodeKey := types.NamespacedName{Namespace: req.Namespace, Name: workitem.Status.CurrentAssignee}
	timeout := flow.Spec.GovernancePolicy.DefaultTimeout.Duration
	if getErr := r.Get(ctx, nodeKey, &node); getErr == nil {
		timeout = resolveTimeout(&node, flow)
	}

	// If no timeout configured (zero duration), skip timeout enforcement.
	if timeout <= 0 {
		return ctrl.Result{}, nil
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
			r.publishAudit(ctx, "audit.workitem.failed", workitem.Name, map[string]string{
				"action": "failed",
				"reason": "TIMEOUT_EXCEEDED",
			})
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
				r.publishAudit(ctx, "audit.workitem.failed", workitem.Name, map[string]string{
					"action": "failed",
					"reason": guardErr.Code,
				})
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
	fresh.Status.Phase = result.Phase
	fresh.Status.CurrentAssignee = result.NextAssignee
	fresh.Status.RoutingInstruction = nil // Clear to prevent re-processing.
	fresh.Status.AssignedAt = nil         // Clear for next assignment.

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
		r.publishAudit(ctx, "audit.workitem.completed", workitem.Name, map[string]string{
			"action":    "completed",
			"last_node": previousAssignee,
		})
	} else {
		log.Info("Moving Workitem",
			"name", workitem.Name,
			"from", previousAssignee,
			"to", result.NextAssignee,
		)
		r.publishAudit(ctx, "audit.workitem.routed", workitem.Name, map[string]string{
			"action": "routed",
			"from":   previousAssignee,
			"to":     result.NextAssignee,
		})
	}

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

// SetupWithManager sets up the controller with the Manager.
func (r *WorkitemReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&flowv1.Workitem{}).
		Named("workitem").
		Complete(r)
}
