package main

import (
	"context"
	"fmt"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// defaultManifestExpiry is the default TTL for export manifests.
const defaultManifestExpiry = 1 * time.Hour

// archivistReader is the subset of ArchivistServiceServer used by the
// manifest builder to read artefacts and stamps. Both the spy (in tests)
// and the generated gRPC server interface satisfy this.
type archivistReader interface {
	ListArtefacts(context.Context, *flowv1.ListArtefactsRequest) (*flowv1.ListArtefactsResponse, error)
	GetArtefact(context.Context, *flowv1.GetArtefactRequest) (*flowv1.GetArtefactResponse, error)
	GetStamps(context.Context, *flowv1.GetStampsRequest) (*flowv1.GetStampsResponse, error)
}

// buildExportManifest reads a Workitem's artefacts and stamps via the
// Archivist and constructs a TransferManifest suitable for cross-flow
// export. It returns the manifest, a map of governed-artefact-name to
// content bytes (for streaming), and any error.
func buildExportManifest(
	ctx context.Context,
	archivist archivistReader,
	workitemID string,
	importType string,
	targetFlow string,
	cfg *embassyConfig,
) (*flowv1.TransferManifest, map[string][]byte, error) {
	// List artefacts on the workitem.
	listResp, err := archivist.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId: workitemID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("embassy manifest: list artefacts: %w", err)
	}

	refs := listResp.GetArtefactRefs()
	if len(refs) == 0 {
		return nil, nil, fmt.Errorf("embassy manifest: no artefacts on workitem %s", workitemID)
	}

	sourceFlow := ""
	if cfg != nil {
		sourceFlow = cfg.FederationIdentity
	}

	manifest := &flowv1.TransferManifest{
		ImportType: importType,
		SourceFlow: sourceFlow,
		TargetFlow: targetFlow,
		TransferId: uuid.New().String(),
		ExpiresAt:  timestamppb.New(time.Now().Add(defaultManifestExpiry)),
	}

	contentMap := make(map[string][]byte, len(refs))

	for _, ref := range refs {
		artName := ref.GetGovernedArtefact()

		// Read artefact content.
		getResp, err := archivist.GetArtefact(ctx, &flowv1.GetArtefactRequest{
			WorkitemId: workitemID,
			ArtefactId: artName,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("embassy manifest: get artefact %q: %w", artName, err)
		}

		artContent := getResp.GetContent()
		contentMap[artName] = artContent

		artManifest := &flowv1.ArtefactManifest{
			GovernedArtefact: artName,
			Digest:           computeSHA256(artContent),
			SizeBytes:        int64(len(artContent)),
		}

		// Read stamps for this artefact and include them as ForeignStamps.
		stampsResp, err := archivist.GetStamps(ctx, &flowv1.GetStampsRequest{
			WorkitemId: workitemID,
			ArtefactId: artName,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("embassy manifest: get stamps for %q: %w", artName, err)
		}

		for _, stamp := range stampsResp.GetStamps() {
			artManifest.ForeignStamps = append(artManifest.ForeignStamps, &flowv1.ForeignStamp{
				StampName: stamp.GetName(),
				Issuer:    sourceFlow,
				Subject:   sourceFlow,
				Digest:    stamp.GetContentHash(),
			})
		}

		manifest.Artefacts = append(manifest.Artefacts, artManifest)
	}

	return manifest, contentMap, nil
}
