// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"os"
	"testing"
	"time"
)

// TestNodeIsDaemonProc verifies which invocations count as the daemon (where
// the dashboard pollers now run) vs the launcher / CLI (where they must NOT):
// only tailscaled-flag invocations (first arg starts with '-') are the daemon.
func TestNodeIsDaemonProc(t *testing.T) {
	tests := []struct {
		name string
		args []string // os.Args including argv[0]
		want bool
	}{
		{"daemon --statedir", []string{"exe", "--statedir=x", "--tun=userspace-networking"}, true},
		{"daemon --tun", []string{"exe", "--tun=tailscale0"}, true},
		{"launcher no args", []string{"exe"}, false},
		{"launcher verb vpn", []string{"exe", "vpn"}, false},
		{"launcher verb portable", []string{"exe", "portable"}, false},
		{"cli status", []string{"exe", "status"}, false},
		{"cli drive share", []string{"exe", "drive", "share", "tool", "E:\\Tool"}, false},
	}
	saved := os.Args
	defer func() { os.Args = saved }()
	for _, tt := range tests {
		os.Args = tt.args
		if got := nodeIsDaemonProc(); got != tt.want {
			t.Errorf("%s: nodeIsDaemonProc(%v) = %v, want %v", tt.name, tt.args, got, tt.want)
		}
	}
}

// TestNodeDaemonMetricsURL covers how the daemon picks its dashboard base URL.
// The daemon cannot read node.conf (maybeRunNode returns before
// runNodeLauncher does), so the launcher's TS_METRICS_REPORT is its only route
// to an operator override — without this, the runtime poll, folder browse and
// device-register loops keep using the baked URL while the reporters follow the
// override, and the two disagree.
func TestNodeDaemonMetricsURL(t *testing.T) {
	const baked = "https://baked.example/app"
	tests := []struct {
		name   string
		envVal string
		envSet bool
		want   string
	}{
		{
			name:   "launcher supplied an override: follow it, not the baked default",
			envVal: "https://dashboard.example/app",
			envSet: true,
			want:   "https://dashboard.example/app",
		},
		{
			// "metrics_url=" in node.conf means "turn the dashboard off". The
			// launcher passes that through as a set-but-blank env var, so blank
			// must NOT fall back to the baked URL — doing so would re-enable
			// reporting on a machine that deliberately disabled it.
			name:   "launcher supplied blank: disabled, do not resurrect baked URL",
			envVal: "",
			envSet: true,
			want:   "",
		},
		{
			// A daemon started by hand (or by an older launcher) has no such
			// env var. Treating that as "blank" would silently kill every
			// dashboard loop, so absence must keep the baked default.
			name:   "no env var at all: hand-started daemon keeps baked default",
			envVal: "",
			envSet: false,
			want:   baked,
		},
		{
			name:   "no env var and nothing baked in: stays empty",
			envVal: "",
			envSet: false,
			want:   "",
		},
	}
	for _, tt := range tests {
		bakedIn := baked
		if tt.want == "" && !tt.envSet {
			bakedIn = ""
		}
		if got := nodeDaemonMetricsURL(tt.envVal, tt.envSet, bakedIn); got != tt.want {
			t.Errorf("%s: nodeDaemonMetricsURL(%q, %v, %q) = %q, want %q",
				tt.name, tt.envVal, tt.envSet, bakedIn, got, tt.want)
		}
	}
}

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
