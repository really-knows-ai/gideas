package main

import (
	"crypto/sha256"
	"fmt"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

// computeSHA256 returns the sha256 digest string for the given data in the
// format "sha256:<hex>".
func computeSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", h[:])
}

// verifyPackageDigests verifies both the trailer package digest and per-
// artefact digests declared in the manifest against the staged content.
//
// Trailer digest: computed over the concatenation of all content chunks.
// Per-artefact digest: each artefact in the manifest declares a digest;
// because artefacts are serialised as ordered content chunks in the stream,
// we verify each artefact against its corresponding content chunk.
func verifyPackageDigests(staged *flow.EmbassyStagedPackage) error {
	// Collect content bytes from chunks.
	var allContent []byte
	var contentChunks [][]byte
	for _, chunk := range staged.Chunks {
		if content := chunk.GetContent(); len(content) > 0 {
			allContent = append(allContent, content...)
			contentChunks = append(contentChunks, content)
		}
	}

	// --- Trailer digest verification ---
	if trailer := findTrailer(staged.Chunks); trailer != nil {
		expected := trailer.GetPackageDigest()
		if expected != "" {
			actual := computeSHA256(allContent)
			if actual != expected {
				return fmt.Errorf(
					"embassy digest: package digest mismatch: "+
						"expected %s, got %s",
					expected, actual,
				)
			}
		}
	}

	// --- Per-artefact digest verification ---
	if staged.Manifest != nil {
		artefacts := staged.Manifest.GetArtefacts()
		for i, art := range artefacts {
			expected := art.GetDigest()
			if expected == "" {
				continue
			}
			if i >= len(contentChunks) {
				return fmt.Errorf(
					"embassy digest: artefact %q declared "+
						"digest but no matching content chunk",
					art.GetGovernedArtefact(),
				)
			}
			actual := computeSHA256(contentChunks[i])
			if actual != expected {
				return fmt.Errorf(
					"embassy digest: artefact %q digest "+
						"mismatch: expected %s, got %s",
					art.GetGovernedArtefact(), expected, actual,
				)
			}
		}
	}

	return nil
}

// findTrailer scans chunks for the trailer.
func findTrailer(chunks []*flowv1.PackageChunk) *flowv1.PackageTrailer {
	for _, chunk := range chunks {
		if t := chunk.GetTrailer(); t != nil {
			return t
		}
	}
	return nil
}

// extractManifest scans chunks for the manifest header.
func extractManifest(
	chunks []*flowv1.PackageChunk,
) *flowv1.TransferManifest {
	for _, chunk := range chunks {
		if m := chunk.GetManifest(); m != nil {
			return m
		}
	}
	return nil
}
