// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !windows

package main

import "os/exec"

// nodeHideChildWindow is a no-op on non-Windows platforms.
func nodeHideChildWindow(c *exec.Cmd) {}

// nodeKillConflicting is a no-op off Windows.
func nodeKillConflicting() {}

// nodeEnsureWintun is a no-op off Windows: TUN mode uses the kernel tun driver
// (Linux), so there is no wintun.dll to stage.
func nodeEnsureWintun(dir string) error { return nil }
