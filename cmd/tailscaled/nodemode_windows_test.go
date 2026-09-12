// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build windows

package main

import (
	"strings"
	"testing"

	"github.com/tailscale/wireguard-go/tun"
	_ "tailscale.com/net/tstun" // its init pins tun.WintunStaticRequestedGUID
)

// The stale-adapter cleanup must target exactly the devnode tstun creates;
// if the pinned GUID ever changes, removal would silently miss (or worse, hit
// another app's adapter).
func TestNodeStaleTunInstanceMatchesTstunGUID(t *testing.T) {
	g := tun.WintunStaticRequestedGUID
	if g == nil {
		t.Fatal("tstun did not pin a wintun GUID")
	}
	want := `SWD\Wintun\` + strings.ToUpper(g.String())
	if !strings.EqualFold(nodeStaleTunInstance, want) {
		t.Fatalf("nodeStaleTunInstance = %q, want %q", nodeStaleTunInstance, want)
	}
	if !strings.HasPrefix(nodeStaleTunInstance, `SWD\Wintun\{`) {
		t.Fatalf("instance %q is not a wintun software device", nodeStaleTunInstance)
	}
}
