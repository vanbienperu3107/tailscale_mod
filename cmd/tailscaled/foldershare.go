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
	"strings"
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

// ---------------------------------------------------------------------------
// Owner side: reconcile Taildrive shares.
// ---------------------------------------------------------------------------

// nodeReconcileShares makes this node's live Taildrive shares match desired.
// Fail-open: a listing/apply error is logged and left for the next poll tick.
func nodeReconcileShares(exe string, desired []nodeShareDesired) {
	current, err := nodeCurrentShares(exe)
	if err != nil {
		log.Printf("node: folder-share: could not list current shares: %v", err)
		return
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
	for _, name := range toAdd {
		path := want[name]
		if err := nodeDriveShare(exe, name, path); err != nil {
			log.Printf("node: folder-share: share %q -> %q failed: %v", name, path, err)
			continue
		}
		log.Printf("node: folder-share: sharing %q -> %q", name, path)
	}
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

// nodeMountedDrives tracks drive letters this launcher has mounted, keyed by
// drive letter ("Z:") -> stable share key ("machine|share"), so later polls
// can detect "already correct" (skip) vs. "grant revoked" (unmount).
var nodeMountedDrives = map[string]string{}

// nodeReconcileMounts makes this node's mapped drives match desired. No-op on
// non-Windows (drive-letter mounting is a Windows concept; other platforms
// would use `tailscale drive` peer access directly or a WebDAV client).
func nodeReconcileMounts(exe string, desired []nodeMountDesired) {
	if runtime.GOOS != "windows" {
		return
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
	for _, m := range toMount {
		shortName, suffix, err := nodeResolveDrivePeer(exe, m.OwnerIP)
		if err != nil {
			log.Printf("node: folder-mount: resolve peer %s failed: %v", m.OwnerIP, err)
			continue
		}
		unc := nodeDriveMountUNC(suffix, shortName, m.Share)
		if err := nodeMountDrive(m.Drive, unc); err != nil {
			log.Printf("node: folder-mount: mount %s -> %s failed: %v", m.Drive, unc, err)
			continue
		}
		log.Printf("node: folder-mount: mounted %s -> %s", m.Drive, unc)
		nodeMountedDrives[m.Drive] = m.Key
	}
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
// unmount. freeLetters is called to auto-pick a letter for entries with no
// explicit Drive; injected so this stays unit-testable without touching the
// real filesystem. Deterministic given a deterministic freeLetters. Ignores
// desired entries missing OwnerIP or Share (nothing to resolve/mount).
func nodePlanMounts(desired []nodeMountDesired, mounted map[string]string, freeLetters func() string) (toMount []driveMountPlan, toUnmount []string) {
	want := map[string]bool{}
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

		drive := nodeNormalizeDriveLetter(m.Drive)
		if drive == "" {
			drive = nodeDriveForKey(mounted, key)
		}
		if drive == "" {
			drive = freeLetters()
			for drive != "" && usedThisPass[drive] {
				drive = freeLetters()
			}
		}
		if drive == "" {
			continue // no free letter available this pass
		}
		usedThisPass[drive] = true

		if existing, ok := mounted[drive]; ok && existing == key {
			continue // already correctly mounted
		}
		toMount = append(toMount, driveMountPlan{Drive: drive, Key: key, OwnerIP: m.OwnerIP, Share: m.Share})
	}

	for drive, key := range mounted {
		if !want[key] {
			toUnmount = append(toUnmount, drive)
		}
	}
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
// floppy/reserved) for a letter with no local filesystem root, i.e. not
// already a drive (mapped or physical). Best-effort: a race with something
// else claiming the same letter between check and `net use` just surfaces as
// a mount error on the next line, logged and retried next poll.
var nodeNextFreeDriveLetter = func() string {
	for l := 'Z'; l >= 'D'; l-- {
		d := string(l) + ":"
		if _, err := os.Stat(d + `\`); os.IsNotExist(err) {
			return d
		}
	}
	return ""
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

// nodeBrowsePoll checks whether the dashboard has a pending "list this
// directory" request for us (admin clicked "Duyệt…" in the folder-share
// dialog) and, if so, lists it and reports the result back. Runs on the same
// 20s cadence as the runtime poll — the folder picker is not latency-critical
// (an admin is choosing a path, not automating something time-sensitive).
// Fail-open and silent on transport errors: this is best-effort UI sugar, not
// core connectivity.
func nodeBrowsePoll(client *http.Client, mac string) {
	path, err := nodeFetchBrowseRequest(client, mac)
	if err != nil || path == "" {
		return
	}
	entries, err := nodeListDir(path)
	if err != nil {
		log.Printf("node: folder-browse: list %q failed: %v", path, err)
		entries = nil // still report back so the admin UI doesn't hang waiting
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
