package components

import (
	"strings"
	"testing"
)

func TestStatusNormalState(t *testing.T) {
	m := NewStatusBar()
	m.ScreenName = "Namespace Selection"
	m.Namespace = "my-ns"
	v := m.View()
	if !strings.Contains(v, "Namespace Selection") {
		t.Error("expected screen name in view, got:", v)
	}
	if !strings.Contains(v, "my-ns") {
		t.Error("expected namespace in view, got:", v)
	}
}

func TestStatusWorkitemDetail(t *testing.T) {
	m := NewStatusBar()
	m.ScreenName = "Workitem Detail"
	m.Namespace = "my-ns"
	m.WorkitemName = "wi-001"
	m.State = "Running"
	v := m.View()
	if !strings.Contains(v, "wi-001") {
		t.Error("expected workitem name in view, got:", v)
	}
}

func TestStatusWarningBanner(t *testing.T) {
	m := NewStatusBar()
	m.ScreenName = "Test"
	m.Warning = "Archivist port-forward failed"
	v := m.View()
	if !strings.Contains(v, "Archivist port-forward failed") {
		t.Error("expected warning text in view, got:", v)
	}
}

func TestStatusConnectedIndicator(t *testing.T) {
	m := NewStatusBar()
	m.ScreenName = "Test"
	m.Connected = true
	v := m.View()
	if !strings.Contains(v, "●") {
		t.Error("expected green connected indicator in view, got:", v)
	}
}

func TestStatusDisconnectedIndicator(t *testing.T) {
	m := NewStatusBar()
	m.ScreenName = "Test"
	m.Connected = false
	m.Disconnected = true
	v := m.View()
	if !strings.Contains(v, "Disconnected") {
		t.Error("expected disconnected text in view, got:", v)
	}
}
