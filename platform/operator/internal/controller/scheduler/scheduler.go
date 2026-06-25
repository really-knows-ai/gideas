// Package scheduler implements routing logic for Workitem transitions.
//
// The Scheduler resolves the next destination for a Workitem by reading
// the current FoundryNode's outputs and matching them against the
// RoutingInstruction. It enforces the spec-defined guard evaluation order:
//
//  1. Instruction shape validity
//  2. Routing target or exit eligibility validity
//  3. Timeout and thrash guard compliance
//  4. Lifecycle transition application
//
// The Scheduler is a pure decision-maker: it reads CRD state, queries
// artefact state for contract validation, and returns the next assignee
// and phase. It never mutates resources directly.
package scheduler

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	flowv1 "github.com/gideas/flow/operator/api/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ArtefactQuerier queries the Archivist for artefact state.
// The Operator uses this to validate exit contracts against current
// artefact presence and stamp state.
type ArtefactQuerier interface {
	// QueryArtefactState returns artefact presence and stamp state for the
	// given workitem, filtered by governed artefact names.
	QueryArtefactState(ctx context.Context, workitemID string, governedArtefacts []string) ([]ArtefactState, error)
}

// ArtefactState represents a single artefact's state for contract validation.
type ArtefactState struct {
	ArtefactID       string
	GovernedArtefact string
	StampNames       []string
}

// GuardError represents a guard pipeline failure with a stable error code.
// The controller uses the Code field to populate WorkitemStatus.FailureReason.
type GuardError struct {
	// Code is the stable error identifier (e.g. INVALID_ROUTE, THRASH_BUDGET_EXCEEDED).
	Code string
	// Message is the human-readable description.
	Message string
}

func (e *GuardError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Result holds the outcome of a scheduling decision.
type Result struct {
	// NextAssignee is the name of the next FoundryNode, or empty if the
	// Workitem has reached a terminal state.
	NextAssignee string

	// Phase is the target lifecycle phase: "Pending", "Completed", or "Suspended".
	Phase string

	// SuspendCondition is the CEL expression for auto-resume.
	// Set only when Phase is "Suspended". Empty means manual Resume() required.
	SuspendCondition string

	// SuspendTimeout is the resolved timeout duration for a suspended workitem.
	// Set only when Phase is "Suspended". Resolved from the instruction,
	// flow defaults, and flow max cap.
	SuspendTimeout string
}

// Scheduler resolves routing decisions by reading FoundryNode CRDs.
type Scheduler struct {
	Client    client.Client
	Namespace string
	Querier   ArtefactQuerier
}

// New returns a Scheduler wired to the given Kubernetes client and namespace.
func New(c client.Client, namespace string) *Scheduler {
	return &Scheduler{Client: c, Namespace: namespace}
}

// CalculateNextStep determines where a Workitem should go next.
//
// It enforces the spec-defined guard evaluation order:
//  1. Instruction shape validity (type is valid, target is present).
//  2. Routing target or exit eligibility (output resolution, target existence, exit-bound + contract).
//  3. Thrash guard compliance (aggregate visit count vs maxVisits).
//  4. Returns the lifecycle transition result.
//
// Parameters:
//   - currentAssignee: the node currently processing the Workitem.
//   - instruction: the routing instruction submitted by the node.
//   - workitem: the Workitem being routed (for thrash counter state).
//   - flow: the FoundryFlow (for maxVisits and exit contracts). May be nil to
//     skip thrash/contract checks (for backward compatibility in tests).
func (s *Scheduler) CalculateNextStep(
	ctx context.Context,
	currentAssignee string,
	instruction flowv1.RoutingInstruction,
	workitem *flowv1.Workitem,
	flow *flowv1.FoundryFlow,
) (*Result, error) {
	// 1. For instructions that don't require the current node, handle directly.
	switch instruction.Type {
	case "route_to":
		// route_to only validates the target — no current node needed.
		// This supports child workitems which have no current assignee yet.
		result, err := s.handleRouteTo(ctx, instruction.Target)
		if err != nil {
			return nil, err
		}
		if err := s.checkThrashGuard(workitem, flow); err != nil {
			return nil, err
		}
		return result, nil
	}

	// 2. Instructions that require the current node.
	var node flowv1.FoundryNode
	key := types.NamespacedName{
		Namespace: s.Namespace,
		Name:      currentAssignee,
	}
	if err := s.Client.Get(ctx, key, &node); err != nil {
		return nil, fmt.Errorf("failed to fetch FoundryNode %q: %w", currentAssignee, err)
	}

	switch instruction.Type {
	case "complete":
		return s.handleComplete(ctx, &node, workitem, flow)

	case "route_to_output":
		result, err := s.handleRouteToOutput(&node, instruction.Target)
		if err != nil {
			return nil, err
		}
		if err := s.checkThrashGuard(workitem, flow); err != nil {
			return nil, err
		}
		return result, nil

	case "suspend":
		return s.handleSuspend(instruction, flow)

	default:
		return nil, &GuardError{
			Code:    "INVALID_ROUTE",
			Message: fmt.Sprintf("unknown routing instruction type %q", instruction.Type),
		}
	}
}

// handleComplete processes a "complete" instruction.
// The node must be exit-bound (Spec.Exit != "") to accept completion.
// If a FoundryFlow is provided, the bound exit contract is validated
// against artefact state via the Archivist.
func (s *Scheduler) handleComplete(ctx context.Context, node *flowv1.FoundryNode, workitem *flowv1.Workitem, flow *flowv1.FoundryFlow) (*Result, error) {
	// Children don't need exit contracts — they report back to the parent.
	if workitem != nil && workitem.Status.ParentWorkitemID != "" {
		if err := s.checkThrashGuard(workitem, flow); err != nil {
			return nil, err
		}
		return &Result{
			NextAssignee: "",
			Phase:        "Completed",
		}, nil
	}

	if node.Spec.Exit == "" {
		return nil, &GuardError{
			Code:    "EXIT_NOT_BOUND",
			Message: fmt.Sprintf("node %q received 'complete' instruction but has no exit contract", node.Name),
		}
	}

	// Validate exit contract against artefact state if flow and querier are available.
	if flow != nil && s.Querier != nil && workitem != nil {
		contract, ok := flow.Spec.ExitContracts[node.Spec.Exit]
		if !ok {
			return nil, &GuardError{
				Code:    "CONTRACT_VIOLATION",
				Message: fmt.Sprintf("exit contract %q not found on flow", node.Spec.Exit),
			}
		}

		if err := s.validateContract(ctx, workitem.Name, contract); err != nil {
			return nil, err
		}
	}

	// Thrash check for completion path.
	if err := s.checkThrashGuard(workitem, flow); err != nil {
		return nil, err
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

	return nil, &GuardError{
		Code:    "INVALID_ROUTE",
		Message: fmt.Sprintf("node %q has no output named %q (available: %v)", node.Name, target, outputNames(node.Spec.Outputs)),
	}
}

// handleRouteTo processes a direct "route_to" instruction.
// The target must be non-empty and must reference an existing FoundryNode CRD.
func (s *Scheduler) handleRouteTo(ctx context.Context, target string) (*Result, error) {
	if target == "" {
		return nil, &GuardError{
			Code:    "INVALID_ROUTE",
			Message: "route_to instruction requires a non-empty target",
		}
	}

	// Validate the target node exists.
	var targetNode flowv1.FoundryNode
	key := types.NamespacedName{
		Namespace: s.Namespace,
		Name:      target,
	}
	if err := s.Client.Get(ctx, key, &targetNode); err != nil {
		return nil, &GuardError{
			Code:    "INVALID_ROUTE",
			Message: fmt.Sprintf("target node %q does not exist: %v", target, err),
		}
	}

	return &Result{
		NextAssignee: target,
		Phase:        "Pending",
	}, nil
}

// handleSuspend processes a "suspend" instruction.
// Suspend does not require thrash guard or exit contract checks.
// The timeout is resolved from the instruction, flow defaults, and flow max cap:
//  1. If the instruction specifies a timeout, use it (capped by maxSuspendTimeout).
//  2. If no timeout is specified, apply defaultSuspendTimeout from the flow.
//  3. If no default is configured, apply maxSuspendTimeout.
//  4. If the flow has no SuspensionConfig at all, no timeout is applied.
func (s *Scheduler) handleSuspend(instruction flowv1.RoutingInstruction, flow *flowv1.FoundryFlow) (*Result, error) {
	timeout := instruction.SuspendTimeout
	condition := instruction.SuspendCondition

	// Resolve timeout from flow SuspensionConfig.
	if flow != nil && flow.Spec.Suspension != nil {
		cfg := flow.Spec.Suspension

		if timeout == "" {
			// No explicit timeout — apply default.
			if cfg.DefaultSuspendTimeout != nil {
				timeout = cfg.DefaultSuspendTimeout.Duration.String()
			} else if cfg.MaxSuspendTimeout != nil {
				// Default falls back to max if not explicitly configured.
				timeout = cfg.MaxSuspendTimeout.Duration.String()
			}
		} else {
			// Explicit timeout — validate against max.
			if cfg.MaxSuspendTimeout != nil {
				requested, err := time.ParseDuration(timeout)
				if err != nil {
					return nil, &GuardError{
						Code:    "INVALID_SUSPEND",
						Message: fmt.Sprintf("invalid suspend timeout %q: %v", timeout, err),
					}
				}
				if requested > cfg.MaxSuspendTimeout.Duration {
					return nil, &GuardError{
						Code:    "SUSPEND_TIMEOUT_EXCEEDED",
						Message: fmt.Sprintf("requested suspend timeout %s exceeds maxSuspendTimeout %s", timeout, cfg.MaxSuspendTimeout.Duration),
					}
				}
			}
		}
	}

	return &Result{
		NextAssignee:     "", // Workitem stays with current assignee on resume.
		Phase:            "Suspended",
		SuspendCondition: condition,
		SuspendTimeout:   timeout,
	}, nil
}

// checkThrashGuard verifies that the aggregate visit count does not exceed
// the flow's maxVisits budget. Returns a GuardError if the budget is exceeded.
func (s *Scheduler) checkThrashGuard(workitem *flowv1.Workitem, flow *flowv1.FoundryFlow) error {
	if workitem == nil || flow == nil {
		return nil
	}

	var aggregate int32
	for _, count := range workitem.Status.ThrashCounters {
		aggregate += count
	}

	if aggregate >= flow.Spec.GovernancePolicy.MaxVisits {
		return &GuardError{
			Code:    "THRASH_BUDGET_EXCEEDED",
			Message: fmt.Sprintf("aggregate visit count %d exceeds maxVisits %d", aggregate, flow.Spec.GovernancePolicy.MaxVisits),
		}
	}

	return nil
}

// validateContract checks that all artefact requirements in a contract are
// satisfied by the current artefact state from the Archivist.
func (s *Scheduler) validateContract(ctx context.Context, workitemID string, contract flowv1.Contract) error {
	if len(contract) == 0 {
		return nil // Empty contract means no requirements.
	}

	governedNames := slices.Collect(maps.Keys(contract))

	// Query the Archivist for artefact state.
	states, err := s.Querier.QueryArtefactState(ctx, workitemID, governedNames)
	if err != nil {
		return fmt.Errorf("failed to query artefact state: %w", err)
	}

	// Group artefacts by governed artefact name.
	byName := make(map[string][]ArtefactState)
	for _, state := range states {
		byName[state.GovernedArtefact] = append(byName[state.GovernedArtefact], state)
	}

	// Validate each contract requirement.
	for name, requiredStamps := range contract {
		artefacts, exists := byName[name]
		if !exists || len(artefacts) == 0 {
			return &GuardError{
				Code:    "CONTRACT_VIOLATION",
				Message: fmt.Sprintf("no artefacts of governed type %q found", name),
			}
		}

		// All artefacts of the required type must carry all required stamps.
		for _, artefact := range artefacts {
			stampSet := make(map[string]bool, len(artefact.StampNames))
			for _, s := range artefact.StampNames {
				stampSet[s] = true
			}
			for _, required := range requiredStamps {
				if !stampSet[required] {
					return &GuardError{
						Code:    "CONTRACT_VIOLATION",
						Message: fmt.Sprintf("artefact %q (type %q) missing required stamp %q", artefact.ArtefactID, name, required),
					}
				}
			}
		}
	}

	return nil
}

// outputNames extracts the names from a slice of outputs for error messages.
func outputNames(outputs []flowv1.Output) []string {
	names := make([]string, len(outputs))
	for i, o := range outputs {
		names[i] = o.Name
	}
	return names
}
