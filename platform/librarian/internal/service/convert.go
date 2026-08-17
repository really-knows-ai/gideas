package service

import (
	"github.com/foundry/flow/librarian/internal/store/sqlite"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func storeLawToProto(law sqlite.Law) *flowv1.Law {
	reps := make([]*flowv1.Representation, 0, len(law.Representations))
	for _, r := range law.Representations {
		reps = append(reps, &flowv1.Representation{
			Type:    r.Type,
			Content: r.Content,
		})
	}

	return &flowv1.Law{
		Id:              law.ID,
		Goal:            law.Goal,
		Representations: reps,
		Tier:            flowv1.LawTier(law.Tier),
		AppliesTo:       law.AppliesTo,
		Group:           law.Group,
		VersionHash:     law.VersionHash,
		CreatedAt:       timestamppb.New(law.CreatedAt),
		UpdatedAt:       timestamppb.New(law.UpdatedAt),
	}
}

func storeLawGroupToProto(g *sqlite.LawGroup) *flowv1.LawGroup {
	return &flowv1.LawGroup{
		Name:   g.Name,
		Mode:   g.Mode,
		Passes: int32(g.Passes),
	}
}

func storeDisputeToProto(rec *sqlite.DisputeRecord) *flowv1.DisputeRecord {
	s := flowv1.DisputeStatus_DISPUTE_STATUS_ACTIVE
	if rec.Status == sqlite.DisputeStatusRetired {
		s = flowv1.DisputeStatus_DISPUTE_STATUS_RETIRED
	}
	return &flowv1.DisputeRecord{
		PetitionId:  rec.PetitionID,
		CitedLawIds: rec.CitedLawIDs,
		CreatedAt:   timestamppb.New(rec.CreatedAt),
		Status:      s,
	}
}
