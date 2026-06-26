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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	flowv1 "github.com/gideas/flow/operator/api/v1"
)

// LawGroupReconciler reconciles a LawGroup object.
//
// Stub implementation: logs reconciliation and sets a Ready condition.
// Full sync-to-Librarian logic is added in Phase 5.
type LawGroupReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=flow.gideas.io,resources=lawgroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=flow.gideas.io,resources=lawgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=flow.gideas.io,resources=lawgroups/finalizers,verbs=update

// Reconcile logs the LawGroup and sets a Ready condition.
// This is a stub — full sync logic (including Librarian gRPC sync)
// is implemented in Phase 5.
func (r *LawGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var lg flowv1.LawGroup
	if err := r.Get(ctx, req.NamespacedName, &lg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling LawGroup",
		"name", lg.Name,
		"namespace", lg.Namespace,
		"mode", lg.Spec.Mode,
		"passes", lg.Spec.Passes,
	)

	// ponytail: Stub — sets Ready without validating mode/passes against
	// any external system. Phase 5 adds gRPC sync and validation.
	r.setCondition(&lg, "Ready", metav1.ConditionTrue, "Reconciled",
		fmt.Sprintf("LawGroup %s reconciled (mode=%s, passes=%d)", lg.Name, lg.Spec.Mode, lg.Spec.Passes))

	return ctrl.Result{}, r.persistStatus(ctx, &lg)
}

// setCondition sets a condition on the LawGroup's status (in memory only).
func (r *LawGroupReconciler) setCondition(
	lg *flowv1.LawGroup,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	meta.SetStatusCondition(&lg.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: lg.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// persistStatus re-fetches the LawGroup and persists status updates.
func (r *LawGroupReconciler) persistStatus(ctx context.Context, lg *flowv1.LawGroup) error {
	var fresh flowv1.LawGroup
	if err := r.Get(ctx, client.ObjectKeyFromObject(lg), &fresh); err != nil {
		return client.IgnoreNotFound(err)
	}

	fresh.Status.Conditions = lg.Status.Conditions

	return r.Status().Update(ctx, &fresh)
}

// SetupWithManager sets up the controller with the Manager.
func (r *LawGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&flowv1.LawGroup{}).
		Named("lawgroup").
		Complete(r)
}
