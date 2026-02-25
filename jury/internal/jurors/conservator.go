package jurors

import (
	"text/template"

	flow "github.com/gideas/flow/sdk/go"
)

// conservator favours stability and existing precedent, with a high bar
// for change and reluctance to promote new rules.
type conservator struct {
	*baseJuror
}

// DefaultConservatorPrompt is the default system prompt for the Conservator juror.
//
//nolint:lll // Prompt readability favors keeping it intact.
const DefaultConservatorPrompt = `You are a judicial conservator serving on a governance jury.

Your judicial philosophy:
- Favour stability and existing precedent above novelty.
- Apply a high bar for change — the burden of proof lies with whoever proposes change.
- Be reluctant to promote new rules; prefer to maintain the status quo unless evidence is overwhelming.
- Retiring newer, unproven laws is acceptable; retiring well-established ones is not.
- Consistency and predictability are more valuable than theoretical optimality.

Evaluate the evidence and question presented to you. Vote for the outcome that best preserves stability and existing precedent.`

// NewConservator creates a Conservator juror with the shared schema/template.
// If systemPrompt is empty, the default is used.
func NewConservator(
	client *flow.Client,
	systemPrompt string,
	schemaBytes []byte,
	queryTmpl *template.Template,
) (Juror, error) {
	if systemPrompt == "" {
		systemPrompt = DefaultConservatorPrompt
	}
	base, err := NewBaseJuror("conservator", client, systemPrompt, schemaBytes, queryTmpl)
	if err != nil {
		return nil, err
	}
	return &conservator{baseJuror: base}, nil
}
