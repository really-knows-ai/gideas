package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/foundry/flow/tools/flowctl/internal/tui/components"
)

// updateWorkitemDetail handles semantic messages for the detail screen.
// It dispatches to per-message-family handlers so each family stays small:
// detail/topology state, artefact tree, HITL state machine, banners/errors,
// and screen transitions (refresh/create wizard). The per-family handlers live
// in detail_loaders.go, detail_artefacts.go and detail_hitl.go, with key
// handling in detail_keys.go.
func (m *Model) updateWorkitemDetail(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case WorkitemDetailLoadedMsg, TopologyLoadedMsg:
		return m.handleDetailTopology(msg)
	case ArtefactsLoadedMsg, ArtefactLoadErrorMsg, ArtefactExpandedMsg, ArtefactFeedbackLoadedMsg, ArtefactCollapsedMsg:
		return m.handleArtefacts(msg)
	case components.HitlProbeResultMsg, components.HitlProbeRetryMsg, components.HitlProbeExhaustedMsg,
		components.HitlChoicesBlockedMsg, HitlProbeTriggerMsg, HitlReleasedMsg, HitlDecidedMsg, HitlErrorMsg:
		return m.handleHitl(msg)
	case ErrorMsg, ClearErrorBannerMsg, BannerMsg, BannerDismissMsg, WorkitemDeletedMsg:
		return m.handleBanner(msg)
	case RefreshMsg, CreateStartMsg:
		return m.handleRefreshCreate(msg)
	}
	return m, nil
}

// ClearErrorBannerMsg clears the error banner.
type ClearErrorBannerMsg struct{}
