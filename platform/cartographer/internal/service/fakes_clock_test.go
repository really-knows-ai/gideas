package service

import (
	"sync"
	"time"
)

// fakeClock implements Clock for testing.
type fakeClock struct {
	mu          sync.Mutex
	now         time.Time
	lastTicker  *fakeTicker
	tickerCount int
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }
func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}
func (f *fakeClock) NewTicker(d time.Duration) Ticker {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTicker{ch: make(chan time.Time, 1)}
	f.lastTicker = t
	f.tickerCount++
	return t
}
func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// tickers returns how many tickers the clock has handed out. The SyncWorker
// creates its ticker immediately after the startup cycle, so a non-zero count
// is the barrier for "the worker finished its startup cycle and is parked in
// the select loop".
func (f *fakeClock) tickers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tickerCount
}

// FireTicker emits one tick on the most recently created ticker. The ticker
// channel is buffered (size 1) so the send never blocks; a tick delivered
// before the worker parks in select is consumed when it does.
func (f *fakeClock) FireTicker() {
	f.mu.Lock()
	t := f.lastTicker
	f.mu.Unlock()
	if t == nil {
		return
	}
	select {
	case t.ch <- time.Now():
	default:
	}
}

type fakeTicker struct{ ch chan time.Time }

func (f *fakeTicker) C() <-chan time.Time { return f.ch }
func (f *fakeTicker) Stop()               {}
