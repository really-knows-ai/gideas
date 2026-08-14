package tui

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foundry/flow/tools/flowctl/internal/api"
	"github.com/foundry/flow/tools/flowctl/internal/tui/components"
)

// updateCreateWizard handles semantic messages for the create wizard.
func (m *Model) updateCreateWizard(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case WizardDataLoadedMsg:
		m.createWizard.Loading = false
		if msg.Blocked != "" {
			m.createWizard.Blocked = msg.Blocked
			m.createWizard.Error = msg.BlockedErr
		} else if msg.BlockedErr != "" {
			// Transient API or other error — show as error state
			m.createWizard.Error = msg.BlockedErr
			m.createWizard.Stage = components.StageError
		} else {
			m.createWizard.EntryNodes = msg.EntryNodes
			m.createWizard.Artefacts = msg.Artefacts
			// Store entry contracts data for contract-based artefact filtering
			m.wizardEntryContracts = msg.EntryContracts
			m.wizardNodeEntryMap = msg.NodeEntryMap
		}

	case CreateFieldUpdatedMsg:
		switch msg.Field {
		case "prompt":
			m.createWizard.Fields.PromptText = msg.Value
		case "entryNode":
			m.createWizard.Fields.EntryNode = msg.Value
		case "artefactID":
			m.createWizard.Fields.ArtefactID = msg.Value
		case "governedArtefact":
			m.createWizard.Fields.GovernedArtefact = msg.Value
		}

	case CreateConfirmMsg:
		// Real create flow: pre-validate, create CRD, store artefact, update status
		m.createWizard.Loading = true
		m.err = nil

		// Pre-create validation
		flow, err := m.k8s.GetFoundryFlow(m.ctx, m.namespace)
		if err != nil {
			m.createWizard.Loading = false
			if errors.Is(err, api.ErrMultipleFoundryFlows) {
				m.createWizard.Blocked = "multiple_flows"
				m.createWizard.Error = "multiple FoundryFlows detected; use a namespace with exactly one FoundryFlow"
			} else {
				m.createWizard.Error = fmt.Sprintf("Failed to check FoundryFlow: %v", err)
				m.createWizard.Stage = components.StageError
			}
			m.logIfEnabled("ERROR", "create", fmt.Sprintf("foundryflow check failed: %v", err))
			return m, nil
		}
		if flow == nil {
			m.createWizard.Loading = false
			m.createWizard.Blocked = "no_flow"
			m.createWizard.Error = "no FoundryFlow in namespace"
			m.logIfEnabled("WARN", "create", "create blocked: no FoundryFlow in namespace")
			return m, nil
		}

		// Check entry nodes
		nodes, err := m.k8s.ListFoundryNodes(m.ctx, m.namespace)
		if err != nil {
			m.createWizard.Loading = false
			m.createWizard.Error = fmt.Sprintf("list entry nodes: %v", err)
			m.logIfEnabled("ERROR", "create", m.createWizard.Error)
			return m, nil
		}
		var entryNodes []string
		for _, n := range nodes {
			if n.Entry != "" {
				entryNodes = append(entryNodes, n.Name)
			}
		}
		if len(entryNodes) == 0 {
			m.createWizard.Loading = false
			m.createWizard.Error = "No FoundryNodes with a configured entry point found in namespace"
			m.logIfEnabled("WARN", "create", m.createWizard.Error)
			return m, nil
		}

		// Governed artefacts are already filtered by entry contract in
		// updateCreateWizardKeys when the user selected the entry node.
		// Validate the selection but do not re-filter here.
		if m.createWizard.Fields.GovernedArtefact == "" && len(m.createWizard.Artefacts) > 0 {
			m.createWizard.Fields.GovernedArtefact = m.createWizard.Artefacts[0]
		}

		// Start the create execution as a background command
		m.createWizard.Stage = components.StageCreating
		m.createWizard.Loading = false
		selectedNode := m.createWizard.Fields.EntryNode
		promptText := m.createWizard.Fields.PromptText
		artefactID := m.createWizard.Fields.ArtefactID
		if artefactID == "" {
			artefactID = "petition"
		}
		governedArtefact := m.createWizard.Fields.GovernedArtefact
		if governedArtefact == "" && len(m.createWizard.Artefacts) > 0 {
			governedArtefact = m.createWizard.Artefacts[0]
		}

		return m, func() tea.Msg {
			return m.executeCreate(m.ctx, selectedNode, promptText, artefactID, governedArtefact)
		}

	case CreateSuccessMsg:
		m.createWizard.WorkitemID = msg.WorkitemName
		m.createWizard.SuccessName = msg.WorkitemName
		m.createWizard.Stage = components.StageComplete
		m.createWizard.Loading = false
		m.createHasCRD = false
		m.createHasArtefact = false
		// Populate with real topology data
		topoCmd := m.loadTopology()
		arCmd := m.loadArtefacts(msg.WorkitemName)
		m.workitemDetail.workitemName = msg.WorkitemName
		m.workitemDetail.loading = true
		m.workitemDetail.loaded = true
		m.screen = ScreenWorkitemDetail
		m.err = nil
		if topoCmd != nil && arCmd != nil {
			return m, tea.Batch(topoCmd, arCmd)
		}
		return m, nil

	case CreateErrorMsg:
		m.createWizard.Loading = false
		m.createWizard.Stage = components.StageError
		m.createWizard.Error = msg.Err.Error()
		m.createWizard.Retryable = msg.Retry
		m.createHasCRD = msg.HasCRD
		m.createHasArtefact = msg.HasArtefact
		m.logIfEnabled("ERROR", "create", fmt.Sprintf("create failed: %v", msg.Err))

	case CreateCancelMsg:
		// Return to workitem list
		m.createWizard = components.NewCreateWizard()
		m.screen = ScreenWorkitemList
		m.err = nil
		m.createHasCRD = false
		m.createHasArtefact = false
	}
	return m, nil
}

// updateCreateWizardKeys handles key presses for the create wizard.
func (m *Model) updateCreateWizardKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Snapshot error state and step before component update.
	wasError := m.createWizard.Stage == components.StageError
	wasRetryable := m.createWizard.Retryable
	prevStep := m.createWizard.Step

	// Capture entry node selection before step change (cursor is reset by component).
	selectedNode := m.createWizard.SelectedEntryNode()

	m.createWizard, _ = m.createWizard.Update(msg)

	// Step 4 (confirmation): enter triggers creation
	if key == "enter" && m.createWizard.Step == 4 && !wasError {
		// Advance stage to StageIdle first (component keeps stage at StageIdle after tabbing through)
		return m, func() tea.Msg {
			return CreateConfirmMsg{}
		}
	}

	// If we advanced from step 1 (entry node selection) to step 2, set the
	// entry node field and filter governed artefacts based on the selected
	// node's entry contract so step 3 shows the correct options.
	if prevStep == 1 && m.createWizard.Step == 2 && selectedNode != "" {
		m.createWizard.Fields.EntryNode = selectedNode
		m.filterArtefactsForNode(selectedNode)
	}

	// Handle semantic keys from error state
	if wasError {
		if key == "r" && wasRetryable {
			return m, func() tea.Msg {
				return CreateConfirmMsg{}
			}
		}
		if key == "c" {
			return m, func() tea.Msg {
				return CreateCancelMsg{}
			}
		}
	}

	return m, nil
}

// ─── Create execution helpers ──────────────────────────────────────────────

// executeCreate performs the full create flow: CRD create -> StoreArtefact -> status update.
// It runs as a goroutine command, returning CreateSuccessMsg or CreateErrorMsg.
// On retry (createHasCRD/createHasArtefact), steps 1-3 are skipped.
func (m *Model) executeCreate(ctx context.Context, selectedNode, promptText, artefactID, governedArtefact string) tea.Msg {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Pre-declare for possible retry within the CRD creation loop
	var timestamp int64
	var randomBytes []byte

	// 1. Use existing workitem name on retry, or generate a new one
	var name string
	if m.createHasCRD {
		name = m.createWizard.WorkitemID
		m.logIfEnabled("INFO", "create", fmt.Sprintf("retrying with existing CRD: %s", name))
	} else {
		artefactID = sanitizeName(artefactID)
		timestamp = time.Now().Unix()
		randomBytes = make([]byte, 4)
		if _, err := rand.Read(randomBytes); err != nil {
			return CreateErrorMsg{Err: fmt.Errorf("generate random name: %w", err), Retry: true, HasCRD: false, HasArtefact: false}
		}
		name = fmt.Sprintf("%s-%d-%x", artefactID, timestamp, randomBytes)
		m.logIfEnabled("INFO", "create", fmt.Sprintf("generated name: %s", name))
	}

	// 2. Create Workitem CRD — skip if already created on a prior attempt
	if !m.createHasCRD {
		labels := map[string]string{
			"flow.foundry.io/creator": "flowctl",
		}
		var crdErr error
		for attempt := 0; attempt < 3; attempt++ {
			crdErr = m.k8s.CreateWorkitem(ctx, m.namespace, name, labels)
			if crdErr == nil {
				break
			}
			// If already exists, regenerate name and retry
			if strings.Contains(crdErr.Error(), "already exists") {
				timestamp = time.Now().Unix()
				randomBytes = make([]byte, 4)
				if _, err := rand.Read(randomBytes); err != nil {
					return CreateErrorMsg{Err: fmt.Errorf("generate name: %w", err), Retry: true, HasCRD: false, HasArtefact: false}
				}
				name = fmt.Sprintf("%s-%d-%x", artefactID, timestamp, randomBytes)
				continue
			}
			// Non-conflict error — fail
			break
		}
		if crdErr != nil {
			m.logIfEnabled("ERROR", "create", fmt.Sprintf("create CRD failed: %v", crdErr))
			return CreateErrorMsg{Err: fmt.Errorf("create CRD: %w", crdErr), Retry: true, HasCRD: false, HasArtefact: false}
		}
		m.createWizard.WorkitemID = name
		m.logIfEnabled("INFO", "create", fmt.Sprintf("CRD created: %s", name))
	}

	// 3. Compute SHA-256 and store artefact — skip if already stored
	if !m.createHasArtefact {
		m.createWizard.Stage = components.StageStoringArtefact
		contentHash := api.ComputeSHA256([]byte(promptText))
		storeReq := api.StoreArtefactRequest{
			WorkitemID:       name,
			ArtefactID:       artefactID,
			GovernedArtefact: governedArtefact,
			Content:          []byte(promptText),
			ContentHash:      contentHash,
		}
		if m.archivist == nil {
			err := fmt.Errorf("store artefact: Archivist client unavailable")
			m.logIfEnabled("ERROR", "create", err.Error())
			if !m.createHasCRD {
				if delErr := m.k8s.DeleteWorkitem(ctx, m.namespace, name); delErr != nil {
					m.logIfEnabled("WARN", "create", fmt.Sprintf("cleanup delete failed for %s: %v", name, delErr))
				}
			}
			return CreateErrorMsg{Err: err, Retry: true, HasCRD: m.createHasCRD, HasArtefact: false}
		}
		if err := m.archivist.StoreArtefact(ctx, m.namespace, storeReq); err != nil {
			m.logIfEnabled("ERROR", "create", fmt.Sprintf("StoreArtefact failed: %v", err))
			if !m.createHasCRD {
				if delErr := m.k8s.DeleteWorkitem(ctx, m.namespace, name); delErr != nil {
					m.logIfEnabled("WARN", "create", fmt.Sprintf("cleanup delete failed for %s: %v", name, delErr))
				}
			}
			return CreateErrorMsg{Err: fmt.Errorf("store artefact: %w", err), Retry: true, HasCRD: m.createHasCRD, HasArtefact: false}
		}
		m.logIfEnabled("INFO", "create", fmt.Sprintf("artefact stored: %s/%s", name, artefactID))
	}

	// 4. Update status subresource
	m.createWizard.Stage = components.StageUpdatingStatus
	if err := m.k8s.UpdateWorkitemStatus(ctx, m.namespace, name, "Pending", selectedNode); err != nil {
		m.logIfEnabled("ERROR", "create", fmt.Sprintf("status update failed: %v", err))
		// Status update failed — CRD and artefact exist; retry skips those steps
		return CreateErrorMsg{
			Err:         fmt.Errorf("status update: %w", err),
			Retry:       true,
			HasCRD:      true,
			HasArtefact: true,
		}
	}
	m.logIfEnabled("INFO", "create", fmt.Sprintf("workitem %s created successfully (status set)", name))

	return CreateSuccessMsg{WorkitemName: name}
}

// sanitizeName replaces non-alphanumeric characters with '-' for K8s-safe names.
func sanitizeName(s string) string {
	var result strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			result.WriteRune(r)
		} else {
			result.WriteRune('-')
		}
	}
	return result.String()
}

// loadWizardData fetches entry nodes, governed artefacts, and validates the
// FoundryFlow for the create wizard. Runs as a command, sends WizardDataLoadedMsg.
func (m *Model) loadWizardData() tea.Cmd {
	return func() tea.Msg {
		if m.k8s == nil {
			return WizardDataLoadedMsg{
				Blocked:    "no_flow",
				BlockedErr: "no K8s client",
			}
		}

		// 1. Check FoundryFlow (blocked state detection)
		flow, err := m.k8s.GetFoundryFlow(m.ctx, m.namespace)
		if err != nil {
			if errors.Is(err, api.ErrMultipleFoundryFlows) {
				return WizardDataLoadedMsg{
					Blocked:    "multiple_flows",
					BlockedErr: "multiple FoundryFlows detected; use a namespace with exactly one FoundryFlow",
				}
			}
			return WizardDataLoadedMsg{
				BlockedErr: fmt.Sprintf("Failed to check FoundryFlow: %v", err),
			}
		}
		if flow == nil {
			return WizardDataLoadedMsg{
				Blocked:    "no_flow",
				BlockedErr: "no FoundryFlow in namespace",
			}
		}

		// 2. Load entry nodes (filter by Entry != "")
		nodes, err := m.k8s.ListFoundryNodes(m.ctx, m.namespace)
		var entryNodes []string
		nodeEntryMap := make(map[string]string)
		if err == nil {
			for _, n := range nodes {
				if n.Entry != "" {
					entryNodes = append(entryNodes, n.Name)
					nodeEntryMap[n.Name] = n.Entry
				}
			}
		}

		// 3. Load governed artefacts (for step 3 selection)
		var artefacts []string
		gas, err := m.k8s.ListGovernedArtefacts(m.ctx, m.namespace)
		if err == nil {
			for _, ga := range gas {
				artefacts = append(artefacts, ga.Name)
			}
		}

		return WizardDataLoadedMsg{
			EntryNodes:     entryNodes,
			Artefacts:      artefacts,
			EntryContracts: flow.EntryContracts,
			NodeEntryMap:   nodeEntryMap,
		}
	}
}

// filterArtefactsForNode filters the create wizard's governed artefact list based on the
// selected entry node's contract.  If the entry contract has governed artefact keys, only
// those keys are shown.  Otherwise the full list from GovernedArtefact CRs is retained.
func (m *Model) filterArtefactsForNode(nodeName string) {
	if m.wizardEntryContracts == nil {
		return // no contract data available — keep full list
	}
	contractName := m.wizardNodeEntryMap[nodeName]
	if contractName == "" {
		return // node has no entry contract — keep full list
	}
	ec, ok := m.wizardEntryContracts[contractName]
	if !ok {
		return // contract not found — keep full list
	}
	ecMap, ok := ec.(map[string]interface{})
	if !ok || len(ecMap) == 0 {
		return // empty or invalid contract — fall back to full list
	}

	// Build filtered list from contract keys
	filtered := make([]string, 0, len(ecMap))
	for k := range ecMap {
		filtered = append(filtered, k)
	}
	m.createWizard.Artefacts = filtered

	// Reset the governed artefact field if current selection is no longer valid
	if m.createWizard.Fields.GovernedArtefact != "" {
		stillValid := false
		for _, a := range filtered {
			if a == m.createWizard.Fields.GovernedArtefact {
				stillValid = true
				break
			}
		}
		if !stillValid && len(filtered) > 0 {
			m.createWizard.Fields.GovernedArtefact = filtered[0]
		}
	}
}
