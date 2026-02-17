// Package scheduler implements routing logic for Workitem transitions.
//
// The Scheduler resolves the next destination for a Workitem by reading
// the current FoundryNode's outputs and matching them against the
// RoutingInstruction. It is a pure decision-maker: it reads CRD state
// and returns the next assignee and phase, but never mutates resources.
package scheduler

import (
	"context"
	"fmt"

	flowv1 "github.com/gideas/flow/operator/api/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Result holds the outcome of a scheduling decision.
type Result struct {
	// NextAssignee is the name of the next FoundryNode, or empty if the
	// Workitem has reached a terminal state.
	NextAssignee string

	// Phase is the target lifecycle phase: "Pending" or "Completed".
	Phase string
}

// Scheduler resolves routing decisions by reading FoundryNode CRDs.
type Scheduler struct {
	Client    client.Client
	Namespace string
}

// New returns a Scheduler wired to the given Kubernetes client and namespace.
func New(c client.Client, namespace string) *Scheduler {
	return &Scheduler{Client: c, Namespace: namespace}
}

// CalculateNextStep determines where a Workitem should go next.
//
// It fetches the FoundryNode CRD for the current assignee and resolves
// the routing instruction against that node's outputs.
//
// Routing rules:
//   - complete: The node must have Spec.Exit set (exit-bound). Returns
//     Phase="Completed", NextAssignee="".
//   - route_to_output: Matches instruction.Target against the node's
//     Outputs[].Name. Defaults to "default" if target is empty.
//     Returns Phase="Pending", NextAssignee=Output.Target.
//   - route_to: Direct routing to a named node (bypasses output lookup).
//     Returns Phase="Pending", NextAssignee=instruction.Target.
func (s *Scheduler) CalculateNextStep(
	ctx context.Context,
	currentAssignee string,
	instruction flowv1.RoutingInstruction,
) (*Result, error) {
	// 1. Fetch the FoundryNode CRD for the current assignee.
	var node flowv1.FoundryNode
	key := types.NamespacedName{
		Namespace: s.Namespace,
		Name:      currentAssignee,
	}
	if err := s.Client.Get(ctx, key, &node); err != nil {
		return nil, fmt.Errorf("failed to fetch FoundryNode %q: %w", currentAssignee, err)
	}

	// 2. Dispatch on instruction type.
	switch instruction.Type {
	case "complete":
		return s.handleComplete(&node)

	case "route_to_output":
		return s.handleRouteToOutput(&node, instruction.Target)

	case "route_to":
		return s.handleRouteTo(instruction.Target)

	default:
		return nil, fmt.Errorf("unknown routing instruction type %q", instruction.Type)
	}
}

// handleComplete processes a "complete" instruction.
// The node must be exit-bound (Spec.Exit != "") to accept completion.
func (s *Scheduler) handleComplete(node *flowv1.FoundryNode) (*Result, error) {
	if node.Spec.Exit == "" {
		return nil, fmt.Errorf(
			"node %q received 'complete' instruction but has no exit contract",
			node.Name,
		)
	}
	return &Result{
		NextAssignee: "",
		Phase:        "Completed",
	}, nil
}

// handleRouteToOutput resolves a named output from the node's output list.
// If the instruction target is empty, it defaults to "default".
func (s *Scheduler) handleRouteToOutput(node *flowv1.FoundryNode, target string) (*Result, error) {
	if target == "" {
		target = "default"
	}

	for _, output := range node.Spec.Outputs {
		if output.Name == target {
			return &Result{
				NextAssignee: output.Target,
				Phase:        "Pending",
			}, nil
		}
	}

	return nil, fmt.Errorf(
		"node %q has no output named %q (available: %v)",
		node.Name, target, outputNames(node.Spec.Outputs),
	)
}

// handleRouteTo processes a direct "route_to" instruction.
// The target must be non-empty.
func (s *Scheduler) handleRouteTo(target string) (*Result, error) {
	if target == "" {
		return nil, fmt.Errorf("route_to instruction requires a non-empty target")
	}
	return &Result{
		NextAssignee: target,
		Phase:        "Pending",
	}, nil
}

// outputNames extracts the names from a slice of outputs for error messages.
func outputNames(outputs []flowv1.Output) []string {
	names := make([]string, len(outputs))
	for i, o := range outputs {
		names[i] = o.Name
	}
	return names
}
