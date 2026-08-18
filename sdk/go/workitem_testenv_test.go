package flow

import "testing"

// Shared test constants to avoid goconst warnings.
const (
	testPetitionID    = "petition"
	testArtefactID    = "artefact-001"
	testArtefactID2   = "artefact-002"
	testNextOutput    = "next-output"
	testLaw1          = "law-1"
	testLaw2          = "law-2"
	testLawFriction   = "law-friction-001"
	testFB001         = "fb-001"
	testFB002         = "fb-002"
	testFBAuto001     = "fb-auto-001"
	testTextMarkdown  = "text/markdown"
	testChildAll      = `children.all(c, c.phase == "Completed")`
	testNeedsRevision = "needs revision"
	testLooksGood     = "looks good"
	testTestContent   = "test-content"
	testTestArtefact  = "test-artefact"
)

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

// setupWorkitemTestEnv creates a testEnv with a spy server and returns a
// *Workitem wired to the session. The workitem's ID is set to workitemID.
func setupWorkitemTestEnv(t *testing.T, workitemID string) (*Workitem, *testEnv) {
	t.Helper()
	env := setupTestEnv(t, workitemID)
	wi, err := env.client.GetWorkitem()
	if err != nil {
		t.Fatalf("GetWorkitem() failed: %v", err)
	}
	return wi, env
}
