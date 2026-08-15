package nodeutil

import "github.com/foundry/flow/pkg/metadata"

// ChoiceEntry is a single entry in the GET /choices response served by HITL
// node binaries so the Dashboard can build the choice UI. The wire contract
// is defined once in the shared metadata package and re-exported here.
type ChoiceEntry = metadata.Choice

// ChoicesResponse is the JSON body returned by GET /choices.
type ChoicesResponse = metadata.ChoicesResponse
