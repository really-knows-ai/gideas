package controller

import (
	"testing"

	flowv1 "github.com/gideas/flow/operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEffectiveImportTypesIncludesBuiltInAndFlowDefined(t *testing.T) {
	t.Parallel()

	flow := &flowv1.FoundryFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "flow", Namespace: "default"},
		Spec: flowv1.FoundryFlowSpec{
			CrossFlow: &flowv1.CrossFlowConfig{
				ImportTypes: map[string]flowv1.ImportTypeSpec{
					"external-submission": {Node: "intake"},
				},
			},
		},
	}

	effective := effectiveImportTypes(flow)
	if len(effective) != 2 {
		t.Fatalf("expected 2 effective import types, got %d", len(effective))
	}

	lawPetition, ok := effective[builtInLawPetitionImportType]
	if !ok {
		t.Fatal("expected built-in law-petition import type to be present")
	}
	if !lawPetition.BuiltIn {
		t.Fatal("expected law-petition import type to be built-in")
	}
	if lawPetition.Spec != nil {
		t.Fatal("expected built-in law-petition import type to have no flow-authored spec")
	}

	externalSubmission, ok := effective["external-submission"]
	if !ok {
		t.Fatal("expected flow-defined external-submission import type to be present")
	}
	if externalSubmission.BuiltIn {
		t.Fatal("expected external-submission import type not to be built-in")
	}
	if externalSubmission.Spec == nil || externalSubmission.Spec.Node != "intake" {
		t.Fatalf("expected external-submission node intake, got %#v", externalSubmission.Spec)
	}
}

func TestEffectiveImportTypesIncludesBuiltInWhenCrossFlowMissing(t *testing.T) {
	t.Parallel()

	flow := &flowv1.FoundryFlow{ObjectMeta: metav1.ObjectMeta{Name: "flow", Namespace: "default"}}

	effective := effectiveImportTypes(flow)
	if len(effective) != 1 {
		t.Fatalf("expected only built-in import types, got %d", len(effective))
	}
	if _, ok := effective[builtInLawPetitionImportType]; !ok {
		t.Fatal("expected built-in law-petition import type to be present")
	}
}
