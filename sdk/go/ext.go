package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ponytail: Raw* accessors are escape hatches for nodes that need direct
// proto access (e.g., arbiter for CompletionReason, law-applicator for
// artefact creation). Remove when those features are available through
// domain objects.

// RawOperator returns the raw OperatorServiceClient for advanced use cases
// where the domain surface does not provide the needed operation.
func (c *Client) RawOperator() flowv1.OperatorServiceClient {
	if c.session == nil {
		return nil
	}
	return c.session.Operator
}

// RawArchivist returns the raw ArchivistServiceClient for advanced use cases.
func (c *Client) RawArchivist() flowv1.ArchivistServiceClient {
	if c.session == nil {
		return nil
	}
	return c.session.Archivist
}

// RawLibrarian returns the raw LibrarianServiceClient for advanced use cases.
func (c *Client) RawLibrarian() flowv1.LibrarianServiceClient {
	if c.session == nil {
		return nil
	}
	return c.session.Librarian
}

// RawFrictionLedger returns the raw FrictionLedgerServiceClient for advanced
// use cases.
func (c *Client) RawFrictionLedger() flowv1.FrictionLedgerServiceClient {
	if c.session == nil {
		return nil
	}
	return c.session.FrictionLedger
}

// ponytail: Resume stays on Client because it operates on arbitrary workitems
// (not the current one). Consider moving to a WorkitemPool type or EntryClient
// if the pattern becomes more common.
func (c *Client) Resume(workitemID string) error {
	_, err := c.session.Operator.ResumeWorkitem(context.Background(), &flowv1.ResumeWorkitemRequest{
		WorkitemId: workitemID,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: resume failed: %w", err)
	}
	return nil
}

// ponytail: PublishAuditEvent stays on Client because it is a cross-cutting
// audit emission that is not scoped to a single workitem.
func (c *Client) PublishAuditEvent(
	eventType string, payload any, workitemID, flowNamespace string,
) error {
	if c.session.EventBus == nil {
		return fmt.Errorf("flow sdk: publish audit event requires Event Bus connection (set EVENT_BUS_ADDRESS)")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("flow sdk: marshal audit payload: %w", err)
	}
	_, err = c.session.EventBus.Publish(context.Background(), &flowv1.PublishRequest{
		Channel: "audit",
		Event: &flowv1.FlowEvent{
			EventId:       fmt.Sprintf("%x", time.Now().UnixNano()),
			EventType:     eventType,
			WorkitemId:    workitemID,
			FlowNamespace: flowNamespace,
			Timestamp:     timestamppb.Now(),
			Payload:       raw,
		},
	})
	if err != nil {
		return fmt.Errorf("flow sdk: publish audit event failed: %w", err)
	}
	return nil
}

// ponytail: RecordTelemetry stays on Client because telemetry emission is
// a cross-cutting concern managed by the Agent heartbeat loop, not scoped
// to a single workitem.
func (c *Client) RecordTelemetry(eventType string, payload []byte) error {
	_, err := c.session.Sidecar.RecordTelemetry(context.Background(), &flowv1.RecordTelemetryRequest{
		EventType: eventType,
		Payload:   payload,
	})
	if err != nil {
		return fmt.Errorf("flow sdk: record telemetry failed: %w", err)
	}
	return nil
}
