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

// realClock implements Clock using the standard time package.
type realClock struct{}

func (realClock) Now() time.Time                   { return time.Now() }
func (realClock) NewTicker(d time.Duration) Ticker { return &realTicker{t: time.NewTicker(d)} }

type realTicker struct{ t *time.Ticker }

func (r *realTicker) C() <-chan time.Time { return r.t.C }
func (r *realTicker) Stop()               { r.t.Stop() }

// TransactionManager manages the lifecycle of active transactions.
type TransactionManager struct {
	mu                  sync.RWMutex
	active              map[string]*TransactionState
	defaultTimeout      time.Duration
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
	if !cleanup && state.RollbackOnly {
		state.lifecycle.Unlock()
		return nil, nil, errTransactionRollbackOnly(txID)
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

// NewTransactionManager creates a manager with the real clock.
func NewTransactionManager(
	defaultTimeout, hardMaxTimeout time.Duration, changeLogCap int,
	opts ...func(*TransactionManager),
) *TransactionManager {
	tm := &TransactionManager{
		active:         make(map[string]*TransactionState),
		defaultTimeout: defaultTimeout,
		hardMaxTimeout: hardMaxTimeout,
		changeLogCap:   changeLogCap,
		clock:          &realClock{},
	}
	for _, o := range opts {
		o(tm)
	}
	return tm
}

// WithClock returns an option that sets the clock on the TransactionManager.
func WithClock(c Clock) func(*TransactionManager) {
	return func(tm *TransactionManager) { tm.clock = c }
}

// Create registers a new transaction with the given ID and requested timeout.
// The timeout is capped at the hard maximum. Returns the transaction state.
func (tm *TransactionManager) Create(
	txID string, requestedTimeout time.Duration, mainHeadAtLastSync string,
) (*TransactionState, error) {
	cappedTimeout := requestedTimeout
	if cappedTimeout <= 0 {
		cappedTimeout = tm.defaultTimeout
	}
	if cappedTimeout > tm.hardMaxTimeout {
		cappedTimeout = tm.hardMaxTimeout
	}

	now := tm.clock.Now()
	state := &TransactionState{
		ID:                 txID,
		CreatedAt:          now,
		ExpiresAt:          now.Add(cappedTimeout),
		ChangeLog:          gitstore.NewChangeLogWithCap(tm.changeLogCap),
		StoreBranch:        txID,
		GitBranch:          txID,
		AppliedTimeout:     cappedTimeout,
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

// ValidateActive checks that the txID is a valid UUID, references an active
// transaction, and has not timed out. Returns the appropriate gRPC status error.
func (tm *TransactionManager) ValidateActive(txID string) error {
	_, unlock, err := tm.LockActive(txID)
	if err != nil {
		return err
	}
	unlock()
	return nil
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
// ponytail: acquires the write lock for the entire operation to prevent a TOCTOU
// race between Lookup (RLock) and modification (Lock). The upgrade path is to
// split into a RLock-protected read phase followed by a Lock-protected write
// phase with re-verification, but the write-lock-held duration is negligible so
// this simpler approach is preferred.
func (tm *TransactionManager) ExtendTimeout(txID string, duration time.Duration) error {
	if duration <= 0 {
		return errInvalidExtendTimeoutDuration("duration must be positive")
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	state, ok := tm.active[txID]
	if !ok {
		return errTransactionNotFound(txID)
	}

	now := tm.clock.Now()
	totalLifetime := now.Sub(state.CreatedAt) + duration
	if totalLifetime > tm.hardMaxTimeout {
		return errInvalidExtendTimeoutDuration("total lifetime would exceed 7-day maximum")
	}

	state.ExpiresAt = now.Add(duration)
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
func (tm *TransactionManager) HasActive() bool {
	now := tm.clock.Now()
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	for _, state := range tm.active {
		if !now.After(state.ExpiresAt) {
			return true
		}
	}
	return false
}
