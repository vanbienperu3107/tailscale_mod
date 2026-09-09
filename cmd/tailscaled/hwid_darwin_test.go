// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build darwin

package main

import "testing"

func TestParseIOPlatformSerial(t *testing.T) {
	out := `+-o MacBookAir10,1  <class IOPlatformExpertDevice, id 0x100000113, registered, matched, active, busy 0 (12 ms), retain 40>
    {
      "IOPlatformUUID" = "5E2C0000-0000-4000-8000-000000000000"
      "IOPlatformSerialNumber" = "C02XYZ123ABC"
      "model" = <"MacBookAir10,1">
    }
`
	got, err := parseIOPlatformSerial(out)
	if err != nil || got != "C02XYZ123ABC" {
		t.Fatalf("got %q, %v; want C02XYZ123ABC", got, err)
	}
	if _, err := parseIOPlatformSerial(`{ "model" = <"x"> }`); err == nil {
		t.Errorf("thiếu serial phải báo lỗi")
	}
}
