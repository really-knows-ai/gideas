// Null-Node is a minimal verification node for the Foundry Flow runtime.
//
// It uses the push-based model via flow.Start(), acting as a persistent
// gRPC server that receives work assignments from the Sidecar. When the
// Sidecar calls Process, the handler is invoked:
//
//  1. Log the received workitem context.
//  2. Initialize an SDK client to interact with the Sidecar.
//  3. Send a Heartbeat.
//  4. Store an artefact ("greeting") to prove persistence.
//  5. Retrieve the artefact to prove the round-trip.
//  6. Call Complete to submit a routing instruction.
//  7. Return (the server continues to accept new assignments).
//
// Success Criteria: Step 5 logs "Fetched content: Hello from Step 1".
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
		"namespace", wctx.GetFlowNamespace(),
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	// Initialize the SDK client to interact with the Sidecar.
	// Set the workitem ID from the pushed context.
	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		slog.Error("null-node: failed to create SDK client", "error", err)
		return err
	}
	defer func() { _ = client.Close() }()

	workitem, err := client.GetWorkitem()
	if err != nil {
		slog.Error("null-node: failed to get workitem", "error", err)
		return err
	}

	// Send a Heartbeat.
	if err := workitem.Heartbeat(); err != nil {
		slog.Error("null-node: heartbeat failed", "error", err)
		return err
	}
	slog.Info("null-node: heartbeat acknowledged")

	// --- Step 1 (Write): Store an artefact to prove persistence ---
	greeting, err := workitem.GetArtefact("greeting")
	if err != nil {
		slog.Error("null-node: GetArtefact failed", "error", err)
		return err
	}
	if err := greeting.Store([]byte("Hello from Step 1")); err != nil {
		slog.Error("null-node: StoreArtefact failed", "error", err)
		return err
	}
	slog.Info("null-node: artefact stored",
		"version_hash", greeting.VersionHash(),
		"is_new_version", greeting.IsNewVersion(),
	)

	// --- Step 2 (Read): Retrieve the artefact to prove the round-trip ---
	content, err := greeting.GetContent()
	if err != nil {
		slog.Error("null-node: GetArtefact failed", "error", err)
		return err
	}
	slog.Info("null-node: Fetched content: "+string(content),
		"version_hash", greeting.VersionHash(),
		"governed_artefact", greeting.GovernedArtefact(),
	)

	// Complete — submit routing instruction back through Sidecar -> Operator.
	if err := workitem.Complete(); err != nil {
		slog.Error("null-node: completion failed", "error", err)
		return err
	}
	slog.Info("null-node: completion accepted")

	slog.Info("null-node: done processing",
		"workitem_id", wctx.GetWorkitemId(),
	)

	return nil
}
