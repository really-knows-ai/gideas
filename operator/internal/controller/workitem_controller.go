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
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	flowv1 "github.com/gideas/flow/operator/api/v1"
	"github.com/gideas/flow/operator/internal/controller/dispatcher"
	"github.com/gideas/flow/operator/internal/controller/scheduler"
)

// WorkitemReconciler reconciles a Workitem object
type WorkitemReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=flow.gideas.io,resources=workitems,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=flow.gideas.io,resources=workitems/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=flow.gideas.io,resources=workitems/finalizers,verbs=update
// +kubebuilder:rbac:groups=flow.gideas.io,resources=foundrynodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// The Workitem reconciler handles two phases:
//
//  1. Pending — The Workitem has a currentAssignee but no Pod is processing it.
//     The reconciler uses the Dispatcher to discover a ready Pod and push the
//     assignment via gRPC, then transitions to Running.
//
//  2. Routing — The gRPC server has written a routingInstruction and set the
//     phase to Routing. The reconciler executes the scheduling decision to
//     determine the next destination.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
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
	case "Pending":
		return r.reconcilePending(ctx, &workitem)
	case "Routing":
		return r.reconcileRouting(ctx, req, &workitem)
	default:
		return ctrl.Result{}, nil
	}
}

// reconcilePending handles Workitems in the Pending phase.
// It uses the Dispatcher to discover a ready Pod and push the assignment.
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

	log.Info("Dispatching Workitem",
		"name", workitem.Name,
		"assignee", assignee,
	)

	// Claim the workitem by transitioning to Running BEFORE dispatching.
	// This prevents duplicate dispatches: if two reconciles race, only one
	// will succeed at this status update.
	workitem.Status.Phase = "Running"
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

	_, err := d.Assign(
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
			if fresh.Status.Phase == "Running" {
				fresh.Status.Phase = "Pending"
				_ = r.Status().Update(ctx, &fresh)
			}
		}
		return ctrl.Result{}, err
	}

	log.Info("Assigned Workitem successfully",
		"name", workitem.Name,
		"assignee", assignee,
	)

	return ctrl.Result{}, nil
}

// reconcileRouting handles Workitems in the Routing phase.
// It executes the scheduling decision based on the routing instruction.
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

	// Execute the scheduling decision.
	sched := scheduler.New(r.Client, req.Namespace)
	result, err := sched.CalculateNextStep(
		ctx,
		workitem.Status.CurrentAssignee,
		*workitem.Status.RoutingInstruction,
	)
	if err != nil {
		log.Error(err, "Failed to calculate next step",
			"name", workitem.Name,
			"assignee", workitem.Status.CurrentAssignee,
		)
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
	if fresh.Status.Phase != "Routing" {
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

	// Persist the status update.
	if err := r.Status().Update(ctx, &fresh); err != nil {
		log.Error(err, "Failed to update Workitem status",
			"name", workitem.Name,
		)
		return ctrl.Result{}, err
	}

	if result.Phase == "Completed" {
		log.Info("Workitem completed",
			"name", workitem.Name,
			"lastNode", previousAssignee,
		)
	} else {
		log.Info("Moving Workitem",
			"name", workitem.Name,
			"from", previousAssignee,
			"to", result.NextAssignee,
		)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkitemReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&flowv1.Workitem{}).
		Named("workitem").
		Complete(r)
}
