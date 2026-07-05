package controller

import (
	"testing"
)

// TestArchivistEnvVars_IncludesOperatorAddress is a regression guard: the
// Archivist needs OPERATOR_ADDRESS to validate cross-Workitem reads (see
// ArchivistServer.validateChildAccess). Without it, every parent's attempt
// to read a child Workitem's artefact (e.g. appraisal collecting fan-out
// review-output) fails with "cross-Workitem reads not available: Operator
// client not configured" — a production-only failure mode, since envtest
// suites construct ArchivistServer directly and never exercise this
// generated Deployment env var list.
func TestArchivistEnvVars_IncludesOperatorAddress(t *testing.T) {
	r := &FoundryFlowReconciler{}
	envs := r.archivistEnvVars(nil)

	found := false
	for _, e := range envs {
		if e.Name != "OPERATOR_ADDRESS" {
			continue
		}
		found = true
		want := "flow-operator.operator-system.svc.cluster.local:50052"
		if e.Value != want {
			t.Errorf("OPERATOR_ADDRESS = %q, want %q", e.Value, want)
		}
	}
	if !found {
		t.Fatal("archivistEnvVars() missing OPERATOR_ADDRESS — Archivist cannot validate cross-Workitem reads")
	}
}
