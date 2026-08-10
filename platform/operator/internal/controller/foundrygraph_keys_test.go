/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

func TestGenerateEd25519KeyPair(t *testing.T) {
	pub, priv, err := generateEd25519KeyPair()
	if err != nil {
		t.Fatalf("generateEd25519KeyPair() returned error: %v", err)
	}

	if len(pub) != ed25519.PublicKeySize {
		t.Errorf("public key length = %d, want %d", len(pub), ed25519.PublicKeySize)
	}
	if len(priv) != ed25519.PrivateKeySize {
		t.Errorf("private key length = %d, want %d", len(priv), ed25519.PrivateKeySize)
	}
}

func TestGeneratedKeySignVerify(t *testing.T) {
	pub, priv, err := generateEd25519KeyPair()
	if err != nil {
		t.Fatalf("generateEd25519KeyPair() returned error: %v", err)
	}

	message := []byte("test message")
	sig := ed25519.Sign(ed25519.PrivateKey(priv), message)

	if !ed25519.Verify(ed25519.PublicKey(pub), message, sig) {
		t.Error("signature verification failed")
	}
}

func TestReconcileSecretsCreatesPerNamespaceKeys(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)

	operatorNS := operatorTestNS
	targetNS := graphTestNS

	// Operator namespace holds the shared signing-key secrets.
	operatorKey := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: operatorNS},
		Data: map[string][]byte{
			"key":         []byte("op-pub"),
			"private-key": []byte("op-priv"),
		},
	}
	sidecarKey := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: sidecarSigningKeySecretName, Namespace: operatorNS},
		Data: map[string][]byte{
			"key":         []byte("sd-pub"),
			"private-key": []byte("sd-priv"),
		},
	}

	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: targetNS}}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(operatorKey, sidecarKey).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, OperatorNamespace: operatorNS}

	ctx := context.Background()
	if err := r.reconcileSecrets(ctx, fg); err != nil {
		t.Fatalf("reconcileSecrets: %v", err)
	}

	// The per-namespace key Secrets must have been created. The public `key`
	// values are base64-encoded for env-var transport (a raw Ed25519 key can
	// contain a NUL byte, which POSIX env vars cannot hold), so the assertions
	// compare against the base64 of the operator-namespace raw bytes; the
	// sidecar `private-key` is copied through raw (nothing consumes it from an
	// env var today).
	var sd corev1.Secret
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: sidecarKeySecretName, Namespace: targetNS}, &sd); err != nil {
		t.Fatalf("expected sidecar key Secret in namespace %q: %v", targetNS, err)
	}
	if string(sd.Data["key"]) != base64.StdEncoding.EncodeToString([]byte("sd-pub")) || string(sd.Data["private-key"]) != "sd-priv" {
		t.Errorf("sidecar key Secret data mismatch: %v", sd.Data)
	}

	var op corev1.Secret
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: operatorKeySecretName, Namespace: targetNS}, &op); err != nil {
		t.Fatalf("expected operator key Secret in namespace %q: %v", targetNS, err)
	}
	if string(op.Data["key"]) != base64.StdEncoding.EncodeToString([]byte("op-pub")) {
		t.Errorf("operator key Secret data mismatch: %v", op.Data)
	}
}

func TestReconcileSecretsIdempotentWhenPresent(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)

	operatorNS := operatorTestNS
	targetNS := graphTestNS
	// Operator-namespace signing secrets (reconciled FROM) must exist now that
	// reconcileSecrets always reads them.
	operatorKey := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: operatorNS},
		Data:       map[string][]byte{"key": []byte("op-current"), "private-key": []byte("op-priv")},
	}
	sidecarKey := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: sidecarSigningKeySecretName, Namespace: operatorNS},
		Data:       map[string][]byte{"key": []byte("sd-current"), "private-key": []byte("sd-priv")},
	}
	// Both per-namespace Secrets already exist.
	sd := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: sidecarKeySecretName, Namespace: targetNS},
		Data:       map[string][]byte{"key": []byte("existing-sd"), "private-key": []byte("existing-sd-priv")},
	}
	op := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: operatorKeySecretName, Namespace: targetNS},
		Data:       map[string][]byte{"key": []byte("existing-op")},
	}

	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: targetNS}}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(operatorKey, sidecarKey, sd, op).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, OperatorNamespace: operatorNS}

	ctx := context.Background()
	if err := r.reconcileSecrets(ctx, fg); err != nil {
		t.Fatalf("reconcileSecrets: %v", err)
	}

	// Existing data is reconciled to the operator-namespace values (converged), so the
	// per-namespace operator key reflects the current operator public key.
	var got corev1.Secret
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: operatorKeySecretName, Namespace: targetNS}, &got); err != nil {
		t.Fatalf("get operator Secret: %v", err)
	}
	if string(got.Data["key"]) != base64.StdEncoding.EncodeToString([]byte("op-current")) {
		t.Errorf("operator key should be converged to the current operator public key, got %q", got.Data["key"])
	}
}

// TestReconcileSecretsPropagatesKeyRotation (item 7) asserts that when the operator's
// signing key is rotated in the operator namespace, a subsequent reconcile updates the
// per-namespace public-key Secret — the stale public key does not persist while the proxy
// signs with the new private key (fail-closed learning).
func TestReconcileSecretsPropagatesKeyRotation(t *testing.T) {
	s := scheme.Scheme
	_ = flowv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)

	operatorNS := operatorTestNS
	targetNS := graphTestNS

	fg := &flowv1.FoundryGraph{ObjectMeta: metav1.ObjectMeta{Name: defaultGraphName, Namespace: targetNS}}

	// Old per-namespace operator key (stale, from a previous key generation).
	staleOp := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: operatorKeySecretName, Namespace: targetNS},
		Data:       map[string][]byte{"key": []byte("stale-pub")},
	}
	// Operator namespace holds the ORIGINAL signing key.
	operatorKey := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: operatorNS},
		Data:       map[string][]byte{"key": []byte("orig-pub"), "private-key": []byte("orig-priv")},
	}
	sidecarKey := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: sidecarSigningKeySecretName, Namespace: operatorNS},
		Data:       map[string][]byte{"key": []byte("sd-pub"), "private-key": []byte("sd-priv")},
	}

	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(staleOp, operatorKey, sidecarKey).Build()
	r := &FoundryGraphReconciler{Client: fakeCli, Scheme: s, OperatorNamespace: operatorNS}
	ctx := context.Background()

	if err := r.reconcileSecrets(ctx, fg); err != nil {
		t.Fatalf("reconcileSecrets: %v", err)
	}

	// Operator key rotated in the operator namespace to a NEW pair.
	var rotatedOperator corev1.Secret
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: operatorSigningKeySecretName, Namespace: operatorNS}, &rotatedOperator); err != nil {
		t.Fatalf("get operator signing Secret: %v", err)
	}
	rotatedOperator.Data["key"] = []byte("rotated-pub")
	rotatedOperator.Data["private-key"] = []byte("rotated-priv")
	if err := fakeCli.Update(ctx, &rotatedOperator); err != nil {
		t.Fatalf("update rotated operator signing Secret: %v", err)
	}

	// A second reconcile must propagate the rotated public key to the per-namespace Secret.
	if err := r.reconcileSecrets(ctx, fg); err != nil {
		t.Fatalf("reconcileSecrets after rotation: %v", err)
	}
	var perNS corev1.Secret
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: operatorKeySecretName, Namespace: targetNS}, &perNS); err != nil {
		t.Fatalf("get per-namespace operator key Secret: %v", err)
	}
	if string(perNS.Data["key"]) != base64.StdEncoding.EncodeToString([]byte("rotated-pub")) {
		t.Errorf("expected per-namespace operator key reconciled to rotated public key, got %q", perNS.Data["key"])
	}
}

// TestInitializeOperatorSigningKeyCreatesOnceAndReuses covers the create-once /
// reuse-on-restart preamble (SPEC R6): the first call generates and persists the operator
// signing key Secret with data keys "key" (public) and "private-key" (private); a second
// call must read the existing Secret and return the SAME private key — a new signing key
// is not generated per restart or per FoundryGraph resource.
func TestInitializeOperatorSigningKeyCreatesOnceAndReuses(t *testing.T) {
	s := scheme.Scheme
	_ = corev1.AddToScheme(s)
	fakeCli := fake.NewClientBuilder().WithScheme(s).Build()
	ctx := context.Background()

	key1, err := InitializeOperatorSigningKey(ctx, fakeCli, "operator-ns")
	if err != nil {
		t.Fatalf("InitializeOperatorSigningKey (first): %v", err)
	}
	if len(key1) != ed25519.PrivateKeySize {
		t.Errorf("expected a 64-byte Ed25519 private key, got %d bytes", len(key1))
	}
	// The generated Secret must carry both data keys with valid key material.
	var secret corev1.Secret
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: operatorSigningKeySecretName, Namespace: "operator-ns"}, &secret); err != nil {
		t.Fatalf("get generated operator signing key Secret: %v", err)
	}
	if len(secret.Data["key"]) != ed25519.PublicKeySize {
		t.Errorf("expected data key %q to hold a 32-byte public key, got %d bytes", "key", len(secret.Data["key"]))
	}
	if len(secret.Data["private-key"]) != ed25519.PrivateKeySize {
		t.Errorf("expected data key %q to hold a 64-byte private key, got %d bytes", "private-key", len(secret.Data["private-key"]))
	}

	// Restart: a second call must reuse the persisted key, not generate a new one.
	key2, err := InitializeOperatorSigningKey(ctx, fakeCli, "operator-ns")
	if err != nil {
		t.Fatalf("InitializeOperatorSigningKey (second): %v", err)
	}
	if !bytes.Equal(key1, key2) {
		t.Error("expected the signing key to be reused across restarts, got a different key")
	}
}

// TestInitializeOperatorSigningKeyEmptyPrivateKeyError covers the empty-private-key
// branch: a pre-existing Secret whose data key "private-key" is empty is a corrupted
// state and must fail loudly (SPEC R6 persists the private key under "private-key").
func TestInitializeOperatorSigningKeyEmptyPrivateKeyError(t *testing.T) {
	s := scheme.Scheme
	_ = corev1.AddToScheme(s)
	// Secret exists but carries no private-key data.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: operatorSigningKeySecretName, Namespace: "operator-ns"},
		Data:       map[string][]byte{"key": []byte("pub")},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithObjects(secret).Build()

	_, err := InitializeOperatorSigningKey(context.Background(), fakeCli, "operator-ns")
	if err == nil {
		t.Fatal("expected an error for a Secret with an empty private-key")
	}
	if !strings.Contains(err.Error(), "empty private-key") {
		t.Errorf("expected the empty-private-key error, got: %v", err)
	}
}

// TestInitializeOperatorSigningKeyGetError covers the non-NotFound Get error branch: an
// apiserver/RBAC error on the Get must surface (wrapped), not be treated as "not found"
// and overwritten with a freshly generated key.
func TestInitializeOperatorSigningKeyGetError(t *testing.T) {
	s := scheme.Scheme
	_ = corev1.AddToScheme(s)
	interceptorFuncs := interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return errors.New("apiserver unavailable")
			}
			return nil
		},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptorFuncs).Build()

	_, err := InitializeOperatorSigningKey(context.Background(), fakeCli, "operator-ns")
	if err == nil {
		t.Fatal("expected the non-NotFound Get error to surface")
	}
	if !strings.Contains(err.Error(), "get operator signing key Secret") {
		t.Errorf("expected the wrapped Get error, got: %v", err)
	}
}

// TestInitializeSidecarSigningKeyCreatesOnce covers the create-once preamble for the
// sidecar signing key (SPEC R6): the first call generates the Secret with data keys
// "key" and "private-key"; a second call succeeds without regenerating (the persisted
// data is unchanged).
func TestInitializeSidecarSigningKeyCreatesOnce(t *testing.T) {
	s := scheme.Scheme
	_ = corev1.AddToScheme(s)
	fakeCli := fake.NewClientBuilder().WithScheme(s).Build()
	ctx := context.Background()

	if err := InitializeSidecarSigningKey(ctx, fakeCli, "operator-ns"); err != nil {
		t.Fatalf("InitializeSidecarSigningKey (first): %v", err)
	}
	var secret corev1.Secret
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: sidecarSigningKeySecretName, Namespace: "operator-ns"}, &secret); err != nil {
		t.Fatalf("get generated sidecar signing key Secret: %v", err)
	}
	if len(secret.Data["key"]) != ed25519.PublicKeySize || len(secret.Data["private-key"]) != ed25519.PrivateKeySize {
		t.Errorf("expected sidecar Secret data keys key/private-key with valid Ed25519 material, got %d/%d bytes",
			len(secret.Data["key"]), len(secret.Data["private-key"]))
	}
	before := map[string][]byte{"key": secret.Data["key"], "private-key": secret.Data["private-key"]}

	// Restart: no error, and the persisted key material is unchanged (create-once).
	if err := InitializeSidecarSigningKey(ctx, fakeCli, "operator-ns"); err != nil {
		t.Fatalf("InitializeSidecarSigningKey (second): %v", err)
	}
	var afterSecret corev1.Secret
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: sidecarSigningKeySecretName, Namespace: "operator-ns"}, &afterSecret); err != nil {
		t.Fatalf("get sidecar signing key Secret after restart: %v", err)
	}
	if !bytes.Equal(before["key"], afterSecret.Data["key"]) || !bytes.Equal(before["private-key"], afterSecret.Data["private-key"]) {
		t.Error("expected the sidecar signing key to be reused across restarts, not regenerated")
	}
}

// TestInitializeSidecarSigningKeyGetError covers the non-NotFound Get error branch: an
// apiserver/RBAC error on the Get must surface (wrapped), not be treated as "not found".
func TestInitializeSidecarSigningKeyGetError(t *testing.T) {
	s := scheme.Scheme
	_ = corev1.AddToScheme(s)
	interceptorFuncs := interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return errors.New("apiserver unavailable")
			}
			return nil
		},
	}
	fakeCli := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptorFuncs).Build()

	err := InitializeSidecarSigningKey(context.Background(), fakeCli, "operator-ns")
	if err == nil {
		t.Fatal("expected the non-NotFound Get error to surface")
	}
	if !strings.Contains(err.Error(), "get sidecar signing key Secret") {
		t.Errorf("expected the wrapped Get error, got: %v", err)
	}
}
