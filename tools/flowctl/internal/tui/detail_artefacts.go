package tui

import (
	"context"
	"fmt"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/foundry/flow/tools/flowctl/internal/tui/types"
)

// handleArtefacts applies artefact tree state (load, expand, collapse, errors).
func (m *Model) handleArtefacts(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case ArtefactsLoadedMsg:
		// Preserve expansion state across refreshes
		expandedSet := make(map[string]bool)
		for _, art := range m.workitemDetail.artefacts.Artefacts {
			if art.Expanded {
				expandedSet[art.ArtefactID] = true
			}
		}

		// Convert and sort artefacts
		nodes := make([]types.ArtefactNode, len(msg.Artefacts))
		for i, a := range msg.Artefacts {
			nodes[i] = toArtefactNode(a)
			if expandedSet[a.ID] {
				nodes[i].Expanded = true
			}
		}
		sort.Slice(nodes, func(i, j int) bool {
			return nodes[i].ArtefactID < nodes[j].ArtefactID
		})
		m.workitemDetail.artefacts.Artefacts = nodes
		m.workitemDetail.artefacts.Loading = false
		m.errorBanner = ""

		// Fetch content for expanded artefacts and feedback for every artefact
		var cmds []tea.Cmd
		if m.archivist != nil {
			for _, art := range m.workitemDetail.artefacts.Artefacts {
				if art.Expanded {
					// Expanded artefacts: fetch content + feedback together
					cmds = append(cmds, m.fetchArtefactContent(msg.WorkitemID, art.ArtefactID))
				} else {
					// Collapsed artefacts: fetch feedback only
					cmds = append(cmds, m.fetchArtefactFeedback(msg.WorkitemID, art.ArtefactID))
				}
			}
		}
		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}

	case ArtefactLoadErrorMsg:
		m.workitemDetail.artefacts.Loading = false
		m.logIfEnabled("ERROR", "archivist", fmt.Sprintf("artefact load error for %s/%s: %v", msg.WorkitemID, msg.ArtefactID, msg.Error))
		if msg.ArtefactID == "" {
			// List failed
			m.workitemDetail.artefacts.Error = fmt.Sprintf("Artefacts unavailable: %v", msg.Error)
		} else {
			// Per-artefact failure — show in the artefact node
			for i, art := range m.workitemDetail.artefacts.Artefacts {
				if art.ArtefactID == msg.ArtefactID {
					m.workitemDetail.artefacts.Artefacts[i].Content = fmt.Sprintf("Failed to load: %v", msg.Error)
					break
				}
			}
		}

	case ArtefactExpandedMsg:
		for i, art := range m.workitemDetail.artefacts.Artefacts {
			if art.ArtefactID == msg.ArtefactID {
				m.workitemDetail.artefacts.Artefacts[i].Expanded = true
				if msg.IsBinary {
					m.workitemDetail.artefacts.Artefacts[i].IsBinary = true
					m.workitemDetail.artefacts.Artefacts[i].BinarySize = msg.BinarySize
				}
				m.workitemDetail.artefacts.Artefacts[i].Content = msg.Content
				m.workitemDetail.artefacts.Artefacts[i].Feedback = convertAPIFeedback(msg.FeedbackItems)
				break
			}
		}

	case ArtefactFeedbackLoadedMsg:
		for i, art := range m.workitemDetail.artefacts.Artefacts {
			if art.ArtefactID == msg.ArtefactID {
				m.workitemDetail.artefacts.Artefacts[i].Feedback = convertAPIFeedback(msg.FeedbackItems)
				break
			}
		}

	case ArtefactCollapsedMsg:
		for i, art := range m.workitemDetail.artefacts.Artefacts {
			if art.ArtefactID == msg.ArtefactID {
				m.workitemDetail.artefacts.Artefacts[i].Expanded = false
				m.workitemDetail.artefacts.Artefacts[i].Content = ""
				m.workitemDetail.artefacts.Artefacts[i].Feedback = nil
				break
			}
		}
	}
	return m, nil
}

// connectArchivist establishes a port-forward to the Archivist pod and creates
// a gRPC client. It is called after namespace resolution so the connection is
// ready when entering Workitem detail. If it fails, m.archivist stays nil and
// loadArtefacts will report the error at entry time.
func (m *Model) connectArchivist() tea.Cmd {
	return func() tea.Msg {
		if m.pfm == nil || m.systemNS == "" {
			return nil // silently skip — archivist won't be available
		}
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		if err := m.ensureArchivistConnected(ctx); err != nil {
			m.logIfEnabled("WARN", "archivist", err.Error())
		}
		return nil
	}
}

// ensureArchivistConnected finds the Ready Archivist pod, opens a port-forward
// to it, builds a gRPC client, and assigns it to m.archivist (closing any
// previous client first). It returns the failure so each caller decides how to
// surface it — connectArchivist logs a WARN and returns nil, while the lazy
// connect inside loadArtefacts returns an ErrorMsg.
func (m *Model) ensureArchivistConnected(ctx context.Context) error {
	if m.pfm == nil || m.systemNS == "" {
		return fmt.Errorf("no port-forward manager or system namespace")
	}
	archivistPod, found, err := m.pfm.FindReadyPod(ctx, m.systemNS, "app.kubernetes.io/name=flow-archivist")
	if err != nil {
		return fmt.Errorf("find archivist pod: %w", err)
	}
	if !found {
		return fmt.Errorf("no Ready archivist pod found in namespace %s", m.systemNS)
	}
	_, localPort, err := m.pfm.ForwardPod(ctx, m.systemNS, archivistPod, 50054)
	if err != nil {
		return fmt.Errorf("forward pod: %w", err)
	}
	archivist, err := api.NewArchivistClient(fmt.Sprintf("localhost:%d", localPort))
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	// Close previous client if any
	if m.archivist != nil {
		m.archivist.Close()
	}
	m.archivist = archivist
	return nil
}

// loadArtefacts loads artefacts for a Workitem. The Archivist connection should
// already be established by connectArchivist (called after namespace resolution).
// Falls back to connecting synchronously if m.archivist is nil.
func (m *Model) loadArtefacts(workitemID string) tea.Cmd {
	return func() tea.Msg {
		if m.pfm == nil {
			m.logIfEnabled("ERROR", "archivist", "no port-forward manager")
			return ErrorMsg{Source: "archivist-forward", Message: "no port-forward manager"}
		}

		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}

		// Connect lazily if eager connect after namespace resolution did not run or failed
		if m.archivist == nil {
			if err := m.ensureArchivistConnected(ctx); err != nil {
				m.logIfEnabled("ERROR", "archivist", err.Error())
				return ErrorMsg{Source: "archivist-forward", Message: err.Error()}
			}
		}

		// List artefacts via the (now established) client
		artefacts, err := m.archivist.ListArtefacts(ctx, m.namespace, workitemID)
		if err != nil {
			m.logIfEnabled("ERROR", "archivist", fmt.Sprintf("list artefacts for %s: %v", workitemID, err))
			return ArtefactLoadErrorMsg{WorkitemID: workitemID, Error: err}
		}

		return ArtefactsLoadedMsg{WorkitemID: workitemID, Artefacts: artefacts}
	}
}

// fetchArtefactContent fetches content and feedback for an expanded artefact.
func (m *Model) fetchArtefactContent(workitemID, artefactID string) tea.Cmd {
	return func() tea.Msg {
		if m.archivist == nil {
			return ArtefactExpandedMsg{
				WorkitemID:    workitemID,
				ArtefactID:    artefactID,
				Content:       "Archivist not connected",
				FeedbackItems: nil,
			}
		}

		// Derive context from model or use background
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}

		// Get content
		content, err := m.archivist.GetArtefact(ctx, m.namespace, workitemID, artefactID)
		if err != nil {
			return ArtefactExpandedMsg{
				WorkitemID:    workitemID,
				ArtefactID:    artefactID,
				Content:       fmt.Sprintf("Failed to load content: %v", err),
				FeedbackItems: nil,
			}
		}

		// Format content (text or hex preview)
		contentStr, isBinary, binarySize := formatArtefactContent(content)

		// Get feedback
		feedback, err := m.archivist.GetFeedback(ctx, m.namespace, workitemID, artefactID)
		if err != nil {
			// Non-fatal: show content with error feedback item
			feedback = []api.FeedbackItem{
				{
					State:      api.FeedbackStateNew,
					SourceNode: "archivist",
					Message:    fmt.Sprintf("Feedback unavailable: %v", err),
					CreatedAt:  time.Now(),
				},
			}
		}

		return ArtefactExpandedMsg{
			WorkitemID:    workitemID,
			ArtefactID:    artefactID,
			Content:       contentStr,
			IsBinary:      isBinary,
			BinarySize:    binarySize,
			FeedbackItems: feedback,
		}
	}
}

// fetchArtefactFeedback fetches feedback for a specific artefact.
func (m *Model) fetchArtefactFeedback(workitemID, artefactID string) tea.Cmd {
	return func() tea.Msg {
		if m.archivist == nil {
			return ArtefactFeedbackLoadedMsg{
				WorkitemID:    workitemID,
				ArtefactID:    artefactID,
				FeedbackItems: nil,
			}
		}

		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}

		feedback, err := m.archivist.GetFeedback(ctx, m.namespace, workitemID, artefactID)
		if err != nil {
			return ArtefactLoadErrorMsg{
				WorkitemID: workitemID,
				ArtefactID: artefactID,
				Error:      err,
			}
		}

		return ArtefactFeedbackLoadedMsg{
			WorkitemID:    workitemID,
			ArtefactID:    artefactID,
			FeedbackItems: feedback,
		}
	}
}
