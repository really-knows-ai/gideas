package nodeutil

import (
	"context"
	"fmt"
	"log/slog"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flow "github.com/foundry/flow/sdk/go"
)

// Judiciary wire-contract strings shared by the watcher trio (friction-watcher,
// petition-watcher, ttl-watcher) and the tribunal. Defined once here and
// imported so sibling modules cannot silently diverge.
const (
	// LawIDKey is the workitem-metadata key that carries the target law's ID.
	LawIDKey = "law_id"

	// LawReferenceArtefact is the artefacts ID where the hearing target law's
	// ID is stored for the tribunal to read.
	LawReferenceArtefact = "law-reference"
)

// HandleHearing is the shared hearing-workitem handler used by the judiciary
// watcher nodes (friction-watcher, ttl-watcher). It sets up an SDK client and
// delegates to ProcessHearing. name is the log/error prefix, e.g.
// "friction-watcher".
func HandleHearing(ctx context.Context, wctx *flowv1.WorkitemContext, name string) error {
	client, workitem, err := SetupHandler(ctx, wctx, name+": handler")
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	return ProcessHearing(workitem, wctx, name)
}

// ProcessHearing performs the core hearing handler logic: validate the law_id
// metadata, heartbeat, store a law-reference artefact, and route to the
// default output. name is the log/error prefix, e.g. "friction-watcher".
func ProcessHearing(workitem *flow.Workitem, wctx *flowv1.WorkitemContext, name string) error {
	lawID := wctx.GetMetadata()[LawIDKey]
	if lawID == "" {
		return fmt.Errorf("%s: handler: missing law_id in metadata", name)
	}

	slog.Info(name+": handling hearing",
		"workitem_id", wctx.GetWorkitemId(),
		"law_id", lawID,
	)

	// Send a heartbeat to signal liveness.
	if err := workitem.Heartbeat(); err != nil {
		return fmt.Errorf("%s: handler: heartbeat: %w", name, err)
	}

	// Store law-reference artefact.
	lawRef, err := workitem.GetArtefact(LawReferenceArtefact)
	if err != nil {
		return fmt.Errorf("%s: handler: get law-reference: %w", name, err)
	}
	if err := lawRef.Store([]byte(lawID)); err != nil {
		return fmt.Errorf("%s: handler: store law-reference: %w", name, err)
	}

	slog.Info(name+": stored law-reference artefact", "law_id", lawID)

	// Route to default output (-> tribunal).
	if err := workitem.RouteTo("default"); err != nil {
		return fmt.Errorf("%s: handler: route: %w", name, err)
	}

	slog.Info(name+": routed to default output",
		"workitem_id", wctx.GetWorkitemId())

	return nil
}
