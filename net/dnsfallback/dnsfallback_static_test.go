// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package dnsfallback

import (
	"context"
	"net/netip"
	"reflect"
	"testing"
)

func TestParseStaticHints(t *testing.T) {
	got := parseStaticHints(" VPN2.hangocthanh.io.vn. = 45.119.87.220, ::ffff:1.2.3.4 ;bad;=1.1.1.1;x.example=notanip,9.9.9.9;")
	want := map[string][]netip.Addr{
		"vpn2.hangocthanh.io.vn": {netip.MustParseAddr("45.119.87.220"), netip.MustParseAddr("1.2.3.4")},
		"x.example":              {netip.MustParseAddr("9.9.9.9")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseStaticHints = %v, want %v", got, want)
	}
	if len(parseStaticHints("")) != 0 {
		t.Fatal("empty input must yield no hints")
	}
}

// With a hint set, lookup must answer from it without touching bootstrap DNS
// (which is what stalled joins when derp*.tailscale.com was unreachable).
func TestLookupUsesStaticHint(t *testing.T) {
	t.Setenv(staticHintsEnv, "vpn2.hangocthanh.io.vn=45.119.87.220")
	ips, err := lookup(context.Background(), "VPN2.hangocthanh.io.vn", t.Logf, nil, nil)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if want := []netip.Addr{netip.MustParseAddr("45.119.87.220")}; !reflect.DeepEqual(ips, want) {
		t.Fatalf("lookup = %v, want %v", ips, want)
	}
	if got := staticHint("other.example"); got != nil {
		t.Fatalf("unrelated host got hint %v", got)
	}
}
