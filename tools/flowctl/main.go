package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gideas/flow/tools/flowctl/internal/config"
	"github.com/spf13/cobra"
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
		_ = cfg // Phase 2+ uses this
		fmt.Println("flowctl watch — not yet implemented")
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
		// closeAll() placeholder — populated in later phases
		os.Exit(0)
	}()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
