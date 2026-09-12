// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"context"
	"net"
	"net/url"
	"strings"
	"time"
)

// nodeControlDNSHint resolves the control server's hostname with the OS
// resolver (the same one the launcher's own enrollment call uses) and returns
// a TS_DNSFALLBACK_STATIC entry for the daemon, or "" if it can't.
//
// Why: a freshly started daemon has no last-good IP for control. When the OS
// resolver briefly fails for it (seen on VOTAM-PC's MiFi link), tailscale
// falls back to bootstrap-DNS on derp*.tailscale.com, unreachable from Peru —
// every candidate times out and the join stalled ~2.5 minutes (26 failed
// lookups) although the launcher had just resolved the same name fine.
// Resolved fresh on every launcher start, so a server IP change self-heals.
func nodeControlDNSHint(loginServer string) string {
	u, err := url.Parse(loginServer)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	host := u.Hostname()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, _ := net.DefaultResolver.LookupHost(ctx, host)
	return nodeDNSHintEntry(host, ips)
}

// nodeDNSHintEntry formats "host=ip1,ip2", keeping only valid IP literals;
// "" when host is itself an IP or nothing usable remains.
func nodeDNSHintEntry(host string, ips []string) string {
	if host == "" || net.ParseIP(host) != nil {
		return ""
	}
	var ok []string
	for _, ip := range ips {
		if net.ParseIP(ip) != nil {
			ok = append(ok, ip)
		}
	}
	if len(ok) == 0 {
		return ""
	}
	return host + "=" + strings.Join(ok, ",")
}
