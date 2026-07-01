package flow

import flowv1 "github.com/gideas/flow/gen/flow/v1"

// Node is a domain object wrapping a flow node.
type Node struct {
	session *session
	pb      *flowv1.FlowNode
}
