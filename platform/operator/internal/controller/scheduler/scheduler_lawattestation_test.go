package scheduler

import (
	"context"
	"errors"
	"testing"

	flowv1 "github.com/foundry/flow/operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---------------------------------------------------------------------------
// Law attestation enforcement tests
// ---------------------------------------------------------------------------

func TestComplete_LawAttestation_MissingStamps(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "exit-node", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest", Exit: "standard-exit"},
	}
	sched := newTestScheduler(node)
	sched.LawQuerier = &mockLawQuerier{
		laws: map[string][]LawInfo{
			"haiku": {
				{ID: "no-weather", Group: "default", Representations: []string{"text/markdown"}},
			},
		},
		groups: []LawGroupInfo{{Name: "default", Mode: "law-by-law"}},
	}
	sched.Querier = func(_ context.Context, _ string, _ []string) ([]ArtefactState, error) {
		return []ArtefactState{
			{ArtefactID: "art-1", GovernedArtefact: "haiku", StampNames: []string{"appraisal"}},
		}, nil
	}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{"standard-exit": {"haiku": {"appraisal"}}})

	_, err := sched.CalculateNextStep(context.Background(), "exit-node", flowv1.RoutingInstruction{Type: "complete"}, wi, flow)
	if err == nil {
		t.Fatal("expected CONTRACT_VIOLATION for missing attestation stamp, got nil")
	}
	assertGuardCode(t, err, "CONTRACT_VIOLATION")
}

func TestComplete_LawAttestation_AllStampsPresent(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "exit-node", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest", Exit: "standard-exit"},
	}
	sched := newTestScheduler(node)
	sched.LawQuerier = &mockLawQuerier{
		laws: map[string][]LawInfo{
			"haiku": {
				{ID: "no-weather", Group: "default", Representations: []string{"text/markdown"}},
			},
		},
		groups: []LawGroupInfo{{Name: "default", Mode: "law-by-law"}},
	}
	sched.Querier = func(_ context.Context, _ string, _ []string) ([]ArtefactState, error) {
		return []ArtefactState{
			{
				ArtefactID:       "art-1",
				GovernedArtefact: "haiku",
				StampNames:       []string{"appraisal", "law-no-weather-text-markdown", "lawgrp-default"},
			},
		}, nil
	}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{"standard-exit": {"haiku": {"appraisal"}}})

	result, err := sched.CalculateNextStep(context.Background(), "exit-node", flowv1.RoutingInstruction{Type: "complete"}, wi, flow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != phaseCompleted {
		t.Errorf("expected Phase=Completed, got %q", result.Phase)
	}
}

func TestComplete_LawAttestation_LibrarianUnavailable(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "exit-node", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest", Exit: "standard-exit"},
	}
	sched := newTestScheduler(node)
	sched.LawQuerier = &mockLawQuerier{err: errors.New("librarian unreachable")}
	sched.Querier = func(_ context.Context, _ string, _ []string) ([]ArtefactState, error) {
		return []ArtefactState{
			{ArtefactID: "art-1", GovernedArtefact: "haiku", StampNames: []string{"appraisal"}},
		}, nil
	}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{"standard-exit": {"haiku": {"appraisal"}}})

	_, err := sched.CalculateNextStep(context.Background(), "exit-node", flowv1.RoutingInstruction{Type: "complete"}, wi, flow)
	if err == nil {
		t.Fatal("expected error for Librarian unavailability, got nil")
	}
}

func TestComplete_LawAttestation_BundleMode(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "exit-node", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest", Exit: "standard-exit"},
	}
	sched := newTestScheduler(node)
	sched.LawQuerier = &mockLawQuerier{
		laws: map[string][]LawInfo{
			"haiku": {
				{ID: "no-weather", Group: "default", Representations: []string{"text/markdown"}},
			},
		},
		groups: []LawGroupInfo{{Name: "default", Mode: "bundle"}},
	}
	sched.Querier = func(_ context.Context, _ string, _ []string) ([]ArtefactState, error) {
		return []ArtefactState{
			{
				ArtefactID:       "art-1",
				GovernedArtefact: "haiku",
				StampNames:       []string{"appraisal", "lawgrp-default"},
			},
		}, nil
	}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{"standard-exit": {"haiku": {"appraisal"}}})

	result, err := sched.CalculateNextStep(context.Background(), "exit-node", flowv1.RoutingInstruction{Type: "complete"}, wi, flow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != phaseCompleted {
		t.Errorf("expected Phase=Completed, got %q", result.Phase)
	}
}

func TestComplete_LawAttestation_MultiRepLawByLaw(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "exit-node", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest", Exit: "standard-exit"},
	}
	sched := newTestScheduler(node)
	sched.LawQuerier = &mockLawQuerier{
		laws: map[string][]LawInfo{
			"haiku": {{
				ID:              "syllable",
				Group:           "content",
				Representations: []string{"text/markdown", "application/rego"},
			}},
		},
		groups: []LawGroupInfo{{Name: "content", Mode: "law-by-law"}},
	}
	sched.Querier = func(_ context.Context, _ string, _ []string) ([]ArtefactState, error) {
		return []ArtefactState{
			{
				ArtefactID:       "art-1",
				GovernedArtefact: "haiku",
				StampNames: []string{
					"appraisal",
					"law-syllable-text-markdown",
					"law-syllable-application-rego",
					"lawgrp-content",
				},
			},
		}, nil
	}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{"standard-exit": {"haiku": {"appraisal"}}})

	result, err := sched.CalculateNextStep(context.Background(), "exit-node", flowv1.RoutingInstruction{Type: "complete"}, wi, flow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != phaseCompleted {
		t.Errorf("expected Phase=Completed, got %q", result.Phase)
	}
}

func TestComplete_LawAttestation_NoApplicableLaws(t *testing.T) {
	node := flowv1.FoundryNode{
		ObjectMeta: metav1.ObjectMeta{Name: "exit-node", Namespace: "default"},
		Spec:       flowv1.FoundryNodeSpec{Image: "alpine:latest", Exit: "standard-exit"},
	}
	sched := newTestScheduler(node)
	sched.LawQuerier = &mockLawQuerier{
		laws:   map[string][]LawInfo{}, // no laws for any artefact
		groups: []LawGroupInfo{},
	}
	sched.Querier = func(_ context.Context, _ string, _ []string) ([]ArtefactState, error) {
		return []ArtefactState{
			{
				ArtefactID:       "art-1",
				GovernedArtefact: "haiku",
				StampNames:       []string{"appraisal"},
			},
		}, nil
	}
	wi := newTestWorkitem(nil)
	flow := newTestFlow(100, map[string]flowv1.Contract{"standard-exit": {"haiku": {"appraisal"}}})

	result, err := sched.CalculateNextStep(context.Background(), "exit-node", flowv1.RoutingInstruction{Type: "complete"}, wi, flow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != phaseCompleted {
		t.Errorf("expected Phase=Completed, got %q", result.Phase)
	}
}
