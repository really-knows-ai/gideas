package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/gideas/flow/tools/flowctl/internal/flow"
)

// NewInitCmd creates the `flowctl init` cobra command.
func NewInitCmd() *cobra.Command {
	var version string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Bootstrap the Foundry Flow control plane",
		Long: `Install CRDs, RBAC, and the operator Deployment on the current
Kubernetes cluster, then wait for the operator to become ready.

Flags:
  --version   Operator image tag (default: "latest")
  --dry-run   Print manifests to stdout without applying`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return flow.Bootstrap(cmd.Context(), "", version, dryRun, os.Stdout, os.Stderr)
		},
	}

	cmd.Flags().StringVar(&version, "version", "latest", "Operator image tag")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print manifests to stdout without applying")
	return cmd
}
