package flow

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fanoutSpy — configurable spy for fan-out / await / collect tests.
type fanoutSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// CreateChildWorkitem state.
	childCounter int
	createErr    error // if set, CreateChildWorkitem returns this error

	// RouteChild state.
	routeChildErr  error
	routedChildren []string

	// StoreArtefact state.
	storeErr        error
	storedArtefacts []storedArt

	// GetChildren state — returns getChildrenResp on each call.
	// If getChildrenFunc is set, it takes priority.
	getChildrenResp *flowv1.GetChildrenResponse
	getChildrenFunc func() (*flowv1.GetChildrenResponse, error)

	// GetArtefact state.
	artefactData map[string][]byte // key: "childID/artefactID" → content
	getArtErr    error

	// PauseTimer / ResumeTimer tracking.
	pauseCalls  atomic.Int32
	resumeCalls atomic.Int32
	pauseErr    error
	resumeErr   error
}

type storedArt struct {
	workitemID       string
	artefactID       string
	governedArtefact string
	content          []byte
}

func (s *fanoutSpy) CreateChildWorkitem(
	_ context.Context, _ *flowv1.CreateChildWorkitemRequest,
) (*flowv1.CreateChildWorkitemResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return nil, s.createErr
	}
	s.childCounter++
	return &flowv1.CreateChildWorkitemResponse{
		ChildWorkitemId: fmt.Sprintf("child-%03d", s.childCounter),
	}, nil
}

func (s *fanoutSpy) RouteChild(
	_ context.Context, req *flowv1.RouteChildRequest,
) (*flowv1.RouteChildResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.routeChildErr != nil {
		return nil, s.routeChildErr
	}
	s.routedChildren = append(s.routedChildren, req.GetChildWorkitemId())
	return &flowv1.RouteChildResponse{Accepted: true}, nil
}

func (s *fanoutSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storeErr != nil {
		return nil, s.storeErr
	}
	s.storedArtefacts = append(s.storedArtefacts, storedArt{
		workitemID:       req.GetWorkitemId(),
		artefactID:       req.GetArtefactId(),
		governedArtefact: req.GetGovernedArtefact(),
		content:          req.GetContent(),
	})
	return &flowv1.StoreArtefactResponse{VersionHash: "hash-ok", IsNewVersion: true}, nil
}

func (s *fanoutSpy) GetChildren(
	_ context.Context, _ *flowv1.GetChildrenRequest,
) (*flowv1.GetChildrenResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getChildrenFunc != nil {
		return s.getChildrenFunc()
	}
	if s.getChildrenResp != nil {
		return s.getChildrenResp, nil
	}
	return &flowv1.GetChildrenResponse{}, nil
}

func (s *fanoutSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	if s.getArtErr != nil {
		return nil, s.getArtErr
	}
	key := req.GetTargetWorkitemId() + "/" + req.GetArtefactId()
	s.mu.Lock()
	content, ok := s.artefactData[key]
	s.mu.Unlock()
	if !ok {
		return nil, status.Errorf(codes.NotFound, "artefact not found: %s", key)
	}
	return &flowv1.GetArtefactResponse{Content: content}, nil
}

func (s *fanoutSpy) PauseTimer(
	_ context.Context, _ *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	s.pauseCalls.Add(1)
	if s.pauseErr != nil {
		return nil, s.pauseErr
	}
	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

func (s *fanoutSpy) ResumeTimer(
	_ context.Context, _ *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	s.resumeCalls.Add(1)
	if s.resumeErr != nil {
		return nil, s.resumeErr
	}
	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

func (s *fanoutSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

// setGetChildrenResp updates the response returned by GetChildren.
func (s *fanoutSpy) setGetChildrenResp(resp *flowv1.GetChildrenResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getChildrenResp = resp
}

// errorSpyServer returns errors on every Archivist RPC, used for testing error propagation.
type errorSpyServer struct {
	flowv1.UnimplementedArchivistServiceServer
}

func (s *errorSpyServer) GetArtefact(ctx context.Context, req *flowv1.GetArtefactRequest) (*flowv1.GetArtefactResponse, error) {
	return nil, fmt.Errorf("archivist error: get artefact failed")
}

func (s *errorSpyServer) StoreArtefact(ctx context.Context, req *flowv1.StoreArtefactRequest) (*flowv1.StoreArtefactResponse, error) {
	return nil, fmt.Errorf("archivist error: store artefact failed")
}

func (s *errorSpyServer) StampArtefact(ctx context.Context, req *flowv1.StampArtefactRequest) (*flowv1.StampArtefactResponse, error) {
	return nil, fmt.Errorf("archivist error: stamp artefact failed")
}

func (s *errorSpyServer) GetStamps(ctx context.Context, req *flowv1.GetStampsRequest) (*flowv1.GetStampsResponse, error) {
	return nil, fmt.Errorf("archivist error: get stamps failed")
}

func (s *errorSpyServer) HasStamp(ctx context.Context, req *flowv1.HasStampRequest) (*flowv1.HasStampResponse, error) {
	return nil, fmt.Errorf("archivist error: has stamp failed")
}

func (s *errorSpyServer) GetFeedback(ctx context.Context, req *flowv1.GetFeedbackRequest) (*flowv1.GetFeedbackResponse, error) {
	return nil, fmt.Errorf("archivist error: get feedback failed")
}

func (s *errorSpyServer) HasUnresolvedFeedback(ctx context.Context, req *flowv1.HasUnresolvedFeedbackRequest) (*flowv1.HasUnresolvedFeedbackResponse, error) {
	return nil, fmt.Errorf("archivist error: has unresolved feedback failed")
}
