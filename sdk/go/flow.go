package flow

import (
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// Flow wraps the flow topology response for the current namespace.
// Constructed by Client.GetFlow() or Workitem.GetTopology().
type Flow struct {
	resp *flowv1.GetFlowTopologyResponse
}

// newFlow constructs a Flow from a topology response.
func newFlow(resp *flowv1.GetFlowTopologyResponse) *Flow {
	return &Flow{resp: resp}
}

// GetExitContract flattens the proto's StampRequirements into a simple
// map of governed artefact name to required stamp names.
// Not cached — the map is small and this is an all-local operation.
func (f *Flow) GetExitContract() map[string][]string {
	ec := f.resp.GetExitContract()
	if ec == nil {
		return nil
	}
	out := make(map[string][]string, len(ec))
	for k, v := range ec {
		out[k] = v.GetStamps()
	}
	return out
}
