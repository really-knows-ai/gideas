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

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	flowv1 "github.com/gideas/flow/operator/api/v1"
)

// FoundryFlowReconciler reconciles a FoundryFlow object.
//
// Responsibilities:
//   - Validate configuration completeness (contracts, governance policy, cross-flow settings).
//   - Validate referential integrity (routing targets, importNode, stamp vocabulary).
//   - Manage phase status (Initialising -> Ready / Degraded / Failed).
//   - Set status conditions reflecting reconciliation health.
type FoundryFlowReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=flow.gideas.io,resources=foundryflows,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=flow.gideas.io,resources=foundryflows/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=flow.gideas.io,resources=foundryflows/finalizers,verbs=update
// +kubebuilder:rbac:groups=flow.gideas.io,resources=foundrynodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=flow.gideas.io,resources=governedartefacts,verbs=get;list;watch

// Reconcile validates the FoundryFlow configuration, checks referential integrity,
// and transitions the Flow phase to Ready when all constraints are met.
func (r *FoundryFlowReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the FoundryFlow instance.
	var flow flowv1.FoundryFlow
	if err := r.Get(ctx, req.NamespacedName, &flow); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling FoundryFlow",
		"name", flow.Name,
		"namespace", flow.Namespace,
		"phase", flow.Status.Phase,
	)

	// Set initial phase if empty.
	if flow.Status.Phase == "" {
		flow.Status.Phase = phaseInitialising
	}

	// Validate governance policy internal consistency.
	if err := r.validateGovernancePolicy(&flow); err != nil {
		return r.setPhaseAndCondition(ctx, &flow, phaseFailed,
			metav1.ConditionFalse, "GovernancePolicyInvalid", err.Error())
	}

	// Validate contract references against GovernedArtefact stamp vocabularies.
	if err := r.validateContracts(ctx, &flow); err != nil {
		return r.setPhaseAndCondition(ctx, &flow, phaseDegraded,
			metav1.ConditionFalse, "ContractValidationFailed", err.Error())
	}

	// Validate importNode reference.
	if err := r.validateImportNode(ctx, &flow); err != nil {
		return r.setPhaseAndCondition(ctx, &flow, phaseFailed,
			metav1.ConditionFalse, "ImportNodeInvalid", err.Error())
	}

	// Validate that all nodes' routing outputs target existing nodes.
	if err := r.validateNodeTopology(ctx, &flow); err != nil {
		return r.setPhaseAndCondition(ctx, &flow, phaseDegraded,
			metav1.ConditionFalse, "TopologyValidationFailed", err.Error())
	}

	// All validations passed — transition to Ready.
	return r.setPhaseAndCondition(ctx, &flow, phaseReady,
		metav1.ConditionTrue, "Reconciled", "Flow configuration is valid and reconciled")
}

// validateGovernancePolicy checks governance policy internal consistency.
func (r *FoundryFlowReconciler) validateGovernancePolicy(flow *flowv1.FoundryFlow) error {
	policy := flow.Spec.GovernancePolicy

	if policy.MaxTimeout.Duration < policy.DefaultTimeout.Duration {
		return fmt.Errorf("maxTimeout (%v) must be >= defaultTimeout (%v)",
			policy.MaxTimeout.Duration, policy.DefaultTimeout.Duration)
	}

	return nil
}

// validateContracts checks that contract stamp references exist in GovernedArtefact vocabularies.
func (r *FoundryFlowReconciler) validateContracts(ctx context.Context, flow *flowv1.FoundryFlow) error {
	// Build a map of GovernedArtefact name -> stamp vocabulary.
	var artefacts flowv1.GovernedArtefactList
	if err := r.List(ctx, &artefacts, client.InNamespace(flow.Namespace)); err != nil {
		return fmt.Errorf("could not list GovernedArtefacts: %w", err)
	}

	vocabularies := make(map[string]map[string]bool)
	for _, ga := range artefacts.Items {
		stamps := make(map[string]bool)
		for _, s := range ga.Spec.Stamps {
			stamps[s] = true
		}
		vocabularies[ga.Name] = stamps
	}

	// Validate entry contracts.
	for contractName, contract := range flow.Spec.EntryContracts {
		if err := r.validateContractStamps(contractName, "entry", contract, vocabularies); err != nil {
			return err
		}
	}

	// Validate exit contracts.
	for contractName, contract := range flow.Spec.ExitContracts {
		if err := r.validateContractStamps(contractName, "exit", contract, vocabularies); err != nil {
			return err
		}
	}

	return nil
}

// validateContractStamps checks a single contract's stamp references against vocabularies.
func (r *FoundryFlowReconciler) validateContractStamps(
	contractName, contractType string,
	contract flowv1.Contract,
	vocabularies map[string]map[string]bool,
) error {
	for gaName, requiredStamps := range contract {
		vocab, exists := vocabularies[gaName]
		if !exists {
			// GovernedArtefact not found — this is a warning, not a hard failure.
			// The artefact might not be created yet.
			continue
		}
		for _, stamp := range requiredStamps {
			if !vocab[stamp] {
				return fmt.Errorf("%s contract %q references stamp %q not in GovernedArtefact %q vocabulary",
					contractType, contractName, stamp, gaName)
			}
		}
	}
	return nil
}

// validateImportNode validates the importNode reference if present.
func (r *FoundryFlowReconciler) validateImportNode(ctx context.Context, flow *flowv1.FoundryFlow) error {
	if flow.Spec.ImportNode == "" {
		return nil
	}

	var node flowv1.FoundryNode
	if err := r.Get(ctx, types.NamespacedName{
		Name:      flow.Spec.ImportNode,
		Namespace: flow.Namespace,
	}, &node); err != nil {
		return fmt.Errorf("importNode %q does not reference an existing FoundryNode", flow.Spec.ImportNode)
	}

	// importNode must be entry-bound.
	if node.Spec.Entry == "" {
		return fmt.Errorf("importNode %q must have an entry contract binding", flow.Spec.ImportNode)
	}

	return nil
}

// validateNodeTopology checks that all nodes in the namespace have valid routing targets.
func (r *FoundryFlowReconciler) validateNodeTopology(ctx context.Context, flow *flowv1.FoundryFlow) error {
	var nodes flowv1.FoundryNodeList
	if err := r.List(ctx, &nodes, client.InNamespace(flow.Namespace)); err != nil {
		return fmt.Errorf("could not list FoundryNodes: %w", err)
	}

	// Build a set of known node names.
	nodeSet := make(map[string]bool)
	for _, n := range nodes.Items {
		nodeSet[n.Name] = true
	}

	// Validate each node's output targets.
	for _, n := range nodes.Items {
		for _, output := range n.Spec.Outputs {
			if !nodeSet[output.Target] {
				return fmt.Errorf("node %q output %q targets nonexistent node %q",
					n.Name, output.Name, output.Target)
			}
		}
	}

	return nil
}

// setPhaseAndCondition updates the Flow's phase and the Ready condition, then persists.
func (r *FoundryFlowReconciler) setPhaseAndCondition(
	ctx context.Context,
	flow *flowv1.FoundryFlow,
	phase string,
	status metav1.ConditionStatus,
	reason, message string,
) (ctrl.Result, error) {
	newCondition := metav1.Condition{
		Type:               conditionReady,
		Status:             status,
		ObservedGeneration: flow.Generation,
		Reason:             reason,
		Message:            message,
	}

	// Check if already up-to-date.
	existing := meta.FindStatusCondition(flow.Status.Conditions, conditionReady)
	if flow.Status.Phase == phase &&
		existing != nil &&
		existing.Status == status &&
		existing.Reason == reason &&
		existing.Message == message &&
		existing.ObservedGeneration == flow.Generation {
		return ctrl.Result{}, nil
	}

	// Re-fetch to get latest resourceVersion.
	var fresh flowv1.FoundryFlow
	if err := r.Get(ctx, client.ObjectKeyFromObject(flow), &fresh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	fresh.Status.Phase = phase
	meta.SetStatusCondition(&fresh.Status.Conditions, newCondition)

	if !equality.Semantic.DeepEqual(fresh.Status, flow.Status) || fresh.Status.Phase != flow.Status.Phase {
		if err := r.Status().Update(ctx, &fresh); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *FoundryFlowReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&flowv1.FoundryFlow{}).
		Named("foundryflow").
		Complete(r)
}
