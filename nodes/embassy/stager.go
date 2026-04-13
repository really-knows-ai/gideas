package main

import (
	"context"
	"fmt"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

// embassyStager implements flow.EmbassyPackageStager. It accumulates streamed
// package chunks in memory and returns a staged package on Complete().
type embassyStager struct {
	manifest *flowv1.TransferManifest
	chunks   []*flowv1.PackageChunk
}

// newEmbassyStager creates a new embassyStager.
func newEmbassyStager() *embassyStager {
	return &embassyStager{}
}

// StageManifest stores the transfer manifest header.
func (s *embassyStager) StageManifest(_ context.Context, manifest *flowv1.TransferManifest) error {
	if manifest == nil {
		return fmt.Errorf("embassy stager: nil manifest")
	}
	s.manifest = manifest
	return nil
}

// StageChunk accumulates a package chunk (content or trailer).
func (s *embassyStager) StageChunk(_ context.Context, chunk *flowv1.PackageChunk) error {
	if chunk == nil {
		return fmt.Errorf("embassy stager: nil chunk")
	}
	s.chunks = append(s.chunks, chunk)
	return nil
}

// Complete returns the staged package. It errors if no manifest was staged.
func (s *embassyStager) Complete(_ context.Context) (*flow.EmbassyStagedPackage, error) {
	if s.manifest == nil {
		return nil, fmt.Errorf("embassy stager: no manifest staged — empty chunk stream")
	}
	return &flow.EmbassyStagedPackage{
		Manifest: s.manifest,
		Chunks:   s.chunks,
	}, nil
}

// Compile-time interface check.
var _ flow.EmbassyPackageStager = (*embassyStager)(nil)
