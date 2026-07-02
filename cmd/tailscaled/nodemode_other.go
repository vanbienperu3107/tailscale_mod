// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !windows

package main

import "os/exec"

// nodeHideChildWindow is a no-op on non-Windows platforms.
func nodeHideChildWindow(c *exec.Cmd) {}

// nodeEnsureElevated is a no-op off Windows. Userspace mode needs no privileges;
// TS_NODE_TUN=full requires running the exe under sudo (a real TUN needs root).
func nodeEnsureElevated() bool { return false }

// nodeKillConflicting is a no-op off Windows.
func nodeKillConflicting() {}
