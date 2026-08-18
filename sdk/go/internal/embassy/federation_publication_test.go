package embassy

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// --- Slice 12.3.2 tests: publication + events ---

func TestFederationClient_SubmitPublication_Accepted(t *testing.T) {
	spy := &federationSpyServer{}
	client := setupFederationTestClient(t, spy)

	law := &flowv1.Law{
		Id:   "law-1",
		Goal: "Test law",
	}
	err := client.SubmitPublication(law, "flow-publisher")
	if err != nil {
		t.Fatalf("SubmitPublication() returned error: %v", err)
	}
	if spy.lastSubmitPublication.GetSourceFlowIdentity() != "flow-publisher" {
		t.Fatalf("expected source identity flow-publisher, got %q",
			spy.lastSubmitPublication.GetSourceFlowIdentity())
	}
	if spy.lastSubmitPublication.GetLaw().GetId() != "law-1" {
		t.Fatalf("expected law ID law-1, got %q",
			spy.lastSubmitPublication.GetLaw().GetId())
	}
}

func TestFederationClient_SubmitPublication_Rejected(t *testing.T) {
	spy := &federationSpyServer{
		submitPublicationResp: &flowv1.SubmitPublicationResponse{
			Accepted: false,
			Rejection: &flowv1.PublicationRejection{
				Reason:            flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_CONFLICT,
				ConflictingLawIds: []string{"law-existing"},
				RemediationText:   "Conflicts with existing law-existing",
			},
		},
	}
	client := setupFederationTestClient(t, spy)

	err := client.SubmitPublication(&flowv1.Law{Id: "law-2"}, "flow-x")
	if err == nil {
		t.Fatal("expected error from SubmitPublication for rejected publication, got nil")
	}
}

func TestFederationClient_SubmitPublication_NoConnection(t *testing.T) {
	client := &FederationClient{}
	err := client.SubmitPublication(&flowv1.Law{}, "")
	if err == nil {
		t.Fatal("expected error when federation connection is missing")
	}
}
