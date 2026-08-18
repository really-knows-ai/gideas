package service

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Session Unit Tests
// ---------------------------------------------------------------------------

func TestSession_PauseResumeCycle(t *testing.T) {
	sess, _ := newSession(context.Background(), "w", "n", time.Second)
	defer sess.stop()

	if sess.paused {
		t.Fatal("new session should not be paused")
	}

	if !sess.pause() {
		t.Fatal("first pause should succeed")
	}
	if !sess.paused {
		t.Fatal("should be paused after pause()")
	}
	if sess.pause() {
		t.Fatal("second pause should fail")
	}

	if !sess.resume() {
		t.Fatal("resume should succeed after pause")
	}
	if sess.paused {
		t.Fatal("should not be paused after resume")
	}
	if sess.resume() {
		t.Fatal("resume without pause should fail")
	}
}

func TestSession_TimeoutCancelsContext(t *testing.T) {
	sess, ctx := newSession(context.Background(), "w", "n", 50*time.Millisecond)
	defer sess.stop()

	select {
	case <-ctx.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("context should have been cancelled by timeout")
	}

	if !sess.isTimedOut() {
		t.Fatal("session should be timed out")
	}
}

func TestSession_PausePreventsTimeout(t *testing.T) {
	sess, ctx := newSession(context.Background(), "w", "n", 100*time.Millisecond)
	defer sess.stop()

	if !sess.pause() {
		t.Fatal("pause should succeed")
	}

	// Wait longer than timeout.
	time.Sleep(200 * time.Millisecond)

	// Context should NOT be cancelled.
	select {
	case <-ctx.Done():
		t.Fatal("context should not be cancelled while paused")
	default:
		// expected
	}

	if sess.isTimedOut() {
		t.Fatal("session should not be timed out while paused")
	}
}

func TestSession_ResetTimerWhilePaused(t *testing.T) {
	sess, _ := newSession(context.Background(), "w", "n", time.Second)
	defer sess.stop()

	sess.pause()
	// resetTimer should be a no-op while paused.
	sess.resetTimer()
	if !sess.paused {
		t.Fatal("should still be paused after resetTimer")
	}
}

// ---------------------------------------------------------------------------
// Session Child Tracking Unit Tests
// ---------------------------------------------------------------------------

func TestSession_AddChild_HasChild(t *testing.T) {
	sess, _ := newSession(context.Background(), "w", "n", time.Second)
	defer sess.stop()

	if _, ok := sess.childIDs["child-1"]; ok {
		t.Fatal("new session should not have any children")
	}

	sess.addChild("child-1")
	sess.addChild("child-2")

	if _, ok := sess.childIDs["child-1"]; !ok {
		t.Fatal("expected child-1 to be tracked")
	}
	if _, ok := sess.childIDs["child-2"]; !ok {
		t.Fatal("expected child-2 to be tracked")
	}
	if _, ok := sess.childIDs["child-3"]; ok {
		t.Fatal("child-3 was not added")
	}
}

func TestSession_AddChild_Idempotent(t *testing.T) {
	sess, _ := newSession(context.Background(), "w", "n", time.Second)
	defer sess.stop()

	sess.addChild("child-1")
	sess.addChild("child-1") // duplicate add

	if _, ok := sess.childIDs["child-1"]; !ok {
		t.Fatal("expected child-1 to be tracked")
	}
}
