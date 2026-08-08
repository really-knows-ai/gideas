package service

import (
	"sync"
	"testing"
	"time"
)

// intervalRecordingClock records the duration handed to NewTicker so tests can
// assert the SyncWorker's periodic interval without waiting on real time.
type intervalRecordingClock struct {
	mu      sync.Mutex
	dur     time.Duration
	tickers int
}

func (c *intervalRecordingClock) Now() time.Time { return time.Now() }

func (c *intervalRecordingClock) NewTicker(d time.Duration) Ticker {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dur = d
	c.tickers++
	return &noopTicker{ch: make(chan time.Time, 1)}
}

type noopTicker struct{ ch chan time.Time }

func (t *noopTicker) C() <-chan time.Time { return t.ch }
func (t *noopTicker) Stop()               {}

// created reports whether the worker created its ticker (which happens right
// after the startup cycle) and, if so, the interval it used.
func (c *intervalRecordingClock) created() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dur, c.tickers > 0
}

// TestSyncWorkerInterval pins the SPEC R10 "(configurable)" contract: the
// periodic sync interval defaults to DefaultSyncInterval (one minute) and is
// overridable via SyncWorkerWithSyncInterval.
func TestSyncWorkerInterval(t *testing.T) {
	tests := []struct {
		name string
		opts []SyncWorkerOption
		want time.Duration
	}{
		{"defaults to one minute", nil, DefaultSyncInterval},
		{"option overrides the interval",
			[]SyncWorkerOption{SyncWorkerWithSyncInterval(30 * time.Second)}, 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := &intervalRecordingClock{}
			w := NewSyncWorker("", nil, nil, clk, tt.opts...)
			go w.Run()
			t.Cleanup(w.Stop)
			// The worker creates its ticker immediately after the startup
			// catch-up cycle; the empty remoteURL keeps that cycle a no-op.
			waitFor(t, func() bool {
				_, ok := clk.created()
				return ok
			}, "worker to create its ticker")
			got, _ := clk.created()
			if got != tt.want {
				t.Fatalf("sync interval = %v, want %v", got, tt.want)
			}
		})
	}
}
