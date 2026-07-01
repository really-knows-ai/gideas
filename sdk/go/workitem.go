package flow

import (
	"context"
	"fmt"
	"sort"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// Workitem is the composition root for workitem-scoped operations.
// Constructed by Client.GetWorkitem(). All methods manage their own
// gRPC context via the internal session.
type Workitem struct {
	session   *session
	id        string
	namespace string
	// ponytail: local cache only; stale if resumed externally.
	// No IsSuspended RPC exists in the proto contract (non-goal).
	suspended bool
}

// ID returns the workitem identifier.
func (w *Workitem) ID() string { return w.id }

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Complete submits a completion action to the Operator. No bool return —
// the operator returns a gRPC error when it rejects the action.
func (w *Workitem) Complete(opts ...CompleteOption) error {
	action := &flowv1.CompleteAction{}
	for _, o := range opts {
		o(action)
	}
	_, err := w.session.Operator.SubmitResult(context.Background(), &flowv1.SubmitResultRequest{
		WorkitemId: w.id,
		Action:     &flowv1.SubmitResultRequest_Complete{Complete: action},
	})
	if err != nil {
		return fmt.Errorf("flow sdk: complete failed: %w", err)
	}
	return nil
}

// RouteTo submits a routing action through the named output channel.
func (w *Workitem) RouteTo(outputName string) error {
	_, err := w.session.Operator.SubmitResult(context.Background(), &flowv1.SubmitResultRequest{
		WorkitemId: w.id,
		Action: &flowv1.SubmitResultRequest_Route{
			Route: &flowv1.RouteAction{Target: outputName, Output: true},
		},
	})
	if err != nil {
		return fmt.Errorf("flow sdk: route failed: %w", err)
	}
	return nil
}

// Suspend transitions the workitem to Suspended phase and updates the
// local cached suspension state.
func (w *Workitem) Suspend(opts ...SuspendOption) error {
	action := &flowv1.SuspendAction{}
	for _, o := range opts {
		o(action)
	}
	_, err := w.session.Operator.SubmitResult(context.Background(), &flowv1.SubmitResultRequest{
		WorkitemId: w.id,
		Action:     &flowv1.SubmitResultRequest_Suspend{Suspend: action},
	})
	if err != nil {
		return fmt.Errorf("flow sdk: suspend failed: %w", err)
	}
	w.suspended = true
	return nil
}

// Resume requests that a suspended workitem be re-dispatched and clears
// the local cached suspension state.
func (w *Workitem) Resume() error {
	_, err := w.session.Operator.ResumeWorkitem(context.Background(), &flowv1.ResumeWorkitemRequest{
		WorkitemId: w.id,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: resume failed: %w", err)
	}
	w.suspended = false
	return nil
}

// Heartbeat resets the Sidecar's inactivity timer for this workitem.
func (w *Workitem) Heartbeat() error {
	_, err := w.session.Sidecar.Heartbeat(context.Background(), &flowv1.HeartbeatRequest{
		WorkitemId: w.id,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: heartbeat failed: %w", err)
	}
	return nil
}

// PauseTimer suspends the Sidecar's inactivity timer (Sidecar-local).
func (w *Workitem) PauseTimer() error {
	_, err := w.session.Sidecar.PauseTimer(context.Background(), &flowv1.PauseTimerRequest{
		WorkitemId: w.id,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: pause timer failed: %w", err)
	}
	return nil
}

// ResumeTimer resumes the Sidecar's inactivity timer.
func (w *Workitem) ResumeTimer() error {
	_, err := w.session.Sidecar.ResumeTimer(context.Background(), &flowv1.ResumeTimerRequest{
		WorkitemId: w.id,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: resume timer failed: %w", err)
	}
	return nil
}

// IsSuspended returns the locally cached suspension state. Returns false
// if this Workitem was never suspended or was resumed through this SDK
// instance. Does NOT make an RPC. If the workitem was resumed externally
// (outside this SDK instance), the cached value may be stale.
func (w *Workitem) IsSuspended() (bool, error) {
	return w.suspended, nil
}

// ---------------------------------------------------------------------------
// Artefacts
// ---------------------------------------------------------------------------

// GetArtefact returns the current (head) artefact for the given governed
// artefact kind (e.g. "petition", "haiku"). Returns a domain *Artefact
// wired with the session.
func (w *Workitem) GetArtefact(governedArtefact string) (*Artefact, error) {
	resp, err := w.session.Archivist.GetArtefact(context.Background(), &flowv1.GetArtefactRequest{
		WorkitemId: w.id,
		ArtefactId: governedArtefact,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get artefact failed: %w", err)
	}
	return newArtefact(
		w.session, governedArtefact,
		resp.GetGovernedArtefact(), resp.GetContent(), resp.GetVersionHash(),
	), nil
}

// FindArtefact looks up a specific artefact by its unique artefact ID
// (e.g. "art-abc-123"). Returns a domain *Artefact wired with the session.
func (w *Workitem) FindArtefact(artefactID string) (*Artefact, error) {
	resp, err := w.session.Archivist.GetArtefact(context.Background(), &flowv1.GetArtefactRequest{
		WorkitemId: w.id,
		ArtefactId: artefactID,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: find artefact failed: %w", err)
	}
	return newArtefact(
		w.session, artefactID,
		resp.GetGovernedArtefact(), resp.GetContent(), resp.GetVersionHash(),
	), nil
}

// ---------------------------------------------------------------------------
// Feedback
// ---------------------------------------------------------------------------

// AddFeedback creates a new feedback item on the specified artefact.
// Returns the generated feedback ID string.
func (w *Workitem) AddFeedback(artefactID string, canWontFix bool, message string) (string, error) {
	resp, err := w.session.Archivist.AddFeedback(context.Background(), &flowv1.AddFeedbackRequest{
		WorkitemId: w.id,
		ArtefactId: artefactID,
		CanWontFix: canWontFix,
		Message:    message,
	})
	if err != nil {
		return "", fmt.Errorf("flow sdk: add feedback failed: %w", err)
	}
	return resp.GetFeedbackId(), nil
}

// GetFeedback returns all feedback items for the specified artefact as
// domain *Feedback objects wired with the session.
func (w *Workitem) GetFeedback(artefactID string) ([]*Feedback, error) {
	resp, err := w.session.Archivist.GetFeedback(context.Background(), &flowv1.GetFeedbackRequest{
		WorkitemId: w.id,
		ArtefactId: artefactID,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get feedback failed: %w", err)
	}
	items := resp.GetFeedbackItems()
	fbs := make([]*Feedback, 0, len(items))
	for _, item := range items {
		fbs = append(fbs, newFeedback(item, w.session))
	}
	return fbs, nil
}

// HasUnresolvedFeedback returns true if any feedback for the artefact
// is not in RESOLVED state.
func (w *Workitem) HasUnresolvedFeedback(artefactID string) (bool, error) {
	resp, err := w.session.Archivist.HasUnresolvedFeedback(context.Background(), &flowv1.HasUnresolvedFeedbackRequest{
		WorkitemId: w.id,
		ArtefactId: artefactID,
	})
	if err != nil {
		return false, fmt.Errorf("flow sdk: has unresolved feedback failed: %w", err)
	}
	return resp.GetHasUnresolved(), nil
}

// ---------------------------------------------------------------------------
// Laws
// ---------------------------------------------------------------------------

// GetLawGroups returns all law groups for the given representation type.
// repType may be empty to query all laws without filtering by rep type.
// Composes QueryLaws + ListLawGroups.
func (w *Workitem) GetLawGroups(repType string) ([]*LawGroup, error) {
	var filter *flowv1.LawFilter
	if repType != "" {
		filter = &flowv1.LawFilter{RepresentationType: repType}
	}
	lawsResp, err := w.session.Librarian.QueryLaws(context.Background(), &flowv1.QueryLawsRequest{Filter: filter})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: query laws failed: %w", err)
	}

	// Collect unique group names from matching laws.
	groupNames := make(map[string]bool)
	for _, law := range lawsResp.GetLaws() {
		gn := law.GetGroup()
		if gn == "" {
			gn = "default"
		}
		groupNames[gn] = true
	}
	if len(groupNames) == 0 {
		return nil, nil
	}

	// List law group configs.
	groupsResp, err := w.session.Librarian.ListLawGroups(context.Background(), &flowv1.ListLawGroupsRequest{})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: list law groups failed: %w", err)
	}
	configs := make(map[string]*LawGroup, len(groupsResp.GetGroups()))
	for _, g := range groupsResp.GetGroups() {
		configs[g.GetName()] = newLawGroup(g.GetName(), GroupMode(g.GetMode()), g.GetPasses(), w.session.Librarian)
	}

	// Return groups in deterministic order.
	names := make([]string, 0, len(groupNames))
	for n := range groupNames {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]*LawGroup, 0, len(names))
	for _, n := range names {
		lg, ok := configs[n]
		if !ok {
			// ponytail: groups absent from ListLawGroups use built-in defaults.
			lg = newLawGroup(n, GroupModeBundle, 1, w.session.Librarian)
		}
		out = append(out, lg)
	}
	return out, nil
}

// VerifyLawAttestations returns the list of stamp names that would need
// to be present on the current artefact for full attestation.
//
// Canonical computation:
//  1. QueryLaws(governedArtefact, "") — get all laws for this artefact kind
//  2. For each law, for each representation type, compute the expected
//     attestation stamp name: "law-<lawID>-<repType>"
//  3. Get the current artefact's stamps via GetArtefact.GetStamps()
//  4. Return the expected stamp names that are NOT present on the artefact
func (w *Workitem) VerifyLawAttestations(governedArtefact string) ([]string, error) {
	// 1. Query laws for this artefact kind.
	lawsResp, err := w.session.Librarian.QueryLaws(context.Background(), &flowv1.QueryLawsRequest{
		Filter: &flowv1.LawFilter{GovernedArtefact: governedArtefact},
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: query laws failed: %w", err)
	}

	// 2. Compute expected stamp names from law representations.
	var expected []string
	for _, law := range lawsResp.GetLaws() {
		for _, rep := range law.GetRepresentations() {
			stampName := "law-" + law.GetId() + "-" + strings.ReplaceAll(rep.GetType(), "/", "-")
			expected = append(expected, stampName)
		}
	}
	if len(expected) == 0 {
		return nil, nil
	}

	// 3. Get current artefact's stamps.
	art, err := w.GetArtefact(governedArtefact)
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get artefact for attestation check: %w", err)
	}
	stamps, err := art.GetStamps()
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get stamps for attestation check: %w", err)
	}
	stampSet := make(map[string]bool, len(stamps))
	for _, s := range stamps {
		stampSet[s.Name] = true
	}

	// 4. Return expected stamps that are missing.
	var missing []string
	for _, name := range expected {
		if !stampSet[name] {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

// Cite records usage of one or more laws via the Librarian Cite RPC.
func (w *Workitem) Cite(lawIDs ...string) error {
	_, err := w.session.Librarian.Cite(context.Background(), &flowv1.CiteRequest{
		LawIds: lawIDs,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: cite failed: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Topology
// ---------------------------------------------------------------------------

// GetTopology returns the flow topology visible to the calling node.
// Returns a *Flow stub (Phase 1) wrapping the proto response.
func (w *Workitem) GetTopology() (*Flow, error) {
	resp, err := w.session.Operator.GetFlowTopology(context.Background(), &flowv1.GetFlowTopologyRequest{})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get flow topology failed: %w", err)
	}
	return newFlow(resp, w.namespace), nil
}

// ---------------------------------------------------------------------------
// Friction
// ---------------------------------------------------------------------------

// QueryFriction returns aggregated friction data from the Friction Ledger.
// Uses proto types directly (no SDK wrapper).
func (w *Workitem) QueryFriction(filter *flowv1.FrictionFilter) ([]*flowv1.FrictionAggregate, error) {
	resp, err := w.session.FrictionLedger.QueryFriction(context.Background(), &flowv1.QueryFrictionRequest{
		Filter: filter,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: query friction failed: %w", err)
	}
	return resp.GetFrictionAggregates(), nil
}
