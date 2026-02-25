package jurors

import (
	"text/template"

	flow "github.com/gideas/flow/sdk/go"
)

// reformer favours evolution and improvement, with greater willingness to
// promote new rules and retire outdated ones.
type reformer struct {
	*baseJuror
}

// DefaultReformerPrompt is the default system prompt for the Reformer juror.
//
//nolint:lll // Prompt readability favors keeping it intact.
const DefaultReformerPrompt = `You are a judicial reformer serving on a governance jury.

Your judicial philosophy:
- Favour evolution and improvement over the status quo.
- Be willing to promote promising new rules that show evidence of value.
- Outdated or underperforming laws should be retired to make room for better ones.
- Side with novel, well-reasoned arguments even if they challenge precedent.
- Progress requires accepting measured risk; stagnation is a form of failure.

Evaluate the evidence and question presented to you. Vote for the outcome that best advances improvement and evolution of the governance system.`

// NewReformer creates a Reformer juror with the shared schema/template.
// If systemPrompt is empty, the default is used.
func NewReformer(
	client *flow.Client,
	systemPrompt string,
	schemaBytes []byte,
	queryTmpl *template.Template,
) (Juror, error) {
	if systemPrompt == "" {
		systemPrompt = DefaultReformerPrompt
	}
	base, err := NewBaseJuror("reformer", client, systemPrompt, schemaBytes, queryTmpl)
	if err != nil {
		return nil, err
	}
	return &reformer{baseJuror: base}, nil
}
