// Package controller implements Kubebuilder controllers for Federation CRDs.
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
	"sigs.k8s.io/controller-runtime/pkg/log"

	federationv1 "github.com/gideas/flow/federation/api/v1"
)

// validPublisherLevels is the set of accepted Level values for PublisherRoleSpec.
var validPublisherLevels = map[string]bool{
	"state":      true,
	"federation": true,
}

// FederationMemberReconciler reconciles a FederationMember object.
// It validates the spec, sets a Ready condition, and records the JoinedAt
// timestamp on first reconcile.
type FederationMemberReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=federation.gideas.io,resources=federationmembers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=federation.gideas.io,resources=federationmembers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=federation.gideas.io,resources=federationstates,verbs=get;list;watch

// Reconcile validates the FederationMember spec, sets the Ready condition,
// and records the JoinedAt timestamp.
func (r *FederationMemberReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var member federationv1.FederationMember
	if err := r.Get(ctx, req.NamespacedName, &member); err != nil {
		// Deleted -- nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Validate the spec.
	if reason := validateMemberSpec(&member.Spec); reason != "" {
		logger.Info("FederationMember spec invalid", "name", member.Name, "reason", reason)
		return ctrl.Result{}, r.setReadyCondition(ctx, &member, metav1.ConditionFalse, "InvalidSpec", reason)
	}

	// Set JoinedAt on first reconcile.
	if member.Status.JoinedAt == nil {
		now := metav1.NewTime(time.Now())
		member.Status.JoinedAt = &now
	}

	logger.Info("FederationMember valid", "name", member.Name)
	return ctrl.Result{}, r.setReadyCondition(ctx, &member, metav1.ConditionTrue, "Valid", "spec is valid")
}

// validateMemberSpec returns a non-empty reason string if the spec is invalid.
func validateMemberSpec(spec *federationv1.FederationMemberSpec) string {
	if spec.FlowIdentity == "" {
		return "flowIdentity is required"
	}
	if spec.EmbassyEndpoint == "" {
		return "embassyEndpoint is required"
	}
	for i, role := range spec.PublisherRoles {
		if role.Scope == "" {
			return fmt.Sprintf("publisherRoles[%d].scope is required", i)
		}
		if !validPublisherLevels[role.Level] {
			return fmt.Sprintf("publisherRoles[%d].level %q is invalid; must be \"state\" or \"federation\"", i, role.Level)
		}
	}
	return ""
}

// setReadyCondition updates the Ready condition on the member's status
// and writes it back via the status subresource.
func (r *FederationMemberReconciler) setReadyCondition(
	ctx context.Context,
	member *federationv1.FederationMember,
	status metav1.ConditionStatus,
	reason, message string,
) error {
	meta.SetStatusCondition(&member.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		ObservedGeneration: member.Generation,
		Reason:             reason,
		Message:            message,
	})
	return r.Status().Update(ctx, member)
}

// SetupWithManager registers the FederationMemberReconciler with the manager.
func (r *FederationMemberReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&federationv1.FederationMember{}).
		Named("federationmember").
		Complete(r)
}
