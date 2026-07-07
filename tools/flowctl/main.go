package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/gideas/flow/tools/flowctl/internal/api"
	"github.com/gideas/flow/tools/flowctl/internal/config"
	"github.com/gideas/flow/tools/flowctl/internal/tui"
)

var rootCmd = &cobra.Command{
	Use:   "flowctl",
	Short: "Flowctl — Foundry Flow Workitem browser",
}

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Watch Workitems interactively",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.ParseFlags(cmd)
		if err != nil {
			return err
		}

		k8s, err := api.NewK8sClient("")
		if err != nil {
			return fmt.Errorf("failed to connect to Kubernetes: %w\n"+
				"Verify KUBECONFIG or ~/.kube/config points to a Foundry Flow cluster.", err)
		}

		ctx := cmd.Context()
		model := tui.NewModel(k8s, cfg, ctx)
		program := tea.NewProgram(&model, tea.WithAltScreen())
		model.Program = program

		go func() {
			<-ctx.Done()
			program.Quit()
		}()

		if _, err := program.Run(); err != nil {
			return fmt.Errorf("TUI error: %w", err)
		}
		return nil
	},
}

func init() {
	watchCmd.Flags().String("namespace", "", "Workitem namespace (overrides FLOW_NAMESPACE)")
	watchCmd.Flags().String("system-namespace", "", "System services namespace (overrides FLOW_SYSTEM_NAMESPACE)")
	watchCmd.Flags().Int("hitl-port", 0, "HITL REST port (overrides FLOW_HITL_PORT)")
	rootCmd.AddCommand(watchCmd)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		fmt.Println("\nShutting down...")
		os.Exit(0)
	}()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
