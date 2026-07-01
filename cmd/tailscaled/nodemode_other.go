// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !windows

package main

import "os/exec"

// nodeHideChildWindow is a no-op on non-Windows platforms.
func nodeHideChildWindow(c *exec.Cmd) {}
