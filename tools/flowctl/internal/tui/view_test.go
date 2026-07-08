package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gideas/flow/tools/flowctl/internal/api"
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
	m.workitemList.Items = []api.WorkitemSummary{
		{Name: "wi-001", State: "Running", Node: "sort", ChildrenCount: 2, Age: 2 * time.Minute},
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
	m.workitemDetail.loaded = true
	v := m.View()
	if !strings.Contains(v, "wi-001") {
		t.Error("expected workitem name in detail view, got:", v)
	}
	if !strings.Contains(v, "Workitem: wi-001") {
		t.Error("expected 'Workitem: wi-001' in view, got:", v)
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

func TestViewDetailLayout(t *testing.T) {
	m := initialModel()
	m.screen = ScreenWorkitemDetail
	m.workitemDetail.workitemName = "wi-001"
	m.workitemDetail.statusBar.ScreenName = "Workitem Detail"
	m.workitemDetail.statusBar.WorkitemName = "wi-001"
	m.workitemDetail.loaded = true
	v := m.View()
	// Detail layout renders status bar, topology, and artefacts
	if !strings.Contains(v, "wi-001") {
		t.Error("expected workitem name in detail layout, got:", v)
	}
	if !strings.Contains(v, "Flow Topology") {
		t.Error("expected Flow Topology in detail layout, got:", v)
	}
	if !strings.Contains(v, "Artefacts") {
		t.Error("expected Artefacts in detail layout, got:", v)
	}
}
