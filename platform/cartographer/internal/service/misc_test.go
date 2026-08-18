package service

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestGitLockSerialization(t *testing.T) {
	srv, _ := newTestServer(t)

	var wg sync.WaitGroup
	concurrent := 0
	var mu sync.Mutex

	for range 5 {
		wg.Go(func() {
			err := srv.withGitLock(func() error {
				mu.Lock()
				concurrent++
				if concurrent > 1 {
					mu.Unlock()
					return fmt.Errorf("detected concurrent git lock holders")
				}
				mu.Unlock()

				// Simulate some work.
				time.Sleep(10 * time.Millisecond)

				mu.Lock()
				concurrent--
				mu.Unlock()
				return nil
			})
			if err != nil {
				t.Errorf("withGitLock failed: %v", err)
			}
		})
	}
	wg.Wait()
}
