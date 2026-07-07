package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gideas/flow/tools/flowctl/internal/tui/types"
)

func TestUpdateNamespaceSelectedTransitionsToWorkitemList(t *testing.T) {
	m := initialModel()
	m.namespaceSelector = m.namespaceSelector.SetNamespaces([]string{"test-ns"}, "")

	model, cmd := m.Update(NamespaceSelectedMsg{Namespace: "test-ns"})
	m2 := model.(*Model)

	if m2.screen != ScreenWorkitemList {
		t.Errorf("expected screen WorkitemList, got %d", m2.screen)
	}
	if m2.workitemList.Namespace != "test-ns" {
		t.Errorf("expected namespace test-ns, got %q", m2.workitemList.Namespace)
	}
	if len(m2.workitemList.Items) == 0 {
		t.Error("expected non-empty workitem list")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateNamespaceLoadErrorStaysOnSelector(t *testing.T) {
	m := initialModel()

	model, cmd := m.Update(NamespaceLoadErrorMsg{Err: errors.New("permission denied")})
	m2 := model.(*Model)

	if m2.screen != ScreenNamespaceSelect {
		t.Errorf("expected screen NamespaceSelect, got %d", m2.screen)
	}
	if m2.namespaceSelector.Error != "permission denied" {
		t.Errorf("expected error 'permission denied', got %q", m2.namespaceSelector.Error)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateNamespaceLoadErrorEmptyListFallback(t *testing.T) {
	m := initialModel()

	model, cmd := m.Update(NamespaceLoadErrorMsg{Err: errors.New("empty list")})
	m2 := model.(*Model)

	if m2.screen != ScreenNamespaceSelect {
		t.Errorf("expected screen NamespaceSelect, got %d", m2.screen)
	}
	if m2.namespaceSelector.Error != "empty list" {
		t.Errorf("expected error 'empty list', got %q", m2.namespaceSelector.Error)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateWorkitemSelectedTransitionsToDetail(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = []types.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: "2m"},
		{Name: "wi-002", State: "Completed", Node: "-", ChildrenCount: 0, Age: "12m"},
	}
	m.workitemList.Namespace = "test-ns"

	model, cmd := m.Update(WorkitemSelectedMsg{Name: "wi-001"})
	m2 := model.(*Model)

	if m2.screen != ScreenWorkitemDetail {
		t.Errorf("expected screen WorkitemDetail, got %d", m2.screen)
	}
	if m2.workitemDetail.workitemName != "wi-001" {
		t.Errorf("expected workitemName wi-001, got %q", m2.workitemDetail.workitemName)
	}
	if m2.workitemDetail.topology.Loading {
		t.Error("expected topology loading=false (fake data populated)")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateWorkitemUpdateModifiesItemInPlace(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = []types.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: "2m"},
	}
	m.workitemList.Namespace = "test-ns"

	model, cmd := m.Update(WorkitemUpdateMsg{
		Item: types.WorkitemSummary{Name: "wi-001", State: "Completed", Node: "-", ChildrenCount: 0, Age: "12m"},
	})
	m2 := model.(*Model)

	if len(m2.workitemList.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(m2.workitemList.Items))
	}
	if m2.workitemList.Items[0].State != "Completed" {
		t.Errorf("expected state Completed, got %q", m2.workitemList.Items[0].State)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateWatchDisconnectedShowsBanner(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = []types.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: "2m"},
	}
	m.workitemList.Namespace = "test-ns"

	model, cmd := m.Update(WatchDisconnectedMsg{Error: errors.New("connection lost")})
	m2 := model.(*Model)

	if !m2.workitemList.Disconnected {
		t.Error("expected Disconnected=true")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateWatchReconnectedHidesBanner(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Disconnected = true
	m.workitemList.Items = []types.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: "2m"},
	}
	m.workitemList.Namespace = "test-ns"

	model, cmd := m.Update(WatchReconnectedMsg{})
	m2 := model.(*Model)

	if m2.workitemList.Disconnected {
		t.Error("expected Disconnected=false")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateCreateStartTransitionsToWizard(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = []types.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: "2m"},
	}
	m.workitemList.Namespace = "test-ns"

	model, cmd := m.Update(CreateStartMsg{})
	m2 := model.(*Model)

	if m2.screen != ScreenCreateWizard {
		t.Errorf("expected screen CreateWizard, got %d", m2.screen)
	}
	if len(m2.createWizard.FoundryFlows) == 0 {
		t.Error("expected wizard to be initialised with fake data")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateCreateSuccessTransitionsToDetail(t *testing.T) {
	m := initialModel()
	m.screen = ScreenCreateWizard

	model, cmd := m.Update(CreateSuccessMsg{WorkitemName: "wi-new-001"})
	m2 := model.(*Model)

	if m2.screen != ScreenWorkitemDetail {
		t.Errorf("expected screen WorkitemDetail, got %d", m2.screen)
	}
	if m2.workitemDetail.workitemName != "wi-new-001" {
		t.Errorf("expected workitemName wi-new-001, got %q", m2.workitemDetail.workitemName)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateCreateCancelReturnsToList(t *testing.T) {
	m := initialModel()
	m.screen = ScreenCreateWizard

	model, cmd := m.Update(CreateCancelMsg{})
	m2 := model.(*Model)

	if m2.screen != ScreenWorkitemList {
		t.Errorf("expected screen WorkitemList, got %d", m2.screen)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateDeleteNonTerminalBlocked(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = []types.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: "2m"},
	}
	m.workitemList.Namespace = "test-ns"

	model, cmd := m.Update(DeleteConfirmMsg{WorkitemName: "wi-001", Phase: "Running"})
	m2 := model.(*Model)

	if m2.screen != ScreenWorkitemList {
		t.Errorf("expected screen unchanged, got %d", m2.screen)
	}
	if m2.err == nil {
		t.Error("expected error for non-terminal delete")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateDeleteTerminalAllowed(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = []types.WorkitemSummary{
		{Name: "wi-001", State: "Completed", Node: "-", ChildrenCount: 0, Age: "12m"},
	}
	m.workitemList.Namespace = "test-ns"

	model, cmd := m.Update(DeleteConfirmMsg{WorkitemName: "wi-001", Phase: "Completed"})
	m2 := model.(*Model)

	if m2.screen != ScreenWorkitemList {
		t.Errorf("expected screen unchanged, got %d", m2.screen)
	}
	// Completed items should not trigger error
	if m2.err != nil {
		t.Errorf("expected no error for terminal delete, got %v", m2.err)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateDetailEscReturnsToList(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"
	m.workitemList.Items = []types.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: "2m"},
	}
	m.workitemList.Namespace = "test-ns"

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := model.(*Model)

	if m2.screen != ScreenWorkitemList {
		t.Errorf("expected screen WorkitemList, got %d", m2.screen)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateHitlProbeFoundShowsPrompt(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"

	model, cmd := m.Update(HitlProbeResultMsg{
		WorkitemID: "wi-001",
		NodeName:   "forge",
		QueueItem:  struct{}{}, // non-nil means found
		Choices: []types.Choice{
			{Value: "approve", Label: "Approve", Type: "route"},
		},
		HasCancel: true,
	})
	m2 := model.(*Model)

	if !m2.workitemDetail.hitl.Visible {
		t.Error("expected HITL prompt visible")
	}
	if m2.workitemDetail.hitl.Loading {
		t.Error("expected HITL loading=false")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateHitlProbeNotFoundHidesPrompt(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"

	model, cmd := m.Update(HitlProbeResultMsg{
		WorkitemID: "wi-001",
		NodeName:   "forge",
		QueueItem:  nil, // nil means not found
	})
	m2 := model.(*Model)

	if m2.workitemDetail.hitl.Visible {
		t.Error("expected HITL prompt hidden")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateHitlDecideClosesPrompt(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.hitl.Visible = true
	m.workitemDetail.hitl.Error = "some error"

	model, cmd := m.Update(HitlDecidedMsg{WorkitemID: "wi-001", Choice: "approve"})
	m2 := model.(*Model)

	if m2.workitemDetail.hitl.Visible {
		t.Error("expected HITL prompt hidden after decision")
	}
	if m2.workitemDetail.hitl.Error != "" {
		t.Error("expected HITL error cleared after decision")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateHitlErrorShowsRetry(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"

	model, cmd := m.Update(HitlErrorMsg{
		WorkitemID: "wi-001",
		Err:        errors.New("request failed"),
		Retryable:  true,
	})
	m2 := model.(*Model)

	if m2.workitemDetail.hitl.Error == "" {
		t.Error("expected HITL error set")
	}
	if !m2.workitemDetail.hitl.ErrorRetry {
		t.Error("expected ErrorRetry true")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateArtefactExpanded(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.artefacts.Loading = false
	m.workitemDetail.artefacts.Artefacts = []types.ArtefactNode{
		{ArtefactID: "haiku", GovernedBy: "haiku", Expanded: false},
	}

	model, cmd := m.Update(ArtefactExpandedMsg{
		WorkitemID:    "wi-001",
		ArtefactID:    "haiku",
		Content:       "old pond",
		IsBinary:      false,
		FeedbackItems: nil,
	})
	m2 := model.(*Model)

	if !m2.workitemDetail.artefacts.Artefacts[0].Expanded {
		t.Error("expected artefact expanded")
	}
	if m2.workitemDetail.artefacts.Artefacts[0].Content != "old pond" {
		t.Errorf("expected content 'old pond', got %q", m2.workitemDetail.artefacts.Artefacts[0].Content)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateArtefactCollapsed(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.artefacts.Loading = false
	m.workitemDetail.artefacts.Artefacts = []types.ArtefactNode{
		{ArtefactID: "haiku", GovernedBy: "haiku", Expanded: true},
	}

	model, cmd := m.Update(ArtefactCollapsedMsg{ArtefactID: "haiku"})
	m2 := model.(*Model)

	if m2.workitemDetail.artefacts.Artefacts[0].Expanded {
		t.Error("expected artefact collapsed")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateRefreshSetsLoadingState(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.loading = false
	m.workitemDetail.artefacts.Loading = false
	m.workitemDetail.topology.Loading = false

	model, cmd := m.Update(RefreshMsg{})
	m2 := model.(*Model)

	if !m2.workitemDetail.loading {
		t.Error("expected detail loading=true after refresh")
	}
	if !m2.workitemDetail.artefacts.Loading {
		t.Error("expected artefacts loading=true after refresh")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateCtrlCQuits(t *testing.T) {
	m := initialModel()

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Error("expected non-nil command for ctrl+c")
	}
	// Verify the command produces a QuitMsg
	if msg := cmd(); msg != nil {
		if _, ok := msg.(tea.QuitMsg); !ok {
			t.Errorf("expected QuitMsg from quit command, got %T", msg)
		}
	}
}

func TestUpdateQKeyQuits(t *testing.T) {
	m := initialModel()

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Error("expected non-nil command for q")
	}
}

func TestUpdateDeleteResultPartialFailure(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = []types.WorkitemSummary{
		{Name: "wi-001", State: "Completed", Node: "-", ChildrenCount: 2, Age: "12m"},
	}

	model, cmd := m.Update(DeleteResultMsg{
		WorkitemName:   "wi-001",
		Err:            errors.New("child deletion failed"),
		FailedChildren: []string{"child-a", "child-b"},
	})
	m2 := model.(*Model)

	if m2.err == nil {
		t.Error("expected error for partial failure")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateHitlErrorNotFoundClearsPrompt(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.hitl.Visible = true
	m.workitemDetail.workitemName = "wi-001"

	model, cmd := m.Update(HitlErrorMsg{
		WorkitemID: "wi-001",
		Err:        errors.New("not found"),
		Retryable:  false,
	})
	m2 := model.(*Model)

	if m2.workitemDetail.hitl.Visible {
		t.Error("expected HITL prompt hidden for not-found error")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateHitlErrorAlreadyClaimedShowsRetry(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"

	model, cmd := m.Update(HitlErrorMsg{
		WorkitemID: "wi-001",
		Err:        errors.New("already claimed"),
		Retryable:  true,
	})
	m2 := model.(*Model)

	if m2.workitemDetail.hitl.Error == "" {
		t.Error("expected HITL error set")
	}
	if !m2.workitemDetail.hitl.ErrorRetry {
		t.Error("expected ErrorRetry true")
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateErrorMsgSetsRootError(t *testing.T) {
	m := initialModel()

	model, cmd := m.Update(ErrorMsg{Source: "archivist", Message: "connection failed"})
	m2 := model.(*Model)

	if m2.err == nil {
		t.Error("expected root error set")
	}
	if m2.err.Error() != "archivist: connection failed" {
		t.Errorf("expected 'archivist: connection failed', got %v", m2.err)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateWindowSizeMsg(t *testing.T) {
	m := initialModel()

	model, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	m2 := model.(*Model)

	if m2.width != 100 {
		t.Errorf("expected width 100, got %d", m2.width)
	}
	if m2.height != 50 {
		t.Errorf("expected height 50, got %d", m2.height)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}
