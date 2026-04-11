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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
//   - Validate referential integrity (routing targets, import types, stamp vocabulary).
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
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

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

	// Enforce the one-namespace-one-flow singleton invariant.
	// A namespace must contain exactly one FoundryFlow. If multiple exist,
	// all are degraded until the violation is resolved.
	var allFlows flowv1.FoundryFlowList
	if err := r.List(ctx, &allFlows, client.InNamespace(flow.Namespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("list FoundryFlows in namespace %q: %w", flow.Namespace, err)
	}
	if len(allFlows.Items) > 1 {
		log.Error(nil, "Singleton violation: multiple FoundryFlows in namespace",
			"namespace", flow.Namespace,
			"count", len(allFlows.Items),
		)
		return r.setPhaseAndCondition(ctx, &flow, phaseDegraded,
			metav1.ConditionFalse, "SingletonViolation",
			fmt.Sprintf("namespace %q contains %d FoundryFlows; exactly 1 is required",
				flow.Namespace, len(allFlows.Items)))
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

	// Validate cross-flow import type references.
	if err := r.validateImportTypes(ctx, &flow); err != nil {
		return r.setPhaseAndCondition(ctx, &flow, phaseFailed,
			metav1.ConditionFalse, "ImportTypesInvalid", err.Error())
	}

	// Validate NodeGroup configuration (membership, contracts, routing isolation).
	if err := r.validateNodeGroups(ctx, &flow); err != nil {
		return r.setPhaseAndCondition(ctx, &flow, phaseDegraded,
			metav1.ConditionFalse, "NodeGroupValidationFailed", err.Error())
	}

	// Validate that all nodes' routing outputs target existing nodes.
	if err := r.validateNodeTopology(ctx, &flow); err != nil {
		return r.setPhaseAndCondition(ctx, &flow, phaseDegraded,
			metav1.ConditionFalse, "TopologyValidationFailed", err.Error())
	}

	// Reconcile control-plane infrastructure (Event Bus, Friction Ledger, Flow Monitor).
	if err := r.reconcileInfrastructure(ctx, &flow); err != nil {
		return r.setPhaseAndCondition(ctx, &flow, phaseDegraded,
			metav1.ConditionFalse, "InfraReconcileFailed", err.Error())
	}

	// All validations passed and infrastructure reconciled — transition to Ready.
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

// validateImportTypes validates crossFlow.importTypes references if present.
func (r *FoundryFlowReconciler) validateImportTypes(ctx context.Context, flow *flowv1.FoundryFlow) error {
	if flow.Spec.CrossFlow == nil || len(flow.Spec.CrossFlow.ImportTypes) == 0 {
		return nil
	}

	for importType, spec := range flow.Spec.CrossFlow.ImportTypes {
		var node flowv1.FoundryNode
		if err := r.Get(ctx, types.NamespacedName{
			Name:      spec.Node,
			Namespace: flow.Namespace,
		}, &node); err != nil {
			return fmt.Errorf("crossFlow.importTypes[%q].node %q does not reference an existing FoundryNode", importType, spec.Node)
		}

		if node.Spec.Entry == "" {
			return fmt.Errorf("crossFlow.importTypes[%q].node %q must have an entry contract binding", importType, spec.Node)
		}
	}

	return nil
}

// validateNodeGroups checks NodeGroup configuration for referential integrity.
//
// Validations:
//  1. Every node listed in a group must exist as a FoundryNode in the namespace.
//  2. A node can belong to at most one group.
//  3. Routing outputs from nodes inside a group must target nodes in the same group
//     (routing isolation), with the exception that exit-bound nodes may complete.
//  4. Group entry/exit contract stamp references must resolve against GovernedArtefact
//     vocabularies (reuses validateContractStamps).
func (r *FoundryFlowReconciler) validateNodeGroups(ctx context.Context, flow *flowv1.FoundryFlow) error {
	if len(flow.Spec.NodeGroups) == 0 {
		return nil
	}

	// Build the set of existing FoundryNodes in the namespace.
	var nodeList flowv1.FoundryNodeList
	if err := r.List(ctx, &nodeList, client.InNamespace(flow.Namespace)); err != nil {
		return fmt.Errorf("could not list FoundryNodes: %w", err)
	}
	existingNodes := make(map[string]*flowv1.FoundryNode, len(nodeList.Items))
	for i := range nodeList.Items {
		existingNodes[nodeList.Items[i].Name] = &nodeList.Items[i]
	}

	// Build GovernedArtefact vocabularies for contract stamp validation.
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

	// Track node-to-group membership to detect duplicates.
	nodeToGroup := make(map[string]string)

	for groupName, group := range flow.Spec.NodeGroups {
		// Build the set of nodes in this group.
		groupNodeSet := make(map[string]bool, len(group.Nodes))
		for _, nodeName := range group.Nodes {
			groupNodeSet[nodeName] = true
		}

		for _, nodeName := range group.Nodes {
			// 1. Node must exist.
			if _, exists := existingNodes[nodeName]; !exists {
				return fmt.Errorf("node group %q references nonexistent node %q", groupName, nodeName)
			}

			// 2. Node can belong to at most one group.
			if otherGroup, already := nodeToGroup[nodeName]; already {
				return fmt.Errorf("node %q belongs to multiple groups: %q and %q", nodeName, otherGroup, groupName)
			}
			nodeToGroup[nodeName] = groupName

			// 3. Routing isolation: outputs must target nodes in the same group.
			node := existingNodes[nodeName]
			for _, output := range node.Spec.Outputs {
				if !groupNodeSet[output.Target] {
					return fmt.Errorf("node %q in group %q has output %q targeting node %q outside the group",
						nodeName, groupName, output.Name, output.Target)
				}
			}
		}

		// 4. Validate group entry contract stamp references.
		for contractName, contract := range group.EntryContracts {
			if err := r.validateContractStamps(
				fmt.Sprintf("group/%s/%s", groupName, contractName),
				"entry",
				contract,
				vocabularies,
			); err != nil {
				return err
			}
		}

		// 5. Validate group exit contract stamp references.
		for contractName, contract := range group.ExitContracts {
			if err := r.validateContractStamps(
				fmt.Sprintf("group/%s/%s", groupName, contractName),
				"exit",
				contract,
				vocabularies,
			); err != nil {
				return err
			}
		}
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
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Named("foundryflow").
		Complete(r)
}
