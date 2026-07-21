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
	mu             sync.RWMutex
	active         map[string]*TransactionState
	defaultTimeout time.Duration
	hardMaxTimeout time.Duration // 7 days
	changeLogCap   int           // 100000
	clock          Clock
}

// TransactionState holds the runtime state for a single transaction.
type TransactionState struct {
	ID                 string
	CreatedAt          time.Time
	ExpiresAt          time.Time
	ChangeLog          *gitstore.ChangeLog
	StoreBranch        string
	GitBranch          string
	AppliedTimeout     time.Duration
	MainHeadAtLastSync string
	SchemaHash         string // hash of schema at begin time
}

// NewTransactionManager creates a manager with the real clock.
func NewTransactionManager(defaultTimeout, hardMaxTimeout time.Duration, changeLogCap int, opts ...func(*TransactionManager)) *TransactionManager {
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
func (tm *TransactionManager) Create(txID string, requestedTimeout time.Duration, mainHeadAtLastSync string) (*TransactionState, error) {
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
		ChangeLog:          gitstore.NewChangeLog(),
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
	if txID == "" {
		return errInvalidTransactionIDFormat(txID)
	}
	if !isValidUUID(txID) {
		return errInvalidTransactionIDFormat(txID)
	}

	state, err := tm.Lookup(txID)
	if err != nil {
		return errTransactionNotFound(txID)
	}

	if tm.clock.Now().After(state.ExpiresAt) {
		return errTransactionTimedOut(txID)
	}
	return nil
}

// ExtendTimeout extends the transaction timeout by the given duration.
// The total lifetime (now - createdAt + duration) must not exceed the hard max.
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

	state.ChangeLog.Add(entry)
	return nil
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

// ActiveCount returns the number of active (non-expired) transactions.
func (tm *TransactionManager) ActiveCount() int {
	now := tm.clock.Now()
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	count := 0
	for _, state := range tm.active {
		if !now.After(state.ExpiresAt) {
			count++
		}
	}
	return count
}
