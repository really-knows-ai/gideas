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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	flowv1 "github.com/gideas/flow/operator/api/v1"
)

// GovernedArtefactReconciler reconciles a GovernedArtefact object.
//
// Responsibilities:
//   - Register the stamp vocabulary declared by the GovernedArtefact.
//   - Set status conditions reflecting reconciliation health.
type GovernedArtefactReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=flow.gideas.io,resources=governedartefacts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=flow.gideas.io,resources=governedartefacts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=flow.gideas.io,resources=governedartefacts/finalizers,verbs=update

// Reconcile validates the GovernedArtefact and sets a Ready condition.
func (r *GovernedArtefactReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the GovernedArtefact instance.
	var ga flowv1.GovernedArtefact
	if err := r.Get(ctx, req.NamespacedName, &ga); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling GovernedArtefact",
		"name", ga.Name,
		"namespace", ga.Namespace,
		"stamps", len(ga.Spec.Stamps),
	)

	// Check for duplicate stamps in the vocabulary.
	seen := make(map[string]bool)
	for _, stamp := range ga.Spec.Stamps {
		if seen[stamp] {
			return r.setCondition(ctx, &ga, metav1.ConditionFalse,
				"DuplicateStamp", "Duplicate stamp name in vocabulary: "+stamp)
		}
		seen[stamp] = true
	}

	return r.setCondition(ctx, &ga, metav1.ConditionTrue,
		"Reconciled", "GovernedArtefact stamp vocabulary registered")
}

// setCondition updates the Ready status condition on the GovernedArtefact and persists it.
func (r *GovernedArtefactReconciler) setCondition(
	ctx context.Context,
	ga *flowv1.GovernedArtefact,
	status metav1.ConditionStatus,
	reason, message string,
) (ctrl.Result, error) {
	newCondition := metav1.Condition{
		Type:               conditionReady,
		Status:             status,
		ObservedGeneration: ga.Generation,
		Reason:             reason,
		Message:            message,
	}

	existing := meta.FindStatusCondition(ga.Status.Conditions, conditionReady)
	if existing != nil &&
		existing.Status == status &&
		existing.Reason == reason &&
		existing.Message == message &&
		existing.ObservedGeneration == ga.Generation {
		return ctrl.Result{}, nil
	}

	// Re-fetch to get latest resourceVersion.
	var fresh flowv1.GovernedArtefact
	if err := r.Get(ctx, client.ObjectKeyFromObject(ga), &fresh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	meta.SetStatusCondition(&fresh.Status.Conditions, newCondition)

	if err := r.Status().Update(ctx, &fresh); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GovernedArtefactReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&flowv1.GovernedArtefact{}).
		Named("governedartefact").
		Complete(r)
}
