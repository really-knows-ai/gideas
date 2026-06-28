package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeutil"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// testWorkitemID is the workitem ID used across all facilitator tests.
const testWorkitemID = "test-workitem"

// newSpyGRPCServer creates a gRPC server with the facilitatorSpy registered
// for the five Foundry Flow service interfaces the Facilitator depends on.
func newSpyGRPCServer(spy *facilitatorSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// facilitatorSpy captures calls to service operations for test assertions.
// It supports the full Facilitator lifecycle: topology discovery, feedback
// scanning, evidence assembly (5 artefacts), child creation, child artefact
// storage, child routing, suspend, and post-resume routing/completion.
type facilitatorSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// ── Configurable inputs ─────────────────────────────────────────

	// TopologyResponse is returned by GetFlowTopology.
	TopologyResponse *flowv1.GetFlowTopologyResponse

	// FeedbackItemsByArtefact is returned by GetFeedback. Keyed by artefact ID;
	// if nil the flat FeedbackItemsFlat list is returned for all artefact IDs.
	FeedbackItemsByArtefact map[string][]*flowv1.FeedbackItem
	FeedbackItemsFlat       []*flowv1.FeedbackItem

	// ArtefactContentByID maps artefact IDs to their content for parent
	// workitem GetArtefact calls. Falls back to ArtefactContent if the
	// ID is not in the map.
	ArtefactContentByID map[string][]byte

	// ArtefactContent is the default content returned by GetArtefact for
	// parent workitem requests when ArtefactContentByID has no match.
	ArtefactContent []byte

	// LawsByID maps law IDs to their Law objects for GetLaw calls.
	LawsByID map[string]*flowv1.Law

	// Laws is returned by QueryLaws.
	Laws []*flowv1.Law

	// FrictionAggregates is returned by QueryFriction when no filter-aware
	// callback is set. Use FrictionByFilter for filtered queries.
	FrictionAggregates []*flowv1.FrictionAggregate

	// FrictionByFilter allows tests to return different friction data
	// depending on the filter. When set, takes precedence over
	// FrictionAggregates.
	FrictionByFilter func(filter *flowv1.FrictionFilter) []*flowv1.FrictionAggregate

	// Children is returned by GetChildren. When non-nil, overrides auto-
	// generation from CreatedChildren.
	Children []*flowv1.ChildWorkitemStatus

	// Auto-created child IDs (returned by CreateChildWorkitem).
	nextChildID int

	// ── Configurable error returns ──────────────────────────────────

	GetFlowTopologyErr error
	GetFeedbackErr     error
	GetArtefactErr     error
	GetLawErr          error
	QueryLawsErr       error
	QueryFrictionErr   error
	RouteToOutputErr   error
	CompleteErr        error
	SuspendErr         error
	CreateChildErr     error
	RouteChildErr      error
	GetChildrenErr     error
	StoreArtefactErr   error

	// ── Recorded operations for assertions ──────────────────────────

	// RoutedOutputs records output names passed to RouteToOutput.
	RoutedOutputs []string

	// CompletedReasons records CompletionReason from Complete actions.
	CompletedReasons []flowv1.CompletionReason

	// SuspendActions records suspend conditions and timeouts.
	SuspendActions []suspendRecord

	// CreatedChildren records child workitem IDs returned by
	// CreateChildWorkitem.
	CreatedChildren []string

	// RoutedChildren records child routing instructions (child ID → target).
	RoutedChildren []routedChild

	// ChildStoredArtefacts records artefact content stored on child
	// workitems, keyed as "childID:artefactID".
	ChildStoredArtefacts map[string][]byte

	// StoreArtefactCalls records parent artefact store operations.
	StoreArtefactCalls []storeArtefactRecord

	// TelemetryEvents records telemetry events emitted via RecordTelemetry.
	TelemetryEvents []telemetryRecord

	// GetLawCalls records law IDs requested via GetLaw.
	GetLawCalls []string

	// QueryFrictionCalls records friction filter requests.
	QueryFrictionCalls []*flowv1.FrictionFilter
}

// suspendRecord captures a Suspend action for assertion.
type suspendRecord struct {
	Condition string
	Timeout   string // Duration string, empty if unset.
}

// storeArtefactRecord captures a parent artefact store for assertion.
type storeArtefactRecord struct {
	ArtefactID string
	Content    []byte
}

// routedChild captures a child routing instruction for assertion.
type routedChild struct {
	ChildID    string
	TargetNode string
}

// telemetryRecord captures a telemetry event for assertion.
type telemetryRecord struct {
	EventType string
	Payload   map[string]any
}

func newFacilitatorSpy() *facilitatorSpy {
	return &facilitatorSpy{
		TopologyResponse:     defaultFacilitatorTopology(),
		ArtefactContent:      []byte("sample artefact content"),
		ChildStoredArtefacts: make(map[string][]byte),
	}
}

// defaultFacilitatorTopology returns a topology appropriate for facilitator
// tests. Includes the facilitator self node with a "resolved" output and
// an arbiter target.
func defaultFacilitatorTopology() *flowv1.GetFlowTopologyResponse {
	return &flowv1.GetFlowTopologyResponse{
		Self: &flowv1.FlowNode{
			Name: "facilitator",
			Outputs: []*flowv1.FlowOutput{
				{Name: "resolved", Target: "sort"},
			},
		},
		Nodes: map[string]*flowv1.FlowNode{
			"facilitator": {Name: "facilitator"},
			"arbiter":     {Name: "arbiter"},
			"sort":        {Name: "sort"},
		},
		ExitContract: map[string]*flowv1.StampRequirements{
			"haiku": {Stamps: []string{"linter", "review", "approval"}},
		},
	}
}

// defaultDeadlockedFeedback returns a pair of deadlocked feedback items
// with different severities. fb-1 is HIGH (with citations), fb-2 is LOW.
func defaultDeadlockedFeedback() []*flowv1.FeedbackItem {
	return []*flowv1.FeedbackItem{
		{
			Id:      "fb-1",
			Source:  "reviewer-A",
			State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
			Message: "The haiku does not follow traditional kigo conventions.",
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_Citation{
					Citation: &flowv1.Citation{
						CitationIds: []string{"law-kigo"},
					},
				},
			},
			History: []*flowv1.FeedbackEvent{
				{Action: "raised", Actor: "reviewer-A", Message: "Missing seasonal reference."},
				{Action: "challenged", Actor: "forge", Message: "The seasonal reference is implicit."},
			},
		},
		{
			Id:      "fb-2",
			Source:  "reviewer-B",
			State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
			Message: "Line count is ambiguous.",
			Justification: &flowv1.Justification{
				Kind: &flowv1.Justification_NovelArgument{
					NovelArgument: &flowv1.NovelArgument{
						Argument: "A strict reading requires exactly 5-7-5 syllables.",
					},
				},
			},
		},
	}
}

// defaultWorkitemContext returns a WorkitemContext for tests.
func defaultWorkitemContext() *flowv1.WorkitemContext {
	return &flowv1.WorkitemContext{
		WorkitemId:    testWorkitemID,
		FlowNamespace: "test-ns",
		NodeId:        "facilitator",
		Metadata: map[string]string{
			"request_id": "req-123",
		},
	}
}

// setupFacilitatorTest creates a flow.Client backed by the spy, suitable for
// calling handleFacilitator directly.
func setupFacilitatorTest(t *testing.T, spy *facilitatorSpy) *flow.Client {
	t.Helper()

	lis, err := nodeutil.NewLocalListener()
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := newSpyGRPCServer(spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	t.Setenv(flow.EnvWorkitemID, testWorkitemID)
	client, err := flow.NewClient(
		flow.WithSidecarAddress(lis.Addr().String()),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *facilitatorSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *facilitatorSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch a := req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Route:
		if s.RouteToOutputErr != nil {
			return nil, s.RouteToOutputErr
		}
		if a.Route != nil {
			s.RoutedOutputs = append(s.RoutedOutputs, a.Route.GetTarget())
		}

	case *flowv1.SubmitResultRequest_Complete:
		if s.CompleteErr != nil {
			return nil, s.CompleteErr
		}
		reason := flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED
		if a.Complete != nil {
			reason = a.Complete.GetReason()
		}
		s.CompletedReasons = append(s.CompletedReasons, reason)

	case *flowv1.SubmitResultRequest_Suspend:
		if s.SuspendErr != nil {
			return nil, s.SuspendErr
		}
		rec := suspendRecord{}
		if a.Suspend != nil {
			rec.Condition = a.Suspend.GetCondition()
			if a.Suspend.GetTimeout() != nil {
				rec.Timeout = a.Suspend.GetTimeout().AsDuration().String()
			}
		}
		s.SuspendActions = append(s.SuspendActions, rec)

	case nil:
		// No action set — treat as no-op.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *facilitatorSpy) RecordTelemetry(
	_ context.Context, req *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var payload map[string]any
	if len(req.GetPayload()) > 0 {
		_ = json.Unmarshal(req.GetPayload(), &payload)
	}
	s.TelemetryEvents = append(s.TelemetryEvents, telemetryRecord{
		EventType: req.GetEventType(),
		Payload:   payload,
	})
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Operator methods
// ---------------------------------------------------------------------------

func (s *facilitatorSpy) GetFlowTopology(
	_ context.Context, _ *flowv1.GetFlowTopologyRequest,
) (*flowv1.GetFlowTopologyResponse, error) {
	if s.GetFlowTopologyErr != nil {
		return nil, s.GetFlowTopologyErr
	}
	return s.TopologyResponse, nil
}

func (s *facilitatorSpy) CreateChildWorkitem(
	_ context.Context, _ *flowv1.CreateChildWorkitemRequest,
) (*flowv1.CreateChildWorkitemResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.CreateChildErr != nil {
		return nil, s.CreateChildErr
	}

	s.nextChildID++
	childID := fmt.Sprintf("child-%d", s.nextChildID)
	s.CreatedChildren = append(s.CreatedChildren, childID)
	return &flowv1.CreateChildWorkitemResponse{ChildWorkitemId: childID}, nil
}

func (s *facilitatorSpy) RouteChild(
	_ context.Context, req *flowv1.RouteChildRequest,
) (*flowv1.RouteChildResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.RouteChildErr != nil {
		return nil, s.RouteChildErr
	}

	s.RoutedChildren = append(s.RoutedChildren, routedChild{
		ChildID:    req.GetChildWorkitemId(),
		TargetNode: req.GetRoutingInstruction().GetTarget(),
	})
	return &flowv1.RouteChildResponse{Accepted: true}, nil
}

func (s *facilitatorSpy) GetChildren(
	_ context.Context, _ *flowv1.GetChildrenRequest,
) (*flowv1.GetChildrenResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.GetChildrenErr != nil {
		return nil, s.GetChildrenErr
	}

	// If explicit children are configured, return them.
	if len(s.Children) > 0 {
		return &flowv1.GetChildrenResponse{Children: s.Children}, nil
	}

	// No children — first invocation scenario.
	return &flowv1.GetChildrenResponse{}, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *facilitatorSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	if s.GetArtefactErr != nil {
		return nil, s.GetArtefactErr
	}

	// Child artefact request (has TargetWorkitemId).
	if target := req.GetTargetWorkitemId(); target != "" {
		key := target + ":" + req.GetArtefactId()
		content, ok := s.ChildStoredArtefacts[key]
		if !ok {
			return nil, status.Errorf(codes.NotFound, "child artefact %q not found", key)
		}
		return &flowv1.GetArtefactResponse{Content: content}, nil
	}

	// Per-artefact-ID content if configured.
	if s.ArtefactContentByID != nil {
		if content, ok := s.ArtefactContentByID[req.GetArtefactId()]; ok {
			return &flowv1.GetArtefactResponse{Content: content}, nil
		}
	}

	return &flowv1.GetArtefactResponse{
		Content: s.ArtefactContent,
	}, nil
}

func (s *facilitatorSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.StoreArtefactErr != nil {
		return nil, s.StoreArtefactErr
	}

	// Distinguish between parent and child artefact stores.
	if req.GetWorkitemId() != "" && req.GetWorkitemId() != testWorkitemID {
		key := req.GetWorkitemId() + ":" + req.GetArtefactId()
		s.ChildStoredArtefacts[key] = req.GetContent()
	} else {
		s.StoreArtefactCalls = append(s.StoreArtefactCalls, storeArtefactRecord{
			ArtefactID: req.GetArtefactId(),
			Content:    req.GetContent(),
		})
	}
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "mock-hash",
		IsNewVersion: true,
	}, nil
}

func (s *facilitatorSpy) GetFeedback(
	_ context.Context, req *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	if s.GetFeedbackErr != nil {
		return nil, s.GetFeedbackErr
	}

	// If per-artefact feedback is configured, use it.
	if s.FeedbackItemsByArtefact != nil {
		items := s.FeedbackItemsByArtefact[req.GetArtefactId()]
		return &flowv1.GetFeedbackResponse{FeedbackItems: items}, nil
	}

	return &flowv1.GetFeedbackResponse{
		FeedbackItems: s.FeedbackItemsFlat,
	}, nil
}

// ---------------------------------------------------------------------------
// Librarian methods
// ---------------------------------------------------------------------------

func (s *facilitatorSpy) QueryLaws(
	_ context.Context, _ *flowv1.QueryLawsRequest,
) (*flowv1.QueryLawsResponse, error) {
	if s.QueryLawsErr != nil {
		return nil, s.QueryLawsErr
	}
	return &flowv1.QueryLawsResponse{Laws: s.Laws}, nil
}

func (s *facilitatorSpy) GetLaw(
	_ context.Context, req *flowv1.GetLawRequest,
) (*flowv1.GetLawResponse, error) {
	s.mu.Lock()
	s.GetLawCalls = append(s.GetLawCalls, req.GetLawId())
	s.mu.Unlock()

	if s.GetLawErr != nil {
		return nil, s.GetLawErr
	}

	if s.LawsByID != nil {
		law, ok := s.LawsByID[req.GetLawId()]
		if !ok {
			return nil, status.Errorf(codes.NotFound, "law %q not found", req.GetLawId())
		}
		return &flowv1.GetLawResponse{Law: law}, nil
	}

	return nil, status.Errorf(codes.NotFound, "law %q not found", req.GetLawId())
}

// ---------------------------------------------------------------------------
// FrictionLedger methods
// ---------------------------------------------------------------------------

func (s *facilitatorSpy) QueryFriction(
	_ context.Context, req *flowv1.QueryFrictionRequest,
) (*flowv1.QueryFrictionResponse, error) {
	s.mu.Lock()
	s.QueryFrictionCalls = append(s.QueryFrictionCalls, req.GetFilter())
	s.mu.Unlock()

	if s.QueryFrictionErr != nil {
		return nil, s.QueryFrictionErr
	}

	if s.FrictionByFilter != nil {
		aggs := s.FrictionByFilter(req.GetFilter())
		return &flowv1.QueryFrictionResponse{FrictionAggregates: aggs}, nil
	}

	return &flowv1.QueryFrictionResponse{
		FrictionAggregates: s.FrictionAggregates,
	}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// getChildArtefact returns the content stored on the given child for the
// given artefact ID. Returns empty string if not found.
func (s *facilitatorSpy) getChildArtefact(childID, artefactID string) string {
	key := childID + ":" + artefactID
	content, ok := s.ChildStoredArtefacts[key]
	if !ok {
		return ""
	}
	return string(content)
}

// getChildDisputedRef parses the disputed-artefact JSON stored on the given
// child. Returns nil if not found or unparseable.
func (s *facilitatorSpy) getChildDisputedRef(childID string) *disputedArtefactRef {
	key := childID + ":" + artefactDisputedRef
	content, ok := s.ChildStoredArtefacts[key]
	if !ok {
		return nil
	}
	var ref disputedArtefactRef
	if err := json.Unmarshal(content, &ref); err != nil {
		return nil
	}
	return &ref
}

// findTelemetry returns the first telemetry event matching the given type,
// or nil if not found.
func (s *facilitatorSpy) findTelemetry(eventType string) *telemetryRecord {
	for i := range s.TelemetryEvents {
		if s.TelemetryEvents[i].EventType == eventType {
			return &s.TelemetryEvents[i]
		}
	}
	return nil
}

// telemetryTypes returns all recorded telemetry event types in order.
func (s *facilitatorSpy) telemetryTypes() []string {
	types := make([]string, len(s.TelemetryEvents))
	for i, ev := range s.TelemetryEvents {
		types[i] = ev.EventType
	}
	return types
}
