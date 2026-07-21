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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	flowv1 "github.com/foundry/flow/operator/api/v1"
)

const (
	operatorSigningKeySecretName = "cartographer-operator-signing-key" // operator's own namespace
	sidecarSigningKeySecretName  = "cartographer-sidecar-signing-key" // operator's own namespace
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

// reconcileSecrets creates the namespace-scoped key Secrets if absent.
// These Secrets do NOT get SetControllerReference so they survive FoundryGraph deletion.
func (r *FoundryGraphReconciler) reconcileSecrets(ctx context.Context, fg *flowv1.FoundryGraph) error {
	log := logf.FromContext(ctx)

	// cartographer-sidecar-key: contains public+private key from the shared sidecar signing key
	sidecarSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sidecarKeySecretName,
			Namespace: fg.Namespace,
		},
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sidecarSecret), sidecarSecret); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get sidecar key Secret: %w", err)
		}
		// Secret does not exist — read from operator namespace and create.
		operatorSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      sidecarSigningKeySecretName,
				Namespace: r.OperatorNamespace,
			},
		}
		if err := r.Get(ctx, client.ObjectKeyFromObject(operatorSecret), operatorSecret); err != nil {
			return fmt.Errorf("get sidecar signing key from operator namespace: %w", err)
		}

		sidecarSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      sidecarKeySecretName,
				Namespace: fg.Namespace,
				Labels:    r.labelsForCartographer(fg),
			},
			Data: map[string][]byte{
				"key":         operatorSecret.Data["key"],
				"private-key": operatorSecret.Data["private-key"],
			},
		}
		if err := r.Create(ctx, sidecarSecret); err != nil {
			return fmt.Errorf("create sidecar key Secret: %w", err)
		}
		log.Info("Created sidecar key Secret", "namespace", fg.Namespace)
	}

	// cartographer-operator-key: contains operator's public key
	operatorSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      operatorKeySecretName,
			Namespace: fg.Namespace,
		},
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(operatorSecret), operatorSecret); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get operator key Secret: %w", err)
		}
		// Secret does not exist — read from operator namespace and create.
		operatorSigningSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      operatorSigningKeySecretName,
				Namespace: r.OperatorNamespace,
			},
		}
		if err := r.Get(ctx, client.ObjectKeyFromObject(operatorSigningSecret), operatorSigningSecret); err != nil {
			return fmt.Errorf("get operator signing key from operator namespace: %w", err)
		}

		operatorSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      operatorKeySecretName,
				Namespace: fg.Namespace,
				Labels:    r.labelsForCartographer(fg),
			},
			Data: map[string][]byte{
				"key": operatorSigningSecret.Data["key"],
			},
		}
		if err := r.Create(ctx, operatorSecret); err != nil {
			return fmt.Errorf("create operator key Secret: %w", err)
		}
		log.Info("Created operator key Secret", "namespace", fg.Namespace)
	}

	return nil
}

// readOperatorSigningKey reads the operator's signing key from the operator namespace.
// Used by tests to get the key for proxy server setup.
func readOperatorSigningKey(ctx context.Context, c client.Client, operatorNamespace string) ([]byte, error) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      operatorSigningKeySecretName,
			Namespace: operatorNamespace,
		},
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(secret), secret); err != nil {
		return nil, fmt.Errorf("read operator signing key: %w", err)
	}
	privKey := secret.Data["private-key"]
	if len(privKey) == 0 {
		return nil, fmt.Errorf("operator signing key Secret %q has empty private-key", operatorSigningKeySecretName)
	}
	return privKey, nil
}


