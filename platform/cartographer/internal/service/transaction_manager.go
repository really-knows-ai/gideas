package service

import (
	"fmt"
	"sync"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
)

// Clock abstracts time for testability.
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
}

// Ticker abstracts time.Ticker for testability.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// RealClock implements Clock using the standard time package.
type RealClock struct{}

func (RealClock) Now() time.Time                   { return time.Now() }
func (RealClock) NewTicker(d time.Duration) Ticker { return &RealTicker{t: time.NewTicker(d)} }

// RealTicker wraps time.Ticker to implement Ticker.
type RealTicker struct{ t *time.Ticker }

func (r *RealTicker) C() <-chan time.Time { return r.t.C }
func (r *RealTicker) Stop()               { r.t.Stop() }

// TransactionManager manages the lifecycle of active transactions.
type TransactionManager struct {
	mu                  sync.RWMutex
	active              map[string]*TransactionState
	hardMaxTimeout      time.Duration // 7 days
	changeLogCap        int           // 100000
	clock               Clock
	beforeLifecycleLock func(string) // test barrier; nil in production
}

// TransactionState holds the runtime state for a single transaction.
type TransactionState struct {
	lifecycle          sync.Mutex
	ID                 string
	CreatedAt          time.Time
	ExpiresAt          time.Time
	ChangeLog          *gitstore.ChangeLog
	StoreBranch        string
	GitBranch          string
	AppliedTimeout     time.Duration
	MainHeadAtLastSync string
	SchemaHash         string // hash of schema at begin time
	MainRehydrated     bool   // main contains branch data from a commit that has not merged
	CommitStarted      bool   // a Git commit may have been created; mutations and refresh are closed
	CommitCreated      bool   // transaction Git commit exists; mutations and refresh are closed
	CommitHydrated     bool   // main rehydration completed successfully for this commit
	MergeCompleted     bool   // transaction commit has reached main; only cleanup remains
	RollbackOnly       bool   // admission failed; only rollback/GC cleanup may proceed
	// BranchRefreshInProgress marks that a RefreshTransaction branch-DB swap is
	// in flight (mirrors store.BranchTransactionState.BranchRefreshInProgress;
	// see durableTransactionState). Set before the swap, cleared by the
	// refresh's final state persist.
	BranchRefreshInProgress bool
}

// LockActive admits an operation on txID and serialises it with lifecycle
// operations for that transaction. The returned unlock function must be called
// after all branch-store and change-log work for the operation is complete.
// Lock order is lifecycle -> change log -> git/store; tm.mu is released before
// waiting for lifecycle and is held only briefly to lookup or revalidate state.
func (tm *TransactionManager) LockActive(txID string) (*TransactionState, func(), error) {
	return tm.lock(txID, false)
}

// LockCleanup serialises rollback cleanup for a registered transaction,
// including rollback-only or expired transactions.
func (tm *TransactionManager) LockCleanup(txID string) (*TransactionState, func(), error) {
	return tm.lock(txID, true)
}

func (tm *TransactionManager) lock(txID string, cleanup bool) (*TransactionState, func(), error) {
	if txID == "" || !isValidUUID(txID) {
		return nil, nil, errInvalidTransactionIDFormat(txID)
	}

	tm.mu.RLock()
	state, ok := tm.active[txID]
	tm.mu.RUnlock()
	if !ok {
		return nil, nil, errTransactionNotFound(txID)
	}

	if tm.beforeLifecycleLock != nil {
		tm.beforeLifecycleLock(txID)
	}
	state.lifecycle.Lock()
	tm.mu.RLock()
	current, stillActive := tm.active[txID]
	tm.mu.RUnlock()
	if !stillActive || current != state {
		state.lifecycle.Unlock()
		return nil, nil, errTransactionNotFound(txID)
	}
	// A rollback-only transaction has been rolled back by the change-log
	// capacity rejection (SPEC error table row "Transaction change log exceeds
	// capacity" — the transaction is "rolled back"), so subsequent operations
	// surface NOT_FOUND ("Transaction not found": "was already committed/rolled
	// back") rather than a FAILED_PRECONDITION rollback-only state the SPEC
	// error table does not define. Only RollbackTransaction (LockCleanup) and
	// the GC may still act on it.
	if !cleanup && state.RollbackOnly {
		state.lifecycle.Unlock()
		return nil, nil, errTransactionNotFound(txID)
	}
	if !cleanup && tm.clock.Now().After(state.ExpiresAt) {
		state.lifecycle.Unlock()
		return nil, nil, errTransactionTimedOut(txID)
	}
	return state, state.lifecycle.Unlock, nil
}

// lockRegistered serialises GC with an operation already admitted on txID.
// Unlike LockActive, it permits expired transactions.
func (tm *TransactionManager) lockRegistered(txID string) (*TransactionState, func(), bool) {
	tm.mu.RLock()
	state, ok := tm.active[txID]
	tm.mu.RUnlock()
	if !ok {
		return nil, nil, false
	}
	if tm.beforeLifecycleLock != nil {
		tm.beforeLifecycleLock(txID)
	}
	state.lifecycle.Lock()
	tm.mu.RLock()
	current, stillActive := tm.active[txID]
	tm.mu.RUnlock()
	if !stillActive || current != state {
		state.lifecycle.Unlock()
		return nil, nil, false
	}
	return state, state.lifecycle.Unlock, true
}

// NewTransactionManager creates a manager with the real clock. The clock
// field remains assignable by same-package tests that need deterministic time.
func NewTransactionManager(hardMaxTimeout time.Duration, changeLogCap int) *TransactionManager {
	return &TransactionManager{
		active:         make(map[string]*TransactionState),
		hardMaxTimeout: hardMaxTimeout,
		changeLogCap:   changeLogCap,
		clock:          &RealClock{},
	}
}

// Create registers a new transaction with the given ID and requested timeout.
// The requested timeout is applied verbatim: a non-positive value or one that
// exceeds the 7-day hard maximum is rejected with INVALID_ARGUMENT (SPEC error
// table row "Invalid transaction timeout duration", applying to both
// BeginTransaction and ExtendTimeout) — no silent capping and no silent
// default-substitution. The omitted-timeout default is applied by the
// BeginTransaction handler before Create is called, so a non-positive value
// reaching Create is an explicit client request.
func (tm *TransactionManager) Create(
	txID string, requestedTimeout time.Duration, mainHeadAtLastSync string,
) (*TransactionState, error) {
	if requestedTimeout <= 0 {
		return nil, errInvalidTransactionTimeoutDuration("duration must be positive")
	}
	if requestedTimeout > tm.hardMaxTimeout {
		return nil, errInvalidTransactionTimeoutDuration("total lifetime would exceed 7-day maximum")
	}

	now := tm.clock.Now()
	state := &TransactionState{
		ID:                 txID,
		CreatedAt:          now,
		ExpiresAt:          now.Add(requestedTimeout),
		ChangeLog:          gitstore.NewChangeLogWithCap(tm.changeLogCap),
		StoreBranch:        txID,
		GitBranch:          txID,
		AppliedTimeout:     requestedTimeout,
		MainHeadAtLastSync: mainHeadAtLastSync,
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()
	if _, exists := tm.active[txID]; exists {
		return nil, fmt.Errorf("transaction %q already exists", txID)
	}
	tm.active[txID] = state
	return state, nil
}

// Lookup returns the transaction state for the given ID.
func (tm *TransactionManager) Lookup(txID string) (*TransactionState, error) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	state, ok := tm.active[txID]
	if !ok {
		return nil, fmt.Errorf("transaction %q not found", txID)
	}
	return state, nil
}

// Delete deregisters a transaction.
func (tm *TransactionManager) Delete(txID string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	delete(tm.active, txID)
}

// ExtendTimeout replaces the transaction expiry with now+duration.
// It does not _extend_ the current expiry (i.e. it is not
// max(oldExpiry, now+duration)). If a previous call set ExpiresAt further in
// the future and the new duration is shorter, the remaining lifetime shrinks.
// The total lifetime (now - createdAt + duration) must not exceed the hard max.
// ponytail: replace-vs-extend semantics are deliberate per SPEC ("resets expiry
// timer"), but the name "ExtendTimeout" is misleading. If max(oldExpiry,
// now+duration) semantics are desired, change the assignment to
// max(state.ExpiresAt, now.Add(duration)).
//
// The ExpiresAt/AppliedTimeout mutation and the persist callback run under the
// transaction's lifecycle lock, because lock(), the GC (both its first expiry
// scan and its re-check), and HasActive read those fields under lifecycle;
// tm.mu guards only the lookup and the
// re-verification that the registered state is unchanged, so no lock-order
// inversion (holding tm.mu while acquiring lifecycle) can occur. The caller
// must NOT already hold the lifecycle lock (the RPC handler uses LockActive as
// an admission gate only and releases it before calling this method).
func (tm *TransactionManager) ExtendTimeout(
	txID string, duration time.Duration, persist func(*TransactionState) error,
) error {
	if duration <= 0 {
		return errInvalidExtendTimeoutDuration("duration must be positive")
	}

	tm.mu.RLock()
	state, ok := tm.active[txID]
	tm.mu.RUnlock()
	if !ok {
		return errTransactionNotFound(txID)
	}

	if tm.beforeLifecycleLock != nil {
		tm.beforeLifecycleLock(txID)
	}
	state.lifecycle.Lock()
	defer state.lifecycle.Unlock()

	// Re-verify under lifecycle, mirroring lock(): a concurrent rollback or GC
	// cleanup may have deregistered (and replaced) the state between the
	// initial lookup and the lifecycle acquisition.
	tm.mu.RLock()
	current, stillActive := tm.active[txID]
	tm.mu.RUnlock()
	if !stillActive || current != state {
		return errTransactionNotFound(txID)
	}
	if state.RollbackOnly {
		return errTransactionNotFound(txID)
	}

	now := tm.clock.Now()
	if now.After(state.ExpiresAt) {
		return errTransactionTimedOut(txID)
	}
	totalLifetime := now.Sub(state.CreatedAt) + duration
	if totalLifetime > tm.hardMaxTimeout {
		return errInvalidExtendTimeoutDuration("total lifetime would exceed 7-day maximum")
	}

	oldExpiresAt := state.ExpiresAt
	oldAppliedTimeout := state.AppliedTimeout
	state.ExpiresAt = now.Add(duration)
	state.AppliedTimeout = duration
	if persist != nil {
		if err := persist(state); err != nil {
			// Revert the in-memory mutations on persist failure so the RPC's
			// reported failure is not contradicted by a live extended expiry
			// (recovery on restart restores the persisted, un-extended timeout —
			// the in-memory/durable divergence would otherwise be silent until
			// then). Mirrors the revert-on-persist-failure pattern used in
			// RefreshTransaction's MainHeadAtLastSync update.
			state.ExpiresAt = oldExpiresAt
			state.AppliedTimeout = oldAppliedTimeout
			return err
		}
	}
	return nil
}

// AddChangeLogEntry adds an entry to the transaction's change log.
// Returns RESOURCE_EXHAUSTED if the cap would be exceeded.
func (tm *TransactionManager) AddChangeLogEntry(txID string, entry gitstore.ChangeLogEntry) error {
	state, err := tm.Lookup(txID)
	if err != nil {
		return errTransactionNotFound(txID)
	}

	return state.ChangeLog.Add(entry)
}

// HasActive returns true if any registered transaction has not timed out.
// ExpiresAt is mutated by ExtendTimeout under the transaction's lifecycle
// lock, so it is read here under the same lock: the registered states are
// snapshotted under tm.mu and each state's expiry is then checked under its
// own lifecycle lock. tm.mu is released before lifecycle is acquired,
// preserving the documented lock order (no tm.mu-while-waiting-on-lifecycle
// inversion).
func (tm *TransactionManager) HasActive() bool {
	now := tm.clock.Now()
	tm.mu.RLock()
	states := make([]*TransactionState, 0, len(tm.active))
	for _, state := range tm.active {
		states = append(states, state)
	}
	tm.mu.RUnlock()
	for _, state := range states {
		state.lifecycle.Lock()
		active := !now.After(state.ExpiresAt)
		state.lifecycle.Unlock()
		if active {
			return true
		}
	}
	return false
}
