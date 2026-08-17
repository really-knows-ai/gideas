// Package codification defines the codification-goal/codification-result wire
// contract shared by the Clerk (codification) and its codify-* children
// (e.g. codify-smt). The Clerk writes a "codification-goal" artefact on each
// child workitem and collects a "codification-result" artefact after fan-out,
// so the wire shape must be defined once here rather than duplicated per node —
// the divergent copies previously let the parent/child contract silently diverge.
package codification

// Goal is the JSON structure of the "codification-goal" artefact written by
// the Clerk (codification) and consumed by the codify-* children.
type Goal struct {
	Goal      string   `json:"goal"`
	AppliesTo []string `json:"applies_to"`
	Tier      int32    `json:"tier"`
	Action    string   `json:"action"`
}

// Result is the JSON structure of the "codification-result" artefact produced
// by the codify-* children and collected by the Clerk (codification).
type Result struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}
