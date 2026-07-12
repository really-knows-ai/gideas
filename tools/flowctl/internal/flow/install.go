package flow

import (
	"context"
	"errors"
	"io"

	"github.com/gideas/flow/tools/flowctl/internal/api"
)

// InstallOptions carries all CLI flags for `flowctl install`.
// Phase 3 will populate this with Force, Yes, DryRun etc.
type InstallOptions struct {
	Source   string // positional: <source> — directory, .tgz, git URL, or owner/repo
	FlowName string // positional: <flow-name>
}

// InstallResult describes the outcome of InstallFlow. Phase 3 will populate.
type InstallResult struct {
	FlowName  string
	Created   int
	Unchanged int
	Failed    int
}

// InstallFlow is a stub for Phase 3. It validates arguments and returns
// "not yet implemented" until Phase 3 fills in the implementation.
func InstallFlow(ctx context.Context, k8s *api.K8sClient, opts InstallOptions, stdout io.Writer, stderr io.Writer, stdin io.Reader) (*InstallResult, error) {
	return nil, errors.New("not yet implemented: flowctl install is part of a future phase")
}
