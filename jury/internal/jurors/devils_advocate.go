package jurors

import (
	"text/template"

	flow "github.com/gideas/flow/sdk/go"
)

// devilsAdvocate challenges the majority position and stress-tests reasoning.
// When evidence seems one-sided, it pushes back to force considered consensus.
type devilsAdvocate struct {
	*baseJuror
}

// DefaultDevilsAdvocatePrompt is the default system prompt for the Devil's Advocate juror.
//
//nolint:lll // Prompt readability favors keeping it intact.
const DefaultDevilsAdvocatePrompt = `You are a devil's advocate serving on a governance jury.

Your judicial philosophy:
- Challenge the majority position and stress-test the reasoning of all sides.
- If evidence seems one-sided or a conclusion appears obvious, push back and identify weaknesses.
- Play the contrarian role to force the jury toward more considered, robust consensus.
- Ask "what could go wrong?" and "what are we missing?" before voting.
- Only agree with the apparent majority if you genuinely cannot find flaws in their reasoning.

Evaluate the evidence and question presented to you. Vote for the outcome that you believe would survive the strongest possible scrutiny, even if it goes against the apparent consensus.`

// NewDevilsAdvocate creates a Devil's Advocate juror with the given model and
// shared schema/template. If systemPrompt is empty, the default is used.
func NewDevilsAdvocate(
	client *flow.Client,
	model *flow.Model,
	systemPrompt string,
	schemaBytes []byte,
	queryTmpl *template.Template,
) (Juror, error) {
	if systemPrompt == "" {
		systemPrompt = DefaultDevilsAdvocatePrompt
	}
	base, err := NewBaseJuror("devils-advocate", client, model, systemPrompt, schemaBytes, queryTmpl)
	if err != nil {
		return nil, err
	}
	return &devilsAdvocate{baseJuror: base}, nil
}
