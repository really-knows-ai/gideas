package cmd

import (
	"github.com/spf13/cobra"
)

// NewGraphCmd creates the parent `flowctl graph` command with no RunE
// (subcommand-only parent).
func NewGraphCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Graph operations for the Foundry knowledge graph",
		Long: `Graph subcommands interact with the Cartographer knowledge graph
service. Use "flowctl graph export" to export the graph for external
visualisation tools.`,
	}

	cmd.AddCommand(newGraphExportCmd())
	return cmd
}
