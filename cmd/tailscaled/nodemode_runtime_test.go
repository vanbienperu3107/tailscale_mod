// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"testing"
	"time"
)

// TestNodeShouldReapply validates the decision logic behind the runtime poll
// loop: reapply on an actual advertise_routes change, OR on a distinct
// reload_at (the CMS "Reload" button), but not on an unchanged poll result.
func TestNodeShouldReapply(t *testing.T) {
	tests := []struct {
		name         string
		resp         nodeRuntimeResponse
		lastRoutes   string
		lastReloadAt string
		want         bool
	}{
		{
			name:       "unchanged, no reload -> no reapply",
			resp:       nodeRuntimeResponse{AdvertiseRoutes: "10.0.0.0/8", ReloadAt: ""},
			lastRoutes: "10.0.0.0/8",
			want:       false,
		},
		{
			name:       "advertise_routes changed -> reapply",
			resp:       nodeRuntimeResponse{AdvertiseRoutes: "10.5.0.0/16", ReloadAt: ""},
			lastRoutes: "10.0.0.0/8",
			want:       true,
		},
		{
			name:         "new reload_at, same routes -> reapply (explicit force)",
			resp:         nodeRuntimeResponse{AdvertiseRoutes: "10.0.0.0/8", ReloadAt: "2026-07-03T10:00:00Z"},
			lastRoutes:   "10.0.0.0/8",
			lastReloadAt: "",
			want:         true,
		},
		{
			name:         "same reload_at as before -> no reapply",
			resp:         nodeRuntimeResponse{AdvertiseRoutes: "10.0.0.0/8", ReloadAt: "2026-07-03T10:00:00Z"},
			lastRoutes:   "10.0.0.0/8",
			lastReloadAt: "2026-07-03T10:00:00Z",
			want:         false,
		},
		{
			name:       "empty reload_at never treated as a reload signal",
			resp:       nodeRuntimeResponse{AdvertiseRoutes: "10.0.0.0/8", ReloadAt: ""},
			lastRoutes: "10.0.0.0/8",
			want:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := nodeShouldReapply(tc.resp, tc.lastRoutes, tc.lastReloadAt)
			if got != tc.want {
				t.Fatalf("nodeShouldReapply() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDaemonRestartDelay validates the supervisor backoff: doubles from base to
// max on repeated fast crashes, resets to base after a run that lasted at least
// max (a lone crash mustn't pin every future restart at the ceiling).
func TestDaemonRestartDelay(t *testing.T) {
	const base, max = 2 * time.Second, 30 * time.Second
	sec := func(n int64) time.Duration { return time.Duration(n) * time.Second }
	tests := []struct {
		name   string
		prev   int64 // seconds
		ranFor int64 // seconds
		want   int64 // seconds
	}{
		{"first crash (prev 0) -> base", 0, 1, 2},
		{"fast crash doubles 2->4", 2, 1, 4},
		{"doubles 4->8", 4, 1, 8},
		{"doubles 8->16", 8, 2, 16},
		{"16->30 clamped at max", 16, 2, 30},
		{"stays at max", 30, 2, 30},
		{"healthy run (== max) resets to base", 30, 30, 2},
		{"healthy run (> max) resets to base", 30, 120, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := daemonRestartDelay(sec(tc.prev), sec(tc.ranFor), base, max)
			if got != sec(tc.want) {
				t.Fatalf("daemonRestartDelay(prev=%ds, ranFor=%ds) = %v, want %ds",
					tc.prev, tc.ranFor, got, tc.want)
			}
		})
	}
}
