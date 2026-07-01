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
