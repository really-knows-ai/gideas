package proxy

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// captureOperatorServer captures Operator RPC calls for assertions.
type captureOperatorServer struct {
	flowv1.UnimplementedOperatorServiceServer
	lastTopologyReq *flowv1.GetFlowTopologyRequest
	topologyResp    *flowv1.GetFlowTopologyResponse
	capturedMD      metadata.MD

	// Child Workitem RPC fields.
	createChildResp *flowv1.CreateChildWorkitemResponse
	createChildErr  error
	routeChildResp  *flowv1.RouteChildResponse
	routeChildErr   error
	getChildrenResp *flowv1.GetChildrenResponse
	lastRouteReq    *flowv1.RouteChildRequest

	// ResumeWorkitem RPC fields.
	resumeResp    *flowv1.ResumeWorkitemResponse
	resumeErr     error
	lastResumeReq *flowv1.ResumeWorkitemRequest

	// ListSuspendedWorkitems RPC fields.
	listSuspendedResp    *flowv1.ListSuspendedWorkitemsResponse
	listSuspendedErr     error
	lastListSuspendedReq *flowv1.ListSuspendedWorkitemsRequest
}

func (s *captureOperatorServer) GetFlowTopology(
	ctx context.Context, req *flowv1.GetFlowTopologyRequest,
) (*flowv1.GetFlowTopologyResponse, error) {
	s.lastTopologyReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return s.topologyResp, nil
}

func (s *captureOperatorServer) CreateChildWorkitem(
	ctx context.Context, _ *flowv1.CreateChildWorkitemRequest,
) (*flowv1.CreateChildWorkitemResponse, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	if s.createChildErr != nil {
		return nil, s.createChildErr
	}
	return s.createChildResp, nil
}

func (s *captureOperatorServer) RouteChild(
	ctx context.Context, req *flowv1.RouteChildRequest,
) (*flowv1.RouteChildResponse, error) {
	s.lastRouteReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	if s.routeChildErr != nil {
		return nil, s.routeChildErr
	}
	return s.routeChildResp, nil
}

func (s *captureOperatorServer) GetChildren(
	ctx context.Context, _ *flowv1.GetChildrenRequest,
) (*flowv1.GetChildrenResponse, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	return s.getChildrenResp, nil
}

func (s *captureOperatorServer) ResumeWorkitem(
	ctx context.Context, req *flowv1.ResumeWorkitemRequest,
) (*flowv1.ResumeWorkitemResponse, error) {
	s.lastResumeReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	if s.resumeErr != nil {
		return nil, s.resumeErr
	}
	return s.resumeResp, nil
}

func (s *captureOperatorServer) ListSuspendedWorkitems(
	ctx context.Context, req *flowv1.ListSuspendedWorkitemsRequest,
) (*flowv1.ListSuspendedWorkitemsResponse, error) {
	s.lastListSuspendedReq = req
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.capturedMD = md
	}
	if s.listSuspendedErr != nil {
		return nil, s.listSuspendedErr
	}
	return s.listSuspendedResp, nil
}

const childWI42 = "child-42"

func setupOperatorProxy(t *testing.T) (*OperatorProxy, *captureOperatorServer) {
	t.Helper()

	capture := &captureOperatorServer{
		topologyResp: &flowv1.GetFlowTopologyResponse{
			Self: &flowv1.FlowNode{
				Name:         "sort",
				Capabilities: []string{"READ:flow", "READ:artefact"},
				Outputs: []*flowv1.FlowOutput{
					{Name: "forge", Target: "forge"},
					{Name: "exit", Target: "exit"},
				},
			},
			Nodes: map[string]*flowv1.FlowNode{
				"forge": {
					Name:         "forge",
					Capabilities: []string{"WRITE:artefact"},
				},
			},
			ExitContract: map[string]*flowv1.StampRequirements{
				"txt": {Stamps: []string{"approved"}},
			},
		},
	}

	conn := dialBufconn(t, func(srv *grpc.Server) {
		flowv1.RegisterOperatorServiceServer(srv, capture)
	})

	proxy := &OperatorProxy{
		client: flowv1.NewOperatorServiceClient(conn),
		conn:   conn,
	}

	return proxy, capture
}
