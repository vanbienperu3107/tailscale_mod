// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

// Built-in home-DERP reporter — every TS_HOMEDERP_REPORT_INTERVAL (default 3s),
// this node POSTs {mac, hostname, homeRegionId, homeRegionCode,
// controllerLatencyMs} to <base>/api/telemetry/home-derp so the dashboard can
// show, per client, which DERP it is currently homed on and how far the
// controller is from it. controllerLatencyMs is the round-trip time of the
// report POST itself — a simple, always-available proxy for controller
// reachability that needs no extra tailscale peer (the controller is reached
// over the same base URL used for reporting, not necessarily a tailscale peer).

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"tailscale.com/envknob"
	"tailscale.com/ipn/ipnlocal"
	"tailscale.com/types/logger"
)

var (
	homeDerpReportURL    = envknob.RegisterString("TS_HOMEDERP_REPORT")
	homeDerpReportSecret = envknob.RegisterString("TS_HOMEDERP_SECRET")
)

const (
	homeDerpReportInterval = 3 * time.Second
	homeDerpHTTPTimeout    = 5 * time.Second
)

func init() {
	hookStartHomeDerpReporter = startHomeDerpReporter
}

type homeDerpReport struct {
	MAC                 string  `json:"mac"`
	Hostname            string  `json:"hostname"`
	HomeRegionID        int     `json:"homeRegionId,omitempty"`
	HomeRegionCode      string  `json:"homeRegionCode,omitempty"`
	ControllerLatencyMs float64 `json:"controllerLatencyMs,omitempty"`
}

// startHomeDerpReporter starts the reporter goroutine. Falls back to
// TS_METRICS_REPORT/TS_METRICS_SECRET as the base URL/secret when the
// feature-specific env vars aren't set, so a single dashboard base URL is
// enough to enable all reporters.
func startHomeDerpReporter(logf logger.Logf, lb *ipnlocal.LocalBackend) {
	base := strings.TrimRight(strings.TrimSpace(homeDerpReportURL()), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(metricsReportURL()), "/")
	}
	if base == "" {
		return
	}
	secret := homeDerpReportSecret()
	if secret == "" {
		secret = metricsReportSecret()
	}
	logf("homederpreport: built-in home-DERP reporter enabled -> %s/api/telemetry/home-derp", base)
	go runHomeDerpReporter(logf, lb, base, secret)
}

func runHomeDerpReporter(logf logger.Logf, lb *ipnlocal.LocalBackend, base, secret string) {
	mac := primaryMAC()
	client := &http.Client{Timeout: homeDerpHTTPTimeout}
	t := time.NewTicker(homeDerpReportInterval)
	defer t.Stop()
	for {
		if rep, ok := collectHomeDerpReport(lb, mac); ok {
			start := time.Now()
			if err := postHomeDerpReport(client, base, secret, rep); err != nil {
				logf("[v1] homederpreport: post failed: %v", err)
			} else {
				rep.ControllerLatencyMs = float64(time.Since(start).Microseconds()) / 1000
				// Latency is only known AFTER the first POST returns, so send a
				// quick follow-up with it filled in rather than delay reporting.
				if err := postHomeDerpReport(client, base, secret, rep); err != nil {
					logf("[v1] homederpreport: latency post failed: %v", err)
				}
			}
		}
		<-t.C
	}
}

// collectHomeDerpReport reads the node's current home DERP off its own
// status/netmap. Returns ok=false when the node isn't ready yet.
func collectHomeDerpReport(lb *ipnlocal.LocalBackend, mac string) (homeDerpReport, bool) {
	st := lb.Status()
	if st == nil || st.Self == nil {
		return homeDerpReport{}, false
	}
	rep := homeDerpReport{
		MAC:      mac,
		Hostname: strings.ToLower(st.Self.HostName),
	}
	if code := strings.TrimSpace(st.Self.Relay); code != "" {
		rep.HomeRegionCode = code
		if nm := lb.NetMap(); nm != nil && nm.DERPMap != nil {
			for id, r := range nm.DERPMap.Regions {
				if r != nil && r.RegionCode == code {
					rep.HomeRegionID = id
					break
				}
			}
		}
	}
	return rep, true
}

func postHomeDerpReport(client *http.Client, base, secret string, rep homeDerpReport) error {
	body, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/telemetry/home-derp", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Headscale-Secret", secret)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
