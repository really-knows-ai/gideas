package main

// newReadSecretFn (SPEC R1 Secret read) tests

import (
	"context"
	"testing"

	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestNewReadSecretFn verifies SPEC R1 (SPEC.md:103): the Cartographer reads
// the remote-auth Secret via its pod's ServiceAccount on each remote operation.
// This is the only direct test of the real k8s wrapper — newReadSecretFn
// (main.go:887-901) — exercised through the fake clientset, the in-memory twin
// of the pod's ServiceAccount reader. Every other test injects a hand-rolled
// readSecretFn closure, so the Secret fetch by name in the pod namespace and
// the Data byte→string decoding are untested elsewhere.
func TestNewReadSecretFn(t *testing.T) {
	ctx := context.Background()
	const (
		namespace = "test-ns"
		secretRef = "remote-auth"
	)
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretRef, Namespace: namespace},
		Data: map[string][]byte{
			"username": []byte(tSecretUsername),
			"password": []byte(tSecretPassword),
			"ignored":  []byte("extra-key"),
		},
	})

	got, err := newReadSecretFn(cs, namespace)(ctx, secretRef)
	if err != nil {
		t.Fatalf("read Secret via fake clientset: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("decoded Secret keys = %d, want 3 (every Data key surfaced)", len(got))
	}
	if got["username"] != tSecretUsername {
		t.Errorf("decoded username = %q, want %q", got["username"], tSecretUsername)
	}
	if got["password"] != tSecretPassword {
		t.Errorf("decoded password = %q, want %q", got["password"], tSecretPassword)
	}
	// The scheme filter (which keys matter) lives in buildResolveAuthFn, not
	// the reader: a Data key the URL scheme ignores must still be surfaced.
	if got["ignored"] != "extra-key" {
		t.Errorf("decoded ignored key = %q, want %q", got["ignored"], "extra-key")
	}
}

// TestNewReadSecretFnNotFoundPropagates verifies the k8s error-propagation
// branch of newReadSecretFn: a Secret absent from the pod namespace surfaces
// the clientset's not-found StatusError unchanged (never a nil error, never a
// partial map) so the callers — buildResolveAuthFn and the pre-flight checks —
// fail closed on it.
func TestNewReadSecretFnNotFoundPropagates(t *testing.T) {
	cs := fake.NewSimpleClientset() // no Secrets in the pod namespace
	got, err := newReadSecretFn(cs, "test-ns")(context.Background(), "remote-auth")
	if err == nil {
		t.Fatal("expected a k8s not-found error for an absent Secret, got nil")
	}
	if got != nil {
		t.Fatalf("expected nil map on error, got %v", got)
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("error = %v, want a k8s not-found StatusError", err)
	}
}

// TestNewReadSecretFnRotationTakesEffect pins SPEC R1's rotation branch
// (SPEC.md:103): the Cartographer reads the Secret via its pod's ServiceAccount
// on each remote operation, so a rotated credential takes effect without
// restart. The full production chain is exercised — newReadSecretFn
// (main.go:1074-1088) over the fake clientset feeding buildResolveAuthFn's
// resolver (main.go:737-836) — and the Secret is re-written between resolutions
// to simulate a rotation. The test fails if either link caches: a reader that
// snapshots the Secret at construction, or a resolver that memoizes the first
// resolved auth, would both keep serving the pre-rotation password.
func TestNewReadSecretFnRotationTakesEffect(t *testing.T) {
	ctx := context.Background()
	const (
		namespace = "test-ns"
		secretRef = "remote-auth"
	)
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretRef, Namespace: namespace},
		Data: map[string][]byte{
			"username": []byte(tSecretUsername),
			"password": []byte("old-pass"),
		},
	})
	fn := buildResolveAuthFn(secretRef, newReadSecretFn(cs, namespace), "https://example.com/repo.git")

	resolve := func(wantPassword string) {
		t.Helper()
		auth, err := fn()
		if err != nil {
			t.Fatalf("auth resolution failed: %v", err)
		}
		basic, ok := auth.(*gogithttp.BasicAuth)
		if !ok {
			t.Fatalf("expected *http.BasicAuth, got %T", auth)
		}
		if basic.Password != wantPassword {
			t.Fatalf("resolved password = %q, want %q", basic.Password, wantPassword)
		}
	}

	// First resolution: the pre-rotation credential.
	resolve("old-pass")

	// Rotate the Secret in place (the fake clientset models a k8s rotation via
	// Update): the next resolution must pick up the new credential with no
	// restart.
	if _, err := cs.CoreV1().Secrets(namespace).Update(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretRef, Namespace: namespace},
		Data: map[string][]byte{
			"username": []byte(tSecretUsername),
			"password": []byte("rotated-pass"),
		},
	}, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("rotate Secret in fake clientset: %v", err)
	}

	resolve("rotated-pass")
}
