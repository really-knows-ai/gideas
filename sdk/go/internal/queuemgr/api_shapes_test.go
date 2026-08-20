package queuemgr_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/foundry/flow/sdk/go/internal/queuemgr"
)

// These tests pin the EXACT exported public API surface of the queuemgr thin
// client. They are compile-time + pure-value contracts: no dialing, no I/O.
// A round-2 implementer must make every referenced name exist with exactly
// these shapes or these tests will not compile.

// ---------------------------------------------------------------- constants

func TestQueueStatus_ConstantValues(t *testing.T) {
	if got := string(queuemgr.QueueStatusWaiting); got != "waiting" {
		t.Fatalf("QueueStatusWaiting = %q, want %q", got, "waiting")
	}
	if got := string(queuemgr.QueueStatusClaimed); got != "claimed" {
		t.Fatalf("QueueStatusClaimed = %q, want %q", got, "claimed")
	}
}

// ----------------------------------------------------------------- sentinels

func TestSentinelErrors_AreErrors(t *testing.T) {
	settl := []error{
		queuemgr.ErrQueueItemNotFound,
		queuemgr.ErrQueueItemAlreadyClaimed,
		queuemgr.ErrQueueItemInvalidState,
		queuemgr.ErrShardUnavailable,
	}
	for _, e := range settl {
		if e == nil {
			t.Fatalf("sentinel %v is nil", e)
		}
	}
	// Distinct sentinels: no two compare equal.
	for i := 0; i < len(settl); i++ {
		for j := i + 1; j < len(settl); j++ {
			if errors.Is(settl[i], settl[j]) {
				t.Fatalf("sentinels %v and %v are not distinct", settl[i], settl[j])
			}
		}
	}
}

// -------------------------------------------------------------- QueueItem

func TestQueueItem_JSONTags(t *testing.T) {
	claimedAt := time.Date(2026, 1, 2, 15, 5, 0, 0, time.UTC)
	item := queuemgr.QueueItem{
		WorkitemID: "wi-1",
		ShardID:    "shard-0",
		QueueName:  "hitl",
		Status:     queuemgr.QueueStatusWaiting,
		EnqueuedAt: time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC),
		ClaimedAt:  &claimedAt,
		Generation: "gen-1",
	}
	b, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	for _, key := range []string{"workitem_id", "shard_id", "queue_name", "status", "enqueued_at", "claimed_at", "generation"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("QueueItem JSON missing key %q in %s", key, string(b))
		}
	}
}

// Compile-time pin: the QueueItem struct fields exist with the exact names and
// types. Assigning a composite literal with these fields fails to compile if
// any field is renamed/removed/rettyped.
func TestQueueItem_FieldTypes(t *testing.T) {
	claimedAt := time.Now()
	item := queuemgr.QueueItem{
		WorkitemID: "id",
		ShardID:    "shard",
		QueueName:  "qn",
		Status:     queuemgr.QueueStatusWaiting,
		EnqueuedAt: time.Now(),
		ClaimedAt:  &claimedAt,
		Generation: "gen",
	}
	_ = item
}

// ------------------------------------------------------------------ filter

func TestQueueFilter_FieldTypes(t *testing.T) {
	want := queuemgr.QueueStatusClaimed
	f := queuemgr.QueueFilter{
		Status: &want,
		Limit:  10,
		Offset: 5,
	}
	_ = f
}

// ----------------------------------------------------------------- interface

// compileQueueManagerStub implements the full QueueManager method set with the
// exact preserved signatures. `var _ queuemgr.QueueManager = (*stub)(nil)`
// below fails to compile if any method signature drifts.
type compileQueueManagerStub struct{}

func (compileQueueManagerStub) Enqueue(context.Context, string) error                                   { return nil }
func (compileQueueManagerStub) GetGlobalQueue(context.Context, queuemgr.QueueFilter) ([]queuemgr.QueueItem, error) {
	return nil, nil
}
func (compileQueueManagerStub) GetItem(context.Context, string) (*queuemgr.QueueItem, error)            { return nil, nil }
func (compileQueueManagerStub) Claim(context.Context, string) (*queuemgr.QueueItem, error)              { return nil, nil }
func (compileQueueManagerStub) Release(context.Context, string) (*queuemgr.QueueItem, error)            { return nil, nil }
func (compileQueueManagerStub) Decide(context.Context, string, string) error                            { return nil }
func (compileQueueManagerStub) WaitForDecision(context.Context, string) (string, error)                 { return "", nil }

var _ queuemgr.QueueManager = compileQueueManagerStub{}
var _ queuemgr.QueueManager = (*queuemgr.Manager)(nil)

// -------------------------------------------------------------- constructor

func TestNewManager_OptionSignatures_Compile(t *testing.T) {
	// Pins the exact functional-option shapes: Option takes *Manager via the
	// func config pattern, and the constructor accepts variadic options.
	var _ queuemgr.Option = queuemgr.WithQueueName("qn")
	var _ queuemgr.Option = queuemgr.WithChoices([]string{"a", "b"})
}
