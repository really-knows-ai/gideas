package flow

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// FanOutTask describes a single child Workitem to create and route.
type FanOutTask struct {
	// TargetNode is the name of the FoundryNode to route the child to.
	TargetNode string

	// Artefacts to store on the child before routing.
	Artefacts []ChildArtefact
}

// ChildArtefact is an artefact to attach to a child Workitem before routing.
type ChildArtefact struct {
	ID               string
	GovernedArtefact string
	Content          []byte
}

// ChildResult pairs a child's terminal status with its collected artefacts.
type ChildResult struct {
	Status    ChildWorkitemStatus
	Artefacts map[string][]byte // artefactID → content (nil if artefact absent)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func allTerminal(children []ChildWorkitemStatus) bool {
	if len(children) == 0 {
		return false
	}
	for _, ch := range children {
		if !isTerminalPhase(ch.Phase) {
			return false
		}
	}
	return true
}

func isTerminalPhase(phase string) bool {
	return phase == PhaseCompleted || phase == PhaseFailed
}
