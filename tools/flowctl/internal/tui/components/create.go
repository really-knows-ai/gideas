package components

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// CreateStage tracks which phase of creation the wizard is in.
type CreateStage int

const (
	StageIdle              CreateStage = iota
	StagePromptText                    // 0: entering prompt text
	StageEntryNode                     // 1: selecting entry node
	StageArtefactID                    // 2: entering artefact ID
	StageGovernedArtefact              // 3: selecting governed artefact
	StageConfirm                       // 4: confirmation
	StageCreating                      // 5: CRD create in progress
	StageStoringArtefact               // 6: StoreArtefact in progress
	StageUpdatingStatus                // 7: Status update in progress
	StageComplete                      // 8: creation complete
	StageError                         // 9: creation error
)

// CreateWizardModel is the model for the create Workitem wizard.
type CreateWizardModel struct {
	Step         int    // 0=prompt, 1=entryNode, 2=artefactID, 3=governedArtefact, 4=confirm, 5=success/error
	Fields       CreateFields
	FoundryFlows []string // fake: ["main-flow"]
	EntryNodes   []string // fake: ["forge", "human-entry"]
	Artefacts    []string // fake: ["petition", "haiku"]
	Loading      bool
	Error        string
	SuccessName  string // set on successful creation
	Blocked      string // "" if not blocked, "no_flow" or "multiple_flows" if blocked

	// Phase 06: create execution stage tracking
	Stage       CreateStage // current creation stage
	WorkitemID  string      // ID of created workitem
	Retryable   bool        // true if retry is possible after error

	cursor int // cursor for selection fields
}

// CreateFields holds the wizard input fields.
type CreateFields struct {
	PromptText       string
	EntryNode        string
	ArtefactID       string // defaults to "petition"
	GovernedArtefact string
}

// NewCreateWizard creates a CreateWizardModel in initial state.
func NewCreateWizard() CreateWizardModel {
	return CreateWizardModel{
		Step:   0,
		Fields: CreateFields{
			ArtefactID: "petition",
		},
		FoundryFlows: []string{"main-flow"},
		EntryNodes:   []string{"forge", "human-entry"},
		Artefacts:    []string{"petition", "haiku"},
	}
}

// View renders the create wizard.
func (m CreateWizardModel) View() string {
	var b strings.Builder

	// Blocked states
	if m.Blocked == "no_flow" {
		b.WriteString("Cannot seed — no FoundryFlow in namespace. A Workitem requires exactly one FoundryFlow.\n\nPress esc to return")
		return b.String()
	}
	if m.Blocked == "multiple_flows" {
		b.WriteString("Cannot seed — multiple FoundryFlows detected. Use a namespace with exactly one FoundryFlow.\n\nPress esc to return")
		return b.String()
	}

	if m.Step == 5 {
		if m.SuccessName != "" {
			b.WriteString(fmt.Sprintf("Workitem created successfully: %s\n\nPress enter to open the Workitem detail", m.SuccessName))
		} else {
			b.WriteString(fmt.Sprintf("Error creating Workitem: %s\n[r]etry  [c]ancel", m.Error))
		}
		return b.String()
	}

	// Phase 06: creation progress stages (Step 6-9 mapped to CreateStage)
	if m.Stage == StageCreating {
		b.WriteString("Creating Workitem CRD...")
		return b.String()
	}
	if m.Stage == StageStoringArtefact {
		b.WriteString("Storing artefact...")
		return b.String()
	}
	if m.Stage == StageUpdatingStatus {
		b.WriteString("Setting Workitem status...")
		return b.String()
	}
	if m.Stage == StageComplete {
		b.WriteString(fmt.Sprintf("Workitem created successfully: %s\n\nPress enter to open the Workitem detail", m.WorkitemID))
		return b.String()
	}
	if m.Stage == StageError {
		if m.Retryable {
			b.WriteString(fmt.Sprintf("Error: %s\n[r]etry  [c]ancel", m.Error))
		} else {
			b.WriteString(fmt.Sprintf("Error: %s\n[c]ancel", m.Error))
		}
		return b.String()
	}

	// Confirmation step
	if m.Step == 4 {
		b.WriteString(lipgloss.NewStyle().Bold(true).Render("Create Workitem — Confirm"))
		b.WriteString("\n")
		b.WriteString(strings.Repeat("─", 40))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("Prompt text:        %s\n", m.Fields.PromptText))
		b.WriteString(fmt.Sprintf("Entry node:         %s\n", m.Fields.EntryNode))
		b.WriteString(fmt.Sprintf("Artefact ID:        %s\n", m.Fields.ArtefactID))
		b.WriteString(fmt.Sprintf("Governed artefact:  %s\n", m.Fields.GovernedArtefact))
		b.WriteString("\nPress enter to create, esc to cancel")
		return b.String()
	}

	// Step prompts
	switch m.Step {
	case 0:
		b.WriteString("Enter prompt text:\n")
		if m.Fields.PromptText != "" {
			b.WriteString(fmt.Sprintf("  %s\n", m.Fields.PromptText))
		} else {
			b.WriteString("  (type and press enter)\n")
		}
	case 1:
		b.WriteString("Select entry node:\n")
		for i, node := range m.EntryNodes {
			cursor := "  "
			if i == m.cursor {
				cursor = "❯ "
			}
			b.WriteString(fmt.Sprintf("%s%s\n", cursor, node))
		}
	case 2:
		b.WriteString("Entry artefact ID:\n")
		if m.Fields.ArtefactID != "" {
			b.WriteString(fmt.Sprintf("  %s\n", m.Fields.ArtefactID))
		} else {
			b.WriteString("  (type or accept default 'petition' and press enter)\n")
		}
	case 3:
		b.WriteString("Select governed artefact:\n")
		for i, art := range m.Artefacts {
			cursor := "  "
			if i == m.cursor {
				cursor = "❯ "
			}
			b.WriteString(fmt.Sprintf("%s%s\n", cursor, art))
		}
	}

	b.WriteString(fmt.Sprintf("\ntab/shift+tab navigate  •  enter confirm  •  esc cancel"))
	return b.String()
}

// Update handles messages for the create wizard.
func (m CreateWizardModel) Update(msg tea.Msg) (CreateWizardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			switch m.Step {
			case 1:
				if m.cursor < len(m.EntryNodes)-1 {
					m.cursor++
				}
			case 3:
				if m.cursor < len(m.Artefacts)-1 {
					m.cursor++
				}
			}
		case "tab":
			if m.Step < 4 {
				m.Step++
				m.cursor = 0
			}
		case "shift+tab":
			if m.Step > 0 {
				m.Step--
				m.cursor = 0
			}
		}
	}
	return m, nil
}
