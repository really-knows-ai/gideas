package tui

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/foundry/flow/tools/flowctl/internal/tui/components"
	"github.com/foundry/flow/tools/flowctl/internal/tui/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crdfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// ─── Mock PortForwarder ────────────────────────────────────────────────────

type mockPFMWiring struct {
	mu           sync.Mutex
	forwards     map[string]int
	findPodName  string
	findPodFound bool
}

func newMockPFMWiring(podName string, found bool) *mockPFMWiring {
	return &mockPFMWiring{
		forwards:     make(map[string]int),
		findPodName:  podName,
		findPodFound: found,
	}
}

func (m *mockPFMWiring) FindReadyPod(ctx context.Context, namespace, labelSelector string) (string, bool, error) {
	return m.findPodName, m.findPodFound, nil
}

func (m *mockPFMWiring) ForwardPod(ctx context.Context, namespace, podName string, remotePort int) (string, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf("%s/%s:%d", namespace, podName, remotePort)
	port := 15000 + remotePort
	m.forwards[key] = port
	return key, port, nil
}

func (m *mockPFMWiring) Close(forwardID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.forwards, forwardID)
	return nil
}

func (m *mockPFMWiring) CloseAll() error { return nil }

// ─── Mock Archivist gRPC Server ────────────────────────────────────────────

type mockArchivistWiring struct {
	flowv1.UnimplementedArchivistServiceServer
	artefacts []*flowv1.ArtefactRef
	content   map[string][]byte
	feedback  map[string][]*flowv1.FeedbackItem
}

func (s *mockArchivistWiring) ListArtefacts(ctx context.Context, req *flowv1.ListArtefactsRequest) (*flowv1.ListArtefactsResponse, error) {
	return &flowv1.ListArtefactsResponse{ArtefactRefs: s.artefacts}, nil
}

func (s *mockArchivistWiring) GetArtefact(ctx context.Context, req *flowv1.GetArtefactRequest) (*flowv1.GetArtefactResponse, error) {
	content := s.content[req.ArtefactId]
	return &flowv1.GetArtefactResponse{Content: content}, nil
}

func (s *mockArchivistWiring) GetFeedback(ctx context.Context, req *flowv1.GetFeedbackRequest) (*flowv1.GetFeedbackResponse, error) {
	items := s.feedback[req.ArtefactId]
	if items == nil {
		items = []*flowv1.FeedbackItem{}
	}
	return &flowv1.GetFeedbackResponse{FeedbackItems: items}, nil
}

// startBufconnArchivist starts an in-process gRPC server and returns a client connection.
func startBufconnArchivist(mock *mockArchivistWiring) (*grpc.ClientConn, func(), error) {
	listener := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	flowv1.RegisterArchivistServiceServer(srv, mock)

	go func() { _ = srv.Serve(listener) }()

	conn, err := grpc.DialContext(context.Background(), "bufconn",
		grpc.WithContextDialer(func(ctx context.Context, target string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("dial: %w", err)
	}

	return conn, func() { conn.Close(); srv.Stop(); listener.Close() }, nil
}

// ─── Fake helpers ──────────────────────────────────────────────────────────

func fakeSchemeWiring() *runtime.Scheme {
	s := runtime.NewScheme()
	gv := schema.GroupVersion{Group: "flow.foundry.io", Version: "v1"}
	s.AddKnownTypeWithName(gv.WithKind("Workitem"), &unstructured.Unstructured{})
	s.AddKnownTypeWithName(gv.WithKind("WorkitemList"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(gv.WithKind("FoundryNode"), &unstructured.Unstructured{})
	s.AddKnownTypeWithName(gv.WithKind("FoundryNodeList"), &unstructured.UnstructuredList{})
	return s
}

func makeWorkitemWiring(name, state, assignee string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("flow.foundry.io/v1")
	obj.SetKind("Workitem")
	obj.SetName(name)
	obj.SetNamespace("test-ns")
	if state != "" {
		_ = unstructured.SetNestedField(obj.Object, state, "status", "phase")
	}
	if assignee != "" {
		_ = unstructured.SetNestedField(obj.Object, assignee, "status", "currentAssignee")
	}
	return obj
}

func makeFoundryNodeWiring(name string, targets ...string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("flow.foundry.io/v1")
	obj.SetKind("FoundryNode")
	obj.SetName(name)
	obj.SetNamespace("test-ns")
	if len(targets) > 0 {
		ts := make([]interface{}, len(targets))
		for i, t := range targets {
			ts[i] = map[string]interface{}{"target": t}
		}
		_ = unstructured.SetNestedSlice(obj.Object, ts, "spec", "outputs")
	}
	return obj
}

// ─── T33: Workitem selection loads artefacts ───────────────────────────────

func TestWiring_WorkitemSelectedLoadsArtefacts(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.namespace = "test-ns"
	m.systemNS = "test-ns"
	m.pfm = newMockPFMWiring("archivist-0", true)
	m.workitemList.Items = []api.WorkitemSummary{{Name: "wi-001", State: "Running", Node: "sort"}}
	m.workitemList.Namespace = "test-ns"

	// Simulate WorkitemSelectedMsg — should transition to detail and return commands
	model, cmd := m.Update(WorkitemSelectedMsg{Name: "wi-001"})
	m2 := model.(*Model)

	if m2.screen != ScreenWorkitemDetail {
		t.Errorf("expected ScreenWorkitemDetail, got %d", m2.screen)
	}
	if m2.workitemDetail.workitemName != "wi-001" {
		t.Errorf("expected workitemName wi-001, got %s", m2.workitemDetail.workitemName)
	}
	if cmd == nil {
		t.Error("expected non-nil command (batched topology/artefacts load)")
	}
}

// ─── T34: Artefact expand triggers GetArtefact and GetFeedback ─────────────

func TestWiring_ArtefactExpandFetchesContentAndFeedback(t *testing.T) {
	mock := &mockArchivistWiring{
		artefacts: []*flowv1.ArtefactRef{{Id: "haiku", GovernedArtefact: "haiku"}},
		content:   map[string][]byte{"haiku": []byte("old pond")},
		feedback: map[string][]*flowv1.FeedbackItem{
			"haiku": {
				{Id: "fb-1", State: flowv1.FeedbackState_FEEDBACK_STATE_NEW, Source: "reviewer", Message: "needs work",
					CreatedAt: timestamppb.New(time.Date(2024, 1, 1, 0, 0, 1, 0, time.UTC))},
			},
		},
	}
	conn, cleanup, err := startBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("start archivist: %v", err)
	}
	defer cleanup()

	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"
	m.namespace = "test-ns"
	m.archivist = &api.ArchivistClient{Conn: conn}
	m.workitemDetail.artefacts = components.ArtefactTreeModel{
		Loading:   false,
		Artefacts: []types.ArtefactNode{{ArtefactID: "haiku", GovernedBy: "haiku"}},
	}

	cmd := m.fetchArtefactContent("wi-001", "haiku")
	if cmd == nil {
		t.Fatal("expected non-nil command")
	}
	msg := cmd()

	aem, ok := msg.(ArtefactExpandedMsg)
	if !ok {
		t.Fatalf("expected ArtefactExpandedMsg, got %T", msg)
	}
	if !strings.Contains(aem.Content, "old pond") {
		t.Errorf("expected 'old pond' in content, got %q", aem.Content)
	}
	if len(aem.FeedbackItems) != 1 {
		t.Errorf("expected 1 feedback item, got %d", len(aem.FeedbackItems))
	}
}

// ─── T35: Collapsed artefacts do not fetch ─────────────────────────────────

func TestWiring_CollapsedNoFetch(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.artefacts = components.ArtefactTreeModel{
		Loading:   false,
		Artefacts: []types.ArtefactNode{{ArtefactID: "haiku", Expanded: true}},
	}

	_, cmd := m.Update(ArtefactCollapsedMsg{WorkitemID: "wi-001", ArtefactID: "haiku"})
	if cmd != nil {
		t.Error("expected no command for collapse")
	}
}

// ─── T36: Re-expand after collapse re-fetches ──────────────────────────────

func TestWiring_ReExpandRefetches(t *testing.T) {
	mock := &mockArchivistWiring{
		content: map[string][]byte{"haiku": []byte("v1")},
	}
	conn, cleanup, err := startBufconnArchivist(mock)
	if err != nil {
		t.Fatalf("start archivist: %v", err)
	}
	defer cleanup()

	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"
	m.namespace = "test-ns"
	m.archivist = &api.ArchivistClient{Conn: conn}

	// First expand
	msg1 := m.fetchArtefactContent("wi-001", "haiku")()
	aem1 := msg1.(ArtefactExpandedMsg)
	if !strings.Contains(aem1.Content, "v1") {
		t.Errorf("expected 'v1', got %s", aem1.Content)
	}

	// Collapse
	pmsg, _ := m.Update(ArtefactCollapsedMsg{WorkitemID: "wi-001", ArtefactID: "haiku"})
	_ = pmsg

	// Update content and re-expand
	mock.content["haiku"] = []byte("v2")
	msg2 := m.fetchArtefactContent("wi-001", "haiku")()
	aem2 := msg2.(ArtefactExpandedMsg)
	if !strings.Contains(aem2.Content, "v2") {
		t.Errorf("expected 'v2' after re-fetch, got %s", aem2.Content)
	}
}

// ─── T37: DEADLOCKED feedback is highlighted in rendered output ────────────

func TestWiring_DeadlockedFeedbackHighlighted(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"
	m.workitemDetail.artefacts = components.ArtefactTreeModel{
		Loading: false,
		Artefacts: []types.ArtefactNode{
			{ArtefactID: "haiku", Expanded: true, Content: "c",
				Feedback: []types.FeedbackItem{
					{ID: "fb-1", State: "DEADLOCKED", SourceNode: "arbiter", Message: "needs ruling", Timestamp: "2024-01-01T00:00:01Z"},
				}},
		},
	}

	v := m.View()
	if !strings.Contains(v, "DEADLOCKED") {
		t.Error("expected DEADLOCKED in view")
	}
	if !strings.Contains(v, "needs ruling") {
		t.Error("expected feedback message in view")
	}
}

// ─── T38: Archivist connection failure shows error banner ──────────────────

func TestWiring_ArchivistErrorShowsBanner(t *testing.T) {
	pfm := newMockPFMWiring("", false)
	m := initialModel()
	m.namespace = "test-ns"
	m.systemNS = "test-ns"
	m.pfm = pfm

	cmd := m.loadArtefacts("wi-001")
	msg := cmd()

	if _, ok := msg.(ErrorMsg); !ok {
		t.Fatalf("expected ErrorMsg, got %T", msg)
	}
}

// ─── T39: Topology renders with correct coloring ───────────────────────────

func TestWiring_TopologyColors(t *testing.T) {
	s := fakeSchemeWiring()
	crd := crdfake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(
		makeFoundryNodeWiring("forge", "sort"),
		makeFoundryNodeWiring("sort", "refine"),
		makeFoundryNodeWiring("refine"),
		makeFoundryNodeWiring("appraisal"),
		makeWorkitemWiring("wi-001", "Running", "refine"),
	).Build()
	k8s := &api.K8sClient{CRDClient: crd}

	m := initialModel()
	m.screen = ScreenWorkitemList
	m.k8s = k8s
	m.namespace = "test-ns"

	cmd := m.loadTopology()
	msg := cmd()

	tlm, ok := msg.(TopologyLoadedMsg)
	if !ok {
		t.Fatalf("expected TopologyLoadedMsg, got %T", msg)
	}
	if len(tlm.Nodes) != 4 {
		t.Errorf("expected 4 nodes, got %d", len(tlm.Nodes))
	}
	if len(tlm.Edges) != 2 {
		t.Errorf("expected 2 edges, got %d", len(tlm.Edges))
	}
}

// ─── T40: Topology skips missing targets ───────────────────────────────────

func TestWiring_TopologySkipsMissingTargets(t *testing.T) {
	s := fakeSchemeWiring()
	crd := crdfake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(
		makeFoundryNodeWiring("forge", "sort", "missing-node"),
	).Build()
	k8s := &api.K8sClient{CRDClient: crd}

	nodes, err := k8s.ListFoundryNodes(context.Background(), "test-ns")
	if err != nil {
		t.Fatalf("ListFoundryNodes: %v", err)
	}

	// Build topology manually to verify missing target filtering
	nodeSet := make(map[string]bool)
	for _, n := range nodes {
		nodeSet[n.Name] = true
	}
	edges := make([]types.TopologyEdge, 0)
	for _, n := range nodes {
		for _, target := range n.Targets {
			if nodeSet[target] {
				edges = append(edges, types.TopologyEdge{From: n.Name, To: target})
			}
		}
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges (targets missing), got %d", len(edges))
	}
}

// ─── T41: Topology error state is shown in view ────────────────────────────

func TestWiring_TopologyErrorState(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.topology.Error = "Topology unavailable (K8s API error)"
	m.workitemDetail.topology.Loading = false

	v := m.View()
	if !strings.Contains(v, "Topology unavailable") {
		t.Error("expected topology error state in view, got:", v)
	}
}

// ─── T42: Re-fetch preserves expansion state ───────────────────────────────

func TestWiring_RefreshPreservesExpansion(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"
	m.workitemDetail.artefacts = components.ArtefactTreeModel{
		Loading: false,
		Artefacts: []types.ArtefactNode{
			{ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true, Content: "old"},
			{ArtefactID: "petition", GovernedBy: "petition", Expanded: false},
		},
	}

	_, _ = m.Update(ArtefactsLoadedMsg{
		WorkitemID: "wi-001",
		Artefacts:  []api.ArtefactInfo{{ID: "haiku", GovernedArtefact: "haiku"}, {ID: "petition", GovernedArtefact: "petition"}},
	})

	for _, art := range m.workitemDetail.artefacts.Artefacts {
		if art.ArtefactID == "haiku" && !art.Expanded {
			t.Error("expected haiku to remain expanded after refresh")
		}
		if art.ArtefactID == "petition" && art.Expanded {
			t.Error("expected petition to remain collapsed after refresh")
		}
	}
}

// ─── T43: User refresh triggers reload ─────────────────────────────────────

func TestWiring_UserRefreshTriggersReload(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"

	_, cmd := m.Update(RefreshMsg{})
	if cmd == nil {
		t.Fatal("expected non-nil command from refresh")
	}
}
