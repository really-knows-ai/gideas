package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/gideas/flow/tools/flowctl/internal/api"
	"github.com/gideas/flow/tools/flowctl/internal/flow"
)

// NewInstallCmd creates the `flowctl install` cobra command.
func NewInstallCmd() *cobra.Command {
	var opts flow.InstallOptions

	cmd := &cobra.Command{
		Use:   "install <source> <flow-name>",
		Short: "Install a flow package onto the cluster",
		Long: `Install a flow package onto the current Kubernetes cluster.

<source> can be:
  - A local directory containing manifest.yaml at its root
  - A .tgz file (flow package archive)
  - A git repository URL (https://...)
  - A GitHub owner/repo shorthand (expanded to https://github.com/owner/repo.git)

For git sources, use --ref to specify a branch or lightweight tag (default: HEAD of
the default branch). Requires git to be installed on the PATH.

Creates a namespace named <flow-name>, rewrites all resource metadata
to reference that namespace, and applies resources in dependency order.

Requires that CRDs are already installed on the cluster (run 'flowctl init').`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Source = args[0]
			opts.FlowName = args[1]

			k8s, err := api.NewK8sClient("")
			if err != nil {
				return fmt.Errorf("failed to connect to Kubernetes: %w", err)
			}
			connectivityCtx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			if err := api.CheckConnectivity(connectivityCtx, k8s); err != nil {
				return err
			}

			result, err := flow.InstallFlow(cmd.Context(), k8s, opts, os.Stdout, os.Stderr, os.Stdin)
			if err != nil {
				if result != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				}
				return err
			}

			fmt.Fprintf(os.Stderr, "Install complete: %d created, %d unchanged\n",
				result.Created, result.Unchanged)
			return nil
		},
	}

	cmd.Flags().BoolVar(&opts.Force, "force", false,
		"Delete the target namespace before installing (destroys ALL resources in that namespace)")
	cmd.Flags().BoolVar(&opts.Yes, "yes", false,
		"Skip confirmation prompt for --force")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false,
		"Print rewritten YAML to stdout without applying")
	cmd.Flags().StringVar(&opts.Ref, "ref", "",
		"Git branch or lightweight tag to check out (default: HEAD of default branch)")
	return cmd
}
