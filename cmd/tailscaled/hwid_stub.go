// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !windows

package main

// machineHardwareID has no portable hardware-serial source; the deterministic
// machine-identity feature currently targets the Windows node build (the only
// platform these nodes run on). On other platforms it returns "" so the
// launcher simply falls back to the upstream random machine key.
func machineHardwareID() (string, error) { return "", nil }
