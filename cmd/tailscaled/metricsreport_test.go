// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
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
