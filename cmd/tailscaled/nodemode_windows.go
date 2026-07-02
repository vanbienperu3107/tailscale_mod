// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"tailscale.com/util/winutil"
)

// nodeHideChildWindow starts the child daemon without its own console window.
func nodeHideChildWindow(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}

// swShowNormal == SW_SHOWNORMAL (show the window normally).
const swShowNormal int32 = 1

// nodeEnsureElevated re-launches this launcher as Administrator if it is not
// already elevated. The userspace daemon's LocalAPI named pipe lives under
// \\.\pipe\ProtectedPrefix\Administrators\... and can only be created by an
// elevated process; without this the daemon fails with "Access is denied" and
// the node exits immediately. Returns true if it re-launched (caller must exit).
func nodeEnsureElevated() bool {
	if winutil.IsCurrentProcessElevated() {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		log.Printf("node: elevate: cannot find own path: %v", err)
		return false
	}
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	var argsPtr *uint16
	if len(os.Args) > 1 {
		argsPtr, _ = syscall.UTF16PtrFromString(strings.Join(os.Args[1:], " "))
	}
	if err := windows.ShellExecute(0, verb, file, argsPtr, nil, swShowNormal); err != nil {
		log.Printf("node: elevation declined/failed (%v); the daemon needs Administrator to run.", err)
		return false
	}
	return true
}

// nodeKillConflicting stops any previously-running tailscaled that still owns
// the LocalAPI pipe so this node's daemon can bind it. Best-effort. Our own
// daemon child is named after this exe (not tailscaled.exe), so it is not hit.
func nodeKillConflicting() {
	_ = exec.Command("taskkill", "/IM", "tailscaled.exe", "/F").Run()
}
