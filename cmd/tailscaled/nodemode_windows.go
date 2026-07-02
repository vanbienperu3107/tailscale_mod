// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build windows

package main

import (
	"os/exec"
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
// (see node.manifest / the generated rsrc_windows_amd64.syso), so Windows shows
// one UAC prompt and runs this process — and its daemon child — elevated. No
// runtime self-relaunch is needed.

// nodeKillConflicting stops any previously-running tailscaled that still owns
// the LocalAPI pipe so this node's daemon can bind it. Best-effort. Our own
// daemon child is named after this exe (not tailscaled.exe), so it is not hit.
func nodeKillConflicting() {
	_ = exec.Command("taskkill", "/IM", "tailscaled.exe", "/F").Run()
}
