package api

import "time"

// WorkitemSummary is a light-weight row for the list view.
type WorkitemSummary struct {
	Name          string
	State         string // from status.phase; "" if not set
	Node          string // from status.currentAssignee; "-" if terminal
	ChildrenCount int
	Age           time.Duration
}

// WorkitemDetail is the full Workitem for the detail screen.
type WorkitemDetail struct {
	WorkitemSummary
	FailureReason  string
	ThrashCounters map[string]int32 // node name -> visit count
	ChildWorkitems []WorkitemSummary
	Labels         map[string]string
	Annotations    map[string]string
}

// FoundryFlowSummary carries the name and entry contracts of the singular flow.
type FoundryFlowSummary struct {
	Name           string
	EntryContracts map[string]interface{} // raw from spec.entryContracts
}

// FoundryNodeSummary carries topology-relevant fields.
type FoundryNodeSummary struct {
	Name    string
	Entry   string            // spec.entry ("" if not an entry node)
	Targets []string          // spec.outputs[].target
	Labels  map[string]string
}

// GovernedArtefactSummary carries the name for seeding selection.
type GovernedArtefactSummary struct {
	Name string
}
