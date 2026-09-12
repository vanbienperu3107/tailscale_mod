// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build windows

package main

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

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

// Closing the job (what Windows does when the launcher dies) must kill the
// child — the fix for an orphaned daemon keeping VPN routes after "stop".
func TestNodeDaemonJobKillsChildOnClose(t *testing.T) {
	child := exec.Command("ping", "-n", "120", "127.0.0.1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Process.Kill() })

	job, err := nodeNewKillOnCloseJob()
	if err != nil {
		t.Fatalf("nodeNewKillOnCloseJob: %v", err)
	}
	if err := nodeAssignToJob(job, child.Process); err != nil {
		windows.CloseHandle(job)
		t.Fatalf("nodeAssignToJob: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case <-done:
		t.Fatal("child exited before the job was closed")
	case <-time.After(300 * time.Millisecond):
	}

	windows.CloseHandle(job)
	select {
	case <-done:
		// killed with the job, as intended
	case <-time.After(10 * time.Second):
		t.Fatal("child still running 10s after its job was closed")
	}
}
