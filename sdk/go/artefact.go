package flow

import (
	"context"
	"fmt"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Stamp type
// ---------------------------------------------------------------------------

// Stamp represents a governance checkpoint applied to an artefact version.
type Stamp struct {
	Name         string
	ApplyingNode string
	ContentHash  string
}

// protoStampToSDK converts a protobuf Stamp to an SDK Stamp.
func protoStampToSDK(s *flowv1.Stamp) *Stamp {
	if s == nil {
		return nil
	}
	return &Stamp{
		Name:         s.GetName(),
		ApplyingNode: s.GetApplyingNode(),
		ContentHash:  s.GetContentHash(),
	}
}

// ---------------------------------------------------------------------------
// Artefact domain object
// ---------------------------------------------------------------------------

// Artefact is a domain object wrapping an artefact in the Archivist.
// It carries a session reference for making gRPC calls and caches the
// artefact's identity and version metadata locally.
type Artefact struct {
	artefactID       string
	governedArtefact string
	content          []byte // populated by GetContent() or Store()
	versionHash      string // populated on construction, updated by GetContent() and Store()
	isNewVersion     bool   // false by default; true after Store creates a new version
	session          *session
}

// ID returns the artefact identifier from construction.
func (a *Artefact) ID() string {
	return a.artefactID
}

// GovernedArtefact returns the governed artefact name from construction.
func (a *Artefact) GovernedArtefact() string {
	return a.governedArtefact
}

// VersionHash returns the current version hash.
func (a *Artefact) VersionHash() string {
	return a.versionHash
}

// IsNewVersion returns true if the artefact was created as a new version.
func (a *Artefact) IsNewVersion() bool {
	return a.isNewVersion
}

// GetContent retrieves the artefact content from the Archivist.
// ponytail: simple first-call cache; add Refresh() or TTL if staleness matters.
func (a *Artefact) GetContent() ([]byte, error) {
	if a.content != nil {
		return a.content, nil
	}
	resp, err := a.session.Archivist.GetArtefact(context.Background(), &flowv1.GetArtefactRequest{
		WorkitemId: a.session.workitemID,
		ArtefactId: a.artefactID,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get artefact content failed: %w", err)
	}
	a.content = resp.GetContent()
	a.governedArtefact = resp.GetGovernedArtefact()
	a.versionHash = resp.GetVersionHash()
	return a.content, nil
}

// Store writes content to the Archivist and updates local state.
func (a *Artefact) Store(content []byte) error {
	resp, err := a.session.Archivist.StoreArtefact(context.Background(), &flowv1.StoreArtefactRequest{
		WorkitemId:       a.session.workitemID,
		ArtefactId:       a.artefactID,
		GovernedArtefact: a.governedArtefact,
		Content:          content,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: store artefact failed: %w", err)
	}
	a.versionHash = resp.GetVersionHash()
	a.isNewVersion = resp.GetIsNewVersion()
	a.content = content
	return nil
}

// Stamp applies a named governance stamp to the current version.
func (a *Artefact) Stamp(name string) error {
	_, err := a.session.Archivist.StampArtefact(context.Background(), &flowv1.StampArtefactRequest{
		WorkitemId: a.session.workitemID,
		ArtefactId: a.artefactID,
		StampName:  name,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: stamp artefact failed: %w", err)
	}
	return nil
}

// GetStamps returns all stamps on the current version. Does NOT cache.
func (a *Artefact) GetStamps() ([]*Stamp, error) {
	resp, err := a.session.Archivist.GetStamps(context.Background(), &flowv1.GetStampsRequest{
		WorkitemId: a.session.workitemID,
		ArtefactId: a.artefactID,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get stamps failed: %w", err)
	}
	protoStamps := resp.GetStamps()
	stamps := make([]*Stamp, 0, len(protoStamps))
	for _, s := range protoStamps {
		stamps = append(stamps, protoStampToSDK(s))
	}
	return stamps, nil
}

// HasStamp checks whether the named stamp exists on the current version.
func (a *Artefact) HasStamp(name string) (bool, error) {
	resp, err := a.session.Archivist.HasStamp(context.Background(), &flowv1.HasStampRequest{
		WorkitemId: a.session.workitemID,
		ArtefactId: a.artefactID,
		StampName:  name,
	})
	if err != nil {
		return false, fmt.Errorf("flow sdk: has stamp failed: %w", err)
	}
	return resp.GetExists(), nil
}

// GetFeedback returns all feedback items for the artefact.
func (a *Artefact) GetFeedback() ([]*Feedback, error) {
	resp, err := a.session.Archivist.GetFeedback(context.Background(), &flowv1.GetFeedbackRequest{
		WorkitemId: a.session.workitemID,
		ArtefactId: a.artefactID,
	})
	if err != nil {
		return nil, fmt.Errorf("flow sdk: get feedback failed: %w", err)
	}
	items := resp.GetFeedbackItems()
	feedback := make([]*Feedback, 0, len(items))
	for _, item := range items {
		feedback = append(feedback, newFeedback(item, a.session))
	}
	return feedback, nil
}

// HasUnresolvedFeedback returns true if any feedback is unresolved.
func (a *Artefact) HasUnresolvedFeedback() (bool, error) {
	resp, err := a.session.Archivist.HasUnresolvedFeedback(context.Background(), &flowv1.HasUnresolvedFeedbackRequest{
		WorkitemId: a.session.workitemID,
		ArtefactId: a.artefactID,
	})
	if err != nil {
		return false, fmt.Errorf("flow sdk: has unresolved feedback failed: %w", err)
	}
	return resp.GetHasUnresolved(), nil
}
