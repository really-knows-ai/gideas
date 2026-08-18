package main

// loadVerificationKey / parseVerificationKey tests

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

func TestParseVerificationKeyMissingEnv(t *testing.T) {
	t.Setenv("OPERATOR_VERIFICATION_KEY", "")
	got, err := parseVerificationKey("OPERATOR_VERIFICATION_KEY")
	if err == nil {
		t.Fatal("expected error for missing verification key env, got nil")
	}
	if got != nil {
		t.Fatalf("expected nil key on error, got %v", got)
	}
}

func TestParseVerificationKeyInvalidLength(t *testing.T) {
	// Valid base64 that decodes to the wrong length: the value is
	// well-formed base64 of a 5-byte key, so the decode succeeds and the
	// length check must reject it (a raw "too-short" string would now fail the
	// base64 decode first, exercising the wrong branch).
	t.Setenv("OPERATOR_VERIFICATION_KEY", base64.StdEncoding.EncodeToString([]byte("short")))
	got, err := parseVerificationKey("OPERATOR_VERIFICATION_KEY")
	if err == nil {
		t.Fatal("expected error for malformed verification key, got nil")
	}
	if got != nil {
		t.Fatalf("expected nil key on malformed input, got %v", got)
	}
}

func TestParseVerificationKeyValid(t *testing.T) {
	// The operator provisions the public key base64-encoded in the Secret's
	// `key` field (reconcileSecrets, operator foundrygraph_keys.go), so the env
	// var holds the base64 of the raw 32-byte key.
	key := bytes.Repeat([]byte{'a'}, ed25519.PublicKeySize)
	t.Setenv("OPERATOR_VERIFICATION_KEY", base64.StdEncoding.EncodeToString(key))
	got, err := parseVerificationKey("OPERATOR_VERIFICATION_KEY")
	if err != nil {
		t.Fatalf("parseVerificationKey: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil key, got nil")
	}
	if len(got) != ed25519.PublicKeySize {
		t.Fatalf("key length = %d, want %d", len(got), ed25519.PublicKeySize)
	}
	if !bytes.Equal(got, ed25519.PublicKey(key)) {
		t.Fatal("parsed key does not match the raw key bytes")
	}
}

// TestParseVerificationKeyNULByte pins the fix for the verification-key NUL
// truncation defect: ~12% of random Ed25519 public keys contain a NUL byte,
// which POSIX env vars cannot hold (execve truncates the value at the first
// NUL), so a raw key would be silently truncated and every verification would
// fail closed (CrashLoopBackOff). The operator therefore base64-encodes the key
// into the Secret, and the base64 of a key containing a NUL byte must decode
// back to the full 32 bytes.
func TestParseVerificationKeyNULByte(t *testing.T) {
	key := make([]byte, ed25519.PublicKeySize)
	key[0] = 0x00 // NUL as the first byte — the value execve would truncate to empty
	key[15] = 0x42
	t.Setenv("OPERATOR_VERIFICATION_KEY", base64.StdEncoding.EncodeToString(key))
	got, err := parseVerificationKey("OPERATOR_VERIFICATION_KEY")
	if err != nil {
		t.Fatalf("parseVerificationKey on base64 of NUL-bearing key: %v", err)
	}
	if len(got) != ed25519.PublicKeySize {
		t.Fatalf("key length = %d, want %d (NUL byte must not truncate)", len(got), ed25519.PublicKeySize)
	}
	if !bytes.Equal(got, ed25519.PublicKey(key)) {
		t.Fatal("NUL-bearing key does not round-trip through base64 decode")
	}
}

// TestParseVerificationKeyInvalidEncoding verifies that a verification-key env
// value that is not valid base64 fails fast (SPEC R5 fail-closed guard): the
// operator now provisions base64-encoded keys, so an un-decodable value is a
// deployment misconfiguration and must not be silently accepted or mangled.
func TestParseVerificationKeyInvalidEncoding(t *testing.T) {
	t.Setenv("OPERATOR_VERIFICATION_KEY", "this is not base64!!!")
	got, err := parseVerificationKey("OPERATOR_VERIFICATION_KEY")
	if err == nil {
		t.Fatal("expected error for non-base64 verification key env, got nil")
	}
	if got != nil {
		t.Fatalf("expected nil key on malformed encoding, got %v", got)
	}
}
