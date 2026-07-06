// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

// Built-in auto-update for the single-file node builds.
//
// The launcher (runNodeLauncher) owns the on-disk exe and re-execs itself as
// the daemon, so it is the natural place to self-update: on start and every
// nodeUpdateCheckInterval (1 minute) it asks the dashboard
// <base>/api/client/latest whether a newer (or admin-pinned) build exists for
// this variant, downloads it, verifies sha256, atomically swaps the exe, and
// restarts.
//
// Safety:
//   - Only runs for versioned node builds (nodeVariant + nodeBuild baked by CI).
//   - sha256 is verified against the value the dashboard returns; mismatch aborts.
//   - The previous exe is kept as <exe>.old for local recovery; fleet-wide
//     rollback is done by pinning an older build in the dashboard (which this
//     same code then "updates" back to, since it compares build != current).
//   - Any error is non-fatal: the launcher proceeds with the current binary.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Set at build time via -ldflags "-X main.nodeVersion=… -X main.nodeBuild=…
// -X main.nodeVariant=…". Empty on normal/dev builds → auto-update is a no-op.
var (
	nodeVersion = ""
	nodeBuild   = "" // build number as a string (ldflag -X only sets strings)
	nodeVariant = "" // "vpn" | "portable" | "proxy" | "linux-amd64"
)

const (
	// 1 minute: this is a single lightweight GET /api/client/latest, isolated
	// in its own goroutine/ticker (nodeUpdateLoop) — it never touches the
	// heavier routes/shares/mounts reconcile in nodeRuntimePollLoop, so a
	// short interval doesn't add load there. It only does real work (download
	// + sha256 verify + swap) on the rare tick where a new build is found.
	nodeUpdateCheckInterval = 1 * time.Minute
	nodeUpdateHTTPTimeout   = 90 * time.Second
)

func nodeCurrentBuild() int {
	n, _ := strconv.Atoi(strings.TrimSpace(nodeBuild))
	return n
}

type nodeLatest struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"` // set when Enabled is false: why (see nodeUpdateReasonText)
	Build   int    `json:"build"`
	Version string `json:"version"`
	URL     string `json:"url"`
	Sha256  string `json:"sha256"`
}

// nodeUpdateReasonText turns the dashboard's machine-readable "reason" for
// enabled:false into the log line an admin can act on — "disabled" and
// "unsupported_variant" used to print the exact same "toggle it on" message,
// which sent an admin chasing a setting that was already correct.
func nodeUpdateReasonText(reason string) string {
	switch reason {
	case "disabled":
		return "auto-update is OFF on dashboard (toggle it on)"
	case "disabled_for_device":
		return "auto-update is OFF for this specific machine (dashboard per-machine override)"
	case "unsupported_variant":
		return fmt.Sprintf("dashboard doesn't recognize variant %q (no matching CI asset)", nodeVariant)
	case "no_release":
		return "dashboard has no eligible release (none published yet, or the pinned build was deleted)"
	case "no_asset":
		return fmt.Sprintf("target release has no asset for variant %q", nodeVariant)
	case "no_sha256":
		return "target release is missing a .sha256 file — refusing to update without one"
	default:
		return "auto-update is OFF on dashboard (toggle it on)"
	}
}

// nodeCleanupOldExe removes the <exe>.old left by a previous update (best-effort;
// on Windows it may still be locked right after the swap and get cleaned later).
func nodeCleanupOldExe(exe string) {
	_ = os.Remove(exe + ".old")
}

// checkAndSelfUpdate replaces the on-disk exe with the dashboard's target build
// for this variant and returns true if it did (caller should then restart). It
// never panics/exits; any failure returns false and leaves the exe untouched.
// mac (best-effort, may be "") lets the dashboard apply a per-machine
// auto-update override on top of the fleet-wide setting.
func checkAndSelfUpdate(base, secret, exe, mac string) bool {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" || nodeVariant == "" || nodeCurrentBuild() == 0 {
		return false // not a versioned node build → never self-update
	}
	latest, err := nodeFetchLatest(base, secret, mac)
	if err != nil {
		log.Printf("node: update check failed: %v", err)
		return false
	}
	if !latest.Enabled {
		log.Printf("node: no update — %s", nodeUpdateReasonText(latest.Reason))
		return false
	}
	if latest.URL == "" || latest.Sha256 == "" {
		// Server contract is that Enabled=true always carries URL+Sha256; reaching
		// here means a server-side inconsistency, not a normal "disabled" case.
		log.Printf("node: no update — dashboard reported enabled but no publishable build for variant %q", nodeVariant)
		return false
	}
	if latest.Build == nodeCurrentBuild() {
		log.Printf("node: already on the latest build %d — nothing to update", latest.Build)
		return false
	}
	log.Printf("node: update available build %d -> %d (%s), downloading…",
		nodeCurrentBuild(), latest.Build, latest.Version)
	if err := nodeDownloadSwap(exe, latest); err != nil {
		log.Printf("node: self-update failed: %v", err)
		return false
	}
	log.Printf("node: updated to build %d (%s); restarting", latest.Build, latest.Version)
	return true
}

func nodeFetchLatest(base, secret, mac string) (*nodeLatest, error) {
	u := base + "/api/client/latest?variant=" + url.QueryEscape(nodeVariant)
	if mac != "" {
		u += "&mac=" + url.QueryEscape(mac)
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if secret != "" {
		req.Header.Set("X-Headscale-Secret", secret)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var l nodeLatest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&l); err != nil {
		return nil, err
	}
	return &l, nil
}

// nodeDownloadSwap downloads l.URL to <exe>.new, verifies sha256, then swaps:
// exe -> exe.old, exe.new -> exe. Restores on swap failure.
func nodeDownloadSwap(exe string, l *nodeLatest) error {
	tmp := exe + ".new"
	if err := nodeDownloadVerify(l.URL, tmp, l.Sha256); err != nil {
		return err
	}
	_ = os.Chmod(tmp, 0o755) // no-op on Windows; needed for the linux node
	_ = os.Remove(exe + ".old")
	if err := os.Rename(exe, exe+".old"); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("stash current exe: %w", err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Rename(exe+".old", exe) // restore
		_ = os.Remove(tmp)
		return fmt.Errorf("install new exe: %w", err)
	}
	return nil
}

func nodeDownloadVerify(dlURL, dst, wantSha string) error {
	client := &http.Client{Timeout: nodeUpdateHTTPTimeout}
	resp, err := client.Get(dlURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, strings.TrimSpace(wantSha)) {
		_ = os.Remove(dst)
		return fmt.Errorf("sha256 mismatch: got %s want %s", got, wantSha)
	}
	return nil
}

// nodeRestartSelf launches a fresh launcher (the just-swapped new binary) and
// exits. The new launcher's nodeKillConflicting() reclaims the LocalAPI pipe
// from our now-orphaned daemon child, so we don't stop it here. Does not return.
func nodeRestartSelf(exe string) {
	c := exec.Command(exe)
	c.Dir = filepath.Dir(exe)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	nodeHideChildWindow(c)
	if err := c.Start(); err != nil {
		log.Printf("node: restart after update failed to spawn new binary: %v", err)
		return // fall through: keep running the (already-swapped) old process
	}
	os.Exit(0)
}

// nodeUpdateLoop periodically checks for updates and restarts into a new build.
func nodeUpdateLoop(exe string) {
	mac := primaryMAC()
	t := time.NewTicker(nodeUpdateCheckInterval)
	defer t.Stop()
	for range t.C {
		if checkAndSelfUpdate(nodeMetricsURL, metricsReportSecret(), exe, mac) {
			nodeRestartSelf(exe)
		}
	}
}

// nodeUpdateSignalPollInterval: an admin's "Cập nhật ngay" click or an
// auto-update toggle change should be felt in a few seconds, not wait for
// the 20s runtime poll (which also does heavier routes/shares/mounts work)
// or nodeUpdateCheckInterval's own periodic tick.
const nodeUpdateSignalPollInterval = 3 * time.Second

// nodeUpdateSignalPollLoop watches GET /api/client/update-signal — a tiny,
// dedicated endpoint separate from /api/client/runtime — on its own fast
// cadence, and runs an immediate self-update check the moment the dashboard's
// update_check_at (fleet-wide or per-machine) changes. The first tick only
// adopts the current value without acting, matching nodeRuntimePollLoop's
// own startup-adoption rule (a stale trigger from before this process
// started shouldn't force an update right away).
func nodeUpdateSignalPollLoop(exe string) {
	if nodeMetricsURL == "" || nodeVariant == "" || nodeCurrentBuild() == 0 {
		return // not a versioned node build → self-update is a no-op anyway
	}
	mac := primaryMAC()
	client := &http.Client{Timeout: 5 * time.Second}
	secret := metricsReportSecret()
	last := ""
	seen := false
	t := time.NewTicker(nodeUpdateSignalPollInterval)
	defer t.Stop()
	for {
		<-t.C
		at, err := nodeFetchUpdateSignal(client, secret, mac)
		if err != nil {
			continue // best-effort; next tick retries
		}
		if !seen {
			seen = true
			last = at
			continue
		}
		if at == "" || at == last {
			continue
		}
		last = at
		log.Printf("node: update-now requested by dashboard, checking…")
		if checkAndSelfUpdate(nodeMetricsURL, secret, exe, mac) {
			nodeRestartSelf(exe)
		}
	}
}

func nodeFetchUpdateSignal(client *http.Client, secret, mac string) (string, error) {
	u := nodeMetricsURL + "/api/client/update-signal"
	if mac != "" {
		u += "?mac=" + url.QueryEscape(mac)
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	if secret != "" {
		req.Header.Set("X-Headscale-Secret", secret)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var out struct {
		At string `json:"at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<12)).Decode(&out); err != nil {
		return "", err
	}
	return out.At, nil
}
