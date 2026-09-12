// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package dnsfallback

import (
	"net/netip"
	"os"
	"strings"
)

// staticHintsEnv names an environment variable of "host=ip[,ip...]" entries
// separated by ';'. The node launcher sets it to the control server's address
// it just resolved (via the OS resolver, which worked for its own enrollment
// call), so a freshly started daemon can still reach control when the OS
// resolver briefly fails for it. Without this the fallback only tries
// bootstrap-DNS on derp*.tailscale.com, which the Peru networks cannot reach:
// each candidate times out and a join took ~2.5 minutes (26 failed lookups).
const staticHintsEnv = "TS_DNSFALLBACK_STATIC"

// parseStaticHints parses the staticHintsEnv format. Malformed entries and
// addresses are skipped; hostnames are matched case-insensitively.
func parseStaticHints(s string) map[string][]netip.Addr {
	m := map[string][]netip.Addr{}
	for _, ent := range strings.Split(s, ";") {
		host, ips, ok := strings.Cut(strings.TrimSpace(ent), "=")
		host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
		if !ok || host == "" {
			continue
		}
		for _, v := range strings.Split(ips, ",") {
			if ip, err := netip.ParseAddr(strings.TrimSpace(v)); err == nil {
				m[host] = append(m[host], ip.Unmap())
			}
		}
	}
	return m
}

// staticHint returns the launcher-provided addresses for host, if any.
func staticHint(host string) []netip.Addr {
	v := os.Getenv(staticHintsEnv)
	if v == "" {
		return nil
	}
	return parseStaticHints(v)[strings.ToLower(strings.TrimSuffix(host, "."))]
}
