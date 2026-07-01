package nodeutil

import (
	"context"
	"encoding/json"
	"log/slog"

	flow "github.com/gideas/flow/sdk/go"
)

// EmitTelemetry records a structured telemetry event via the SDK client.
// The payload is marshalled to JSON internally. Errors are logged but not
// propagated — telemetry failures must not block the caller.
//
// ponytail: Retains ctx because Client.RecordTelemetry has not been updated
// to drop ctx yet. Remove ctx in Phase 10 when the SDK method is updated.
func EmitTelemetry(ctx context.Context, client *flow.Client, eventType string, payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("nodeutil: marshal telemetry payload", "event", eventType, "error", err)
		return
	}
	if err := client.RecordTelemetry(ctx, eventType, data); err != nil {
		slog.Warn("nodeutil: record telemetry", "event", eventType, "error", err)
	}
}
