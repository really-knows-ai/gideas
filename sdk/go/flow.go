package flow

import flowv1 "github.com/gideas/flow/gen/flow/v1"

// Flow is a domain object wrapping the flow topology response.
type Flow struct {
	session *session
	pb      *flowv1.GetFlowTopologyResponse
}
