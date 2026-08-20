package queue

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// newTestStore returns a fresh in-memory Store for one test case. The
// constructor creates a brand-new SQLite :memory: database per call, so every
// test is fully isolated and order-independent. ROUND 0 stub: this constructor
// may return (nil,nil) today; callers t.Fatal so the test still exercises the
// stub (and fails red as expected).
//
// shardID and queueName are always the same in every test case, so they are
// inlined here rather than parameterized.
func newTestStore(t *testing.T) Store {
	t.Helper()
	st, err := NewInMemoryStore("shard-1", "queue-a")
	if err != nil {
		t.Fatalf("NewInMemoryStore error: %v", err)
	}
	return st
}

// item builds a QueueItem with the given identity, a fixed EnqueuedAt and one
// choice so round-trip assertions are stable and deterministic. The queueName
// is always "queue-a" in every test case, so it is inlined here rather than
// parameterized.
func item(workitemID, shardID, generation string) QueueItem {
	return QueueItem{
		WorkitemID: workitemID,
		ShardID:    shardID,
		QueueName:  "queue-a",
		Status:     QueueStatusWaiting,
		EnqueuedAt: time.Unix(1_700_000_000, 0).UTC(),
		Generation: generation,
		Choices:    []string{"approve", "reject"},
	}
}

// getItem fetches a single item by ID through GetLocalQueue with an unlimited
// filter. It fails the test if the item is absent.
func getItem(t *testing.T, st Store, workitemID string) QueueItem {
	t.Helper()
	items, total, err := st.GetLocalQueue(context.Background(), QueueFilter{})
	if err != nil {
		t.Fatalf("GetLocalQueue() error: %v", err)
	}
	if total < 1 {
		t.Fatalf("GetLocalQueue() total = %d, want >= 1", total)
	}
	for _, it := range items {
		if it.WorkitemID == workitemID {
			return it
		}
	}
	t.Fatalf("item %q not present in GetLocalQueue result", workitemID)
	return QueueItem{}
}

func TestApplyItemInsertsRow(t *testing.T) {
	const (
		shardID    = "shard-1"
		queueName  = "queue-a"
		workitemID = "wi-1"
		generation = "0000000000000001-abc"
	)
	in := item(workitemID, shardID, generation)
	in.Status = QueueStatusWaiting

	st := newTestStore(t)
	defer func() { _ = st.Close() }()

	if err := st.ApplyItem(context.Background(), in); err != nil {
		t.Fatalf("ApplyItem() error: %v", err)
	}

	got := getItem(t, st, workitemID)

	if got.WorkitemID != workitemID {
		t.Errorf("WorkitemID = %q, want %q", got.WorkitemID, workitemID)
	}
	if got.ShardID != shardID {
		t.Errorf("ShardID = %q, want %q", got.ShardID, shardID)
	}
	if got.QueueName != queueName {
		t.Errorf("QueueName = %q, want %q", got.QueueName, queueName)
	}
	if got.Status != QueueStatusWaiting {
		t.Errorf("Status = %q, want %q", got.Status, QueueStatusWaiting)
	}
	if !got.EnqueuedAt.Equal(in.EnqueuedAt) {
		t.Errorf("EnqueuedAt = %v, want %v", got.EnqueuedAt, in.EnqueuedAt)
	}
	if got.Generation != generation {
		t.Errorf("Generation = %q, want %q", got.Generation, generation)
	}
	if !reflect.DeepEqual(got.Choices, in.Choices) {
		t.Errorf("Choices = %v, want %v", got.Choices, in.Choices)
	}
}

func TestApplyItemCarriesStatus(t *testing.T) {
	const (
		shardID    = "shard-1"
		workitemID = "wi-carried"
		generation = "0000000000000001-abc"
	)
	in := item(workitemID, shardID, generation)
	in.Status = QueueStatusClaimed

	st := newTestStore(t)
	defer func() { _ = st.Close() }()

	if err := st.ApplyItem(context.Background(), in); err != nil {
		t.Fatalf("ApplyItem() error: %v", err)
	}

	if got := getItem(t, st, workitemID); got.Status != QueueStatusClaimed {
		t.Errorf("Status = %q, want %q (carried claimed status)", got.Status, QueueStatusClaimed)
	}
}

func TestApplyItemGenerationGuard(t *testing.T) {
	const (
		shardID = "shard-1"
		id      = "wi-gen"
	)
	const (
		genOld = "0000000000000001-abc" // strictly older
		genNew = "0000000000000002-def" // strictly newer
	)

	older := item(id, shardID, genOld)
	newer := item(id, shardID, genNew)
	newer.Status = QueueStatusClaimed // make the downgrade observable

	cases := []struct {
		name       string
		seed       QueueItem // already-Applied item (the stored copy)
		apply      QueueItem // what we ApplyItem next
		wantStatus QueueStatus
		wantGen    string
	}{
		{
			name:       "newer generation overwrites stored copy",
			seed:       older,
			apply:      newer,
			wantStatus: QueueStatusClaimed,
			wantGen:    genNew,
		},
		{
			name:       "older generation is a no-op (does not downgrade)",
			seed:       newer,
			apply:      older,
			wantStatus: QueueStatusClaimed,
			wantGen:    genNew,
		},
		{
			name:       "same generation redelivery is idempotent",
			seed:       newer,
			apply:      newer,
			wantStatus: QueueStatusClaimed,
			wantGen:    genNew,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			defer func() { _ = st.Close() }()

			if err := st.ApplyItem(context.Background(), tc.seed); err != nil {
				t.Fatalf("ApplyItem(seed) error: %v", err)
			}
			if err := st.ApplyItem(context.Background(), tc.apply); err != nil {
				t.Fatalf("ApplyItem(apply) error: %v", err)
			}

			got := getItem(t, st, id)
			if got.Generation != tc.wantGen {
				t.Errorf("Generation = %q, want %q", got.Generation, tc.wantGen)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
		})
	}
}

func TestDropItem(t *testing.T) {
	const (
		shardID = "shard-1"
		id      = "wi-drop"
	)
	const (
		genStored = "0000000000000002-def"
		genOld    = "0000000000000001-abc"
		genSame   = genStored
	)

	cases := []struct {
		name        string
		seed        bool // apply an item for id first
		dropGen     string
		wantErr     error
		wantPresent bool // item still in local queue after DropItem
	}{
		{
			name:        "generation match deletes the row",
			seed:        true,
			dropGen:     genSame,
			wantErr:     nil,
			wantPresent: false,
		},
		{
			name:        "generation mismatch returns ErrQueueItemNotFound",
			seed:        true,
			dropGen:     genOld,
			wantErr:     ErrQueueItemNotFound,
			wantPresent: true,
		},
		{
			name:        "absent item returns ErrQueueItemNotFound",
			seed:        false,
			dropGen:     genSame,
			wantErr:     ErrQueueItemNotFound,
			wantPresent: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			defer func() { _ = st.Close() }()

			if tc.seed {
				if err := st.ApplyItem(context.Background(), item(id, shardID, genStored)); err != nil {
					t.Fatalf("ApplyItem(seed) error: %v", err)
				}
			}

			err := st.DropItem(context.Background(), id, tc.dropGen)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("DropItem() error = %v, want %v", err, tc.wantErr)
			}

			if stillThere := findItem(t, st, id); stillThere != tc.wantPresent {
				t.Errorf("item present after DropItem = %v, want %v", stillThere, tc.wantPresent)
			}
		})
	}
}

func TestClaimCAS(t *testing.T) {
	const (
		shardID    = "shard-1"
		id         = "wi-claim"
		generation = "0000000000000001-abc"
	)

	t.Run("waiting to claimed returns claimed item with ClaimedAt set", func(t *testing.T) {
		st := newTestStore(t)
		defer func() { _ = st.Close() }()
		if err := st.ApplyItem(context.Background(), item(id, shardID, generation)); err != nil {
			t.Fatalf("ApplyItem() error: %v", err)
		}

		got, err := st.Claim(context.Background(), id)
		if err != nil {
			t.Fatalf("Claim() error: %v", err)
		}
		if got == nil {
			t.Fatal("Claim() returned nil item")
		}
		if got.Status != QueueStatusClaimed {
			t.Errorf("Status = %q, want %q", got.Status, QueueStatusClaimed)
		}
		if got.ClaimedAt == nil {
			t.Error("ClaimedAt is nil, want non-nil after claim")
		}
		// The stored copy must also reflect the claim.
		if stored := getItem(t, st, id); stored.Status != QueueStatusClaimed || stored.ClaimedAt == nil {
			t.Errorf("stored item after claim = %+v, want claimed with ClaimedAt", stored)
		}
	})

	t.Run("second claim on claimed item returns ErrQueueItemAlreadyClaimed", func(t *testing.T) {
		st := newTestStore(t)
		defer func() { _ = st.Close() }()
		if err := st.ApplyItem(context.Background(), item(id, shardID, generation)); err != nil {
			t.Fatalf("ApplyItem() error: %v", err)
		}
		if _, err := st.Claim(context.Background(), id); err != nil {
			t.Fatalf("first Claim() error: %v", err)
		}
		if _, err := st.Claim(context.Background(), id); !errors.Is(err, ErrQueueItemAlreadyClaimed) {
			t.Fatalf("second Claim() error = %v, want %v", err, ErrQueueItemAlreadyClaimed)
		}
	})

	t.Run("claim on absent item returns ErrQueueItemNotFound", func(t *testing.T) {
		st := newTestStore(t)
		defer func() { _ = st.Close() }()
		if _, err := st.Claim(context.Background(), "does-not-exist"); !errors.Is(err, ErrQueueItemNotFound) {
			t.Fatalf("Claim() error = %v, want %v", err, ErrQueueItemNotFound)
		}
	})
}

func TestReleaseCAS(t *testing.T) {
	const (
		shardID    = "shard-1"
		id         = "wi-release"
		generation = "0000000000000001-abc"
	)

	t.Run("claimed to waiting resets state", func(t *testing.T) {
		st := newTestStore(t)
		defer func() { _ = st.Close() }()
		if err := st.ApplyItem(context.Background(), item(id, shardID, generation)); err != nil {
			t.Fatalf("ApplyItem() error: %v", err)
		}
		if _, err := st.Claim(context.Background(), id); err != nil {
			t.Fatalf("Claim() error: %v", err)
		}

		got, err := st.Release(context.Background(), id)
		if err != nil {
			t.Fatalf("Release() error: %v", err)
		}
		if got == nil {
			t.Fatal("Release() returned nil item")
		}
		if got.Status != QueueStatusWaiting {
			t.Errorf("Status = %q, want %q", got.Status, QueueStatusWaiting)
		}
		if stored := getItem(t, st, id); stored.Status != QueueStatusWaiting {
			t.Errorf("stored item after release = %+v, want waiting", stored)
		}
	})

	t.Run("release on waiting item returns ErrQueueItemInvalidState", func(t *testing.T) {
		st := newTestStore(t)
		defer func() { _ = st.Close() }()
		if err := st.ApplyItem(context.Background(), item(id, shardID, generation)); err != nil {
			t.Fatalf("ApplyItem() error: %v", err)
		}
		if _, err := st.Release(context.Background(), id); !errors.Is(err, ErrQueueItemInvalidState) {
			t.Fatalf("Release() error = %v, want %v", err, ErrQueueItemInvalidState)
		}
	})

	t.Run("release on absent item returns ErrQueueItemNotFound", func(t *testing.T) {
		st := newTestStore(t)
		defer func() { _ = st.Close() }()
		if _, err := st.Release(context.Background(), "does-not-exist"); !errors.Is(err, ErrQueueItemNotFound) {
			t.Fatalf("Release() error = %v, want %v", err, ErrQueueItemNotFound)
		}
	})
}

func TestDecide(t *testing.T) {
	const (
		shardID    = "shard-1"
		id         = "wi-decide"
		generation = "0000000000000001-abc"
	)

	t.Run("decide on claimed item deletes it", func(t *testing.T) {
		st := newTestStore(t)
		defer func() { _ = st.Close() }()
		if err := st.ApplyItem(context.Background(), item(id, shardID, generation)); err != nil {
			t.Fatalf("ApplyItem() error: %v", err)
		}
		if _, err := st.Claim(context.Background(), id); err != nil {
			t.Fatalf("Claim() error: %v", err)
		}

		if err := st.Decide(context.Background(), id, "approve"); err != nil {
			t.Fatalf("Decide() error: %v", err)
		}

		if findItem(t, st, id) {
			t.Errorf("item %q still present after Decide; want deleted", id)
		}
	})

	t.Run("decide on waiting item returns ErrQueueItemInvalidState", func(t *testing.T) {
		st := newTestStore(t)
		defer func() { _ = st.Close() }()
		if err := st.ApplyItem(context.Background(), item(id, shardID, generation)); err != nil {
			t.Fatalf("ApplyItem() error: %v", err)
		}
		if err := st.Decide(context.Background(), id, "approve"); !errors.Is(err, ErrQueueItemInvalidState) {
			t.Fatalf("Decide() error = %v, want %v", err, ErrQueueItemInvalidState)
		}
	})

	t.Run("decide on absent item returns ErrQueueItemNotFound", func(t *testing.T) {
		st := newTestStore(t)
		defer func() { _ = st.Close() }()
		if err := st.Decide(context.Background(), "does-not-exist", "approve"); !errors.Is(err, ErrQueueItemNotFound) {
			t.Fatalf("Decide() error = %v, want %v", err, ErrQueueItemNotFound)
		}
	})
}

// TestGetLocalQueueServesAllOwners verifies the dumb-mirror read behavior:
// items stored under DIFFERENT owner shard_ids are all returned regardless of
// owner shard_id, and the total count reflects every stored row.
func TestGetLocalQueueServesAllOwners(t *testing.T) {
	owners := []string{"shard-1", "shard-2", "shard-9"}

	// Fresh store, seed one item per distinct owner shard_id.
	st := newTestStore(t)
	defer func() { _ = st.Close() }()

	seeded := make(map[string]QueueItem)
	for i, owner := range owners {
		id := "wi-owner-" + string(rune('a'+i))
		in := item(id, owner, "0000000000000001-"+owner)
		if err := st.ApplyItem(context.Background(), in); err != nil {
			t.Fatalf("ApplyItem(%s) error: %v", id, err)
		}
		seeded[id] = in
	}

	items, total, err := st.GetLocalQueue(context.Background(), QueueFilter{})
	if err != nil {
		t.Fatalf("GetLocalQueue() error: %v", err)
	}

	if total != len(seeded) {
		t.Errorf("GetLocalQueue total = %d, want %d (all owners served)", total, len(seeded))
	}
	// Every seeded item (regardless of owner shard_id) must be present.
	seen := make(map[string]bool)
	for _, it := range items {
		seen[it.WorkitemID] = true
	}
	for id := range seeded {
		if !seen[id] {
			t.Errorf("item %q (owner %q) missing from results; dumb mirror must serve all owners", id, seeded[id].ShardID)
		}
	}
}

// TestGetLocalQueueFilter tests the optional status filter/pagination surface
// of GetLocalQueue with a mixed-state store.
func TestGetLocalQueueFilter(t *testing.T) {
	const (
		shardID = "shard-1"
	)
	st := newTestStore(t)
	defer func() { _ = st.Close() }()

	waitID := "wi-filter-wait"
	claimID := "wi-filter-claim"
	if err := st.ApplyItem(context.Background(), item(waitID, shardID, "0000000000000001-a")); err != nil {
		t.Fatalf("ApplyItem(%s) error: %v", waitID, err)
	}
	if err := st.ApplyItem(context.Background(), item(claimID, shardID, "0000000000000002-b")); err != nil {
		t.Fatalf("ApplyItem(%s) error: %v", claimID, err)
	}
	if _, err := st.Claim(context.Background(), claimID); err != nil {
		t.Fatalf("Claim(%s) error: %v", claimID, err)
	}

	status := QueueStatusClaimed
	items, total, err := st.GetLocalQueue(context.Background(), QueueFilter{Status: &status})
	if err != nil {
		t.Fatalf("GetLocalQueue(claimed) error: %v", err)
	}
	if total != 1 {
		t.Errorf("GetLocalQueue(claimed) total = %d, want 1", total)
	}
	if len(items) != 1 || items[0].WorkitemID != claimID {
		t.Errorf("GetLocalQueue(claimed) items = %v, want only %q", items, claimID)
	}
}

// findItem reports whether a workitem id is present in the current local queue.
func findItem(t *testing.T, st Store, workitemID string) bool {
	t.Helper()
	items, _, err := st.GetLocalQueue(context.Background(), QueueFilter{})
	if err != nil {
		t.Fatalf("GetLocalQueue() error: %v", err)
	}
	for _, it := range items {
		if it.WorkitemID == workitemID {
			return true
		}
	}
	return false
}
