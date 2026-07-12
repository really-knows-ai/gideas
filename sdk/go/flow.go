package flow

import (
	"sort"
	"sync"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// Flow wraps the flow topology response for the current namespace.
// Constructed by Client.GetFlow() or Workitem.GetTopology().
type Flow struct {
	resp     *flowv1.GetFlowTopologyResponse
	nodes    []*Node   // cached on first call to GetNodes()
	nodeOnce sync.Once // guards nodes slice construction
	name     string    // the flow namespace
}

// newFlow constructs a Flow from a topology response and namespace string.
func newFlow(resp *flowv1.GetFlowTopologyResponse, namespace string) *Flow {
	return &Flow{
		resp: resp,
		name: namespace,
	}
}

// GetName returns the flow namespace, which serves as the flow identifier.
func (f *Flow) GetName() string { return f.name }

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

// GetNodes lazily constructs and caches the Node slice from the topology
// response. The slice is built once via sync.Once for thread safety.
func (f *Flow) GetNodes() []*Node {
	f.nodeOnce.Do(func() {
		nodes := f.resp.GetNodes()
		if len(nodes) == 0 {
			return
		}
		f.nodes = make([]*Node, 0, len(nodes))
		for _, pb := range nodes {
			f.nodes = append(f.nodes, newNode(pb))
		}
	})
	return f.nodes
}

// GetNodeOrder returns the node names in alphabetical order.
// ponytail: Uses sort.Strings for deterministic ordering. If CRD-defined
// order is needed later, this can be extended with an explicit field without
// changing the public API.
func (f *Flow) GetNodeOrder() []string {
	nodes := f.resp.GetNodes()
	if len(nodes) == 0 {
		return nil
	}
	names := make([]string, 0, len(nodes))
	for k := range nodes {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
