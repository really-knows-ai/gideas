// Package types provides shared data types used by TUI components and messages.
// These are Phase 02 provisional types with string Age; Phase 03 replaces
// WorkitemSummary with api.WorkitemSummary which uses time.Duration for Age.
package types

// WorkitemSummary represents a Workitem in the list view.
// Provisional — Phase 03 replaces with api.WorkitemSummary.
type WorkitemSummary struct {
	Name          string
	State         string // status.phase
	Node          string // status.currentAssignee; "-" if terminal
	ChildrenCount int
	Age           string // human-readable age
}

// ArtefactNode represents an artefact in the tree view.
// Phase 04 receives api.ArtefactInfo and maps it to this type.
type ArtefactNode struct {
	ArtefactID string
	GovernedBy string // governed artefact name
	Expanded   bool
	Content    string
	IsBinary   bool
	BinarySize int
	Feedback   []FeedbackItem
}

// FeedbackItem represents feedback on an artefact.
// Phase 04 receives api.FeedbackItem and maps it to this type.
type FeedbackItem struct {
	ID         string // UUID
	State      string // "NEW", "RESOLVED", "ACTIONED", "REJECTED", "DEADLOCKED", "WONT_FIX"
	SourceNode string
	Message    string // first 120 chars
	Timestamp  string // RFC3339 or Unix timestamp
}

// TopologyColor indicates the visit status of a topology node.
type TopologyColor int

const (
	TopologyCurrent   TopologyColor = iota // current NODE
	TopologyVisited                        // previously visited NODE
	TopologyUnvisited                      // not yet visited NODE
)

// TopologyNode is a node in the flow topology graph.
type TopologyNode struct {
	Name  string
	Color TopologyColor
}

// TopologyEdge is a directed edge between topology nodes.
type TopologyEdge struct {
	From string
	To   string
}

// Choice represents a HITL decision option.
// Rendering type for TUI components. Phase 05 converts from api.Choice.
type Choice struct {
	Value string // "approve", "cancel", etc.
	Label string // "Approve", "Reject", etc.
	Type  string // "route" or "cancel"
}
