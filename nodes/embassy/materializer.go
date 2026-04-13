package main

import (
	"context"
	"fmt"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

// embassyMaterializer implements flow.EmbassyMaterializer.
// It creates a local Workitem, unpacks manifest artefacts via the Archivist,
// applies naturalisation stamps, and returns the created workitem ID.
type embassyMaterializer struct {
	operator       flowv1.OperatorServiceClient
	archivist      flowv1.ArchivistServiceClient
	naturalisation *naturalisationConfig
}

// MaterializeImport creates a local Workitem with imported metadata,
// stores each manifest artefact via the Archivist, and applies
// naturalisation stamps according to config.
func (m *embassyMaterializer) MaterializeImport(
	ctx context.Context,
	importType flow.EmbassyResolvedImportType,
	staged *flow.EmbassyStagedPackage,
) (*flowv1.StreamPackageResponse, error) {
	manifest := staged.Manifest
	if manifest == nil {
		return nil, fmt.Errorf("embassy materializer: no manifest in staged package")
	}

	// --- Create Workitem with imported metadata ---
	metadata := map[string]string{
		"import_type": manifest.GetImportType(),
		"source_flow": manifest.GetSourceFlow(),
		"transfer_id": manifest.GetTransferId(),
	}

	resp, err := m.operator.CreateWorkitem(ctx, &flowv1.CreateWorkitemRequest{
		Metadata: metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("embassy materializer: create workitem: %w", err)
	}
	workitemID := resp.GetWorkitemId()

	// --- Unpack artefacts ---
	// Content chunks are ordered to match manifest artefacts.
	var contentChunks [][]byte
	for _, chunk := range staged.Chunks {
		if content := chunk.GetContent(); len(content) > 0 {
			contentChunks = append(contentChunks, content)
		}
	}

	for i, art := range manifest.GetArtefacts() {
		if i >= len(contentChunks) {
			return nil, fmt.Errorf(
				"embassy materializer: artefact %q has no matching content chunk",
				art.GetGovernedArtefact(),
			)
		}

		_, err := m.archivist.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
			WorkitemId:       workitemID,
			ArtefactId:       art.GetGovernedArtefact(),
			GovernedArtefact: art.GetGovernedArtefact(),
			Content:          contentChunks[i],
		})
		if err != nil {
			return nil, fmt.Errorf(
				"embassy materializer: store artefact %q: %w",
				art.GetGovernedArtefact(), err,
			)
		}
	}

	// --- Naturalisation stamps ---
	if err := m.applyNaturalisationStamps(ctx, workitemID, importType, manifest); err != nil {
		return nil, fmt.Errorf("embassy materializer: naturalisation: %w", err)
	}

	// --- Route to intake node ---
	intakeNode := resolveIntakeNode(importType)
	if intakeNode == "" {
		return nil, fmt.Errorf("embassy materializer: cannot resolve intake node for import type %q", importType.Name)
	}

	_, err = m.operator.SubmitResult(ctx, &flowv1.SubmitResultRequest{
		WorkitemId: workitemID,
		Action: &flowv1.SubmitResultRequest_Route{
			Route: &flowv1.RouteAction{
				Target: intakeNode,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("embassy materializer: route to %q: %w", intakeNode, err)
	}

	return &flowv1.StreamPackageResponse{
		WorkitemId: workitemID,
	}, nil
}

// petitionIntakeNode is the platform-owned intake target for built-in
// law-petition imports. The workitem is routed to this node after
// materialisation.
const petitionIntakeNode = "petition-intake"

// resolveIntakeNode determines the target node for a materialised import
// workitem based on the resolved import type.
//
//   - Built-in system import types (e.g. law-petition) route to their
//     platform-owned intake path.
//   - Flow-authored import types route to the node specified in the import
//     type spec.
func resolveIntakeNode(importType flow.EmbassyResolvedImportType) string {
	if importType.BuiltIn {
		return petitionIntakeNode
	}
	if importType.Spec != nil && importType.Spec.Node != "" {
		return importType.Spec.Node
	}
	return ""
}

// applyNaturalisationStamps applies local attestation stamps for verified
// foreign stamps and any configured requireLocalStamps.
func (m *embassyMaterializer) applyNaturalisationStamps(
	ctx context.Context,
	workitemID string,
	importType flow.EmbassyResolvedImportType,
	manifest *flowv1.TransferManifest,
) error {
	if !m.isAutoNaturalise() {
		return nil
	}

	// Apply "imported-<stamp>" for each verified required foreign stamp.
	if importType.Spec != nil {
		for _, art := range manifest.GetArtefacts() {
			artName := art.GetGovernedArtefact()
			requiredStamps, ok := importType.Spec.RequireForeignStamps[artName]
			if !ok {
				continue
			}

			// Build set of foreign stamps present on this artefact.
			present := make(map[string]bool, len(art.GetForeignStamps()))
			for _, fs := range art.GetForeignStamps() {
				present[fs.GetStampName()] = true
			}

			for _, stampName := range requiredStamps {
				if !present[stampName] {
					continue // Should not happen post-preflight, but be safe.
				}
				if err := m.stampArtefact(ctx, workitemID, artName, "imported-"+stampName); err != nil {
					return err
				}
			}
		}
	}

	// Apply requireLocalStamps from naturalisation config.
	if m.naturalisation != nil {
		for _, stampName := range m.naturalisation.RequireLocalStamps {
			// Apply to every artefact in the manifest.
			for _, art := range manifest.GetArtefacts() {
				if err := m.stampArtefact(ctx, workitemID, art.GetGovernedArtefact(), stampName); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// isAutoNaturalise returns true when auto-naturalisation is enabled.
// Default (nil config or nil field) is true — naturalisation stamps are
// applied unless explicitly disabled.
func (m *embassyMaterializer) isAutoNaturalise() bool {
	if m.naturalisation == nil {
		return true
	}
	if m.naturalisation.AutoNaturalise == nil {
		return true
	}
	return *m.naturalisation.AutoNaturalise
}

// stampArtefact applies a single stamp to an artefact.
func (m *embassyMaterializer) stampArtefact(
	ctx context.Context, workitemID, artefactID, stampName string,
) error {
	_, err := m.archivist.StampArtefact(ctx, &flowv1.StampArtefactRequest{
		WorkitemId: workitemID,
		ArtefactId: artefactID,
		StampName:  stampName,
	})
	if err != nil {
		return fmt.Errorf("stamp %q on artefact %q: %w", stampName, artefactID, err)
	}
	return nil
}

// Compile-time interface check.
var _ flow.EmbassyMaterializer = (*embassyMaterializer)(nil)
