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
	"crypto/x509"
	"encoding/pem"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	flowv1 "github.com/gideas/flow/operator/api/v1"
)

// TreatyReconciler reconciles a Treaty object.
//
// Responsibilities:
//   - Validate trust material (PEM-encoded CA certificate).
//   - Validate directionality (import or export).
//   - Set status conditions reflecting validation health.
type TreatyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=flow.gideas.io,resources=treaties,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=flow.gideas.io,resources=treaties/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=flow.gideas.io,resources=treaties/finalizers,verbs=update

// Reconcile validates the Treaty trust material and directionality,
// then updates the status conditions accordingly.
func (r *TreatyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the Treaty instance.
	var treaty flowv1.Treaty
	if err := r.Get(ctx, req.NamespacedName, &treaty); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling Treaty",
		"name", treaty.Name,
		"namespace", treaty.Namespace,
		"remote", treaty.Spec.RemoteName,
		"direction", treaty.Spec.Direction,
	)

	// Validate the PEM-encoded CA certificate.
	if err := r.validateCACert(&treaty); err != nil {
		return SetStatusCondition(ctx, r.Client, &treaty, conditionReady, metav1.ConditionFalse,
			"CACertInvalid", err.Error(),
			func(t *flowv1.Treaty) *[]metav1.Condition { return &t.Status.Conditions },
		)
	}

	// Validate allowed import types against the receiving Flow's published types.
	if err := r.validateAllowedImportTypes(ctx, &treaty); err != nil {
		return SetStatusCondition(ctx, r.Client, &treaty, conditionReady, metav1.ConditionFalse,
			"AllowedImportTypesInvalid", err.Error(),
			func(t *flowv1.Treaty) *[]metav1.Condition { return &t.Status.Conditions },
		)
	}

	// All validations passed.
	return SetStatusCondition(ctx, r.Client, &treaty, conditionReady, metav1.ConditionTrue,
		"Reconciled", fmt.Sprintf("Treaty with remote %q (%s) validated", treaty.Spec.RemoteName, treaty.Spec.Direction),
		func(t *flowv1.Treaty) *[]metav1.Condition { return &t.Status.Conditions },
	)
}

// validateCACert parses and validates the PEM-encoded CA certificate.
func (r *TreatyReconciler) validateCACert(treaty *flowv1.Treaty) error {
	block, _ := pem.Decode([]byte(treaty.Spec.CACert))
	if block == nil {
		return fmt.Errorf("caCert is not valid PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("caCert could not be parsed as X.509: %w", err)
	}

	if !cert.IsCA {
		return fmt.Errorf("caCert is not a CA certificate")
	}

	return nil
}

// validateAllowedImportTypes ensures every Treaty allowedImportTypes entry is
// published by the receiving namespace's singleton FoundryFlow.
func (r *TreatyReconciler) validateAllowedImportTypes(ctx context.Context, treaty *flowv1.Treaty) error {
	if len(treaty.Spec.AllowedImportTypes) == 0 {
		return nil
	}

	var flows flowv1.FoundryFlowList
	if err := r.List(ctx, &flows, client.InNamespace(treaty.Namespace)); err != nil {
		return fmt.Errorf("could not list FoundryFlows: %w", err)
	}

	if len(flows.Items) != 1 {
		return fmt.Errorf("namespace %q must contain exactly 1 FoundryFlow to validate allowedImportTypes, found %d", treaty.Namespace, len(flows.Items))
	}

	effectiveImportTypes := effectiveImportTypes(&flows.Items[0])

	for _, importType := range treaty.Spec.AllowedImportTypes {
		if _, ok := effectiveImportTypes[importType]; !ok {
			return fmt.Errorf("allowedImportTypes value %q is not published by FoundryFlow %q", importType, flows.Items[0].Name)
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TreatyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&flowv1.Treaty{}).
		Named("treaty").
		Complete(r)
}
