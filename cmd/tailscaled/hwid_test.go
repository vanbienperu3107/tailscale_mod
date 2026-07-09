// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import "testing"

func TestNormalizeSerial(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"trim+upper", "  wd-wcc4e5pz ", "WD-WCC4E5PZ"},
		{"already-clean", "WD-WCC4E5PZ", "WD-WCC4E5PZ"},
		{"collapse-internal-spaces", "s n  1234", "S N 1234"},
		{"tabs-and-newlines", "\tabc\r\n123\t", "ABC 123"},
		{"nvme-underscores-kept", "nvme_serial_0001", "NVME_SERIAL_0001"},
		{"empty", "", ""},
		{"whitespace-only", "   \t ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeSerial(c.in); got != c.want {
				t.Fatalf("normalizeSerial(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestMachineKeySeed(t *testing.T) {
	if got, want := machineKeySeed("abc123"), "v1|ABC123"; got != want {
		t.Fatalf("machineKeySeed(%q) = %q, want %q", "abc123", got, want)
	}

	// Empty/whitespace serial → no seed (caller falls back to a random key).
	for _, in := range []string{"", "   ", "\t\n"} {
		if got := machineKeySeed(in); got != "" {
			t.Fatalf("machineKeySeed(%q) = %q, want \"\" (fall back to random key)", in, got)
		}
	}

	// Stability: cosmetic variants of the same serial must produce the SAME
	// seed, or the derived machine key (and pinned IP) would drift between reads.
	for _, v := range []string{"ABC123", "  abc123  ", "abc  123", "AbC123"} {
		want := "v1|ABC123"
		if v == "abc  123" {
			want = "v1|ABC 123" // internal space is significant, not cosmetic
		}
		if got := machineKeySeed(v); got != want {
			t.Fatalf("machineKeySeed(%q) = %q, want %q", v, got, want)
		}
	}
}
