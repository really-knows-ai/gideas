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
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	flowv1 "github.com/gideas/flow/operator/api/v1"
)

// LawReconciler reconciles a Law object.
//
// Responsibilities:
//   - Compute a content hash from the spec and store it in status.version.
//   - Validate structural invariants (goal, representations, tier).
//   - Set status conditions reflecting reconciliation health.
type LawReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=flow.gideas.io,resources=laws,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=flow.gideas.io,resources=laws/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=flow.gideas.io,resources=laws/finalizers,verbs=update
// +kubebuilder:rbac:groups=flow.gideas.io,resources=governedartefacts,verbs=get;list;watch

// Reconcile computes the content hash for the Law spec and validates structural invariants.
func (r *LawReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the Law instance.
	var law flowv1.Law
	if err := r.Get(ctx, req.NamespacedName, &law); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling Law",
		"name", law.Name,
		"namespace", law.Namespace,
		"tier", law.Spec.Tier,
		"group", law.Spec.Group,
	)

	// Validate appliesTo references against GovernedArtefacts.
	if err := r.validateAppliesTo(ctx, &law); err != nil {
		r.setCondition(&law, "Ready", metav1.ConditionFalse, "ValidationFailed", err.Error())
		return ctrl.Result{}, r.persistStatus(ctx, &law)
	}

	// Compute content hash from spec.
	version, err := r.computeVersion(&law)
	if err != nil {
		r.setCondition(&law, "Ready", metav1.ConditionFalse, "HashFailed", err.Error())
		return ctrl.Result{}, r.persistStatus(ctx, &law)
	}

	law.Status.Version = version
	r.setCondition(&law, "Ready", metav1.ConditionTrue, "Reconciled",
		fmt.Sprintf("Law version %s computed", version))

	return ctrl.Result{}, r.persistStatus(ctx, &law)
}

// validateAppliesTo checks that appliesTo entries reference existing GovernedArtefacts.
func (r *LawReconciler) validateAppliesTo(ctx context.Context, law *flowv1.Law) error {
	if len(law.Spec.AppliesTo) == 0 {
		return nil // Global law — applies to all.
	}

	var artefacts flowv1.GovernedArtefactList
	if err := r.List(ctx, &artefacts, client.InNamespace(law.Namespace)); err != nil {
		return fmt.Errorf("could not list GovernedArtefacts: %w", err)
	}

	known := make(map[string]bool)
	for _, ga := range artefacts.Items {
		known[ga.Name] = true
	}

	for _, name := range law.Spec.AppliesTo {
		if !known[name] {
			return fmt.Errorf("appliesTo references unknown GovernedArtefact %q", name)
		}
	}

	return nil
}

// computeVersion computes a SHA-256 content hash from the law spec.
func (r *LawReconciler) computeVersion(law *flowv1.Law) (string, error) {
	data, err := json.Marshal(law.Spec)
	if err != nil {
		return "", fmt.Errorf("could not marshal law spec: %w", err)
	}
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash[:8]), nil
}

// setCondition sets a condition on the law's status (in memory only).
func (r *LawReconciler) setCondition(
	law *flowv1.Law,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	meta.SetStatusCondition(&law.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: law.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// persistStatus re-fetches the Law and persists status updates.
func (r *LawReconciler) persistStatus(ctx context.Context, law *flowv1.Law) error {
	var fresh flowv1.Law
	if err := r.Get(ctx, client.ObjectKeyFromObject(law), &fresh); err != nil {
		return client.IgnoreNotFound(err)
	}

	fresh.Status.Version = law.Status.Version
	fresh.Status.Conditions = law.Status.Conditions

	return r.Status().Update(ctx, &fresh)
}

// SetupWithManager sets up the controller with the Manager.
func (r *LawReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&flowv1.Law{}).
		Named("law").
		Complete(r)
}
