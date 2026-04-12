package flow

import flowv1 "github.com/gideas/flow/gen/flow/v1"

// IsPetitionAccepted returns true if the petition outcome event indicates
// the authority accepted the petition.
func IsPetitionAccepted(evt *flowv1.PetitionOutcomeEvent) bool {
	if evt == nil {
		return false
	}
	return evt.GetOutcome() == flowv1.PetitionOutcome_PETITION_OUTCOME_ACCEPTED
}

// IsPetitionRejected returns true if the petition outcome event indicates
// the authority rejected the petition.
func IsPetitionRejected(evt *flowv1.PetitionOutcomeEvent) bool {
	if evt == nil {
		return false
	}
	return evt.GetOutcome() == flowv1.PetitionOutcome_PETITION_OUTCOME_REJECTED
}
