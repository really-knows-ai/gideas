package flow

import (
	"encoding/json"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

// ---------------------------------------------------------------------------
// Tests — PublishAuditEvent (no ctx)
// ---------------------------------------------------------------------------

func TestPublishAuditEvent_PublishesToAuditChannel(t *testing.T) {
	spy := &spyServer{}
	client := setupGRPCTestEnvWithEventBus(t, "workitem-publishaudit-001",
		func(s *grpc.Server) {
			flowv1.RegisterSidecarServiceServer(s, spy)
			flowv1.RegisterOperatorServiceServer(s, spy)
			flowv1.RegisterArchivistServiceServer(s, spy)
			flowv1.RegisterLibrarianServiceServer(s, spy)
			flowv1.RegisterFrictionLedgerServiceServer(s, spy)
		},
		func(s *grpc.Server) {
			flowv1.RegisterFlowEventBusServiceServer(s, spy)
		},
	)
	env := &testEnv{client: client, spy: spy}

	err := env.client.PublishAuditEvent("appraisal.coverage", map[string]string{
		"stage": "appraisal",
		"cycle": "test-cycle",
	}, "workitem-publishaudit-001", "test-ns")
	if err != nil {
		t.Fatalf("PublishAuditEvent() returned error: %v", err)
	}

	req := env.spy.lastPublishReq
	if req == nil {
		t.Fatal("Publish was not called")
	}
	if req.GetChannel() != "audit" {
		t.Fatalf("expected channel=audit, got %q", req.GetChannel())
	}
	if req.GetEvent().GetEventType() != "appraisal.coverage" {
		t.Fatalf("expected event_type=appraisal.coverage, got %q", req.GetEvent().GetEventType())
	}
	if len(req.GetEvent().GetEventId()) == 0 {
		t.Fatal("expected non-empty event_id")
	}
	if req.GetEvent().GetTimestamp() == nil {
		t.Fatal("expected non-nil timestamp")
	}

	// Verify payload is valid JSON.
	var payload map[string]string
	if err := json.Unmarshal(req.GetEvent().GetPayload(), &payload); err != nil {
		t.Fatalf("expected valid JSON payload, got error: %v", err)
	}
	if payload["stage"] != "appraisal" {
		t.Fatalf("expected payload.stage=appraisal, got %q", payload["stage"])
	}
}

func TestPublishAuditEvent_NoEventBus_ReturnsError(t *testing.T) {
	client := &Client{session: &session{}}
	err := client.PublishAuditEvent("test.event", map[string]string{}, "", "")
	if err == nil {
		t.Fatal("expected error when EventBus is nil, got nil")
	}
	if err.Error() != "flow sdk: publish audit event requires Event Bus connection (set EVENT_BUS_ADDRESS)" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests — RecordTelemetry (no ctx)
// ---------------------------------------------------------------------------

func TestRecordTelemetry_NoCtx(t *testing.T) {
	const wantID = "workitem-telemetry-001"
	env := setupTestEnv(t, wantID)

	err := env.client.RecordTelemetry("foundry.cost.llm", []byte(`{"model":"gpt-4"}`))
	if err != nil {
		t.Fatalf("RecordTelemetry() returned error: %v", err)
	}
}
