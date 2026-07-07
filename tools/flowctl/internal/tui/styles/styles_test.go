package styles

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// hasColor reports whether a lipgloss.Style has a foreground color set
// (distinct from the default NoColor{}).
func hasColor(s lipgloss.Style) bool {
	fg := s.GetForeground()
	_, isNoColor := fg.(lipgloss.NoColor)
	return !isNoColor
}

// TestStyleStateNew verifies StyleState("FEEDBACK_STATE_NEW") returns cyan style.
func TestStyleStateNew(t *testing.T) {
	s := StyleState("FEEDBACK_STATE_NEW")
	if !hasColor(s) {
		t.Error("expected foreground color, got nil")
	}
}

// TestStyleStateResolved verifies StyleState("FEEDBACK_STATE_RESOLVED") returns green style.
func TestStyleStateResolved(t *testing.T) {
	s := StyleState("FEEDBACK_STATE_RESOLVED")
	if !hasColor(s) {
		t.Error("expected foreground color, got nil")
	}
}

// TestStyleStateDeadlocked verifies StyleState("FEEDBACK_STATE_DEADLOCKED") returns magenta bold.
func TestStyleStateDeadlocked(t *testing.T) {
	s := StyleState("FEEDBACK_STATE_DEADLOCKED")
	if !hasColor(s) {
		t.Error("expected foreground color, got nil")
	}
	if !s.GetBold() {
		t.Error("expected bold")
	}
}

// TestStyleStateUnspecified verifies StyleState("FEEDBACK_STATE_UNSPECIFIED") returns unstyled.
func TestStyleStateUnspecified(t *testing.T) {
	s := StyleState("FEEDBACK_STATE_UNSPECIFIED")
	if hasColor(s) {
		t.Error("expected no foreground color")
	}
}

// TestStyleStateUnknown verifies StyleState("FEEDBACK_STATE_UNKNOWN") returns unstyled.
func TestStyleStateUnknown(t *testing.T) {
	s := StyleState("FEEDBACK_STATE_UNKNOWN")
	if hasColor(s) {
		t.Error("expected no foreground color")
	}
}

// TestStyleStateBogus verifies StyleState("bogus_value") returns unstyled.
func TestStyleStateBogus(t *testing.T) {
	s := StyleState("bogus_value")
	if hasColor(s) {
		t.Error("expected no foreground color")
	}
}

// TestStyleResolved verifies StyleResolved returns dim gray style.
func TestStyleResolved(t *testing.T) {
	s := StyleResolved()
	if !hasColor(s) {
		t.Error("expected foreground color, got nil")
	}
}

// TestStyleDeadlockedFunc verifies StyleDeadlocked returns magenta bold.
func TestStyleDeadlockedFunc(t *testing.T) {
	s := StyleDeadlocked()
	if !hasColor(s) {
		t.Error("expected foreground color, got nil")
	}
	if !s.GetBold() {
		t.Error("expected bold")
	}
}

// TestStyleTopologyCurrent verifies StyleTopologyCurrent returns bold green.
func TestStyleTopologyCurrent(t *testing.T) {
	s := StyleTopologyCurrent()
	if !hasColor(s) {
		t.Error("expected foreground color, got nil")
	}
	if !s.GetBold() {
		t.Error("expected bold")
	}
}

// TestStyleTopologyVisited verifies StyleTopologyVisited returns dim green.
func TestStyleTopologyVisited(t *testing.T) {
	s := StyleTopologyVisited()
	if !hasColor(s) {
		t.Error("expected foreground color, got nil")
	}
}

// TestStyleTopologyUnvisited verifies StyleTopologyUnvisited returns gray.
func TestStyleTopologyUnvisited(t *testing.T) {
	s := StyleTopologyUnvisited()
	if !hasColor(s) {
		t.Error("expected foreground color, got nil")
	}
}

// TestStyleStateColumnCompleted verifies StyleStateColumn("Completed") returns green.
func TestStyleStateColumnCompleted(t *testing.T) {
	s := StyleStateColumn("Completed")
	if !hasColor(s) {
		t.Error("expected foreground color, got nil")
	}
}

// TestStyleStateColumnFailed verifies StyleStateColumn("Failed") returns red.
func TestStyleStateColumnFailed(t *testing.T) {
	s := StyleStateColumn("Failed")
	if !hasColor(s) {
		t.Error("expected foreground color, got nil")
	}
}

// TestStyleStateColumnSuspended verifies StyleStateColumn("Suspended") returns yellow.
func TestStyleStateColumnSuspended(t *testing.T) {
	s := StyleStateColumn("Suspended")
	if !hasColor(s) {
		t.Error("expected foreground color, got nil")
	}
}

// TestStyleStateColumnPending verifies StyleStateColumn("Pending") returns cyan.
func TestStyleStateColumnPending(t *testing.T) {
	s := StyleStateColumn("Pending")
	if !hasColor(s) {
		t.Error("expected foreground color, got nil")
	}
}

// TestStyleStateColumnRunning verifies StyleStateColumn("Running") returns default (unstyled).
func TestStyleStateColumnRunning(t *testing.T) {
	s := StyleStateColumn("Running")
	if hasColor(s) {
		t.Error("expected no foreground (default)")
	}
}

// TestStyleStateColumnRouting verifies StyleStateColumn("Routing") returns default (unstyled).
func TestStyleStateColumnRouting(t *testing.T) {
	s := StyleStateColumn("Routing")
	if hasColor(s) {
		t.Error("expected no foreground (default)")
	}
}

// TestStyleStateColumnUnknown verifies StyleStateColumn("UnknownPhase") returns gray.
func TestStyleStateColumnUnknown(t *testing.T) {
	s := StyleStateColumn("UnknownPhase")
	if !hasColor(s) {
		t.Error("expected foreground color, got nil")
	}
}

// TestStyleNodeColumnDash verifies StyleNodeColumn("-") returns gray.
func TestStyleNodeColumnDash(t *testing.T) {
	s := StyleNodeColumn("-")
	if !hasColor(s) {
		t.Error("expected foreground color, got nil")
	}
}

// TestStyleNodeColumnNormal verifies StyleNodeColumn("sort") returns normal (unstyled).
func TestStyleNodeColumnNormal(t *testing.T) {
	s := StyleNodeColumn("sort")
	if hasColor(s) {
		t.Error("expected no foreground (normal style)")
	}
}
