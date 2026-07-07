package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/gideas/flow/tools/flowctl/internal/tui/types"
)

func TestViewNamespaceSelectScreen(t *testing.T) {
	m := initialModel()
	m.namespaceSelector = m.namespaceSelector.SetNamespaces([]string{"ns-a", "ns-b"}, "")
	v := m.View()
	if !strings.Contains(v, "ns-a") || !strings.Contains(v, "ns-b") {
		t.Error("expected namespace names in view, got:", v)
	}
}

func TestViewWorkitemListScreen(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemList
	m.workitemList.Loading = false
	m.workitemList.Items = []types.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 2, Age: "2m"},
	}
	m.workitemList.Namespace = "test-ns"
	v := m.View()
	if !strings.Contains(v, "wi-001") {
		t.Error("expected workitem list content in view, got:", v)
	}
}

func TestViewWorkitemDetailScreen(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"
	m.workitemDetail.statusBar.ScreenName = "Workitem Detail"
	m.workitemDetail.statusBar.WorkitemName = "wi-001"
	m.workitemDetail.topology.Loading = false
	m.workitemDetail.topology.Nodes = []types.TopologyNode{
		{Name: "forge", Color: types.TopologyVisited},
	}
	m.workitemDetail.artefacts.Loading = false
	m.workitemDetail.artefacts.Artefacts = []types.ArtefactNode{
		{ArtefactID: "haiku", GovernedBy: "haiku"},
	}
	m.workitemDetail.hitl.Visible = false
	v := m.View()
	if !strings.Contains(v, "wi-001") {
		t.Error("expected workitem name in detail view, got:", v)
	}
	if !strings.Contains(v, "forge") {
		t.Error("expected topology content in detail view, got:", v)
	}
	if !strings.Contains(v, "haiku") {
		t.Error("expected artefact content in detail view, got:", v)
	}
}

func TestViewCreateWizardScreen(t *testing.T) {
	m := initialModel()
	m.screen = ScreenCreateWizard
	m.createWizard.Step = 0
	v := m.View()
	if !strings.Contains(v, "Enter prompt text") {
		t.Error("expected wizard content in view, got:", v)
	}
}

func TestViewErrorState(t *testing.T) {
	m := initialModel()
	m.err = errors.New("connection refused")
	v := m.View()
	if !strings.Contains(v, "connection refused") {
		t.Error("expected error text in view, got:", v)
	}
	if !strings.Contains(v, "q to quit") {
		t.Error("expected quit hint in view, got:", v)
	}
}

func TestViewDetailShowsArtefacts(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"
	m.workitemDetail.statusBar.ScreenName = "Workitem Detail"
	m.workitemDetail.statusBar.WorkitemName = "wi-001"
	m.workitemDetail.topology.Loading = false
	m.workitemDetail.artefacts.Loading = false
	m.workitemDetail.artefacts.Artefacts = []types.ArtefactNode{
		{ArtefactID: "petition", GovernedBy: "petition", Expanded: true, Content: "content text"},
	}
	m.workitemDetail.hitl.Visible = false
	v := m.View()
	if !strings.Contains(v, "petition") || !strings.Contains(v, "content text") {
		t.Error("expected artefact content visible in detail view, got:", v)
	}
}

func TestViewDetailLayout(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"
	m.workitemDetail.statusBar.ScreenName = "Workitem Detail"
	m.workitemDetail.statusBar.WorkitemName = "wi-001"
	m.workitemDetail.topology.Loading = false
	m.workitemDetail.artefacts.Loading = false
	m.workitemDetail.hitl.Visible = false
	v := m.View()
	// Detail layout renders status bar, topology, and artefacts
	if !strings.Contains(v, "wi-001") {
		t.Error("expected workitem name in detail layout, got:", v)
	}
}
