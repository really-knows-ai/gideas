package nodeutil

import (
	"context"
	"fmt"
	"log/slog"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flow "github.com/foundry/flow/sdk/go"
)

// SetupHandler replaces the duplicated preamble found in every push-based
// node's handler function. It logs the assignment, creates an SDK client, and
// fetches the workitem.
//
// The caller must close the client when done (typically via defer client.Close()).
//
// The name parameter is a short label used in log and error messages
// (e.g. "forge", "sort"). It should NOT include a trailing colon.
func SetupHandler(
	ctx context.Context,
	wctx *flowv1.WorkitemContext,
	name string,
) (*flow.Client, *flow.Workitem, error) {
	slog.Info(name+": received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)
	client, err := flow.NewClient(flow.WithWorkitemID(wctx.GetWorkitemId()))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: create client: %w", name, err)
	}
	workitem, err := client.GetWorkitem()
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("%s: get workitem: %w", name, err)
	}
	return client, workitem, nil
}
