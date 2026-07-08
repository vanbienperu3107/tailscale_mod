// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

func init() {
	macAdapterExtraNames = windowsAdapterDescriptions
}

// windowsAdapterDescriptions returns each adapter's Description keyed by its
// FriendlyName (which is exactly what net.Interface.Name is on Windows — Go's
// net package fills Name from the same GetAdaptersAddresses FriendlyName
// field). primaryMAC tests the Description against macVirtualMarkers so a
// TAP/VPN adapter — e.g. OpenVPN's, whose *connection* name looks ordinary
// ("Ethernet 2") but whose Description is "TAP-Windows Adapter V9 for OpenVPN
// Connect" — can't masquerade as the real NIC and get picked as this box's
// stable identity MAC. This was the votam-pc case: it registered under the TAP
// adapter's MAC (00:ff:...) instead of its Wi-Fi NIC. Best-effort: returns nil
// on any API error, which falls back to connection-name-only detection.
func windowsAdapterDescriptions() map[string]string {
	ifs, err := winipcfg.GetAdaptersAddresses(windows.AF_UNSPEC, winipcfg.GAAFlagIncludeAllInterfaces)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(ifs))
	for _, ia := range ifs {
		out[ia.FriendlyName()] = ia.Description()
	}
	return out
}
