// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"

	"tailscale.com/ipn/ipnstate"
)

// TestMetricsPingResultToSample validates path/OK/RTT mapping across every ping
// outcome: direct, DERP, peer-relay, ping-error, transport-error, nil, unknown.
func TestMetricsPingResultToSample(t *testing.T) {
	tests := []struct {
		name     string
		pr       *ipnstate.PingResult
		err      error
		wantOK   bool
		wantPath string
	}{
		{"direct", &ipnstate.PingResult{LatencySeconds: 0.012, Endpoint: "1.2.3.4:41641"}, nil, true, "direct"},
		{"derp", &ipnstate.PingResult{LatencySeconds: 0.107, DERPRegionID: 1001, DERPRegionCode: "vpn4-vn"}, nil, true, "derp:vpn4-vn"},
		{"peer-relay", &ipnstate.PingResult{LatencySeconds: 0.05, PeerRelay: "vpn2"}, nil, true, "peer-relay:vpn2"},
		{"unknown path", &ipnstate.PingResult{LatencySeconds: 0.01}, nil, true, "unknown"},
		{"ping error field", &ipnstate.PingResult{Err: "timeout"}, nil, false, ""},
		{"transport error", nil, errors.New("conn reset"), false, ""},
		{"nil result", nil, nil, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := pingResultToSample("Peer-Host", "100.64.0.9", tt.pr, tt.err)
			if s.OK != tt.wantOK {
				t.Fatalf("OK=%v want %v", s.OK, tt.wantOK)
			}
			if s.Dst != "peer-host" {
				t.Fatalf("Dst=%q want lowercased 'peer-host'", s.Dst)
			}
			if s.DstIP != "100.64.0.9" {
				t.Fatalf("DstIP=%q", s.DstIP)
			}
			if tt.wantOK {
				if s.Path != tt.wantPath {
					t.Fatalf("Path=%q want %q", s.Path, tt.wantPath)
				}
				if s.RTTms <= 0 {
					t.Fatalf("RTTms=%v want >0", s.RTTms)
				}
			}
		})
	}
}

func TestMetricsFirstV4(t *testing.T) {
	ips := []netip.Addr{netip.MustParseAddr("fd7a:115c:a1e0::9"), netip.MustParseAddr("100.64.0.9")}
	if got := firstV4(ips); got != "100.64.0.9" {
		t.Fatalf("firstV4=%q want 100.64.0.9", got)
	}
	if got := firstV4(nil); got != "" {
		t.Fatalf("firstV4(nil)=%q want empty", got)
	}
}

// TestNodeNetcheckReport validates the daemon reports exactly the ports it was
// actually launched with (parsed from its own os.Args/env), not a guess.
func TestNodeNetcheckReport(t *testing.T) {
	savedMode, savedArgs := nodeMode, os.Args
	savedRoutes := nodeLANRoutes
	t.Cleanup(func() { nodeMode, os.Args, nodeLANRoutes = savedMode, savedArgs, savedRoutes })

	t.Run("non-node build reports nothing", func(t *testing.T) {
		nodeMode = ""
		if got := nodeNetcheckReport(); got.Mode != "" {
			t.Fatalf("got Mode=%q, want empty for non-node build", got.Mode)
		}
	})

	t.Run("portable/userspace reports socks5 port only", func(t *testing.T) {
		nodeMode = "portable"
		os.Args = []string{"tailscaled", "--statedir=x", "--tun=userspace-networking", "--socks5-server=127.0.0.1:7654"}
		t.Setenv("TS_PEER_HTTP_PROXY", "")
		got := nodeNetcheckReport()
		if got.Mode != "portable" || got.PortSocks5 != 7654 || got.PortHTTP != 0 {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("proxy mode reports http proxy port + advertised routes", func(t *testing.T) {
		nodeMode = "proxy"
		nodeLANRoutes = "10.0.0.0/8"
		os.Args = []string{"tailscaled", "--tun=tailscale0"} // TUN: no --socks5-server
		t.Setenv("TS_PEER_HTTP_PROXY", "7655")
		got := nodeNetcheckReport()
		if got.Mode != "proxy" || got.PortSocks5 != 0 || got.PortHTTP != 7655 || got.AdvertisedRoutes != "10.0.0.0/8" {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestMetricsPostReport(t *testing.T) {
	var gotBody []byte
	var gotSecret, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSecret = r.Header.Get("X-Metrics-Secret")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rep := metricsReport{
		Hostname: "itop",
		IPv4:     "100.64.0.11",
		MAC:      "aa:bb:cc:dd:ee:ff",
		Samples: []metricSample{
			{Dst: "dev2", DstIP: "100.64.0.9", RTTms: 5, Path: "derp:vpn4-vn", OK: true},
		},
	}
	if err := postMetricsReport(srv.Client(), srv.URL, "sekret", rep); err != nil {
		t.Fatalf("post: %v", err)
	}
	if gotPath != "/api/metrics/report" {
		t.Fatalf("path=%q", gotPath)
	}
	if gotSecret != "sekret" {
		t.Fatalf("secret header=%q", gotSecret)
	}
	body := string(gotBody)
	if !strings.Contains(body, `"hostname":"itop"`) || !strings.Contains(body, `"path":"derp:vpn4-vn"`) {
		t.Fatalf("unexpected body: %s", body)
	}
}

// TestPickPrimaryMAC is the core stability guarantee: given the same physical
// NIC, the chosen MAC must not change when virtual adapters (notably the vpn
// variant's wintun TUN) appear or when net.Interfaces() ordering shuffles.
func TestPickPrimaryMAC(t *testing.T) {
	real := macCandidate{mac: "dc:4a:3e:3d:54:70", hasRealIP: true}   // physical Ethernet
	tunA := macCandidate{mac: "00:ff:a8:6a:d8:72", hasRealIP: false}  // wintun (tailnet IP → not "real")
	tunB := macCandidate{mac: "60:45:bd:49:ce:15", hasRealIP: false}  // another virtual, no real IP

	tests := []struct {
		name  string
		cands []macCandidate
		want  string
	}{
		{"empty", nil, ""},
		{"single real", []macCandidate{real}, "dc:4a:3e:3d:54:70"},
		{"real beats virtual regardless of order (proxy variant)", []macCandidate{tunB, real}, "dc:4a:3e:3d:54:70"},
		{"real beats virtual after TUN added (vpn variant)", []macCandidate{tunA, real, tunB}, "dc:4a:3e:3d:54:70"},
		{"real still wins when listed last", []macCandidate{tunA, tunB, real}, "dc:4a:3e:3d:54:70"},
		{"no real IP anywhere → lowest MAC, deterministic", []macCandidate{tunB, tunA}, "00:ff:a8:6a:d8:72"},
		{"no real IP, reversed order → same answer", []macCandidate{tunA, tunB}, "00:ff:a8:6a:d8:72"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickPrimaryMAC(tt.cands); got != tt.want {
				t.Fatalf("pickPrimaryMAC=%q want %q", got, tt.want)
			}
		})
	}

	// Explicit stability assertion: the with-TUN and without-TUN candidate sets
	// (same physical NIC) MUST yield the identical MAC — the regression that
	// caused folder-share owner churn and IP drift across variant switches.
	withoutTUN := pickPrimaryMAC([]macCandidate{real, tunB})
	withTUN := pickPrimaryMAC([]macCandidate{tunA, real, tunB})
	if withoutTUN != withTUN {
		t.Fatalf("MAC changed when TUN adapter present: %q vs %q", withoutTUN, withTUN)
	}
}

func TestMacNameIsVirtual(t *testing.T) {
	virtual := []string{
		"Tailscale", "tailscale0", "wintun", "WireGuard (wg0)", "tun0",
		"tap-windows", "vEthernet (WSL)", "docker0", "VMware Network Adapter",
		"VirtualBox Host-Only", "Hyper-V Virtual Ethernet", "Bluetooth Network",
		"Loopback Pseudo-Interface 1", "isatap.{GUID}", "Teredo Tunneling",
	}
	for _, n := range virtual {
		if !macNameIsVirtual(n) {
			t.Errorf("macNameIsVirtual(%q)=false, want true", n)
		}
	}
	for _, n := range []string{"Ethernet", "Ethernet 2", "Wi-Fi", "eth0", "en0", "Local Area Connection"} {
		if macNameIsVirtual(n) {
			t.Errorf("macNameIsVirtual(%q)=true, want false", n)
		}
	}
}

func TestMacIPv4IsReal(t *testing.T) {
	real := []string{"192.168.1.10", "10.0.0.5", "172.16.3.9", "8.8.8.8"}
	for _, s := range real {
		if !macIPv4IsReal(net.ParseIP(s)) {
			t.Errorf("macIPv4IsReal(%s)=false, want true", s)
		}
	}
	notReal := []string{"100.64.0.12", "100.100.100.100", "169.254.10.1"}
	for _, s := range notReal {
		if macIPv4IsReal(net.ParseIP(s)) {
			t.Errorf("macIPv4IsReal(%s)=true, want false", s)
		}
	}
	if macIPv4IsReal(net.ParseIP("fd7a:115c:a1e0::9")) {
		t.Error("macIPv4IsReal(IPv6)=true, want false")
	}
	if macIPv4IsReal(nil) {
		t.Error("macIPv4IsReal(nil)=true, want false")
	}
}
