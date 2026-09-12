// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import "testing"

func TestNodeDNSHintEntry(t *testing.T) {
	tests := []struct {
		host string
		ips  []string
		want string
	}{
		{"vpn2.hangocthanh.io.vn", []string{"45.119.87.220"}, "vpn2.hangocthanh.io.vn=45.119.87.220"},
		{"vpn2.hangocthanh.io.vn", []string{"45.119.87.220", "bogus", "2001:db8::1"}, "vpn2.hangocthanh.io.vn=45.119.87.220,2001:db8::1"},
		// Lookup failed, or nothing valid: no hint.
		{"vpn2.hangocthanh.io.vn", nil, ""},
		{"vpn2.hangocthanh.io.vn", []string{"x"}, ""},
		// An IP login server needs no hint; neither does an empty host.
		{"45.119.87.220", []string{"45.119.87.220"}, ""},
		{"", []string{"1.2.3.4"}, ""},
	}
	for _, tt := range tests {
		if got := nodeDNSHintEntry(tt.host, tt.ips); got != tt.want {
			t.Errorf("nodeDNSHintEntry(%q, %v) = %q, want %q", tt.host, tt.ips, got, tt.want)
		}
	}
}

func TestNodeDNSHintBadURL(t *testing.T) {
	for _, s := range []string{"", "::not a url", "https://"} {
		if got := nodeControlDNSHint(s); got != "" {
			t.Errorf("nodeControlDNSHint(%q) = %q, want empty", s, got)
		}
	}
}
