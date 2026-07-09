// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package key

import (
	"bytes"
	"testing"
)

func TestNewMachineFromSeed(t *testing.T) {
	// Determinism: the same seed must always produce the same key. This is the
	// whole point — it's what lets a device recover the same machine identity
	// after its state is wiped (which is equivalent to re-deriving from seed).
	// NOTE: avoid the literal "sk-" substring in fixtures (e.g. "DISK-SERIAL-…");
	// secret scanners flag it as a Stripe-style secret-key prefix.
	seed := []byte("v1|HWSERIAL-ABC123")
	a := NewMachineFromSeed(seed)
	b := NewMachineFromSeed(bytes.Clone(seed))
	if !a.Equal(b) {
		t.Fatal("same seed produced different keys (not deterministic)")
	}
	if a.Public() != b.Public() {
		t.Fatal("same seed produced different public keys")
	}

	// A seeded key must be a usable, non-zero, clamped Curve25519 private key.
	if a.IsZero() {
		t.Fatal("seeded MachinePrivate should not be zero")
	}
	if a.Public().IsZero() {
		t.Fatal("seeded MachinePublic should not be zero")
	}
	if a.k[0]&0b111 != 0 || a.k[31]&0b1100_0000 != 0b0100_0000 {
		t.Fatalf("seeded key is not clamped: k[0]=%#x k[31]=%#x", a.k[0], a.k[31])
	}

	// Different seeds must produce different keys.
	c := NewMachineFromSeed([]byte("v1|HWSERIAL-XYZ789"))
	if a.Equal(c) {
		t.Fatal("different seeds produced the same key")
	}
}
