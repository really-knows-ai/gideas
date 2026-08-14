package flow

import (
	"context"
	"fmt"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ChildWorkitem is a handle to a child Workitem created by the parent.
// It provides the same artefact and routing operations as the parent's
// Workitem, but scoped to the child Workitem. The handle is returned by
// Client.CreateChildWorkitem and Workitem.CreateChild.
type ChildWorkitem struct {
	session *session
	id      string
}

// ID returns the child Workitem identifier.
func (c *ChildWorkitem) ID() string {
	return c.id
}

// StoreArtefact stores content as a named artefact on the child Workitem.
func (c *ChildWorkitem) StoreArtefact(artefactID, governedArtefact string, content []byte) error {
	_, err := c.session.Archivist.StoreArtefact(context.Background(), &flowv1.StoreArtefactRequest{
		WorkitemId:       c.id,
		ArtefactId:       artefactID,
		GovernedArtefact: governedArtefact,
		Content:          content,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: child store artefact failed: %w", err)
	}
	return nil
}

// RouteTo routes the child Workitem to the named target node.
// The child must be in Pending state (not yet routed).
func (c *ChildWorkitem) RouteTo(targetNode string) error {
	_, err := c.session.Operator.RouteChild(context.Background(), &flowv1.RouteChildRequest{
		ChildWorkitemId: c.id,
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: targetNode,
		},
	})
	if err != nil {
		return fmt.Errorf("flow sdk: child route to failed: %w", err)
	}
	return nil
}

// ChildWorkitemStatus holds the status of a child Workitem as returned
// by GetChildren.
type ChildWorkitemStatus struct {
	WorkitemID      string
	Phase           string
	CurrentAssignee string
	// CompletionReason is the proto enum string (e.g. "COMPLETION_REASON_CANCELLED")
	// when Phase is "Completed". Empty string otherwise.
	CompletionReason string
}

// ChildLifecycleEvent represents a lifecycle phase change for a child
// Workitem, received via ChildWatcher.Recv.
type ChildLifecycleEvent struct {
	WorkitemID string
	Phase      string
	NodeID     string
}

// Workitem phase constants.
const (
	PhaseRunning   = "Running"
	PhaseCompleted = "Completed"
	PhaseFailed    = "Failed"
)
