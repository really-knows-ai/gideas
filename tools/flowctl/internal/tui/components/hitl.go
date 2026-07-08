package components

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/gideas/flow/tools/flowctl/internal/api"
	"github.com/gideas/flow/tools/flowctl/internal/tui/types"
)

// ─── HitlPromptModel (Rendering Component) ──────────────────────────────────

// HitlPromptModel is the model for the HITL action prompt.
type HitlPromptModel struct {
	Visible        bool // true when a queue item exists
	QueueItemID    string
	Choices        []types.Choice // populated from /choices or defaults
	Loading        bool           // true while probing or deciding
	Error          string
	ErrorRetry     bool // true if retry is possible
	ChoicesLoaded  bool // true after /choices returned 200, even if empty
	DefaultChoices bool // true when using the built-in approve/cancel fallback

	// Cancel confirmation
	ConfirmingCancel bool   // true when user pressed a cancel-type choice
	PendingChoice    string // the cancel choice value pending confirmation
}

// Default choices (hardcoded fallback).
var defaultChoices = []types.Choice{
	{Value: "approve", Label: "Approve", Type: "route"},
	{Value: "cancel", Label: "Cancel", Type: "cancel"},
}

// NewHitlPrompt creates a HitlPromptModel in hidden state.
func NewHitlPrompt() HitlPromptModel {
	return HitlPromptModel{}
}

// View renders the HITL prompt.
func (m HitlPromptModel) View() string {
	if !m.Visible {
		return ""
	}

	var b strings.Builder

	if m.Loading {
		b.WriteString("\nChecking HITL queue...")
		return b.String()
	}

	// Error states
	if m.Error != "" {
		switch {
		case strings.Contains(m.Error, "QUEUE_ITEM_ALREADY_CLAIMED"):
			b.WriteString("HITL error: Already claimed by another client")
			b.WriteString("\n[r]etry")
		case strings.Contains(m.Error, "QUEUE_ITEM_INVALID_STATE"):
			b.WriteString("HITL error: Item in unexpected state")
			b.WriteString("\n[r]etry")
		case strings.Contains(m.Error, "QUEUE_ITEM_NOT_FOUND"):
			b.WriteString("Queue item no longer exists — refreshing...")
		case strings.Contains(m.Error, "QUEUE_UNAVAILABLE"):
			b.WriteString("HITL error: queue unavailable")
			b.WriteString("\n[r]etry")
		case strings.Contains(m.Error, "choices"):
			b.WriteString("Unable to load choices")
			if m.ErrorRetry {
				b.WriteString("  [r]etry")
			}
		default:
			b.WriteString(fmt.Sprintf("HITL error: %s", m.Error))
			if m.ErrorRetry {
				b.WriteString("  [r]etry")
			}
		}
		return b.String()
	}

	// Cancel confirmation
	if m.ConfirmingCancel {
		b.WriteString("Cancel this workitem? This cannot be undone.  [y]es  [n]o")
		return b.String()
	}

	// Ready: show choices
	choices := m.Choices
	usingDefault := m.DefaultChoices || !m.ChoicesLoaded
	if len(choices) == 0 {
		choices = defaultChoices
		usingDefault = true
	}

	b.WriteString("Workitem awaiting decision  ")
	for i, ch := range choices {
		if i > 0 {
			b.WriteString("  ")
		}
		if len(ch.Label) > 0 {
			key := string(ch.Label[0])
			b.WriteString(fmt.Sprintf("[%s]%s", strings.ToLower(key), ch.Label[1:]))
		}
	}
	if !usingDefault {
		b.WriteString("  [R]elease")
	}

	return b.String()
}

// Update handles messages for the HITL prompt.
func (m HitlPromptModel) Update(msg tea.Msg) (HitlPromptModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if !m.Visible {
			return m, nil
		}

		switch msg.String() {
		case "y":
			if m.ConfirmingCancel {
				// Confirm cancel — handled by root update
				m.ConfirmingCancel = false
			}
		case "n":
			if m.ConfirmingCancel {
				m.ConfirmingCancel = false
				m.PendingChoice = ""
			}
		}
	}
	return m, nil
}

// ─── HITL Probe Message Types ───────────────────────────────────────────────

// HitlProbeResultMsg is sent when a HITL queue probe succeeds with a match.
type HitlProbeResultMsg struct {
	WorkitemID     string
	NodeName       string
	QueueItem      *api.QueueItem
	Choices        []api.Choice
	HasCancel      bool
	ChoicesLoaded  bool
	DefaultChoices bool
}

// HitlProbeRetryMsg is returned by the Probe cmd when all pods returned
// 404/error and retries remain.
type HitlProbeRetryMsg struct{}

// HitlProbeExhaustedMsg is emitted when all probe retries are exhausted.
type HitlProbeExhaustedMsg struct {
	WorkitemID string
	NodeName   string
	Diagnostic string
}

// HitlChoicesBlockedMsg is emitted when /choices returns 5xx — blocks HITL.
type HitlChoicesBlockedMsg struct {
	Err error
}

// ─── HitlState (Lifecycle Manager) ──────────────────────────────────────────

// HitlState tracks the HITL interaction lifecycle.
// It manages probe retries, port-forwards, and the HITL client session.
type HitlState struct {
	active        bool // true when a queue item has been found
	queueItem     *api.QueueItem
	choices       []api.Choice
	hasCancel     bool
	hitlClient    *api.HitlClient
	forwardID     string // port-forward ID for cleanup
	nodeName      string
	workitemID    string
	pendingChoice string // stored for retry handlers

	// Probe retry state
	probeAttempts int
	probeMax      int // 5 total
	exhausted     bool

	// Non-default port tracking for debug hint
	hitlPort       int
	debugHintShown bool
}

// NewHitlState creates a HitlState with the given HITL port.
func NewHitlState(hitlPort int) *HitlState {
	return &HitlState{
		probeMax: 5,
		hitlPort: hitlPort,
	}
}

// Probe performs one HITL probe attempt asynchronously.
// It does not reset probeAttempts — the caller (Update) must reset
// probeAttempts = 0 before calling Probe for a new workitem cycle.
// Returns a tea.Cmd that lists pods labeled for the node, opens port-forwards
// sequentially, and returns one of:
//
//	HitlProbeResultMsg     — queue item found
//	HitlProbeRetryMsg      — no match on any pod, retries remain
//	HitlProbeExhaustedMsg  — all attempts used, no match
//	HitlChoicesBlockedMsg  — /choices returned 5xx/transport error
func (h *HitlState) Probe(ctx context.Context, clientset kubernetes.Interface,
	namespace, nodeName, workitemID string, pm api.PortForwarder) tea.Cmd {

	// Close previous HITL forward from any prior attempt
	if h.forwardID != "" {
		pm.Close(h.forwardID)
		h.forwardID = ""
	}
	h.hitlClient = nil
	h.active = false
	h.nodeName = nodeName
	h.workitemID = workitemID

	return func() tea.Msg {
		h.probeAttempts++

		// Per-attempt timeout prevents a stuck probe on a dying pod
		// from blocking subsequent attempts.
		attemptCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		// List pods labeled flow.gideas.io/node-name=<node>
		pods, err := clientset.CoreV1().Pods(namespace).List(attemptCtx, metav1.ListOptions{
			LabelSelector: "flow.gideas.io/node-name=" + nodeName,
		})
		if err != nil {
			if h.probeAttempts >= h.probeMax {
				h.exhausted = true
				return HitlProbeExhaustedMsg{
					WorkitemID: workitemID,
					NodeName:   nodeName,
					Diagnostic: fmt.Sprintf("list pods: %v", err),
				}
			}
			return HitlProbeRetryMsg{}
		}

		// Probe each Ready pod sequentially
		for _, pod := range pods.Items {
			if !api.PodReady(&pod) {
				continue
			}

			// Open port-forward to pod port --hitl-port
			forwardID, localPort, err := pm.ForwardPod(attemptCtx, namespace, pod.Name, h.hitlPort)
			if err != nil {
				continue // try next pod
			}

			client := api.NewHitlClient(fmt.Sprintf("http://localhost:%d", localPort))
			qi, err := client.ProbeQueue(attemptCtx, workitemID)
			if err != nil || qi == nil {
				// 404, error, or transport failure — close forward, try next pod
				pm.Close(forwardID)
				continue
			}

			// Found a matching queue item. Keep this forward open.
			h.forwardID = forwardID
			h.queueItem = qi
			h.hitlClient = client
			h.active = true
			h.probeAttempts = 0

			// Probe /choices on the same forward
			choices, err := client.GetChoices(attemptCtx)
			if err != nil {
				// 5xx or transport error — block HITL interaction
				return HitlChoicesBlockedMsg{Err: err}
			}
			defaultChoiceSet := false
			if choices != nil && len(choices.Choices) > 0 {
				h.choices = choices.Choices
				h.hasCancel = choices.HasCancel
			} else {
				// 404 or empty choices array — use defaults
				h.choices = DefaultAPIChoices()
				h.hasCancel = true
				defaultChoiceSet = true
			}

			return HitlProbeResultMsg{
				WorkitemID:     workitemID,
				NodeName:       nodeName,
				QueueItem:      qi,
				Choices:        h.choices,
				HasCancel:      h.hasCancel,
				ChoicesLoaded:  choices != nil,
				DefaultChoices: defaultChoiceSet,
			}
		}

		// No pod returned 200.
		if h.probeAttempts >= h.probeMax {
			h.exhausted = true
			diagnostic := "HITL probe timed out — node may not have enqueued the item"
			if h.hitlPort != 8080 && !h.debugHintShown {
				h.debugHintShown = true
				diagnostic += "\nHITL probe failed — verify `--hitl-port` matches the node's `FLOW_HITL_PORT`"
			}
			return HitlProbeExhaustedMsg{
				WorkitemID: workitemID,
				NodeName:   nodeName,
				Diagnostic: diagnostic,
			}
		}

		return HitlProbeRetryMsg{}
	}
}

// ClaimAndDecide claims the queue item then decides with the given choice.
func (h *HitlState) ClaimAndDecide(ctx context.Context, choice string) error {
	if !h.active || h.hitlClient == nil {
		return fmt.Errorf("no active HITL session")
	}
	h.pendingChoice = choice
	// Claim
	_, err := h.hitlClient.Claim(ctx, h.workitemID)
	if err != nil {
		return err
	}
	// Decide
	return h.hitlClient.Decide(ctx, h.workitemID, choice)
}

// ReleaseClaim abandons the claim without deciding.
func (h *HitlState) ReleaseClaim(ctx context.Context) error {
	if !h.active || h.hitlClient == nil {
		return fmt.Errorf("no active HITL session")
	}
	_, err := h.hitlClient.Release(ctx, h.workitemID)
	if err != nil {
		return err
	}
	return nil
}

// Close cleans up the HITL port-forward. Does not stop tea.Tick timers
// (those are gated by h.exhausted in the Update loop).
func (h *HitlState) Close(pm api.PortForwarder) {
	if h.forwardID != "" {
		pm.Close(h.forwardID)
		h.forwardID = ""
	}
	h.active = false
	h.exhausted = false
	h.hitlClient = nil
}

// RetryQueueUnavailable performs up to 3 retries with 2s backoff
// for QUEUE_UNAVAILABLE errors on claim/decide/release.
func (h *HitlState) RetryQueueUnavailable(ctx context.Context, fn func(context.Context) error) error {
	var lastErr error
	for i := 0; i < 3; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		err := fn(ctx)
		if err == nil {
			return nil
		}
		if !api.IsQueueUnavailable(err) {
			return err // non-retryable error
		}
		lastErr = err
	}
	return fmt.Errorf("queue unavailable after 3 retries: %w", lastErr)
}

// ResetForNewWorkitem resets probe state for a new workitem cycle.
func (h *HitlState) ResetForNewWorkitem() {
	h.probeAttempts = 0
	h.exhausted = false
	h.debugHintShown = false
}

// ─── Test helpers (for tui package tests) ───────────────────────────────────

// SetActiveForTest sets the active flag for testing.
func (h *HitlState) SetActiveForTest() { h.active = true }

// SetForwardIDForTest sets the forwardID for testing.
func (h *HitlState) SetForwardIDForTest(fid string) { h.forwardID = fid }

// ─── Accessor methods (for tui package use) ─────────────────────────────────

// Exhausted returns true when all probe retries have been exhausted.
func (h *HitlState) Exhausted() bool { return h.exhausted }

// Active returns true when a queue item has been found and HITL is active.
func (h *HitlState) Active() bool { return h.active }

// GetNodeName returns the current node name being probed.
func (h *HitlState) GetNodeName() string { return h.nodeName }

// GetWorkitemID returns the current workitem ID being probed.
func (h *HitlState) GetWorkitemID() string { return h.workitemID }

// GetPendingChoice returns the stored pending choice for retry.
func (h *HitlState) GetPendingChoice() string { return h.pendingChoice }

// DefaultAPIChoices returns the default HITL choices as api.Choice values.
func DefaultAPIChoices() []api.Choice {
	return []api.Choice{
		{Value: "approve", Label: "Approve", Type: "route"},
		{Value: "cancel", Label: "Cancel", Type: "cancel"},
	}
}
