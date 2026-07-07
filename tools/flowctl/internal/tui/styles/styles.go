// Package styles provides lipgloss color constants and style helper functions
// for all TUI components. All colors use AdaptiveColor for light/dark terminal
// support.
package styles

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	// Feedback state colors
	colorGreen      = lipgloss.AdaptiveColor{Light: "#00AA00", Dark: "#00FF00"}
	colorYellow     = lipgloss.AdaptiveColor{Light: "#AAAA00", Dark: "#FFFF00"}
	colorRed        = lipgloss.AdaptiveColor{Light: "#AA0000", Dark: "#FF0000"}
	colorCyan       = lipgloss.AdaptiveColor{Light: "#00AAAA", Dark: "#00FFFF"}
	colorMagenta    = lipgloss.AdaptiveColor{Light: "#AA00AA", Dark: "#FF00FF"}
	colorGray       = lipgloss.AdaptiveColor{Light: "#666666", Dark: "#888888"}
	colorDimGray    = lipgloss.AdaptiveColor{Light: "#AAAAAA", Dark: "#444444"}
	colorDimGreen   = lipgloss.AdaptiveColor{Light: "#88CC88", Dark: "#88AA88"}
	colorBoldGreen  = lipgloss.AdaptiveColor{Light: "#006600", Dark: "#00FF00"}
)

// StyleState returns a lipgloss.Style for the given feedback state string.
// The FEEDBACK_STATE_ prefix is stripped before matching known values.
// Unknown and UNSPECIFIED values return an unstyled style.
func StyleState(state string) lipgloss.Style {
	stripped := strings.TrimPrefix(state, "FEEDBACK_STATE_")
	switch stripped {
	case "RESOLVED":
		return lipgloss.NewStyle().Foreground(colorGreen)
	case "ACTIONED":
		return lipgloss.NewStyle().Foreground(colorYellow)
	case "REJECTED":
		return lipgloss.NewStyle().Foreground(colorRed)
	case "NEW":
		return lipgloss.NewStyle().Foreground(colorCyan)
	case "DEADLOCKED":
		return lipgloss.NewStyle().Foreground(colorMagenta).Bold(true)
	case "WONT_FIX":
		return lipgloss.NewStyle().Foreground(colorGray)
	case "UNSPECIFIED":
		return lipgloss.NewStyle() // no color
	default:
		return lipgloss.NewStyle() // unknown rendered raw
	}
}

// StyleResolved returns a dim gray style for resolved feedback rows.
func StyleResolved() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(colorDimGray)
}

// StyleDeadlocked returns a magenta bold style for DEADLOCKED feedback rows.
func StyleDeadlocked() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(colorMagenta).Bold(true)
}

// StyleTopologyCurrent returns a bold green style for the current NODE in topology.
func StyleTopologyCurrent() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(colorBoldGreen).Bold(true)
}

// StyleTopologyVisited returns a dim green style for a previously visited NODE.
func StyleTopologyVisited() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(colorDimGreen)
}

// StyleTopologyUnvisited returns a gray style for a not-yet-visited NODE.
func StyleTopologyUnvisited() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(colorGray)
}

// StyleStateColumn returns a styled string for the Workitem list STATE column
// based on status.phase.
func StyleStateColumn(phase string) lipgloss.Style {
	switch phase {
	case "Completed":
		return lipgloss.NewStyle().Foreground(colorGreen)
	case "Failed":
		return lipgloss.NewStyle().Foreground(colorRed)
	case "Suspended":
		return lipgloss.NewStyle().Foreground(colorYellow)
	case "Pending":
		return lipgloss.NewStyle().Foreground(colorCyan)
	case "Running", "Routing":
		return lipgloss.NewStyle() // default
	default:
		return lipgloss.NewStyle().Foreground(colorGray)
	}
}

// StyleNodeColumn returns a styled string for the Workitem list NODE column.
// Terminal phases show "-" in gray; otherwise the node name is shown unstyled.
func StyleNodeColumn(node string) lipgloss.Style {
	if node == "-" {
		return lipgloss.NewStyle().Foreground(colorGray)
	}
	return lipgloss.NewStyle() // default
}

// WarningColor returns the yellow color used for warning text in banners.
func WarningColor() lipgloss.AdaptiveColor {
	return colorYellow
}
