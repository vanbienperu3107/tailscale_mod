// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

// Built-in DERP-ping reporter — every TS_DERPPING_REPORT_INTERVAL (default
// 30s), this node pings every DERP region (fresh, standalone derphttp
// connections — independent of magicsock's own active DERP connections) and
// POSTs the RTT/ok per region to <base>/api/telemetry/derp-ping.
//
// Region set = union of this node's own DERPMap AND the full base DERPMap
// fetched from <base>/derpmap.json. The union is what lets a node ping regions
// that are NOT in its own map — e.g. a node locked (exclusive) to a single
// region still has only that one region in its NetMap, but we want full
// monitoring coverage across every region. If the base map can't be fetched we
// fall back to the node's own map (no regression).

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
		var nodeDM *tailcfg.DERPMap
		if nm := lb.NetMap(); nm != nil {
			nodeDM = nm.DERPMap
		}
		dm := mergedPingDERPMap(logf, client, base, secret, nodeDM)
		if dm != nil && len(dm.Regions) > 0 {
			rep := derpPingReport{Client: mac}
			rep.Samples = pingAllRegions(logf, netMon, dm)
			if err := postDerpPingReport(client, base, secret, rep); err != nil {
				logf("[v1] derppingreport: post failed: %v", err)
			}
		}
		<-t.C
	}
}

// mergedPingDERPMap returns the union of the node's own DERPMap regions and the
// full base DERPMap (<base>/derpmap.json). Base-map entries win on regionID
// collision (they carry complete node info). Returns nil if neither source
// yields any region.
func mergedPingDERPMap(logf logger.Logf, client *http.Client, base, secret string, nodeDM *tailcfg.DERPMap) *tailcfg.DERPMap {
	out := &tailcfg.DERPMap{Regions: map[int]*tailcfg.DERPRegion{}}
	if nodeDM != nil {
		for id, r := range nodeDM.Regions {
			out.Regions[id] = r
		}
	}
	if full, err := fetchFullDERPMap(client, base, secret); err != nil {
		logf("[v1] derppingreport: fetch base derpmap failed, using node map only: %v", err)
	} else if full != nil {
		for id, r := range full.Regions {
			out.Regions[id] = r
		}
	}
	if len(out.Regions) == 0 {
		return nil
	}
	return out
}

// fetchFullDERPMap GETs <base>/derpmap.json (the dashboard's full base map) and
// decodes it into a tailcfg.DERPMap. The endpoint is public; the secret header
// is sent when configured (harmless if unused).
func fetchFullDERPMap(client *http.Client, base, secret string) (*tailcfg.DERPMap, error) {
	req, err := http.NewRequest(http.MethodGet, base+"/derpmap.json", nil)
	if err != nil {
		return nil, err
	}
	if secret != "" {
		req.Header.Set("X-Headscale-Secret", secret)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var dm tailcfg.DERPMap
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&dm); err != nil {
		return nil, err
	}
	return &dm, nil
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
