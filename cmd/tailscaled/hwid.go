// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import "strings"

// Deterministic machine identity.
//
// A node build normally generates a random machine key on first start and
// stores it under <exe>/state. If that state is wiped, the exe is moved, or the
// machine is reinstalled, a NEW random key is generated → headscale treats it as
// a brand-new node → the node loses its pinned tailnet IP (the observed
// itop .19→.21, votam .20→.22 drift). To stop that, the launcher seeds the
// daemon's machine key from a STABLE hardware value (the OS disk serial): same
// machine ⇒ same seed ⇒ same machine key ⇒ headscale keeps the same node/IP.
//
// The pieces:
//   - machineHardwareID() (platform-specific) reads the raw serial.
//   - normalizeSerial + machineKeySeed (here, cross-platform + tested) turn it
//     into the TS_MACHINE_KEY_SEED value the launcher passes to the daemon.
//   - the daemon (ipnlocal.initMachineKeyLocked) derives the key from that env
//     via key.NewMachineFromSeed, but only when it has no stored key yet.

// normalizeSerial canonicalizes a raw hardware serial so it is a STABLE anchor
// for the deterministic machine key. It trims surrounding whitespace, collapses
// any run of internal whitespace to a single space, and upper-cases the result.
//
// Normalization must be idempotent and identical across reads: the serial is
// hashed into the machine key, so a one-character difference (a stray space,
// different letter case) yields a completely different key — i.e. a brand-new
// node identity and a lost IP. Returns "" for an empty/whitespace-only serial.
func normalizeSerial(raw string) string {
	// strings.Fields splits on any whitespace and drops empties, so Join with a
	// single space both trims the ends and collapses internal runs.
	return strings.ToUpper(strings.Join(strings.Fields(raw), " "))
}

// machineKeySeed returns the value to place in the TS_MACHINE_KEY_SEED
// environment variable for a given (already-read) hardware serial, or "" if the
// serial is empty/unusable (caller should then fall back to a random key).
//
// The "v1|" prefix version-stamps the derivation scheme: if the seeding logic
// ever needs to change in a way that must NOT collide with keys already derived
// under the current scheme, bump it to "v2|…". The daemon hashes this whole
// string (prefix included) — see key.NewMachineFromSeed.
func machineKeySeed(serial string) string {
	s := normalizeSerial(serial)
	if s == "" {
		return ""
	}
	return "v1|" + s
}
