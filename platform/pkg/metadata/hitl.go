// HITL GET /choices wire contract shared by the node binaries that serve it
// and the flowctl client that decodes it. Defined once here so sibling
// implementations cannot diverge.

package metadata

// Choice is a single decision option in the GET /choices response served by
// HITL node binaries so the Dashboard can build the choice UI.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Type  string `json:"type"` // "route" or "cancel"
}

// ChoicesResponse is the JSON body returned by GET /choices.
type ChoicesResponse struct {
	Choices     []Choice `json:"choices"`
	HasFeedback bool     `json:"hasFeedback"`
	HasCancel   bool     `json:"hasCancel"`
}
