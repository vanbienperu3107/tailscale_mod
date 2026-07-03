// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import "testing"

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
