// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

// Built-in DERP-ping reporter — every TS_DERPPING_REPORT_INTERVAL (default
// 30s), this node pings every DERP region in its current DERPMap (fresh,
// standalone derphttp connections — independent of magicsock's own active
// DERP connections) and POSTs the RTT/ok per region to
// <base>/api/telemetry/derp-ping.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"tailscale.com/derp/derphttp"
	"tailscale.com/envknob"
	"tailscale.com/ipn/ipnlocal"
	"tailscale.com/net/netmon"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

var (
	derpPingReportURL    = envknob.RegisterString("TS_DERPPING_REPORT")
	derpPingReportSecret = envknob.RegisterString("TS_DERPPING_SECRET")
)

const (
	derpPingReportInterval = 30 * time.Second
	derpPingTimeout        = 5 * time.Second
	derpPingHTTPTimeout    = 10 * time.Second
)

func init() {
	hookStartDerpPingReporter = startDerpPingReporter
}

type derpPingSample struct {
	RegionID   int     `json:"regionId"`
	RegionCode string  `json:"regionCode,omitempty"`
	RTTMs      float64 `json:"rttMs,omitempty"`
	OK         bool    `json:"ok"`
}

type derpPingReport struct {
	Client  string           `json:"client"` // mac
	Samples []derpPingSample `json:"samples"`
}

// startDerpPingReporter mirrors startHomeDerpReporter's fallback: reuse
// TS_METRICS_REPORT/TS_METRICS_SECRET as the base URL/secret when the
// feature-specific env vars aren't set.
func startDerpPingReporter(logf logger.Logf, lb *ipnlocal.LocalBackend) {
	base := strings.TrimRight(strings.TrimSpace(derpPingReportURL()), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(metricsReportURL()), "/")
	}
	if base == "" {
		return
	}
	secret := derpPingReportSecret()
	if secret == "" {
		secret = metricsReportSecret()
	}
	logf("derppingreport: built-in DERP-ping reporter enabled -> %s/api/telemetry/derp-ping", base)
	go runDerpPingReporter(logf, lb, base, secret)
}

func runDerpPingReporter(logf logger.Logf, lb *ipnlocal.LocalBackend, base, secret string) {
	mac := primaryMAC()
	netMon := netmon.NewStatic()
	client := &http.Client{Timeout: derpPingHTTPTimeout}
	t := time.NewTicker(derpPingReportInterval)
	defer t.Stop()
	for {
		nm := lb.NetMap()
		if nm != nil && nm.DERPMap != nil {
			rep := derpPingReport{Client: mac}
			rep.Samples = pingAllRegions(logf, netMon, nm.DERPMap)
			if err := postDerpPingReport(client, base, secret, rep); err != nil {
				logf("[v1] derppingreport: post failed: %v", err)
			}
		}
		<-t.C
	}
}

// pingAllRegions pings every DERP region concurrently using fresh, ephemeral
// derphttp clients (no shared state with magicsock's own DERP connections).
func pingAllRegions(logf logger.Logf, netMon *netmon.Monitor, dm *tailcfg.DERPMap) []derpPingSample {
	var (
		mu      sync.Mutex
		samples []derpPingSample
		wg      sync.WaitGroup
	)
	for id, region := range dm.Regions {
		if region == nil {
			continue
		}
		wg.Add(1)
		go func(id int, region *tailcfg.DERPRegion) {
			defer wg.Done()
			s := pingRegionOnce(logf, netMon, id, region)
			mu.Lock()
			samples = append(samples, s)
			mu.Unlock()
		}(id, region)
	}
	wg.Wait()
	return samples
}

func pingRegionOnce(logf logger.Logf, netMon *netmon.Monitor, regionID int, region *tailcfg.DERPRegion) derpPingSample {
	s := derpPingSample{RegionID: regionID, RegionCode: region.RegionCode}
	priv := key.NewNode()
	dc := derphttp.NewRegionClient(priv, logf, netMon, func() *tailcfg.DERPRegion {
		return region
	})
	defer dc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), derpPingTimeout)
	defer cancel()

	if err := dc.Connect(ctx); err != nil {
		return s // OK stays false
	}

	// Ping() only resolves the pong if something is concurrently calling
	// Recv/RecvDetail (see derphttp_client.go docs on Client.Ping) — run that
	// loop in the background for the lifetime of this ping, stopped by
	// dc.Close() (deferred above) once we return.
	go func() {
		for {
			if _, _, err := dc.RecvDetail(); err != nil {
				return
			}
		}
	}()

	start := time.Now()
	if err := dc.Ping(ctx); err != nil {
		return s
	}
	s.OK = true
	s.RTTMs = float64(time.Since(start).Microseconds()) / 1000
	return s
}

func postDerpPingReport(client *http.Client, base, secret string, rep derpPingReport) error {
	body, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/telemetry/derp-ping", bytes.NewReader(body))
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
