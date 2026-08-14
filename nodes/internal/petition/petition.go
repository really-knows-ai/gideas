// Package petition defines the petition JSON contract shared by the judiciary
// nodes. Codification writes petitions (after attaching codification
// representations) and law-applicator later reads them, so the wire shape must
// be defined once here rather than duplicated per node — the divergent copies
// previously let codification drop petition_id (needed for T4-5 dispute
// records) when re-marshalling the petition.
package petition

// Petition is the top-level JSON envelope: {"petition": {...}}.
type Petition struct {
	Petition Body `json:"petition"`
}

// Body is the petition body.
type Body struct {
	PetitionID         string   `json:"petition_id"`
	Context            Context  `json:"context"`
	Changes            []Change `json:"changes"`
	ProseJustification string   `json:"prose_justification"`
}

// Context is the petition context block.
type Context struct {
	Trigger         string `json:"trigger"`
	SourceWorkitem  string `json:"source_workitem"` //nolint:tagliatelle // JSON convention
	Verdict         string `json:"verdict"`
	VerdictDecision string `json:"verdict_decision"` //nolint:tagliatelle // Matches petition schema
	Justification   string `json:"justification"`
}

// Change is a single petition change.
type Change struct {
	Action          string   `json:"action"`
	Tier            int32    `json:"tier,omitempty"`
	Goal            string   `json:"goal,omitempty"`
	AppliesTo       []string `json:"applies_to,omitempty"`
	LawID           string   `json:"law_id,omitempty"`
	FromTier        int32    `json:"from_tier,omitempty"`
	ToTier          int32    `json:"to_tier,omitempty"`
	Justification   string   `json:"justification,omitempty"`
	Representations []Rep    `json:"representations,omitempty"`
}

// Rep is a formal representation attached to a change.
type Rep struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}
