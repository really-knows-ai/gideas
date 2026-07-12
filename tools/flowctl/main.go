package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/gideas/flow/tools/flowctl/internal/api"
	"github.com/gideas/flow/tools/flowctl/internal/cmd"
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

		// Create log writer early so startup errors are appended to FLOW_LOG_FILE
		startupLog := tui.NewLogWriter(cfg.LogFile)
		defer startupLog.Close()

		k8s, err := api.NewK8sClient("")
		if err != nil {
			return fmt.Errorf("failed to connect to Kubernetes: %w\n"+
				"Verify KUBECONFIG or ~/.kube/config points to a Foundry Flow cluster.", err)
		}
		checkCtx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		if _, err := k8s.CoreClient.Discovery().RESTClient().Get().AbsPath("/version").DoRaw(checkCtx); err != nil {
			select {
			case <-checkCtx.Done():
				startupLog.Log("ERROR", "startup", fmt.Sprintf("failed to reach Kubernetes API server: %v", checkCtx.Err()))
				return fmt.Errorf("failed to reach Kubernetes API server: %w", checkCtx.Err())
			default:
				startupLog.Log("ERROR", "startup", fmt.Sprintf("failed to reach Kubernetes API server: %v", err))
				return fmt.Errorf("failed to reach Kubernetes API server: %w", err)
			}
		}

		// Create PortForwardManager for pod port-forwards
		pfm := api.NewPortForwardManager(k8s.GetRESTConfig(), k8s.CoreClient, nil)

		ctx := cmd.Context()
		model := tui.NewModelWithPFM(k8s, pfm, cfg, ctx)
		program := tea.NewProgram(&model, tea.WithAltScreen())
		model.Program = program

		go func() {
			<-ctx.Done()
			program.Quit()
		}()

		if _, err := program.Run(); err != nil {
			return fmt.Errorf("TUI error: %w", err)
		}
		_ = pfm.CloseAll()
		fmt.Println("\nShutting down...")
		return nil
	},
}

func init() {
	watchCmd.Flags().String("namespace", "", "Workitem namespace (overrides FLOW_NAMESPACE)")
	watchCmd.Flags().String("system-namespace", "", "System services namespace (overrides FLOW_SYSTEM_NAMESPACE)")
	watchCmd.Flags().Int("hitl-port", 8080, "HITL REST port (overrides FLOW_HITL_PORT)")
	rootCmd.AddCommand(watchCmd)
	rootCmd.AddCommand(cmd.NewPackageCmd())
	rootCmd.AddCommand(cmd.NewInstallCmd())
	rootCmd.AddCommand(cmd.NewInitCmd())
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
