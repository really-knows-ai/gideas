package components

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"k8s.io/client-go/kubernetes"

	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/foundry/flow/tools/flowctl/internal/tui/types"
)

// QueueServiceLabel selects the queue-service pod to port-forward to. The
// spec pins a single queue-service replica; the flowctl tool reaches it
// directly, never a node (SPEC R-5.1).
// ponytail: assumes the queue-service Deployment carries this label; align
// the Deployment manifest when provisioning becomes explicit.
const QueueServiceLabel = "app=queue-service"

// QueueServiceRESTPort is the queue-service REST port (cmd/main.go defaultRESTPort).
const QueueServiceRESTPort = 8081

// ─── HitlPromptModel (Rendering Component) ──────────────────────────────────

// HitlPromptModel is the model for the HITL action prompt.
type HitlPromptModel struct {
	Visible        bool // true when a queue item exists
	QueueItemID    string
	Choices        []types.Choice // populated from the queue item or defaults
	Loading        bool           // true while probing or deciding
	Error          string
	ErrorRetry     bool // true if retry is possible
	ChoicesLoaded  bool // true once the queue item's choices are loaded
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

// ─── HITL Probe Message Types ───────────────────────────────────────────────

// HitlProbeResultMsg is sent when a HITL queue probe succeeds with a match.
type HitlProbeResultMsg struct {
	WorkitemID     string
	NodeName       string
	QueueItem      *api.QueueItem
	Choices        []api.Choice
	ChoicesLoaded  bool
	DefaultChoices bool
}

// HitlProbeRetryMsg is returned when a queue-service probe attempt found no
// match and retries remain.
type HitlProbeRetryMsg struct{}

// HitlProbeExhaustedMsg is emitted when all probe retries are exhausted.
type HitlProbeExhaustedMsg struct {
	WorkitemID string
	NodeName   string
	Diagnostic string
}

// ─── HitlState (Lifecycle Manager) ──────────────────────────────────────────

// HitlState tracks the HITL interaction lifecycle.
// It manages probe retries, port-forwards, and the HITL client session.
//
// mu guards the fields below: the Probe cmd goroutine writes state (probe
// attempts, exhausted, active session) while the bubbletea Update goroutine
// reads it through the accessors and writes via Close/ResetForNewWorkitem.
type HitlState struct {
	mu sync.Mutex

	active        bool // true when a queue item has been found
	queueItem     *api.QueueItem
	choices       []api.Choice
	hitlClient    *api.HitlClient
	forwardID     string // port-forward ID for cleanup
	nodeName      string
	workitemID    string
	pendingChoice string // stored for retry handlers

	// Probe retry state
	probeAttempts int
	probeMax      int // 5 total
	exhausted     bool
}

// NewHitlState creates a HitlState. The probe reaches the queue-service REST
// port directly (SPEC R-5.1), so no HITL port is needed.
func NewHitlState() *HitlState {
	return &HitlState{probeMax: 5}
}

// Probe performs one HITL probe attempt asynchronously.
// It does not reset probeAttempts — the caller (Update) must reset
// probeAttempts = 0 before calling Probe for a new workitem cycle.
// Returns a tea.Cmd that reaches the queue-service directly (SPEC R-5.1: queue
// interactions never go through a node): finds the queue-service pod, opens a
// port-forward to its REST port, probes the named queue's item, and returns
// one of:
//
//	HitlProbeResultMsg     — queue item found
//	HitlProbeRetryMsg      — no match, retries remain
//	HitlProbeExhaustedMsg  — all attempts used, no match
func (h *HitlState) Probe(ctx context.Context, clientset kubernetes.Interface,
	namespace, queueName, workitemID string, pm api.PortForwarder) tea.Cmd {

	// Close previous HITL forward from any prior attempt.
	h.mu.Lock()
	prevForward := h.forwardID
	h.forwardID = ""
	h.hitlClient = nil
	h.active = false
	h.nodeName = queueName
	h.workitemID = workitemID
	h.mu.Unlock()
	if prevForward != "" {
		pm.Close(prevForward)
	}

	return func() tea.Msg {
		h.mu.Lock()
		h.probeAttempts++
		attempts := h.probeAttempts
		h.mu.Unlock()

		// Per-attempt timeout prevents a stuck probe on a dying pod
		// from blocking subsequent attempts.
		attemptCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		// retryOrExhaust maps a failed attempt to the retry/exhaust message
		// (same semantics as the old pod-probe miss path).
		retryOrExhaust := func(diagnostic string) tea.Msg {
			h.mu.Lock()
			exhausted := attempts >= h.probeMax
			if exhausted {
				h.exhausted = true
			}
			h.mu.Unlock()
			if exhausted {
				return HitlProbeExhaustedMsg{
					WorkitemID: workitemID,
					NodeName:   queueName,
					Diagnostic: diagnostic,
				}
			}
			return HitlProbeRetryMsg{}
		}

		// The queue-service pod is the single interaction point (R-5.1).
		podName, found, err := pm.FindReadyPod(attemptCtx, namespace, QueueServiceLabel)
		switch {
		case err != nil:
			return retryOrExhaust(fmt.Sprintf("find queue-service pod: %v", err))
		case !found:
			return retryOrExhaust("no ready queue-service pod found")
		}

		// Open port-forward to the queue-service REST port.
		forwardID, localPort, err := pm.ForwardPod(attemptCtx, namespace, podName, QueueServiceRESTPort)
		if err != nil {
			return retryOrExhaust(fmt.Sprintf("port-forward to queue-service: %v", err))
		}

		client := api.NewHitlClient(fmt.Sprintf("http://localhost:%d", localPort), queueName)
		qi, err := client.ProbeQueue(attemptCtx, workitemID)
		if err != nil || qi == nil {
			// 404, error, or transport failure — close forward, try again
			pm.Close(forwardID)
			return retryOrExhaust("HITL probe timed out — queue-service has not enqueued the workitem")
		}

		// Found a matching queue item. Keep this forward open.
		h.mu.Lock()
		h.forwardID = forwardID
		h.queueItem = qi
		h.hitlClient = client
		h.active = true
		h.probeAttempts = 0
		h.mu.Unlock()

		// Choices are item metadata (R-5.2); there is no separate /choices call.
		selected, defaultChoiceSet := selectedFromItem(qi)
		h.mu.Lock()
		h.choices = selected
		h.mu.Unlock()

		return HitlProbeResultMsg{
			WorkitemID:     workitemID,
			NodeName:       queueName,
			QueueItem:      qi,
			Choices:        selected,
			ChoicesLoaded:  true,
			DefaultChoices: defaultChoiceSet,
		}
	}
}

// selectedFromItem maps the queue-service item's routing values to rendered
// choices, falling back to the default approve/cancel choices (R-5.2, R-5.3).
// Returns (choices, defaultChoiceSet).
func selectedFromItem(qi *api.QueueItem) ([]api.Choice, bool) {
	if len(qi.Choices) == 0 {
		return DefaultAPIChoices(), true
	}
	selected := make([]api.Choice, 0, len(qi.Choices))
	for _, v := range qi.Choices {
		selected = append(selected, api.Choice{Value: v, Label: v, Type: "route"})
	}
	return selected, false
}

// ClaimAndDecide claims the queue item then decides with the given choice.
func (h *HitlState) ClaimAndDecide(ctx context.Context, choice string) error {
	h.mu.Lock()
	if !h.active || h.hitlClient == nil {
		h.mu.Unlock()
		return fmt.Errorf("no active HITL session")
	}
	h.pendingChoice = choice
	client := h.hitlClient
	workitemID := h.workitemID
	h.mu.Unlock()
	// Claim
	_, err := client.Claim(ctx, workitemID)
	if err != nil {
		return err
	}
	// Decide
	return client.Decide(ctx, workitemID, choice)
}

// ReleaseClaim abandons the claim without deciding.
func (h *HitlState) ReleaseClaim(ctx context.Context) error {
	h.mu.Lock()
	if !h.active || h.hitlClient == nil {
		h.mu.Unlock()
		return fmt.Errorf("no active HITL session")
	}
	client := h.hitlClient
	workitemID := h.workitemID
	h.mu.Unlock()
	_, err := client.Release(ctx, workitemID)
	if err != nil {
		return err
	}
	return nil
}

// Close cleans up the HITL port-forward. Does not stop tea.Tick timers
// (those are gated by h.exhausted in the Update loop).
func (h *HitlState) Close(pm api.PortForwarder) {
	h.mu.Lock()
	forwardID := h.forwardID
	h.forwardID = ""
	h.active = false
	h.exhausted = false
	h.hitlClient = nil
	h.mu.Unlock()
	if forwardID != "" {
		pm.Close(forwardID)
	}
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
	h.mu.Lock()
	h.probeAttempts = 0
	h.exhausted = false
	h.mu.Unlock()
}

// RecordProbeFailure records one failed probe attempt against the queue-service
// and returns the retry/exhaust message, mirroring Probe's retryOrExhaust
// semantics: attempts are counted on HitlState so the probe cycle re-arms while
// retries remain and stops once exhausted (the 3s tick is gated by Exhausted).
// workitemID is passed in because HitlState only records it after a successful
// Probe; resolution failures happen before any queue is known.
func (h *HitlState) RecordProbeFailure(workitemID, diagnostic string) tea.Msg {
	h.mu.Lock()
	h.probeAttempts++
	attempts := h.probeAttempts
	exhausted := attempts >= h.probeMax
	if exhausted {
		h.exhausted = true
	}
	h.mu.Unlock()
	if exhausted {
		return HitlProbeExhaustedMsg{
			WorkitemID: workitemID,
			NodeName:   QueueServiceLabel,
			Diagnostic: diagnostic,
		}
	}
	return HitlProbeRetryMsg{}
}

// ─── Accessor methods (for tui package use) ─────────────────────────────────

// Exhausted returns true when all probe retries have been exhausted.
func (h *HitlState) Exhausted() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exhausted
}

// Active returns true when a queue item has been found and HITL is active.
func (h *HitlState) Active() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active
}

// GetNodeName returns the current node name being probed.
func (h *HitlState) GetNodeName() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.nodeName
}

// GetWorkitemID returns the current workitem ID being probed.
func (h *HitlState) GetWorkitemID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.workitemID
}

// GetPendingChoice returns the stored pending choice for retry.
func (h *HitlState) GetPendingChoice() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pendingChoice
}

// DefaultAPIChoices returns the default HITL choices as api.Choice values.
func DefaultAPIChoices() []api.Choice {
	return []api.Choice{
		{Value: "approve", Label: "Approve", Type: "route"},
		{Value: "cancel", Label: "Cancel", Type: "cancel"},
	}
}
