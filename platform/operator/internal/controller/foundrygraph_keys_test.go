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
	"crypto/ed25519"
	"testing"
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
