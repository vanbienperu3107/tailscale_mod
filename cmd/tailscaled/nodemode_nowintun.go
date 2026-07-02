// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build windows && !ts_node_wintun

package main

import "errors"

// nodeEnsureWintun errors on node builds without an embedded wintun.dll (i.e.
// everything but the "vpn" variant). Use the vpn build for TUN mode, or drop a
// wintun.dll next to the exe and run userspace elsewhere.
func nodeEnsureWintun(dir string) error {
	return errors.New("this node build has no embedded wintun.dll — use the 'vpn' variant for TUN mode, or run '<exe> userspace'")
}
