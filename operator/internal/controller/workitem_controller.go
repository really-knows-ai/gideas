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

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// The Workitem reconciler observes state changes made by the gRPC server
// (e.g. SubmitResult updating routingInstruction and phase) and executes
// the routing decision: reading the current FoundryNode's outputs to
// determine the next destination.
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

	// Only act on Workitems in the Routing phase.
	if workitem.Status.Phase != "Routing" {
		return ctrl.Result{}, nil
	}

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

	// Apply the transition.
	workitem.Status.Phase = result.Phase
	workitem.Status.CurrentAssignee = result.NextAssignee
	workitem.Status.RoutingInstruction = nil // Clear to prevent re-processing.

	// Persist the status update.
	if err := r.Status().Update(ctx, &workitem); err != nil {
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
