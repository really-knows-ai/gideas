// HITL decision-option shape shared by every consuming module. Defined once
// here so sibling implementations cannot diverge.

package metadata

// Choice is a single decision option rendered by the flowctl TUI. The
// options are sourced from queue item metadata served by the queue-service;
// there is no node-local /choices HTTP route (never-a-node, SPEC R-5.1).
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Type  string `json:"type"` // "route" or "cancel"
}
