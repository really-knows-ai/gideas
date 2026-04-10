package flow

import (
	"context"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// ForgeContract defines the boundary between the Forge handler and its
// agent implementation. The handler provides structured inputs (the
// creative brief and applicable laws); the agent returns the generated
// content as a string.
//
// Prompts, schemas, model choice, and output parsing are all encapsulated
// in the agent implementation — the handler never sees them.
type ForgeContract interface {
	// Run generates content from the given input brief and governance laws.
	// The input is the concatenated content of the node's configured input
	// artefacts. Laws are the active governance laws for the governed
	// artefact kind.
	Run(ctx context.Context, input string, laws []*flowv1.Law) (string, error)
}
