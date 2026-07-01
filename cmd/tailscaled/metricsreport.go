// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

// Built-in latency reporter — the in-daemon replacement for metrics-report.ps1.
//
// When TS_METRICS_REPORT is set to a dashboard base URL, this node periodically
// disco-pings every peer, records RTT + path (direct / DERP / peer-relay) and
// POSTs {hostname, ipv4, mac, samples[]} to <base>/api/metrics/report. The
// dashboard's Latency page reads these. TS_METRICS_SECRET, if set, is sent as
// the X-Metrics-Secret header. Both env knobs unset -> the reporter is a no-op.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"tailscale.com/envknob"
	"tailscale.com/ipn/ipnlocal"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/logger"
)

var (
	metricsReportURL    = envknob.RegisterString("TS_METRICS_REPORT")
	metricsReportSecret = envknob.RegisterString("TS_METRICS_SECRET")
)

const (
	metricsReportInterval = 60 * time.Second
	metricsPingTimeout    = 3 * time.Second
	metricsHTTPTimeout    = 20 * time.Second
)

func init() {
	hookStartMetricsReporter = startMetricsReporter
}

type metricSample struct {
	Dst   string  `json:"dst"`
	DstIP string  `json:"dst_ip"`
	RTTms float64 `json:"rtt_ms"`
	Path  string  `json:"path"`
	OK    bool    `json:"ok"`
}

type metricsReport struct {
	Hostname string         `json:"hostname"`
	IPv4     string         `json:"ipv4"`
	MAC      string         `json:"mac"`
	Samples  []metricSample `json:"samples"`
}

func startMetricsReporter(logf logger.Logf, lb *ipnlocal.LocalBackend) {
	base := strings.TrimRight(strings.TrimSpace(metricsReportURL()), "/")
	if base == "" {
		return
	}
	secret := metricsReportSecret()
	logf("metricsreport: built-in latency reporter enabled -> %s/api/metrics/report", base)
	go runMetricsReporter(logf, lb, base, secret)
}

func runMetricsReporter(logf logger.Logf, lb *ipnlocal.LocalBackend, base, secret string) {
	mac := primaryMAC()
	client := &http.Client{Timeout: metricsHTTPTimeout}
	t := time.NewTicker(metricsReportInterval)
	defer t.Stop()
	for {
		if rep, ok := collectMetricsReport(lb, mac); ok {
			if err := postMetricsReport(client, base, secret, rep); err != nil {
				logf("[v1] metricsreport: post failed: %v", err)
			}
		}
		<-t.C
	}
}

// collectMetricsReport builds one report by disco-pinging every peer. Returns
// ok=false when the node isn't ready yet (no Self in status).
func collectMetricsReport(lb *ipnlocal.LocalBackend, mac string) (metricsReport, bool) {
	st := lb.Status()
	if st == nil || st.Self == nil {
		return metricsReport{}, false
	}
	rep := metricsReport{
		Hostname: strings.ToLower(st.Self.HostName),
		IPv4:     firstV4(st.Self.TailscaleIPs),
		MAC:      mac,
	}
	for _, p := range st.Peer {
		if p == nil {
			continue
		}
		ipStr := firstV4(p.TailscaleIPs)
		if ipStr == "" {
			continue
		}
		ip, err := netip.ParseAddr(ipStr)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), metricsPingTimeout)
		pr, perr := lb.Ping(ctx, ip, tailcfg.PingDisco, 0)
		cancel()
		rep.Samples = append(rep.Samples, pingResultToSample(p.HostName, ipStr, pr, perr))
	}
	return rep, true
}

// pingResultToSample converts a single ping outcome into a report sample. Pure;
// unit-tested across direct / DERP / peer-relay / error cases. Path uses the same
// direct/DERP/peer-relay determination as the `tailscale ping` CLI.
func pingResultToSample(hostName, dstIP string, pr *ipnstate.PingResult, err error) metricSample {
	s := metricSample{
		Dst:   strings.ToLower(strings.TrimSpace(hostName)),
		DstIP: dstIP,
	}
	if err != nil || pr == nil || pr.Err != "" {
		return s // OK stays false
	}
	s.OK = true
	s.RTTms = math.Round(pr.LatencySeconds*1000*100) / 100
	switch {
	case pr.PeerRelay != "":
		s.Path = "peer-relay:" + pr.PeerRelay
	case pr.DERPRegionID != 0:
		s.Path = "derp:" + pr.DERPRegionCode
	case pr.Endpoint != "":
		s.Path = "direct"
	default:
		s.Path = "unknown"
	}
	return s
}

func postMetricsReport(client *http.Client, base, secret string, rep metricsReport) error {
	body, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/metrics/report", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Metrics-Secret", secret)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// firstV4 returns the first IPv4 address as a string, or "".
func firstV4(ips []netip.Addr) string {
	for _, ip := range ips {
		if ip.Is4() {
			return ip.String()
		}
	}
	return ""
}

// primaryMAC returns a best-guess primary interface MAC: the first up,
// non-loopback, non-Tailscale interface that has a hardware address.
func primaryMAC() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(ifc.HardwareAddr) == 0 {
			continue
		}
		name := strings.ToLower(ifc.Name)
		if strings.Contains(name, "tailscale") || strings.Contains(name, "wg") {
			continue
		}
		return ifc.HardwareAddr.String()
	}
	return ""
}
