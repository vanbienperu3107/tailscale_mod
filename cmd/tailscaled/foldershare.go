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
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
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
	// Warning carries a non-fatal caveat about a mount that technically
	// succeeded but may not be usable yet — currently "mapped only in the
	// elevated session, reboot pending" (see nodeMountStatusFor). omitempty so
	// the headscale foldershare-status endpoint and the dashboard stay
	// backward-compatible with builds that never send it.
	Warning string `json:"warning,omitempty"`
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

	// Owner backend: ensure a Taildrive WebDAV file server is running for
	// exactly this share set and its address is registered with tailscaled.
	// Recording a share in prefs (above) does NOT by itself serve any bytes —
	// upstream the GUI/tray app starts the file server, which this headless
	// build lacks, so without this every accessor's request returns HTTP 500
	// (surfacing on the grantee as Windows "System error 59"). Windows-only;
	// elsewhere driveimpl spawns per-user servers automatically.
	nodeEnsureDriveFileServer(exe, want)

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
// Owner side: Taildrive WebDAV file-server backend.
//
// `tailscale drive share` only records a share in prefs; it does not serve any
// bytes. Serving requires a separate WebDAV file server whose address is
// registered with tailscaled via DriveSetServerAddr. On platforms where
// drive.AllowShareAs() is true (UNIX) driveimpl spawns that server itself,
// per accessing user. On Windows it's false, and upstream the GUI/tray app is
// responsible for starting one file server and registering its address — which
// this headless single-file build has no equivalent of. The result was that
// itop "shared" a folder (prefs updated, dashboard showed ok) but every
// accessor's WebDAV request returned HTTP 500, surfacing on the grantee as
// Windows "System error 59". We replicate the GUI here: spawn `<exe>
// serve-taildrive <name> <path>...` (a tailscaled subcommand — see
// tailscaled_drive.go, and the fall-through in nodemode.go's maybeRunNode),
// read the "secretToken|addr" line it prints, and register it via `<exe> drive
// set-server-addr`. This runs from the always-on daemon poll loop, so the file
// server is a daemon child and survives a launcher-window close.
var (
	nodeDriveFSMu  sync.Mutex
	nodeDriveFSCmd *exec.Cmd // running serve-taildrive child, or nil
	nodeDriveFSSig string    // signature of the share set the child serves
)

// nodeEnsureDriveFileServer makes the owner's file server match want
// (shareName->path, already filtered to enabled). It (re)starts the
// serve-taildrive child when the share set changes or the child died, and
// registers the new address; when want is empty it stops the child. Windows
// only. Fail-open: any error is logged and retried on the next poll tick.
func nodeEnsureDriveFileServer(exe string, want map[string]string) {
	if runtime.GOOS != "windows" {
		return
	}
	sig := nodeShareSig(want)

	nodeDriveFSMu.Lock()
	defer nodeDriveFSMu.Unlock()

	// Already serving exactly this set with a live child → nothing to do.
	if nodeDriveFSCmd != nil && sig == nodeDriveFSSig {
		return
	}

	// Stop any previous server (share set changed, or it exited).
	if nodeDriveFSCmd != nil && nodeDriveFSCmd.Process != nil {
		_ = nodeDriveFSCmd.Process.Kill()
	}
	nodeDriveFSCmd = nil
	nodeDriveFSSig = ""

	if len(want) == 0 {
		return // no shares → no file server needed
	}

	args := []string{"serve-taildrive"}
	for name, path := range want {
		args = append(args, name, path)
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = os.Environ() // NOT TS_BE_CLI: serve-taildrive is a tailscaled subcommand
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("node: folder-share: fileserver stdout pipe: %v", err)
		return
	}
	nodeHideChildWindow(cmd)
	if err := cmd.Start(); err != nil {
		log.Printf("node: folder-share: start fileserver: %v", err)
		return
	}

	// The child prints "secretToken|127.0.0.1:port" as its first stdout line.
	sc := bufio.NewScanner(stdout)
	if !sc.Scan() {
		log.Printf("node: folder-share: fileserver produced no address")
		_ = cmd.Process.Kill()
		return
	}
	addr := strings.TrimSpace(sc.Text())
	// Drain the rest so the child never blocks writing to a full pipe.
	go func() { _, _ = io.Copy(io.Discard, stdout) }()
	// Reap on exit and clear state so the next poll respawns it.
	go func() {
		_ = cmd.Wait()
		nodeDriveFSMu.Lock()
		if nodeDriveFSCmd == cmd {
			nodeDriveFSCmd = nil
			nodeDriveFSSig = ""
		}
		nodeDriveFSMu.Unlock()
	}()

	if err := nodeDriveSetServerAddr(exe, addr); err != nil {
		log.Printf("node: folder-share: register fileserver addr: %v", err)
		_ = cmd.Process.Kill()
		return
	}
	nodeDriveFSCmd = cmd
	nodeDriveFSSig = sig
	log.Printf("node: folder-share: Taildrive file server up for %d share(s)", len(want))
}

// nodeShareSig is a stable, order-independent signature of the name->path
// share set, so a poll returning the same shares doesn't needlessly restart
// the file server. Pure — unit-tested without exec.
func nodeShareSig(want map[string]string) string {
	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(0)
		b.WriteString(want[n])
		b.WriteByte(0)
	}
	return b.String()
}

// nodeDriveSetServerAddr registers the file-server address with tailscaled via
// the CLI (which owns the localapi client — kept out of this always-compiled
// file so it doesn't pull tailscale.com/client/local into the minimal build,
// see cmd/tailscaled/deps_test.go TestOmitLocalClient).
func nodeDriveSetServerAddr(exe, addr string) error {
	c := exec.Command(exe, "drive", "set-server-addr", addr)
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

// nodeMountSource remembers WHICH security context a drive was mounted in
// (drive letter -> token source), so the unmount runs in the SAME session the
// mount was created in. A `net use /delete` in the wrong token targets the
// wrong DosDevices namespace and orphans the user's drive. Parallel to
// nodeMountedDrives; both are cleaned up together on unmount.
var nodeMountSource = map[string]nodeMountTokenSource{}

// nodeMountsSupported gates the whole grantee-side mount feature to Windows
// (drive-letter mapping is a Windows concept). A var, not a direct
// runtime.GOOS check, so nodeReconcileMounts can be exercised on the Linux CI
// runner by flipping it true and injecting the exec seams below.
var nodeMountsSupported = runtime.GOOS == "windows"

// nodeMountTokenSource is the security context `net use` runs in. The elevated
// daemon (requireAdministrator manifest) must NOT map drives in its own token
// — those land in the admin logon token's namespace, invisible to the
// interactive non-elevated Explorer (UAC "linked connections" isolation, the
// "net use shows Z: but Explorer doesn't" bug). Instead it mounts into the
// user's session; this enum records which route was used, both to unmount
// symmetrically and to report honest visibility to the dashboard.
type nodeMountTokenSource int

const (
	// nodeMountTokenCurrent: plain in-process `net use` (this process's token).
	// Visible to the user ONLY if we're already non-elevated, or if
	// EnableLinkedConnections is in effect this session.
	nodeMountTokenCurrent nodeMountTokenSource = iota
	// nodeMountTokenLinked: spawn `net use` under the elevated token's linked
	// (filtered, non-elevated) sibling — same logon session as Explorer, so the
	// mapping shows up immediately. The normal path for the elevated-user daemon.
	nodeMountTokenLinked
	// nodeMountTokenActiveSession: spawn `net use` under the active console
	// user's token (WTSQueryUserToken). Only reachable when running as SYSTEM.
	nodeMountTokenActiveSession
)

// nodeTokenEnv is the observed security context of this process, gathered by
// the platform-specific nodeCurrentTokenEnv. Pure inputs so
// nodeSelectMountTokenSource is unit-testable without Windows.
type nodeTokenEnv struct {
	Elevated        bool // this process token is UAC-elevated (full admin)
	HaveLinkedToken bool // GetLinkedToken succeeded (a split-token user)
	LinkedElevated  bool // the linked token is itself elevated => WE are the non-elevated one
	IsSystem        bool // running as LocalSystem (S-1-5-18)
	HaveConsoleUser bool // an interactive user is logged on to the console session
}

// nodeSelectMountTokenSource picks the context to run `net use` in. Pure.
func nodeSelectMountTokenSource(e nodeTokenEnv) nodeMountTokenSource {
	switch {
	case e.IsSystem && e.HaveConsoleUser:
		// A SYSTEM service must reach into the logged-on user's session.
		return nodeMountTokenActiveSession
	case e.Elevated && e.HaveLinkedToken && !e.LinkedElevated:
		// We're the elevated half of a split token; the linked token is the
		// non-elevated sibling in the same session as Explorer.
		return nodeMountTokenLinked
	default:
		// Already non-elevated (mapping is directly visible), or no linked token
		// to reach (e.g. UAC off / built-in Administrator — no isolation anyway).
		return nodeMountTokenCurrent
	}
}

// nodeMountVisibleToUser reports whether a mount made via src will actually
// show up in the interactive user's Explorer. userIsolated means this process
// is elevated AND has a non-elevated linked sibling — i.e. a real UAC split
// token where an in-process `net use` lands in a namespace the user can't see.
// Pure — drives the honest status.
func nodeMountVisibleToUser(src nodeMountTokenSource, userIsolated, linkedConnEffective bool) bool {
	switch src {
	case nodeMountTokenLinked, nodeMountTokenActiveSession:
		return true // mapped directly into the user's session
	default:
		// Plain in-process net use: visible unless a real split-token isolates
		// us from the user and linked connections aren't in effect.
		// (Non-elevated, or built-in Administrator / UAC-off with no token
		// split, has userIsolated=false — no isolation to defeat.)
		return !userIsolated || linkedConnEffective
	}
}

// nodeTokenSourceName is a short label for logs. Pure.
func nodeTokenSourceName(s nodeMountTokenSource) string {
	switch s {
	case nodeMountTokenLinked:
		return "linked-user"
	case nodeMountTokenActiveSession:
		return "active-session"
	default:
		return "current"
	}
}

func nodeMountArgv(drive, unc string) []string {
	return []string{"use", drive, unc, "/PERSISTENT:NO"}
}

func nodeUnmountArgv(drive string) []string {
	return []string{"use", drive, "/delete", "/y"}
}

func nodeListMountsArgv() []string {
	return []string{"use"}
}

// nodeParseNetUseTable parses `net use` output into drive-letter -> remote UNC.
// Only rows with an "X:" device and a "\\..." remote are kept; status columns
// (OK/Disconnected/Unavailable), headers, and the trailing "command completed"
// line are ignored. Pure — unit-tested without Windows. Tolerant of the leading
// status word ("OK  Z:  \\...") and of a missing status ("Z:  \\...").
func nodeParseNetUseTable(out string) map[string]string {
	res := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if len(f) == 2 && f[1] == ':' && f[0] >= 'A' && f[0] <= 'Z' {
				// The remote is the next field that starts with "\\".
				for _, g := range fields[i+1:] {
					if strings.HasPrefix(g, `\\`) {
						res[strings.ToUpper(f)] = g
						break
					}
				}
				break
			}
		}
	}
	return res
}

// nodeUNCMatch reports whether two UNC strings refer to the same share,
// ignoring case and trailing separators — `net use` may echo a path back with
// different casing than we mapped it. Pure.
func nodeUNCMatch(a, b string) bool {
	norm := func(s string) string {
		return strings.TrimRight(strings.ToLower(strings.TrimSpace(s)), `\/`)
	}
	return a != "" && norm(a) == norm(b)
}

// nodeIsAlreadyMappedErr reports whether a `net use` failure is the "local
// device name already in use" family (System error 85 / 1219). After a daemon
// restart the in-memory nodeMountedDrives is empty but the user-session mapping
// survives, so re-issuing `net use <pinned letter>` fails this way for a drive
// that is in fact already correct — a false failure, not a real one. Pure.
func nodeIsAlreadyMappedErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "error 85") || strings.Contains(s, "error 1219") ||
		strings.Contains(s, "already in use") || strings.Contains(s, "multiple connections")
}

// nodeMountStatusFor builds the per-mount status reported to the dashboard,
// enforcing the "no misleading green / no false red" rule. Pure so the policy
// is unit-tested off-Windows.
//
//   - mountErr set                -> OK=false, Error=mountErr (a real failure wins).
//   - drive empty                 -> OK=false, Error="not mounted" (no free letter).
//   - mounted & visible to user   -> OK=true.
//   - mounted but NOT visible yet  -> OK=false + a human Error (never OK=false with
//     an empty Error, which the dashboard would render as an unexplained red).
func nodeMountStatusFor(m nodeMountDesired, drive, mountErr string,
	src nodeMountTokenSource, userIsolated, linkedConnEffective bool) nodeMountStatus {
	st := nodeMountStatus{Share: m.Share, Machine: m.Machine, Drive: drive}
	switch {
	case mountErr != "":
		st.Drive = m.Drive // desired letter; the mount didn't take
		st.OK = false
		st.Error = mountErr
	case drive == "":
		st.Drive = m.Drive
		st.OK = false
		st.Error = "not mounted"
	case nodeMountVisibleToUser(src, userIsolated, linkedConnEffective):
		st.OK = true
	default:
		st.OK = false
		msg := "đã map ở phiên elevated; cần reboot để ổ hiện trong Explorer (EnableLinkedConnections)"
		st.Error = msg
		st.Warning = msg
	}
	return st
}

// The three exec seams below are vars (like nodeNextFreeDriveLetter) so tests
// can inject fakes and drive nodeReconcileMounts on a non-Windows CI runner.
// Their default bodies delegate to the platform-specific nodeRunNetUseVia /
// nodeCurrentMountSource / nodeMountEnv (real on Windows, stubs elsewhere).

// nodeMountDrive maps drive->unc and returns the token source actually used
// (needed later to unmount in the same session and to report honest status).
var nodeMountDrive = func(drive, unc string) (nodeMountTokenSource, error) {
	src := nodeCurrentMountSource()
	_, err := nodeRunNetUseVia(src, nodeMountArgv(drive, unc))
	return src, err
}

// nodeUnmountDrive removes a mapping in the SAME session it was created in
// (src is the value stored in nodeMountSource at mount time).
var nodeUnmountDrive = func(drive string, src nodeMountTokenSource) error {
	_, err := nodeRunNetUseVia(src, nodeUnmountArgv(drive))
	return err
}

// nodeListUserMounts returns the drive-letter -> UNC map currently visible in
// the session we mount into, plus the source used to read it. Used to ADOPT
// mappings that survived a daemon restart so we don't re-`net use` a live
// pinned letter (System error 85) forever.
var nodeListUserMounts = func() (map[string]string, nodeMountTokenSource, error) {
	src := nodeCurrentMountSource()
	out, err := nodeRunNetUseVia(src, nodeListMountsArgv())
	if err != nil {
		return nil, src, err
	}
	return nodeParseNetUseTable(out), src, nil
}

// nodeMountEnvFn reports (userIsolated, linkedConnEffective) for honest status:
// userIsolated = elevated with a non-elevated linked sibling (in-process net
// use would be hidden from the user); linkedConnEffective = EnableLinked
// Connections is actually in force this session. A var for test injection;
// default delegates to the platform impl (both false off-Windows).
var nodeMountEnvFn = func() (userIsolated, linkedConnEffective bool) {
	return nodeMountEnv()
}

// nodeLinkedConnNeedsReboot is set by nodeEnsureLinkedConnections (Windows)
// when it just wrote EnableLinkedConnections=1, which only takes effect after a
// reboot. Surfaced in a log line by the daemon wire-up so an admin knows a
// one-time reboot is pending for the fallback (in-process) mount path.
var nodeLinkedConnNeedsReboot bool

// nodeReconcileMounts makes this node's mapped drives match desired. No-op when
// nodeMountsSupported is false (drive-letter mounting is a Windows concept;
// other platforms would use `tailscale drive` peer access directly or a WebDAV
// client). Returns one nodeMountStatus per desired mount, with OK reflecting
// whether the interactive user can actually SEE the drive (not merely that
// `net use` returned 0). nil when unsupported (feature not applicable).
func nodeReconcileMounts(exe string, desired []nodeMountDesired) []nodeMountStatus {
	if !nodeMountsSupported {
		return nil
	}

	userIsolated, linkedConnEff := nodeMountEnvFn()

	// Resolve every owner IP to its Taildrive peer short name + tailnet suffix
	// ONCE per pass (a single `tailscale status --json`), shared by adoption and
	// mounting below. A resolve failure surfaces per-owner as a mount error.
	peers, suffix, resolveErr := nodeResolveDrivePeers(exe)
	uncFor := func(ownerIP, share string) string {
		short, ok := peers[ownerIP]
		if !ok || !nodeValidMountShare(share) {
			return ""
		}
		return nodeDriveMountUNC(suffix, short, share)
	}

	// ADOPT: a daemon restart (routine on this auto-updating fleet) clears the
	// in-memory maps while the user-session mappings survive. Re-derive them from
	// the live `net use` table so a desired mount already mapped to its target
	// UNC is recognized as "already correct" instead of being re-mounted onto a
	// pinned letter (System error 85) every 20s forever — and so
	// nodeNextFreeDriveLetter (which os.Stat's in the elevated token and can't
	// see user-session maps) doesn't hand back a letter already in use there.
	nodeAdoptExistingMounts(desired, uncFor)

	toMount, toUnmount := nodePlanMounts(desired, nodeMountedDrives, nodeNextFreeDriveLetter)

	for _, drive := range toUnmount {
		src := nodeMountSource[drive] // delete in the same session it was created
		if err := nodeUnmountDrive(drive, src); err != nil {
			log.Printf("node: folder-mount: unmount %s failed: %v", drive, err)
			continue
		}
		log.Printf("node: folder-mount: unmounted %s", drive)
		delete(nodeMountedDrives, drive)
		delete(nodeMountSource, drive)
	}

	// key ("machine|share") -> error text / token source of this pass's attempt.
	mountErr := make(map[string]string)
	mountSrc := make(map[string]nodeMountTokenSource)
	for _, m := range toMount {
		if !nodeValidMountShare(m.Share) {
			// Unlike Drive (already normalized in nodePlanMounts), Share
			// reaches here straight from the dashboard's JSON with no charset
			// check — reject anything that could break out of the intended
			// <tailnet>\<peer>\<share> UNC structure before it ever reaches
			// fmt.Sprintf/net use.
			mountErr[m.Key] = "invalid share name"
			log.Printf("node: folder-mount: rejecting mount with invalid share name %q", m.Share)
			continue
		}
		unc := uncFor(m.OwnerIP, m.Share)
		if unc == "" {
			e := "resolve peer " + m.OwnerIP
			if resolveErr != nil {
				e += ": " + resolveErr.Error()
			} else {
				e += ": not found in netmap"
			}
			mountErr[m.Key] = e
			log.Printf("node: folder-mount: %s", e)
			continue
		}
		src, err := nodeMountDrive(m.Drive, unc)
		if err != nil && nodeIsAlreadyMappedErr(err) {
			// The pinned letter is already mapped in the user session (survived a
			// restart and adoption missed it, e.g. a UNC-format quirk); treat as
			// already-correct rather than a false failure.
			log.Printf("node: folder-mount: %s already mapped (adopting) -> %s", m.Drive, unc)
			err = nil
		}
		if err != nil {
			mountErr[m.Key] = err.Error()
			log.Printf("node: folder-mount: mount %s -> %s failed: %v", m.Drive, unc, err)
			continue
		}
		log.Printf("node: folder-mount: mounted %s -> %s", m.Drive, unc)
		nodeMountedDrives[m.Drive] = m.Key
		nodeMountSource[m.Drive] = src
		mountSrc[m.Key] = src
	}

	// Report per desired mount via nodeMountStatusFor so OK means "the user can
	// see the drive", surfacing a reboot/elevated-session caveat instead of a
	// misleading green when it was only mapped in the elevated token.
	var st []nodeMountStatus
	for _, m := range desired {
		if m.OwnerIP == "" || m.Share == "" {
			continue
		}
		key := m.Machine + "|" + m.Share
		drive := nodeDriveForKey(nodeMountedDrives, key)
		src, ok := mountSrc[key]
		if !ok {
			src = nodeMountSource[drive] // adopted / already-mounted before this pass
		}
		st = append(st, nodeMountStatusFor(m, drive, mountErr[key], src, userIsolated, linkedConnEff))
	}
	return st
}

// nodeAdoptExistingMounts seeds nodeMountedDrives/nodeMountSource from the live
// user-session `net use` table for any desired mount already mapped to its
// target UNC (uncFor(m)), so a post-restart reconcile treats it as
// already-correct. Best-effort: a listing error just means no adoption this
// pass and the mount loop's nodeIsAlreadyMappedErr fallback still guards pinned
// letters.
func nodeAdoptExistingMounts(desired []nodeMountDesired, uncFor func(ownerIP, share string) string) {
	live, src, err := nodeListUserMounts()
	if err != nil {
		log.Printf("node: folder-mount: could not list existing mounts for adoption: %v", err)
		return
	}
	for _, m := range desired {
		if m.OwnerIP == "" || m.Share == "" {
			continue
		}
		key := m.Machine + "|" + m.Share
		if nodeDriveForKey(nodeMountedDrives, key) != "" {
			continue // already tracked
		}
		want := uncFor(m.OwnerIP, m.Share)
		if want == "" {
			continue
		}
		for drive, unc := range live {
			if _, taken := nodeMountedDrives[drive]; taken {
				continue
			}
			if nodeUNCMatch(unc, want) {
				nodeMountedDrives[drive] = key
				nodeMountSource[drive] = src
				log.Printf("node: folder-mount: adopted existing mount %s -> %s", drive, unc)
				break
			}
		}
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

// nodeResolveDrivePeers fetches the netmap once and returns ownerIP -> peer
// Taildrive short name for every peer, plus the tailnet MagicDNS suffix (no
// trailing dot). The short name is DNSName's first label, which is what
// Taildrive's WebDAV path uses for the peer segment (see
// tailcfg.Node.DisplayName/ComputedName) — NOT the dashboard's self-reported
// hostname, which can diverge (rename, dedup suffix). A reconcile pass with
// several mounts runs `status --json` a single time instead of once per owner.
// Returns a nil map + error if status can't be read/decoded. A var so tests can
// drive nodeReconcileMounts without a live daemon.
var nodeResolveDrivePeers = func(exe string) (byIP map[string]string, tailnetSuffix string, err error) {
	c := exec.Command(exe, "status", "--json")
	c.Env = append(os.Environ(), "TS_BE_CLI=1")
	out, err := c.Output()
	if err != nil {
		return nil, "", fmt.Errorf("status --json: %w", err)
	}
	var st nodeStatusForDrive
	if err := json.Unmarshal(out, &st); err != nil {
		return nil, "", fmt.Errorf("decode status: %w", err)
	}
	byIP = make(map[string]string)
	for _, p := range st.Peer {
		short := nodeDNSShortName(p.DNSName)
		for _, ip := range p.TailscaleIPs {
			byIP[ip] = short
		}
	}
	return byIP, strings.TrimSuffix(st.MagicDNSSuffix, "."), nil
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
