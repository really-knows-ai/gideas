// Law Applicator is a pure action node in the Foundry judiciary architecture.
//
// It receives an approved petition from an upstream Rule Router and applies
// each change via the Librarian (WriteLaw / RetireLaw / demote). After all
// changes are applied, it stores an "approval-stamp" artefact recording the
// results.
//
// For petitions whose highest-tier change is Tier 1-3 the node calls
// Complete(). For petitions with any Tier 4 or Tier 5 change the node
// instead creates a dispute record via the Librarian and routes the
// workitem to the "embassy" output for cross-flow export.
//
// Input artefacts:
//
//   - "petition" — JSON petition drafted by Clerk (changes[], context)
//
// Output artefacts:
//
//   - "approval-stamp" — JSON recording each applied change and its result
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Well-known constants
// ---------------------------------------------------------------------------

const (
	// Artefact IDs.
	artefactPetition      = "petition"
	artefactApprovalStamp = "approval-stamp"
)

// ---------------------------------------------------------------------------
// Petition (read from artefact, produced by Clerk)
// ---------------------------------------------------------------------------

type petition struct {
	Petition petitionBody `json:"petition"`
}

type petitionBody struct {
	PetitionID         string           `json:"petition_id"`
	Context            petitionContext  `json:"context"`
	Changes            []petitionChange `json:"changes"`
	ProseJustification string           `json:"prose_justification"`
}

type petitionContext struct {
	Trigger        string `json:"trigger"`
	SourceWorkitem string `json:"source_workitem"` //nolint:tagliatelle // JSON convention
	Verdict        string `json:"verdict"`
	Justification  string `json:"justification"`
}

type petitionChange struct {
	Action          string        `json:"action"`
	Tier            int32         `json:"tier,omitempty"`
	Goal            string        `json:"goal,omitempty"`
	AppliesTo       []string      `json:"applies_to,omitempty"`
	LawID           string        `json:"law_id,omitempty"`
	Justification   string        `json:"justification,omitempty"`
	Representations []petitionRep `json:"representations,omitempty"`
}

type petitionRep struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// ---------------------------------------------------------------------------
// Approval Stamp (stored as artefact on application)
// ---------------------------------------------------------------------------

type approvalStamp struct {
	Applied    bool             `json:"applied"`
	LawResults []lawApplyResult `json:"law_results"`
}

type lawApplyResult struct {
	Action      string `json:"action"`
	LawID       string `json:"law_id"`
	VersionHash string `json:"version_hash,omitempty"`
}

// ---------------------------------------------------------------------------
// Entry Point
// ---------------------------------------------------------------------------

func main() {
	slog.Info("law-applicator: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("law-applicator: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("law-applicator: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("law-applicator: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	return handleLawApplicator(ctx, client)
}

// ---------------------------------------------------------------------------
// Core Logic
// ---------------------------------------------------------------------------

func handleLawApplicator(ctx context.Context, client *flow.Client) error {
	_, _ = client.Heartbeat(ctx)

	// -- Step 1: Read petition artefact -----------------------------------
	petResp, err := client.GetArtefact(ctx, artefactPetition)
	if err != nil {
		return fmt.Errorf("law-applicator: get petition: %w", err)
	}

	var pet petition
	if err := json.Unmarshal(petResp.GetContent(), &pet); err != nil {
		return fmt.Errorf("law-applicator: parse petition: %w", err)
	}

	slog.Info("law-applicator: read petition",
		"changes", len(pet.Petition.Changes),
	)

	// -- Step 2: Apply each change via Librarian --------------------------
	stamp, err := applyPetition(ctx, client, &pet)
	if err != nil {
		return err
	}

	// -- Step 3: Store approval stamp artefact ----------------------------
	stampJSON, err := json.Marshal(stamp)
	if err != nil {
		return fmt.Errorf("law-applicator: marshal approval stamp: %w", err)
	}

	if _, err := client.StoreArtefact(ctx, artefactApprovalStamp, "", stampJSON); err != nil {
		return fmt.Errorf("law-applicator: store approval stamp: %w", err)
	}

	// -- Step 4: Tier-dependent completion --------------------------------
	maxTier := maxPetitionTier(pet.Petition.Changes)

	if maxTier >= 4 { //nolint:mnd // Tier 4 is the Embassy threshold.
		// T4-5 path: create dispute record, then route to Embassy.
		citedIDs := collectCitedLawIDs(pet.Petition.Changes, stamp)
		if err := createDisputeRecord(ctx, client, pet.Petition.PetitionID, citedIDs); err != nil {
			return err
		}

		if _, err := client.RouteToOutput(ctx, "embassy"); err != nil {
			return fmt.Errorf("law-applicator: route to embassy: %w", err)
		}

		slog.Info("law-applicator: T4-5 petition routed to embassy",
			"petition_id", pet.Petition.PetitionID,
			"cited_law_ids", len(citedIDs),
		)
	} else {
		// T1-3 path: complete as before.
		if _, err := client.Complete(ctx); err != nil {
			return fmt.Errorf("law-applicator: complete: %w", err)
		}

		slog.Info("law-applicator: petition applied, workitem completed",
			"law_results", len(stamp.LawResults),
		)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Petition Application
// ---------------------------------------------------------------------------

// applyPetition processes each change in the petition and applies it via
// the Librarian. Returns an approval stamp recording the results.
func applyPetition(ctx context.Context, client *flow.Client, pet *petition) (*approvalStamp, error) {
	stamp := &approvalStamp{Applied: true}

	for _, change := range pet.Petition.Changes {
		result, err := applyChange(ctx, client, &change)
		if err != nil {
			return nil, err
		}
		stamp.LawResults = append(stamp.LawResults, *result)
	}

	return stamp, nil
}

// applyChange applies a single petition change via the Librarian.
func applyChange(ctx context.Context, client *flow.Client, change *petitionChange) (*lawApplyResult, error) {
	switch change.Action {
	case "create":
		return applyCreate(ctx, client, change)
	case "retire":
		return applyRetire(ctx, client, change)
	case "demote":
		return applyDemote(ctx, client, change)
	default:
		return nil, fmt.Errorf("law-applicator: unknown petition action %q", change.Action)
	}
}

// applyCreate writes a new law to the Librarian.
func applyCreate(ctx context.Context, client *flow.Client, change *petitionChange) (*lawApplyResult, error) {
	reps := make([]*flowv1.Representation, len(change.Representations))
	for i, r := range change.Representations {
		reps[i] = &flowv1.Representation{
			Type:    r.Type,
			Content: r.Content,
		}
	}

	law := &flowv1.Law{
		Goal:            change.Goal,
		Representations: reps,
		Tier:            flowv1.LawTier(change.Tier),
		AppliesTo:       change.AppliesTo,
	}

	resp, err := client.Librarian.WriteLaw(ctx, &flowv1.WriteLawRequest{Law: law})
	if err != nil {
		return nil, fmt.Errorf("law-applicator: write law: %w", err)
	}

	return &lawApplyResult{
		Action:      "create",
		LawID:       resp.GetLawId(),
		VersionHash: resp.GetVersionHash(),
	}, nil
}

// applyRetire retires an existing law via the Librarian.
func applyRetire(ctx context.Context, client *flow.Client, change *petitionChange) (*lawApplyResult, error) {
	_, err := client.Librarian.RetireLaw(ctx, &flowv1.RetireLawRequest{LawId: change.LawID})
	if err != nil {
		return nil, fmt.Errorf("law-applicator: retire law %q: %w", change.LawID, err)
	}

	return &lawApplyResult{
		Action: "retire",
		LawID:  change.LawID,
	}, nil
}

// applyDemote fetches the existing law, updates its tier, and writes it back.
func applyDemote(ctx context.Context, client *flow.Client, change *petitionChange) (*lawApplyResult, error) {
	existing, err := client.GetLaw(ctx, change.LawID)
	if err != nil {
		return nil, fmt.Errorf("law-applicator: get law %q for demote: %w", change.LawID, err)
	}

	existing.Tier = flowv1.LawTier(change.Tier)

	resp, err := client.Librarian.WriteLaw(ctx, &flowv1.WriteLawRequest{Law: existing})
	if err != nil {
		return nil, fmt.Errorf("law-applicator: demote law %q: %w", change.LawID, err)
	}

	return &lawApplyResult{
		Action:      "demote",
		LawID:       resp.GetLawId(),
		VersionHash: resp.GetVersionHash(),
	}, nil
}

// ---------------------------------------------------------------------------
// Tier Detection & Embassy Routing (T4-5)
// ---------------------------------------------------------------------------

// maxPetitionTier returns the highest tier across all petition changes.
func maxPetitionTier(changes []petitionChange) int32 {
	var max int32
	for _, c := range changes {
		if c.Tier > max {
			max = c.Tier
		}
	}
	return max
}

// collectCitedLawIDs gathers law IDs referenced by a T4-5 petition.
// It includes explicit LawID fields from changes (e.g. retire/demote targets)
// as well as newly created law IDs recorded in the approval stamp.
func collectCitedLawIDs(changes []petitionChange, stamp *approvalStamp) []string {
	seen := make(map[string]struct{})
	var ids []string

	// Explicit law IDs in the petition changes.
	for _, c := range changes {
		if c.LawID != "" {
			if _, ok := seen[c.LawID]; !ok {
				seen[c.LawID] = struct{}{}
				ids = append(ids, c.LawID)
			}
		}
	}

	// Newly created/demoted law IDs from the stamp results.
	for _, r := range stamp.LawResults {
		if r.LawID != "" {
			if _, ok := seen[r.LawID]; !ok {
				seen[r.LawID] = struct{}{}
				ids = append(ids, r.LawID)
			}
		}
	}

	return ids
}

// createDisputeRecord calls Librarian.CreateDisputeRecord. An AlreadyExists
// error is treated as idempotent (logged and ignored); all other errors are
// returned to the caller.
func createDisputeRecord(ctx context.Context, client *flow.Client, petitionID string, citedLawIDs []string) error {
	_, err := client.Librarian.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  petitionID,
		CitedLawIds: citedLawIDs,
	})
	if err != nil {
		if status.Code(err) == codes.AlreadyExists {
			slog.Warn("law-applicator: dispute record already exists (idempotent)",
				"petition_id", petitionID,
			)
			return nil
		}
		return fmt.Errorf("law-applicator: create dispute record: %w", err)
	}
	return nil
}
