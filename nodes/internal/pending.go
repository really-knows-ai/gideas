package internal

import "sync"

// PendingTracker provides best-effort per-replica deduplication of IDs.
type PendingTracker struct {
	mu      sync.Mutex
	pending map[string]struct{}
}

// NewPendingTracker creates a new PendingTracker.
func NewPendingTracker() *PendingTracker {
	return &PendingTracker{pending: make(map[string]struct{})}
}

// MarkPending returns true if the ID was not already pending.
func (p *PendingTracker) MarkPending(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.pending[id]; ok {
		return false
	}
	p.pending[id] = struct{}{}
	return true
}

// ClearPending removes an ID from the pending set.
func (p *PendingTracker) ClearPending(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.pending, id)
}
