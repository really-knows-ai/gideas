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

// NewPackageCmd creates the `flowctl package <flow-name>` cobra command.
func NewPackageCmd() *cobra.Command {
	var opts flow.PackageOptions

	cmd := &cobra.Command{
		Use:   "package <flow-name>",
		Short: "Package a running flow into a portable .tgz archive or directory",
		Long: `Package a running flow from the current Kubernetes cluster.

Discovers the FoundryFlow named <flow-name> in namespace <flow-name>,
collects all associated resources (FoundryNodes, GovernedArtefacts,
Laws, LawGroups, Treaties, ConfigMaps), strips cluster-specific metadata,
and creates a portable .tgz archive (or directory with --output-dir)
for distribution or backup.

The output archive or directory can be installed on any cluster with
'flowctl install'.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.FlowName = args[0]

			// --output and --output-dir are mutually exclusive
			if opts.OutputPath != "" && opts.OutputDir != "" {
				return fmt.Errorf("--output and --output-dir are mutually exclusive")
			}

			// Default: --output-dir takes precedence for default, else --output
			if opts.OutputDir != "" {
				// --output-dir is set explicitly, nothing needed
			} else if opts.OutputPath == "" {
				opts.OutputPath = fmt.Sprintf("./%s.tgz", opts.FlowName)
			}

			k8s, err := api.NewK8sClient("")
			if err != nil {
				return fmt.Errorf("failed to connect to Kubernetes: %w", err)
			}
			connectivityCtx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			if err := api.CheckConnectivity(connectivityCtx, k8s); err != nil {
				return err
			}

			result, err := flow.PackageFlow(cmd.Context(), k8s, opts)
			if err != nil {
				return err
			}

			output := result.OutputPath
			if result.OutputDir != "" {
				output = result.OutputDir
			}
			fmt.Fprintf(os.Stderr, "Packaged flow `%s`: %d resources in %d files -> %s\n",
				result.FlowName, result.TotalResources, result.FileCount, output)
			return nil
		},
	}

	cmd.Flags().StringVarP(&opts.OutputPath, "output", "o", "",
		"Output file path (default: ./<flow-name>.tgz)")
	cmd.Flags().StringVar(&opts.OutputDir, "output-dir", "",
		"Output directory (write files directly, mutually exclusive with --output)")
	cmd.Flags().StringVar(&opts.Version, "version", "0.0.0",
		"Package version string for manifest.yaml")
	cmd.Flags().StringVar(&opts.Description, "description", "",
		"Human-readable description for manifest.yaml")
	return cmd
}
