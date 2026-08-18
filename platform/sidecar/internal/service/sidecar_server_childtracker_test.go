package service

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// SidecarServer Child Tracker / Authorizer Tests
// ---------------------------------------------------------------------------

func TestSidecarServer_TrackChild(t *testing.T) {
	srv := NewSidecarServer("test-ns", "node-1", "")

	// Create a session.
	sess, _ := newSession(context.Background(), "wi-1", "node-1", DefaultTimeout)
	defer sess.stop()
	srv.mu.Lock()
	srv.sessions["wi-1"] = sess
	srv.mu.Unlock()

	srv.TrackChild("wi-1", "child-1")
	srv.TrackChild("wi-1", "child-2")

	if _, ok := sess.childIDs["child-1"]; !ok {
		t.Fatal("expected child-1 to be tracked in session")
	}
	if _, ok := sess.childIDs["child-2"]; !ok {
		t.Fatal("expected child-2 to be tracked in session")
	}
}

func TestSidecarServer_TrackChild_NoSession(t *testing.T) {
	srv := NewSidecarServer("test-ns", "node-1", "")

	// Should not panic when no session exists.
	srv.TrackChild("nonexistent", "child-1")
}

func TestSidecarServer_AuthorizeChildAccess_Allowed(t *testing.T) {
	srv := NewSidecarServer("test-ns", "node-1", "")

	sess, _ := newSession(context.Background(), "wi-1", "node-1", DefaultTimeout)
	defer sess.stop()
	srv.mu.Lock()
	srv.sessions["wi-1"] = sess
	srv.mu.Unlock()

	srv.TrackChild("wi-1", "child-1")

	decision := srv.AuthorizeChildAccess("wi-1", "child-1")
	if decision != ChildAccessAllowed {
		t.Fatalf("expected ChildAccessAllowed, got %d", decision)
	}
}

func TestSidecarServer_AuthorizeChildAccess_Denied(t *testing.T) {
	srv := NewSidecarServer("test-ns", "node-1", "")

	sess, _ := newSession(context.Background(), "wi-1", "node-1", DefaultTimeout)
	defer sess.stop()
	srv.mu.Lock()
	srv.sessions["wi-1"] = sess
	srv.mu.Unlock()

	// Session has children but the target is not one of them.
	srv.TrackChild("wi-1", "child-1")

	decision := srv.AuthorizeChildAccess("wi-1", "child-unknown")
	if decision != ChildAccessDenied {
		t.Fatalf("expected ChildAccessDenied, got %d", decision)
	}
}

func TestSidecarServer_AuthorizeChildAccess_Unknown_NoSession(t *testing.T) {
	srv := NewSidecarServer("test-ns", "node-1", "")

	decision := srv.AuthorizeChildAccess("nonexistent", "child-1")
	if decision != ChildAccessUnknown {
		t.Fatalf("expected ChildAccessUnknown, got %d", decision)
	}
}

func TestSidecarServer_AuthorizeChildAccess_Unknown_NoChildren(t *testing.T) {
	srv := NewSidecarServer("test-ns", "node-1", "")

	// Session exists but has no children (collection phase).
	sess, _ := newSession(context.Background(), "wi-1", "node-1", DefaultTimeout)
	defer sess.stop()
	srv.mu.Lock()
	srv.sessions["wi-1"] = sess
	srv.mu.Unlock()

	decision := srv.AuthorizeChildAccess("wi-1", "child-1")
	if decision != ChildAccessUnknown {
		t.Fatalf("expected ChildAccessUnknown for session with no children, got %d", decision)
	}
}
