// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build windows && ts_node_wintun

package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// wintunDLL is the Windows TUN driver, embedded into the "vpn" node build so the
// single exe can run a real VPN interface without shipping a separate DLL. The
// CI build copies wintun.dll next to this file before compiling with the
// ts_node_wintun tag (see build-windows-portable.yml).
//
//go:embed wintun.dll
var wintunDLL []byte

// nodeEnsureWintun writes the embedded wintun.dll next to the exe (where the
// daemon's DLL search finds it) so TUN mode works. Rewrites only if missing or
// a different size, so it is cheap on every launch.
func nodeEnsureWintun(dir string) error {
	dst := filepath.Join(dir, "wintun.dll")
	if fi, err := os.Stat(dst); err == nil && fi.Size() == int64(len(wintunDLL)) {
		return nil
	}
	if err := os.WriteFile(dst, wintunDLL, 0o644); err != nil {
		return fmt.Errorf("staging wintun.dll to %s: %w", dst, err)
	}
	return nil
}
