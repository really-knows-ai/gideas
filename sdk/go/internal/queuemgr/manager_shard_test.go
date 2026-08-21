package queuemgr_test

import (
	"context"
	"errors"
	"testing"

	"github.com/foundry/flow/sdk/go/internal/queuemgr"
)

// When the queue-service RPC returns gRPC codes.Unavailable, the thin client's
// Claim must resolve to ErrShardUnavailable via errors.Is.
func TestManager_Claim_ShardUnavailable(t *testing.T) {
	m, fake := newBufconnManager(t, queuemgr.WithQueueName("hitl"))
	fake.claimUnavailable = true

	_, err := m.Claim(context.Background(), "wi-shard")
	if !errors.Is(err, queuemgr.ErrShardUnavailable) {
		t.Fatalf("Claim err = %v, want ErrShardUnavailable", err)
	}
}
