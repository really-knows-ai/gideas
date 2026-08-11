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
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

const (
	operatorSigningKeySecretName = "cartographer-operator-signing-key" // operator's own namespace
	sidecarSigningKeySecretName  = "cartographer-sidecar-signing-key"  // operator's own namespace
	operatorKeySecretName        = "cartographer-operator-key"         // per namespace
	sidecarKeySecretName         = "cartographer-sidecar-key"          // per namespace
)

// generateEd25519KeyPair generates an Ed25519 key pair.
// Returns raw 32-byte public key and 64-byte private key.
func generateEd25519KeyPair() (publicKey, privateKey []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate Ed25519 key pair: %w", err)
	}
	return []byte(pub), []byte(priv), nil
}

// InitializeOperatorSigningKey creates or reads the operator's Ed25519 signing key.
// Returns the private key for use by the proxy server.
func InitializeOperatorSigningKey(ctx context.Context, c client.Client, operatorNamespace string) ([]byte, error) {
	log := logf.FromContext(ctx)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      operatorSigningKeySecretName,
			Namespace: operatorNamespace,
		},
	}

	if err := c.Get(ctx, client.ObjectKeyFromObject(secret), secret); err == nil {
		// Secret already exists — read and return the private key.
		privKey := secret.Data["private-key"]
		if len(privKey) == 0 {
			return nil, fmt.Errorf("operator signing key Secret %q has empty private-key", operatorSigningKeySecretName)
		}
		log.Info("Operator signing key already exists", "namespace", operatorNamespace)
		return privKey, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get operator signing key Secret: %w", err)
	}

	// Secret does not exist — generate a new key pair.
	pubKey, privKey, err := generateEd25519KeyPair()
	if err != nil {
		return nil, fmt.Errorf("generate operator signing key: %w", err)
	}

	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      operatorSigningKeySecretName,
			Namespace: operatorNamespace,
		},
		Data: map[string][]byte{
			"key":         pubKey,
			"private-key": privKey,
		},
	}
	if err := c.Create(ctx, secret); err != nil {
		return nil, fmt.Errorf("create operator signing key Secret: %w", err)
	}

	log.Info("Operator signing key generated and stored", "namespace", operatorNamespace)
	return privKey, nil
}

// InitializeSidecarSigningKey creates or verifies the sidecar's Ed25519 signing key.
func InitializeSidecarSigningKey(ctx context.Context, c client.Client, operatorNamespace string) error {
	log := logf.FromContext(ctx)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sidecarSigningKeySecretName,
			Namespace: operatorNamespace,
		},
	}

	if err := c.Get(ctx, client.ObjectKeyFromObject(secret), secret); err == nil {
		log.Info("Sidecar signing key already exists", "namespace", operatorNamespace)
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get sidecar signing key Secret: %w", err)
	}

	// Generate new key pair.
	pubKey, privKey, err := generateEd25519KeyPair()
	if err != nil {
		return fmt.Errorf("generate sidecar signing key: %w", err)
	}

	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sidecarSigningKeySecretName,
			Namespace: operatorNamespace,
		},
		Data: map[string][]byte{
			"key":         pubKey,
			"private-key": privKey,
		},
	}
	if err := c.Create(ctx, secret); err != nil {
		return fmt.Errorf("create sidecar signing key Secret: %w", err)
	}

	log.Info("Sidecar signing key generated and stored", "namespace", operatorNamespace)
	return nil
}

// reconcileSecrets reconciles the namespace-scoped key Secrets against the
// operator-namespace signing Secrets. These Secrets do NOT get SetControllerReference
// so they survive FoundryGraph deletion.
//
// The operator's signing keys are provisioned once per namespace (SPEC R6 preamble) but
// may be rotated later (the operator regenerates its signing key Secret). When that
// happens the per-namespace public-key Secrets must be reconciled to the new operator
// key — otherwise the proxy signs with the new private key while the Cartographer
// verifies against a stale public key, failing closed on every request. So this
// reconciles (CreateOrUpdate) rather than create-only.
//
// The per-namespace public `key` values are base64-encoded for env-var transport:
// Kubernetes env vars from secretKeyRef cannot hold NUL bytes (POSIX execve truncates
// the value at the first NUL), and ~12% of random Ed25519 public keys contain a NUL
// byte, so a raw 32-byte key would be silently truncated and fail closed on every
// verification (CrashLoopBackOff at startup). The Cartographer's parseVerificationKey
// base64-decodes the env var (cartographer/cmd/main.go).
// The per-namespace sidecar `private-key` is base64-encoded for the same reason: the
// Node operator injects it into every Sidecar container via a secretKeyRef env var
// (SIDECAR_SIGNING_KEY) at node pod creation time (SPEC R5/R6), and a raw 64-byte
// private key hits the same NUL-truncation seam as the public key. The Sidecar
// base64-decodes it with a length check and fails fast on bad encoding
// (platform/sidecar/cmd/main.go).
// ponytail: pre-existing per-namespace Secrets created before this encoding hold raw
// bytes until the next reconcile re-encodes them (CreateOrUpdate converges the data
// field), but a pod already running keeps the old env value until it restarts, so a
// roll-out mid-migration can briefly carry stale raw bytes. The Cartographer reads
// only the `key` data key; the Sidecar reads only `private-key` — the two consumers
// are independent, so encoding one cannot affect the other. Upgrade path: consumers
// could read the Secrets as mounted files (byte-exact) instead of env vars.
func (r *FoundryGraphReconciler) reconcileSecrets(ctx context.Context, fg *flowv1.FoundryGraph) error {
	log := logf.FromContext(ctx)

	labels := r.labelsForCartographer(fg)

	// Sidecar signing key from the operator namespace provides both public and private.
	operatorSidecar := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sidecarSigningKeySecretName,
			Namespace: r.OperatorNamespace,
		},
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(operatorSidecar), operatorSidecar); err != nil {
		return fmt.Errorf("get sidecar signing key from operator namespace: %w", err)
	}
	sidecarSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sidecarKeySecretName,
			Namespace: fg.Namespace,
		},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, sidecarSecret, func() error {
		sidecarSecret.Labels = labels
		sidecarSecret.Data = map[string][]byte{
			// The per-namespace public `key` is base64-encoded so the
			// Cartographer can read it from a secretKeyRef env var: POSIX
			// execve truncates env values at the first NUL byte, and ~12% of
			// random Ed25519 public keys contain a NUL byte — a raw 32-byte
			// key would be silently truncated and every verification request
			// would fail closed (CrashLoopBackOff at startup).
			"key": []byte(base64.StdEncoding.EncodeToString(operatorSidecar.Data["key"])),
			// The per-namespace sidecar `private-key` is base64-encoded too:
			// the Node operator injects it into every Sidecar container via a
			// secretKeyRef env var (SIDECAR_SIGNING_KEY), and a raw 64-byte
			// private key hits the same NUL-truncation seam as the public key.
			// The Sidecar base64-decodes it with a length check (main.go).
			"private-key": []byte(base64.StdEncoding.EncodeToString(operatorSidecar.Data["private-key"])),
		}
		return nil
	}); err != nil {
		return fmt.Errorf("reconcile sidecar key Secret: %w", err)
	}

	// Operator signing key: the per-namespace Secret carries the operator's public key.
	operatorSigningSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      operatorSigningKeySecretName,
			Namespace: r.OperatorNamespace,
		},
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(operatorSigningSecret), operatorSigningSecret); err != nil {
		return fmt.Errorf("get operator signing key from operator namespace: %w", err)
	}
	operatorSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      operatorKeySecretName,
			Namespace: fg.Namespace,
		},
	}
	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, operatorSecret, func() error {
		operatorSecret.Labels = labels
		operatorSecret.Data = map[string][]byte{
			// Base64-encoded public key, matching the sidecar Secret above
			// (the Cartographer base64-decodes it from the env var — see
			// parseVerificationKey in cartographer/cmd/main.go).
			"key": []byte(base64.StdEncoding.EncodeToString(operatorSigningSecret.Data["key"])),
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconcile operator key Secret: %w", err)
	}
	if result == controllerutil.OperationResultCreated {
		log.Info("Created operator key Secret", "namespace", fg.Namespace)
	}
	return nil
}
