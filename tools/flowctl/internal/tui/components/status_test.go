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
	if !strings.Contains(v, "Reconnecting") {
		t.Error("expected 'Reconnecting' text in view, got:", v)
	}
}

// ─── Phase 06: Connection status indicator tests ───────────────────────────

func TestStatusK8sOK(t *testing.T) {
	m := NewStatusBar()
	m.K8sStatus = StatusOK
	v := m.View()
	if !strings.Contains(v, "K8s:OK") {
		t.Error("expected K8s:OK in view, got:", v)
	}
}

func TestStatusK8sWARN(t *testing.T) {
	m := NewStatusBar()
	m.K8sStatus = StatusWarn
	v := m.View()
	if !strings.Contains(v, "K8s:WARN") {
		t.Error("expected K8s:WARN in view, got:", v)
	}
}

func TestStatusK8sERR(t *testing.T) {
	m := NewStatusBar()
	m.K8sStatus = StatusErr
	v := m.View()
	if !strings.Contains(v, "K8s:ERR") {
		t.Error("expected K8s:ERR in view, got:", v)
	}
}

func TestStatusArchivistOK(t *testing.T) {
	m := NewStatusBar()
	m.ArchivistStatus = StatusOK
	v := m.View()
	if !strings.Contains(v, "ARC:OK") {
		t.Error("expected ARC:OK in view, got:", v)
	}
}

func TestStatusArchivistWARN(t *testing.T) {
	m := NewStatusBar()
	m.ArchivistStatus = StatusWarn
	v := m.View()
	if !strings.Contains(v, "ARC:WARN") {
		t.Error("expected ARC:WARN in view, got:", v)
	}
}

func TestStatusArchivistERR(t *testing.T) {
	m := NewStatusBar()
	m.ArchivistStatus = StatusErr
	v := m.View()
	if !strings.Contains(v, "ARC:ERR") {
		t.Error("expected ARC:ERR in view, got:", v)
	}
}

func TestStatusHitlOK(t *testing.T) {
	m := NewStatusBar()
	m.HitlStatus = StatusOK
	v := m.View()
	if !strings.Contains(v, "HITL:OK") {
		t.Error("expected HITL:OK in view, got:", v)
	}
}

func TestStatusHitlOFF(t *testing.T) {
	m := NewStatusBar()
	m.HitlStatus = StatusOff
	v := m.View()
	if !strings.Contains(v, "HITL:OFF") {
		t.Error("expected HITL:OFF in view, got:", v)
	}
}

func TestStatusAllIndicators(t *testing.T) {
	m := NewStatusBar()
	m.ScreenName = "Workitem Detail"
	m.Namespace = "test-ns"
	m.K8sStatus = StatusOK
	m.ArchivistStatus = StatusWarn
	m.HitlStatus = StatusOff
	v := m.View()
	if !strings.Contains(v, "K8s:OK") {
		t.Error("expected K8s:OK")
	}
	if !strings.Contains(v, "ARC:WARN") {
		t.Error("expected ARC:WARN")
	}
	if !strings.Contains(v, "HITL:OFF") {
		t.Error("expected HITL:OFF")
	}
}
