package tui

import (
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gideas/flow/tools/flowctl/internal/api"
	"github.com/gideas/flow/tools/flowctl/internal/tui/components"
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
	if cmd == nil {
		t.Error("expected non-nil command (loadWorkitems)")
	}
}

func TestUpdateNamespaceFallbackTransitionsToList(t *testing.T) {
	m := initialModel()

	model, cmd := m.Update(NamespaceFallbackMsg{Namespace: "default", Error: errors.New("permission denied")})
	m2 := model.(*Model)

	if m2.screen != ScreenWorkitemList {
		t.Errorf("expected screen WorkitemList, got %d", m2.screen)
	}
	if m2.namespaceSelector.Error != "permission denied" {
		t.Errorf("expected error 'permission denied', got %q", m2.namespaceSelector.Error)
	}
	if cmd == nil {
		t.Error("expected non-nil command (loadWorkitems)")
	}
}

func TestUpdateWorkitemSelectedTransitionsToDetail(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: 2 * time.Minute},
		{Name: "wi-002", State: "Completed", Node: "-", ChildrenCount: 0, Age: 12 * time.Minute},
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
	if cmd == nil {
		t.Error("expected non-nil command (batched topology/artefacts load)")
	}
}

func TestUpdateWorkitemUpdateModifiesItemInPlace(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: 2 * time.Minute},
	}
	m.workitemList.Namespace = "test-ns"

	model, cmd := m.Update(WorkitemUpdateMsg{
		Event: "MODIFIED",
		Item:  api.WorkitemSummary{Name: "wi-001", State: "Completed", Node: "-", ChildrenCount: 0, Age: 12 * time.Minute},
	})
	m2 := model.(*Model)

	if len(m2.workitemList.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(m2.workitemList.Items))
	}
	if m2.workitemList.Items[0].State != "Completed" {
		t.Errorf("expected state Completed, got %q", m2.workitemList.Items[0].State)
	}
	if cmd == nil {
		t.Error("expected non-nil command (debounced child count refresh)")
	}
}

func TestUpdateWatchDisconnectedShowsBanner(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: 2 * time.Minute},
	}
	m.workitemList.Namespace = "test-ns"

	model, cmd := m.Update(WatchDisconnectedMsg{})
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
	m.workitemList.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: 2 * time.Minute},
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

func TestUpdateWorkitemDeletedRemovesItem(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: 2 * time.Minute},
		{Name: "wi-002", State: "Completed", Node: "-", ChildrenCount: 0, Age: 12 * time.Minute},
	}
	m.workitemList.Namespace = "test-ns"

	model, cmd := m.Update(WorkitemDeletedMsg{Name: "wi-001"})
	m2 := model.(*Model)

	if len(m2.workitemList.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(m2.workitemList.Items))
	}
	if m2.workitemList.Items[0].Name != "wi-002" {
		t.Errorf("expected wi-002, got %s", m2.workitemList.Items[0].Name)
	}
	if cmd != nil {
		t.Error("expected nil command")
	}
}

func TestUpdateWorkitemAddedAppendsToList(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: 2 * time.Minute},
	}
	m.workitemList.Namespace = "test-ns"

	model, cmd := m.Update(WorkitemUpdateMsg{
		Event: "ADDED",
		Item:  api.WorkitemSummary{Name: "wi-003", State: "Pending", Node: "forge", ChildrenCount: 0, Age: 5 * time.Second},
	})
	m2 := model.(*Model)

	if len(m2.workitemList.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(m2.workitemList.Items))
	}
	if m2.workitemList.Items[1].Name != "wi-003" {
		t.Errorf("expected wi-003, got %s", m2.workitemList.Items[1].Name)
	}
	if cmd == nil {
		t.Error("expected non-nil command (debounced child count refresh)")
	}
}

func TestUpdateCreateStartTransitionsToWizard(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: 2 * time.Minute},
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
	m.workitemList.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: 2 * time.Minute},
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
	m.workitemList.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Completed", Node: "-", ChildrenCount: 0, Age: 12 * time.Minute},
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
	m.workitemList.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 0, Age: 2 * time.Minute},
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

	model, cmd := m.Update(components.HitlProbeResultMsg{
		WorkitemID: "wi-001",
		NodeName:   "forge",
		QueueItem:  &api.QueueItem{WorkitemID: "wi-001"},
		Choices: []api.Choice{
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

func TestUpdateHitlProbeFoundWithChoices(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"

	// HitlProbeResultMsg is only emitted by the Probe cmd when a queue item match is found.
	// In Phase 05, it always means "active".
	model, cmd := m.Update(components.HitlProbeResultMsg{
		WorkitemID: "wi-001",
		NodeName:   "human-approval",
		QueueItem:  &api.QueueItem{WorkitemID: "wi-001"},
		Choices: []api.Choice{
			{Value: "approve", Label: "Approve", Type: "route"},
			{Value: "cancel", Label: "Cancel", Type: "cancel"},
		},
		HasCancel: true,
	})
	m2 := model.(*Model)

	if !m2.workitemDetail.hitl.Visible {
		t.Error("expected HITL prompt visible when probe succeeds")
	}
	if len(m2.workitemDetail.hitl.Choices) != 2 {
		t.Errorf("expected 2 choices, got %d", len(m2.workitemDetail.hitl.Choices))
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
	m.workitemList.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Completed", Node: "-", ChildrenCount: 2, Age: 12 * time.Minute},
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
