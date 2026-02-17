// Null-Node is a minimal verification node for the Foundry Flow runtime.
//
// It uses the push-based model via flow.Start(), acting as a persistent
// gRPC server that receives work assignments from the Sidecar. When the
// Sidecar calls Process, the handler is invoked:
//
//  1. Log the received workitem context.
//  2. Initialize an SDK client to interact with the Sidecar.
//  3. Send a Heartbeat.
//  4. Simulate work (1 second).
//  5. Call Complete to submit a routing instruction.
//  6. Return (the server continues to accept new assignments).
//
// Usage:
//
//	go run ./nodes/null-node/main.go
//
// The node listens on :50053 (default) for Process calls from the Sidecar.
// Override with FLOW_NODE_PORT=<port>.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

func main() {
	slog.Info("null-node: starting push-based server")

	if err := flow.Start(handler); err != nil {
		slog.Error("null-node: server failed", "error", err)
		os.Exit(1)
	}
}

// handler is the user-provided work processing function.
// It is called by the SDK server when the Sidecar forwards an AssignWork request.
func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("null-node: Processing...",
		"flow_id", wctx.GetFlowId(),
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	// Initialize the SDK client to interact with the Sidecar.
	// Set the workitem ID from the pushed context.
	os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		slog.Error("null-node: failed to create SDK client", "error", err)
		return err
	}
	defer client.Close()

	// Send a Heartbeat.
	ack, err := client.Heartbeat(ctx)
	if err != nil {
		slog.Error("null-node: heartbeat failed", "error", err)
		return err
	}
	slog.Info("null-node: heartbeat acknowledged", "ack", ack)

	// Simulate work.
	slog.Info("null-node: simulating work for 1 second...")
	time.Sleep(1 * time.Second)

	// Complete — submit routing instruction back through Sidecar -> Operator.
	accepted, err := client.Complete(ctx, "")
	if err != nil {
		slog.Error("null-node: completion failed", "error", err)
		return err
	}
	slog.Info("null-node: completion accepted", "accepted", accepted)

	slog.Info("null-node: done processing",
		"workitem_id", wctx.GetWorkitemId(),
	)

	return nil
}
