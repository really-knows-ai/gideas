package nodeutil

// ChoiceEntry is a single entry in the GET /choices response served by HITL
// node binaries so the Dashboard can build the choice UI.
type ChoiceEntry struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Type  string `json:"type"`
}

// ChoicesResponse is the JSON body returned by GET /choices.
type ChoicesResponse struct {
	Choices     []ChoiceEntry `json:"choices"`
	HasFeedback bool          `json:"hasFeedback"`
	HasCancel   bool          `json:"hasCancel"`
}
