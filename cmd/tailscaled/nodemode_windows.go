// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// nodeHideChildWindow starts the child daemon without its own console window.
func nodeHideChildWindow(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}

// Elevation is handled at launch by the embedded requireAdministrator manifest
// (node.manifest, compiled over manifest_windows_amd64.syso in CI), so Windows
// shows one UAC prompt and runs this process — and its daemon child — elevated.
// No runtime self-relaunch is needed.

// nodeKillConflicting stops processes that would fight this launcher's daemon
// for the LocalAPI pipe and the SOCKS5 / peer-HTTP-proxy ports. Runs once at
// launcher startup so the newest instance cleanly takes over. Best-effort.
//
// Two classes are killed:
//  1. stock tailscaled.exe (owns the LocalAPI pipe);
//  2. OTHER instances of THIS node exe — leftover launchers and their daemon
//     children from earlier double-clicks. Those share our exe name and our
//     ports, so a second copy's daemon can never bind and crash-loops forever
//     ("daemon exited... restarting" every ~2s). Excluding our own PID makes
//     this "newest instance wins" instead of a mutual-kill loop.
func nodeKillConflicting() {
	_ = exec.Command("taskkill", "/IM", "tailscaled.exe", "/F").Run()

	exe, err := os.Executable()
	if err != nil {
		return
	}
	name := filepath.Base(exe)
	if name == "" || strings.EqualFold(name, "tailscaled.exe") {
		return // already handled above; avoid a redundant self-targeting kill
	}
	// /FI "PID ne <self>" spares this launcher; every other same-named process
	// (old launcher + its daemon) is terminated.
	_ = exec.Command("taskkill", "/F", "/IM", name,
		"/FI", fmt.Sprintf("PID ne %d", os.Getpid())).Run()
}
