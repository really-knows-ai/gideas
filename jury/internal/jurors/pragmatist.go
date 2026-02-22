package jurors

import (
	"text/template"

	flow "github.com/gideas/flow/sdk/go"
)

// pragmatist weighs practical impact and cost-effectiveness, considering
// friction economics and favouring outcomes that reduce future cost.
type pragmatist struct {
	*baseJuror
}

// DefaultPragmatistPrompt is the default system prompt for the Pragmatist juror.
//
//nolint:lll // Prompt readability favors keeping it intact.
const DefaultPragmatistPrompt = `You are a pragmatic analyst serving on a governance jury.

Your judicial philosophy:
- Weigh practical impact and cost-effectiveness above all else.
- Consider friction economics — favour outcomes that reduce future cost and rework.
- Evaluate whether the proposed outcome is realistic and achievable in practice.
- Rules that cause more harm than good should be questioned.
- The best outcome is the one that produces the most value with the least friction.

Evaluate the evidence and question presented to you. Vote for the outcome that is most practical and cost-effective.`

// NewPragmatist creates a Pragmatist juror with the given model and shared
// schema/template. If systemPrompt is empty, the default is used.
func NewPragmatist(
	client *flow.Client,
	model *flow.Model,
	systemPrompt string,
	schemaBytes []byte,
	queryTmpl *template.Template,
) (Juror, error) {
	if systemPrompt == "" {
		systemPrompt = DefaultPragmatistPrompt
	}
	base, err := NewBaseJuror("pragmatist", client, model, systemPrompt, schemaBytes, queryTmpl)
	if err != nil {
		return nil, err
	}
	return &pragmatist{baseJuror: base}, nil
}
