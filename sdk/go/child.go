package flow

import (
	"context"
	"fmt"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// ChildWorkitem is a handle to a child Workitem created by the parent.
// It provides the same artefact and routing operations as the parent's
// Client, but scoped to the child Workitem. The handle is returned by
// Client.CreateChildWorkitem and should not be constructed directly.
type ChildWorkitem struct {
	id        string
	parent    *Client
	archivist flowv1.ArchivistServiceClient
	operator  flowv1.OperatorServiceClient
}

// ID returns the child Workitem identifier.
func (cw *ChildWorkitem) ID() string {
	return cw.id
}

// StoreArtefact stores content as a named artefact on the child Workitem.
// The target_workitem_id is set to the child's ID so the Archivist stores
// the artefact against the child's scope.
func (cw *ChildWorkitem) StoreArtefact(
	ctx context.Context, artefactID, governedArtefact string, content []byte,
) (*flowv1.StoreArtefactResponse, error) {
	resp, err := cw.archivist.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       cw.id,
		ArtefactId:       artefactID,
		GovernedArtefact: governedArtefact,
		Content:          content,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: child store artefact failed: %w", err)
	}
	return resp, nil
}

// StampArtefact applies a named governance stamp to the specified artefact
// on the child Workitem.
func (cw *ChildWorkitem) StampArtefact(
	ctx context.Context, artefactID, stampName string,
) (*flowv1.StampArtefactResponse, error) {
	resp, err := cw.archivist.StampArtefact(ctx, &flowv1.StampArtefactRequest{
		WorkitemId: cw.id,
		ArtefactId: artefactID,
		StampName:  stampName,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: child stamp artefact failed: %w", err)
	}
	return resp, nil
}

// RouteTo routes the child Workitem to the named target node.
// The child must be in Pending state (not yet routed).
func (cw *ChildWorkitem) RouteTo(ctx context.Context, targetNode string) (bool, error) {
	resp, err := cw.operator.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: cw.id,
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO,
			Target: targetNode,
		},
	})
	if err != nil {
		return false, fmt.Errorf("flow sdk: child route to failed: %w", err)
	}
	return resp.GetAccepted(), nil
}

// RouteToOutput routes the child Workitem through the named output channel.
// The child must be in Pending state (not yet routed).
func (cw *ChildWorkitem) RouteToOutput(ctx context.Context, outputName string) (bool, error) {
	resp, err := cw.operator.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: cw.id,
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type:   flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO_OUTPUT,
			Target: outputName,
		},
	})
	if err != nil {
		return false, fmt.Errorf("flow sdk: child route to output failed: %w", err)
	}
	return resp.GetAccepted(), nil
}

// Complete marks the child Workitem as complete with a simple completion
// (no exit contract validation). The child must be assigned to a node.
func (cw *ChildWorkitem) Complete(ctx context.Context) (bool, error) {
	resp, err := cw.operator.RouteChild(ctx, &flowv1.RouteChildRequest{
		ChildWorkitemId: cw.id,
		RoutingInstruction: &flowv1.RoutingInstruction{
			Type: flowv1.RoutingType_ROUTING_TYPE_COMPLETE,
		},
	})
	if err != nil {
		return false, fmt.Errorf("flow sdk: child complete failed: %w", err)
	}
	return resp.GetAccepted(), nil
}

// ChildWorkitemStatus holds the status of a child Workitem as returned
// by Client.GetChildren.
type ChildWorkitemStatus struct {
	WorkitemID      string
	Phase           string
	CurrentAssignee string
	Artefacts       []*flowv1.ArtefactRef
}

// ChildLifecycleEvent represents a lifecycle phase change for a child
// Workitem, received via Client.WatchChildren.
type ChildLifecycleEvent struct {
	WorkitemID string
	Phase      string
	NodeID     string
}
