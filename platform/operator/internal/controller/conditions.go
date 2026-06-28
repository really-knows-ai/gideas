package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SetStatusCondition sets a condition on the object and persists it via status
// update. Uses a callback to access the type-specific Conditions slice since
// each CRD struct differs.
//
// ponytail: always re-fetches and writes even when condition is unchanged.
// The per-controller copies had inconsistent early-exit optimisations that
// duplicated meta.SetStatusCondition's own dedup logic. Trading a GET+PUT
// per reconciliation for removing that duplication is the right call.
func SetStatusCondition[T client.Object](
	ctx context.Context,
	c client.Client,
	obj T,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
	getConditions func(T) *[]metav1.Condition,
) (ctrl.Result, error) {
	// Re-fetch to get the latest resourceVersion before updating status.
	if err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	meta.SetStatusCondition(getConditions(obj), metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: obj.GetGeneration(),
		Reason:             reason,
		Message:            message,
	})

	if err := c.Status().Update(ctx, obj); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	return ctrl.Result{}, nil
}
