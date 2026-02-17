// Null-Node is a minimal verification node for the Foundry Flow runtime.
//
// It exercises the SDK -> Sidecar link by performing the simplest possible
// node lifecycle:
//
//  1. Connect to the Sidecar via the SDK.
//  2. Send a Heartbeat.
//  3. Wait 1 second (simulating work).
//  4. Call Complete with a success message.
//  5. Exit.
//
// Usage:
//
//	FLOW_WORKITEM_ID=test-123 go run ./nodes/null-node/main.go
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	flow "github.com/gideas/flow/sdk/go"
)

func main() {
	slog.Info("null-node: starting")

	// 1. Initialize the SDK.
	client, err := flow.NewClient()
	if err != nil {
		slog.Error("null-node: failed to create SDK client", "error", err)
		slog.Error("null-node: is the sidecar running?")
		os.Exit(1)
	}
	defer client.Close()

	slog.Info("null-node: SDK connected",
		"workitem_id", client.WorkitemID(),
		"sidecar", flow.DefaultSidecarAddress,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 2. Send a Heartbeat.
	ack, err := client.Heartbeat(ctx)
	if err != nil {
		slog.Error("null-node: heartbeat failed", "error", err)
		os.Exit(1)
	}
	slog.Info("null-node: heartbeat acknowledged", "ack", ack)

	// 3. Simulate work.
	slog.Info("null-node: simulating work for 1 second...")
	time.Sleep(1 * time.Second)

	// 4. Complete.
	accepted, err := client.Complete(ctx, "")
	if err != nil {
		slog.Error("null-node: completion failed", "error", err)
		os.Exit(1)
	}
	slog.Info("null-node: completion accepted", "accepted", accepted)

	// 5. Exit.
	slog.Info("null-node: done")
}
