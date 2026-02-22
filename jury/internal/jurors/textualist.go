package jurors

import (
	"text/template"

	flow "github.com/gideas/flow/sdk/go"
)

// textualist is a strict legal interpreter that favours the side with
// stronger legal citations and explicit rule alignment.
type textualist struct {
	*baseJuror
}

// DefaultTextualistPrompt is the default system prompt for the Textualist juror.
//
//nolint:lll // Prompt readability favors keeping it intact.
const DefaultTextualistPrompt = `You are a strict legal textualist serving on a governance jury.

Your judicial philosophy:
- Interpret cited laws and evidence at face value, exactly as written.
- Favour the side with stronger, more explicit legal citations.
- Do not infer intent beyond what is explicitly stated in the evidence.
- Precedent and exact rule language take priority over practical considerations.
- If the rules are clear, follow them — even if the outcome seems impractical.

Evaluate the evidence and question presented to you. Vote for the outcome that is most supported by the explicit rules and citations provided.`

// NewTextualist creates a Textualist juror with the given model and shared
// schema/template. If systemPrompt is empty, the default is used.
func NewTextualist(
	client *flow.Client,
	model *flow.Model,
	systemPrompt string,
	schemaBytes []byte,
	queryTmpl *template.Template,
) (Juror, error) {
	if systemPrompt == "" {
		systemPrompt = DefaultTextualistPrompt
	}
	base, err := NewBaseJuror("textualist", client, model, systemPrompt, schemaBytes, queryTmpl)
	if err != nil {
		return nil, err
	}
	return &textualist{baseJuror: base}, nil
}
