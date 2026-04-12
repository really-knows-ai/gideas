package flowv1_test

import (
	"path/filepath"
	"testing"
)

// TestFederationProtoDoesNotContainRetiredConcepts ensures that the Federation
// proto never re-introduces architectural concepts that were explicitly removed
// during the redesign. See ARCHITECTURE.md "What Gets Removed".
func TestFederationProtoDoesNotContainRetiredConcepts(t *testing.T) {
	t.Parallel()

	content := mustReadRepoFile(t, filepath.Join("..", "..", "..", "proto", "flow", "v1", "federation.proto"))

	retired := []string{
		"ExportWorkitem",
		"ImportWorkitem",
		"CreateHearingWorkitem",
		"GovernanceFlow",
	}
	for _, concept := range retired {
		assertNotContains(t, content, concept)
	}
}
