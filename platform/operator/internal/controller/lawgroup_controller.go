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
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	flowv1gen "github.com/gideas/flow/gen/flow/v1"
	flowv1 "github.com/gideas/flow/operator/api/v1"
)

// LawGroupReconciler reconciles a LawGroup object by syncing it to the
// Librarian via gRPC.
type LawGroupReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Librarian flowv1gen.LibrarianServiceClient
}

// +kubebuilder:rbac:groups=flow.gideas.io,resources=lawgroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=flow.gideas.io,resources=lawgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=flow.gideas.io,resources=lawgroups/finalizers,verbs=update

// Reconcile syncs the LawGroup CRD into the Librarian.
// On deletion: calls DeleteLawGroup on the Librarian.
// On create/update: calls SyncLawGroup on the Librarian.
// Sets Ready condition based on success or failure.
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

	if r.Librarian == nil {
		err := fmt.Errorf("librarian client not configured")
		r.setCondition(&lg, metav1.ConditionFalse, "LibrarianUnavailable", err.Error())
		return ctrl.Result{RequeueAfter: time.Second}, r.persistStatus(ctx, &lg)
	}

	// Deletion: sync removal to Librarian.
	if !lg.DeletionTimestamp.IsZero() {
		if _, err := r.Librarian.DeleteLawGroup(ctx, &flowv1gen.DeleteLawGroupRequest{
			GroupName: lg.Name,
		}); err != nil {
			log.Error(err, "Failed to delete LawGroup from Librarian", "name", lg.Name)
			r.setCondition(&lg, metav1.ConditionFalse, "DeleteFailed", err.Error())
			return ctrl.Result{RequeueAfter: time.Second}, r.persistStatus(ctx, &lg)
		}
		log.Info("LawGroup deleted from Librarian", "name", lg.Name)
		return ctrl.Result{}, nil
	}

	// Create/update: sync to Librarian.
	reqProto := &flowv1gen.SyncLawGroupRequest{
		Group: &flowv1gen.LawGroup{
			Name:   lg.Name,
			Mode:   lg.Spec.Mode,
			Passes: lg.Spec.Passes,
		},
	}
	if _, err := r.Librarian.SyncLawGroup(ctx, reqProto); err != nil {
		log.Error(err, "Failed to sync LawGroup to Librarian", "name", lg.Name)
		r.setCondition(&lg, metav1.ConditionFalse, "SyncFailed", err.Error())
		return ctrl.Result{RequeueAfter: time.Second}, r.persistStatus(ctx, &lg)
	}

	r.setCondition(&lg, metav1.ConditionTrue, "Synced",
		fmt.Sprintf("LawGroup %s synced (mode=%s, passes=%d)", lg.Name, lg.Spec.Mode, lg.Spec.Passes))

	return ctrl.Result{}, r.persistStatus(ctx, &lg)
}

// setCondition sets a Ready condition on the LawGroup's status (in memory only).
func (r *LawGroupReconciler) setCondition(
	lg *flowv1.LawGroup,
	status metav1.ConditionStatus,
	reason, message string,
) {
	meta.SetStatusCondition(&lg.Status.Conditions, metav1.Condition{
		Type:               "Ready",
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
