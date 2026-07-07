// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

// Per-PC folder sharing, driven by the dashboard's GET /api/client/runtime
// (shares/mounts fields, see nodemode.go's nodeRuntimeResponse).
//
//   - Owner side: reconcile this node's Taildrive shares (`<exe> drive
//     share|unshare`) to match the dashboard's desired list. Access control
//     (who may reach a share, ro/rw) is enforced by headscale via node
//     attributes + CapGrants (see the headscale hscontrol/taildrive patch
//     module) — this launcher only declares WHAT is shared, not WHO may
//     access it.
//   - Grantee side: auto-mount granted shares as Windows drive letters,
//     backed by Taildrive's local WebDAV server at 100.100.100.100:8080.
//
// All of this is fail-open and best-effort, matching the rest of nodemode.go:
// a dashboard hiccup or a single exec failure is logged and retried on the
// next 20s poll tick, never fatal to the node's own connectivity.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"tailscale.com/envknob"
)

// nodeShareDesired is one folder this node should be exporting via Taildrive.
type nodeShareDesired struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

// nodeMountDesired is one remote share this node should auto-mount as a
// drive letter. OwnerIP is the authoritative way to find the owner in this
// node's netmap (see nodeResolveDrivePeer) — Machine (hostname) is a display
// hint only and is NOT used to build the mount path, since it can diverge
// from the MagicDNS name Taildrive actually mounts under.
type nodeMountDesired struct {
	Machine string `json:"machine"`
	OwnerIP string `json:"owner_ip"`
	Share   string `json:"share"`
	Drive   string `json:"drive"`  // "" = auto-pick a free letter
	Access  string `json:"access"` // "ro" | "rw" — informational; enforced by headscale
}

// nodeShareStatus / nodeMountStatus are what this node REPORTS back to the
// dashboard after a reconcile pass (POST /api/internal/foldershare-status), so
// an admin can see per-machine whether serving/mounting actually worked and,
// on failure, the exact error (e.g. "System error 67") — instead of guessing.
type nodeShareStatus struct {
	Name  string `json:"name"`
	Path  string `json:"path,omitempty"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type nodeMountStatus struct {
	Share   string `json:"share"`
	Machine string `json:"machine,omitempty"`
	Drive   string `json:"drive,omitempty"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// Owner side: reconcile Taildrive shares.
// ---------------------------------------------------------------------------

// nodeReconcileShares makes this node's live Taildrive shares match desired.
// Fail-open: a listing/apply error is logged and left for the next poll tick.
// Returns one nodeShareStatus per ENABLED desired share (ok=true if now being
// served, ok=false+error if the apply failed) for reporting to the dashboard.
func nodeReconcileShares(exe string, desired []nodeShareDesired) []nodeShareStatus {
	current, err := nodeCurrentShares(exe)
	if err != nil {
		log.Printf("node: folder-share: could not list current shares: %v", err)
		// Couldn't read live state → report every enabled desired share as
		// failed with the list error, so the dashboard shows something is wrong
		// rather than silently nothing.
		var st []nodeShareStatus
		for _, s := range desired {
			if s.Enabled && s.Name != "" && s.Path != "" {
				st = append(st, nodeShareStatus{Name: s.Name, Path: s.Path, OK: false, Error: "list shares: " + err.Error()})
			}
		}
		return st
	}

	want := make(map[string]string, len(desired))
	for _, s := range desired {
		if s.Enabled && s.Name != "" && s.Path != "" {
			want[s.Name] = s.Path
		}
	}

	toAdd, toRemove := nodeDiffShares(current, want)

	for _, name := range toRemove {
		if err := nodeDriveUnshare(exe, name); err != nil {
			log.Printf("node: folder-share: unshare %q failed: %v", name, err)
			continue
		}
		log.Printf("node: folder-share: unshared %q", name)
	}
	applyErr := make(map[string]string)
	for _, name := range toAdd {
		path := want[name]
		if err := nodeDriveShare(exe, name, path); err != nil {
			applyErr[name] = err.Error()
			log.Printf("node: folder-share: share %q -> %q failed: %v", name, path, err)
			continue
		}
		log.Printf("node: folder-share: sharing %q -> %q", name, path)
	}

	// A desired enabled share is OK unless its apply just failed — this covers
	// both "already serving" (not in toAdd) and "just added" (in toAdd, no err).
	var st []nodeShareStatus
	for _, s := range desired {
		if !s.Enabled || s.Name == "" || s.Path == "" {
			continue
		}
		if e, bad := applyErr[s.Name]; bad {
			st = append(st, nodeShareStatus{Name: s.Name, Path: s.Path, OK: false, Error: e})
		} else {
			st = append(st, nodeShareStatus{Name: s.Name, Path: s.Path, OK: true})
		}
	}
	return st
}

// nodeDiffShares is the pure diff between the live share set and the desired
// one. Returns share names to add-or-update and names to remove. A name whose
// path is unchanged is left alone. Deterministic order — unit-tested without
// exec/network.
func nodeDiffShares(current, want map[string]string) (toAdd, toRemove []string) {
	for name := range current {
		if _, ok := want[name]; !ok {
			toRemove = append(toRemove, name)
		}
	}
	for name, path := range want {
		if cur, ok := current[name]; !ok || cur != path {
			toAdd = append(toAdd, name)
		}
	}
	return toAdd, toRemove
}

// nodeCurrentShares shells out to `<exe> debug prefs` (always available under
// ts_include_cli, regardless of drive omission) and reads the live
// ipn.Prefs.DriveShares list. Returns an empty map (not an error) if the
// build omits Taildrive — DriveShares is simply absent from the JSON.
func nodeCurrentShares(exe string) (map[string]string, error) {
	c := exec.Command(exe, "debug", "prefs")
	c.Env = append(os.Environ(), "TS_BE_CLI=1")
	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("debug prefs: %w", err)
	}
	var p struct {
		DriveShares []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"DriveShares"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, fmt.Errorf("decode prefs: %w", err)
	}
	cur := make(map[string]string, len(p.DriveShares))
	for _, s := range p.DriveShares {
		cur[s.Name] = s.Path
	}
	return cur, nil
}

func nodeDriveShare(exe, name, path string) error {
	c := exec.Command(exe, "drive", "share", name, path)
	c.Env = append(os.Environ(), "TS_BE_CLI=1")
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func nodeDriveUnshare(exe, name string) error {
	c := exec.Command(exe, "drive", "unshare", name)
	c.Env = append(os.Environ(), "TS_BE_CLI=1")
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Grantee side: auto-mount granted shares as drive letters (Windows only).
//
// SPIKE NOTE: this has not been verified against a real Windows box + a live
// Taildrive owner yet (no local Go toolchain / Windows machine in this
// session — validated by structure and by the upstream Taildrive docs in
// cmd/tailscale/cli/drive.go, not by an end-to-end run). Before relying on
// this in production, confirm on a real machine: (1) the WebClient service
// mounts a UNC path of the form \\100.100.100.100@8080\<tailnet>\<peer>\<share>
// without prompting for credentials, and (2) net use accepts that literal
// form (some Windows builds want \\100.100.100.100@8080@SSL\... or a mapped
// port differently — Taildrive's own docs use the @8080 form, matched here).
// ---------------------------------------------------------------------------

// dashboardSecretKnob authenticates the folder-picker calls below to the
// CMS's public /api/client/*, /api/internal/* endpoints (checked
// server-side against HEADSCALE_DASHBOARD_SECRET). Matches the
// TS_<FEATURE>_SECRET envknob pattern already used by the other dashboard
// reporters in this directory (derppingreport.go's TS_DERPPING_SECRET,
// metricsreport.go's TS_METRICS_SECRET) — unlike those, this one
// specifically covers nodePostBrowseResult, which WRITES data the admin UI
// displays, so leaving it unauthenticated would let anyone who can reach
// the dashboard's public port plant a fake directory listing for any mac.
// Empty (unset) is a no-op, same fail-open shape as every other secret knob
// in this file.
var dashboardSecretKnob = envknob.RegisterString("TS_DASHBOARD_SECRET")

// nodeMountedDrives tracks drive letters this launcher has mounted, keyed by
// drive letter ("Z:") -> stable share key ("machine|share"), so later polls
// can detect "already correct" (skip) vs. "grant revoked" (unmount).
var nodeMountedDrives = map[string]string{}

// nodeReconcileMounts makes this node's mapped drives match desired. No-op on
// non-Windows (drive-letter mounting is a Windows concept; other platforms
// would use `tailscale drive` peer access directly or a WebDAV client).
// Returns one nodeMountStatus per desired mount (ok=true if mounted, ok=false
// + error — e.g. "System error 67" — if resolve/mount failed) for the
// dashboard. nil on non-Windows (feature not applicable).
func nodeReconcileMounts(exe string, desired []nodeMountDesired) []nodeMountStatus {
	if runtime.GOOS != "windows" {
		return nil
	}

	toMount, toUnmount := nodePlanMounts(desired, nodeMountedDrives, nodeNextFreeDriveLetter)

	for _, drive := range toUnmount {
		if err := nodeUnmountDrive(drive); err != nil {
			log.Printf("node: folder-mount: unmount %s failed: %v", drive, err)
			continue
		}
		log.Printf("node: folder-mount: unmounted %s", drive)
		delete(nodeMountedDrives, drive)
	}
	// key ("machine|share") -> error text of this pass's mount attempt.
	mountErr := make(map[string]string)
	for _, m := range toMount {
		if !nodeValidMountShare(m.Share) {
			// Unlike Drive (already normalized in nodePlanMounts), Share
			// reaches here straight from the dashboard's JSON with no
			// charset check — reject anything that could break out of the
			// intended <tailnet>\<peer>\<share> UNC structure before it ever
			// reaches fmt.Sprintf/net use.
			mountErr[m.Key] = "invalid share name"
			log.Printf("node: folder-mount: rejecting mount with invalid share name %q", m.Share)
			continue
		}
		shortName, suffix, err := nodeResolveDrivePeer(exe, m.OwnerIP)
		if err != nil {
			mountErr[m.Key] = "resolve peer " + m.OwnerIP + ": " + err.Error()
			log.Printf("node: folder-mount: resolve peer %s failed: %v", m.OwnerIP, err)
			continue
		}
		unc := nodeDriveMountUNC(suffix, shortName, m.Share)
		if err := nodeMountDrive(m.Drive, unc); err != nil {
			mountErr[m.Key] = err.Error()
			log.Printf("node: folder-mount: mount %s -> %s failed: %v", m.Drive, unc, err)
			continue
		}
		log.Printf("node: folder-mount: mounted %s -> %s", m.Drive, unc)
		nodeMountedDrives[m.Drive] = m.Key
	}

	// Report per desired mount: OK if it now holds a drive letter and had no
	// error this pass; else surface the error (or "not mounted" if it never
	// got a letter — e.g. none free).
	var st []nodeMountStatus
	for _, m := range desired {
		if m.OwnerIP == "" || m.Share == "" {
			continue
		}
		key := m.Machine + "|" + m.Share
		drive := nodeDriveForKey(nodeMountedDrives, key)
		switch {
		case mountErr[key] != "":
			st = append(st, nodeMountStatus{Share: m.Share, Machine: m.Machine, Drive: m.Drive, OK: false, Error: mountErr[key]})
		case drive != "":
			st = append(st, nodeMountStatus{Share: m.Share, Machine: m.Machine, Drive: drive, OK: true})
		default:
			st = append(st, nodeMountStatus{Share: m.Share, Machine: m.Machine, Drive: m.Drive, OK: false, Error: "not mounted"})
		}
	}
	return st
}

// driveMountPlan is one drive-letter assignment nodePlanMounts decided on.
type driveMountPlan struct {
	Drive   string // normalized, e.g. "Z:"
	Key     string // "machine|share" — matches nodeMountedDrives values
	OwnerIP string
	Share   string
}

// nodePlanMounts is the pure diff between desired mounts and the drives
// already mapped (mounted: drive -> key), producing what to mount and what to
// unmount. freeLetters is called (at most once per desired entry — see the
// nodeNextFreeDriveLetter doc comment for why it must be exclude-aware, not
// retried) to auto-pick a letter for entries with no explicit Drive; injected
// so this stays unit-testable without touching the real filesystem.
// Deterministic given a deterministic freeLetters. Ignores desired entries
// missing OwnerIP or Share (nothing to resolve/mount).
func nodePlanMounts(desired []nodeMountDesired, mounted map[string]string, freeLetters func(exclude map[string]bool) string) (toMount []driveMountPlan, toUnmount []string) {
	want := map[string]bool{}
	toUnmountSet := map[string]bool{}
	usedThisPass := map[string]bool{}
	for d := range mounted {
		usedThisPass[d] = true
	}

	for _, m := range desired {
		if m.OwnerIP == "" || m.Share == "" {
			continue
		}
		key := m.Machine + "|" + m.Share
		want[key] = true

		// oldDrive is whatever this key is CURRENTLY mounted under (if any),
		// found before deciding this round's drive — needed below to detect
		// "admin pinned an explicit letter different from the one already in
		// use" and release the stale one, instead of leaking it.
		oldDrive := nodeDriveForKey(mounted, key)

		drive := nodeNormalizeDriveLetter(m.Drive)
		if drive == "" {
			drive = oldDrive // no explicit pin -> keep the stable auto-assigned one
		}
		if drive == "" {
			// freeLetters is given usedThisPass so it can never hand back a
			// letter already claimed earlier in this same pass — no retry
			// loop here (a retry against a freeLetters that doesn't consult
			// usedThisPass would spin forever, since nothing actually mounts
			// until nodeReconcileMounts runs later; see the historical bug
			// this replaced).
			drive = freeLetters(usedThisPass)
		}
		if drive == "" {
			continue // no free letter available this pass
		}
		usedThisPass[drive] = true

		if oldDrive != "" && oldDrive != drive {
			toUnmountSet[oldDrive] = true
		}

		if existing, ok := mounted[drive]; ok && existing == key {
			continue // already correctly mounted
		}
		toMount = append(toMount, driveMountPlan{Drive: drive, Key: key, OwnerIP: m.OwnerIP, Share: m.Share})
	}

	for drive, key := range mounted {
		if !want[key] {
			toUnmountSet[drive] = true
		}
	}
	for drive := range toUnmountSet {
		toUnmount = append(toUnmount, drive)
	}
	sort.Strings(toUnmount) // deterministic order — toUnmountSet iteration isn't
	return toMount, toUnmount
}

func nodeDriveForKey(mounted map[string]string, key string) string {
	for d, k := range mounted {
		if k == key {
			return d
		}
	}
	return ""
}

// nodeNormalizeDriveLetter turns "z", "Z", "z:", "Z:" into "Z:", or "" if s
// isn't exactly one letter (with or without a trailing colon).
func nodeNormalizeDriveLetter(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, ":")
	if len(s) != 1 || s[0] < 'A' || s[0] > 'Z' {
		return ""
	}
	return s + ":"
}

// nodeNextFreeDriveLetter scans Z down to D (skip A/B/C — conventionally
// floppy/reserved) for a letter not in exclude with no local filesystem root,
// i.e. not already a drive (mapped or physical). exclude MUST contain every
// letter already claimed earlier in the current nodePlanMounts pass — os.Stat
// alone can't see those, since nothing is actually `net use`d until
// nodeReconcileMounts runs later, so two auto-pick entries checked back to
// back would otherwise both see the same free letter. Best-effort otherwise:
// a race with something else claiming the same letter between this check and
// `net use` just surfaces as a mount error on the next line, logged and
// retried next poll.
var nodeNextFreeDriveLetter = func(exclude map[string]bool) string {
	for l := 'Z'; l >= 'D'; l-- {
		d := string(l) + ":"
		if exclude[d] {
			continue
		}
		if _, err := os.Stat(d + `\`); os.IsNotExist(err) {
			return d
		}
	}
	return ""
}

// nodeValidMountShare reports whether share is safe to interpolate into a UNC
// path segment — must contain no path separator that could break out of the
// intended <tailnet>\<peer>\<share> structure. Defense in depth: the owner's
// own drive.NormalizeShareName (tailscale/drive/remote.go) already rejects
// these characters when a share is created, but Share here comes straight
// from the dashboard's JSON with no charset check of its own, unlike Drive
// (already normalized by nodeNormalizeDriveLetter before reaching this far).
func nodeValidMountShare(share string) bool {
	return share != "" && !strings.ContainsAny(share, `\/`)
}

// nodeDriveMountUNC builds the WebDAV UNC path Taildrive serves a peer's
// share at, per cmd/tailscale/cli/drive.go's documented path shape
// /<tailnet>/<machine>/<share> served locally at 100.100.100.100:8080.
func nodeDriveMountUNC(tailnetSuffix, peerShortName, share string) string {
	return fmt.Sprintf(`\\100.100.100.100@8080\%s\%s\%s`, tailnetSuffix, peerShortName, share)
}

// nodeStatusForDrive is the subset of `tailscale status --json` needed to
// resolve a peer's Taildrive short name (its MagicDNS base name, which is
// what the mount path uses) from its Tailscale IP.
type nodeStatusForDrive struct {
	MagicDNSSuffix string `json:"MagicDNSSuffix"`
	Peer           map[string]struct {
		DNSName      string   `json:"DNSName"`
		TailscaleIPs []string `json:"TailscaleIPs"`
	} `json:"Peer"`
}

// nodeResolveDrivePeer finds the peer with the given Tailscale IP in this
// node's current netmap and returns (peer short name, tailnet MagicDNS
// suffix without trailing dot). The short name is DNSName's first label,
// which is what Taildrive's WebDAV path uses for the peer segment (see
// tailcfg.Node.DisplayName/ComputedName) — NOT the dashboard's self-reported
// hostname, which can diverge (rename, dedup suffix).
func nodeResolveDrivePeer(exe, ownerIP string) (shortName, tailnetSuffix string, err error) {
	c := exec.Command(exe, "status", "--json")
	c.Env = append(os.Environ(), "TS_BE_CLI=1")
	out, err := c.Output()
	if err != nil {
		return "", "", fmt.Errorf("status --json: %w", err)
	}
	var st nodeStatusForDrive
	if err := json.Unmarshal(out, &st); err != nil {
		return "", "", fmt.Errorf("decode status: %w", err)
	}
	suffix := strings.TrimSuffix(st.MagicDNSSuffix, ".")
	for _, p := range st.Peer {
		for _, ip := range p.TailscaleIPs {
			if ip == ownerIP {
				return nodeDNSShortName(p.DNSName), suffix, nil
			}
		}
	}
	return "", "", fmt.Errorf("peer with IP %s not found in netmap", ownerIP)
}

// nodeDNSShortName reduces a FQDN like "mylaptop.tailxyz.ts.net." to its
// first label "mylaptop". Pure — unit-tested without exec/network.
func nodeDNSShortName(fqdn string) string {
	s := strings.TrimSuffix(fqdn, ".")
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	return s
}

func nodeMountDrive(drive, unc string) error {
	c := exec.Command("net", "use", drive, unc, "/PERSISTENT:NO")
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func nodeUnmountDrive(drive string) error {
	c := exec.Command("net", "use", drive, "/delete", "/y")
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Folder picker: list a directory on request from the dashboard admin UI.
// ---------------------------------------------------------------------------

// nodeBrowseEntry is one child of a listed directory.
type nodeBrowseEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
}

// 1s: user feedback after testing at 3s — clicking into a subfolder still
// felt slow (up to 3s per click, worse than a native file explorer). This is
// a single lightweight GET, best-effort, in its own goroutine — cheap enough
// to run this often.
const nodeBrowsePollInterval = 1 * time.Second

// nodeBrowsePollLoop checks for a pending folder-browse request on its own
// fast cadence, independent of the 20s runtime poll (nodeRuntimePollLoop).
// Browsing is an interactive admin action — a click on "Duyệt…" needs to
// feel responsive, not wait up to 20s for the next shared maintenance tick.
// A short interval also shrinks the window for the dashboard-side staleness
// check (POST /api/internal/browse-result only accepts a reply matching the
// latest requested path) to drop a reply because the admin clicked into
// another subfolder before this node polled again.
func nodeBrowsePollLoop() {
	if nodeMetricsURL == "" {
		return
	}
	mac := primaryMAC()
	if mac == "" {
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	t := time.NewTicker(nodeBrowsePollInterval)
	defer t.Stop()
	for {
		<-t.C
		nodeBrowsePoll(client, mac)
	}
}

// nodeBrowseDrivesSentinel is what the dashboard sends as the request path
// when the admin hasn't typed/picked a starting folder yet (new share, empty
// localPath). A hardcoded default like "D:\" used to be sent instead — which
// fails outright on any machine without that exact drive letter (no D:, only
// C:/E:/...). This can't collide with a real Windows path (":" is only valid
// right after a single drive letter).
const nodeBrowseDrivesSentinel = "::drives::"

// nodeBrowsePoll checks whether the dashboard has a pending "list this
// directory" request for us (admin clicked "Duyệt…" in the folder-share
// dialog) and, if so, lists it and reports the result back. Fail-open and
// silent on transport errors: this is best-effort UI sugar, not core
// connectivity.
func nodeBrowsePoll(client *http.Client, mac string) {
	path, err := nodeFetchBrowseRequest(client, mac)
	if err != nil || path == "" {
		return
	}
	var entries []nodeBrowseEntry
	if path == nodeBrowseDrivesSentinel {
		entries = nodeListDrives()
		log.Printf("node: folder-browse: listed available drives (%d found)", len(entries))
		if err := nodePostBrowseResult(client, mac, path, entries); err != nil {
			log.Printf("node: folder-browse: report result failed: %v", err)
		}
		return
	}
	entries, err = nodeListDir(path)
	if err != nil {
		log.Printf("node: folder-browse: list %q failed: %v", path, err)
		entries = nil // still report back so the admin UI doesn't hang waiting
	} else {
		// Every other dashboard-driven action in this file logs what it did
		// on success, not just on failure (share/unshare, mount/unmount).
		// This is the one action that can enumerate any directory name on the
		// whole disk — leaving it silent on success would be the only gap in
		// an otherwise-complete local audit trail.
		log.Printf("node: folder-browse: listed %q (%d entries)", path, len(entries))
	}
	if err := nodePostBrowseResult(client, mac, path, entries); err != nil {
		log.Printf("node: folder-browse: report result failed: %v", err)
	}
}

func nodeFetchBrowseRequest(client *http.Client, mac string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, nodeMetricsURL+"/api/client/browse-request?mac="+mac, nil)
	if err != nil {
		return "", err
	}
	if authValue := dashboardSecretKnob(); authValue != "" {
		req.Header.Set("X-Headscale-Secret", authValue)
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
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Path, nil
}

func nodeListDir(path string) ([]nodeBrowseEntry, error) {
	des, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	entries := make([]nodeBrowseEntry, 0, len(des))
	for _, d := range des {
		entries = append(entries, nodeBrowseEntry{Name: d.Name(), IsDir: d.IsDir()})
	}
	return entries, nil
}

// nodeListDrives reports the actual drive letters present on this machine —
// used as the root of the folder picker instead of guessing a hardcoded one
// ("D:\" used to be sent by the dashboard as the default starting path,
// which fails outright on any machine without that exact letter). Linux has
// no drive-letter concept; the dashboard only sends the sentinel on Windows
// clients (variant-agnostic here, but os.Stat("A:\") etc. simply never
// matches on Linux, so this safely returns an empty list there too).
func nodeListDrives() []nodeBrowseEntry {
	var out []nodeBrowseEntry
	for l := 'A'; l <= 'Z'; l++ {
		d := string(l) + ":"
		if _, err := os.Stat(d + `\`); err == nil {
			out = append(out, nodeBrowseEntry{Name: d, IsDir: true})
		}
	}
	return out
}

func nodePostBrowseResult(client *http.Client, mac, path string, entries []nodeBrowseEntry) error {
	if entries == nil {
		entries = []nodeBrowseEntry{}
	}
	body, err := json.Marshal(struct {
		Mac     string            `json:"mac"`
		Path    string            `json:"path"`
		Entries []nodeBrowseEntry `json:"entries"`
	}{mac, path, entries})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, nodeMetricsURL+"/api/internal/browse-result", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if authValue := dashboardSecretKnob(); authValue != "" {
		req.Header.Set("X-Headscale-Secret", authValue)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// nodeReportFolderShareStatus POSTs the result of a reconcile pass (which
// shares this node serves, which mounts it holds, plus any errors) to the
// dashboard's POST /api/internal/foldershare-status, so an admin sees per-PC
// serve/mount health without inspecting each machine. Best-effort: an empty
// base/mac or an HTTP failure just returns an error the caller logs — never
// affects reconcile or connectivity. shares/mounts marshal as [] (not null)
// when empty so the server sees an explicit "nothing to report" for this mac.
func nodeReportFolderShareStatus(base, mac, hostname string, shares []nodeShareStatus, mounts []nodeMountStatus) error {
	if base == "" || mac == "" {
		return nil
	}
	if shares == nil {
		shares = []nodeShareStatus{}
	}
	if mounts == nil {
		mounts = []nodeMountStatus{}
	}
	body, err := json.Marshal(struct {
		Mac      string            `json:"mac"`
		Hostname string            `json:"hostname,omitempty"`
		Shares   []nodeShareStatus `json:"shares"`
		Mounts   []nodeMountStatus `json:"mounts"`
	}{mac, hostname, shares, mounts})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(base, "/")+"/api/internal/foldershare-status", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if authValue := dashboardSecretKnob(); authValue != "" {
		req.Header.Set("X-Headscale-Secret", authValue)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
