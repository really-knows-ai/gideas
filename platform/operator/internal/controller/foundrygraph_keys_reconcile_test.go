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
	"context"
	"encoding/base64"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

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

	// The per-namespace key Secrets must have been created. Both the public
	// `key` and the sidecar `private-key` values are base64-encoded for env-var
	// transport (a raw Ed25519 key can contain a NUL byte, which POSIX env vars
	// cannot hold), so the assertions compare against the base64 of the
	// operator-namespace raw bytes.
	var sd corev1.Secret
	if err := fakeCli.Get(ctx, types.NamespacedName{Name: sidecarKeySecretName, Namespace: targetNS}, &sd); err != nil {
		t.Fatalf("expected sidecar key Secret in namespace %q: %v", targetNS, err)
	}
	if string(sd.Data["key"]) != base64.StdEncoding.EncodeToString([]byte("sd-pub")) || string(sd.Data["private-key"]) != base64.StdEncoding.EncodeToString([]byte("sd-priv")) {
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
