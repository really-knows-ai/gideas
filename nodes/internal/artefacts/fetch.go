// Package artefacts provides shared helpers for fetching and assembling
// artefact content across Foundry Flow nodes.
package artefacts

import (
	"fmt"
	"strings"

	flow "github.com/gideas/flow/sdk/go"
)

// FetchInputs fetches each artefact by ID and concatenates them with
// "## <id>" Markdown headers. The result is a single string suitable for
// passing to an LLM prompt as combined input context.
//
// If ids is empty, FetchInputs returns an empty string and nil error —
// callers that require at least one input should validate their config
// before calling.
func FetchInputs(workitem *flow.Workitem, ids []string) (string, error) {
	if len(ids) == 0 {
		return "", nil
	}

	var b strings.Builder
	for i, id := range ids {
		artefact, err := workitem.GetArtefact(id)
		if err != nil {
			return "", fmt.Errorf("fetch input artefact %s: %w", id, err)
		}
		content, err := artefact.GetContent()
		if err != nil {
			return "", fmt.Errorf("get content for artefact %s: %w", id, err)
		}

		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "## %s\n\n", id)
		b.WriteString(string(content))
		b.WriteString("\n")
	}

	return b.String(), nil
}

// InputLabel returns a human-readable label for a set of input artefact IDs,
// suitable for use in prompt templates. For a single ID it returns the ID
// itself (e.g. "petition"); for multiple IDs it returns them comma-joined
// (e.g. "petition, style-guide").
func InputLabel(ids []string) string {
	return strings.Join(ids, ", ")
}
