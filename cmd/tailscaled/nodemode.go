// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

// Combined single-file "node" launcher.
//
// When this binary is built with -tags ts_include_cli AND -ldflags sets
// main.nodeMode ("proxy" or "portable"), the SAME executable is daemon + CLI +
// launcher in one file:
//
//   <exe>            -> node launcher: start the daemon (this binary) with the
//                       baked env in the default mode, then bring the node up
//                       against the self-hosted control server. Double-click to run.
//   <exe> tun|vpn    -> run as a real VPN interface (OS ping + all apps route
//                       via the tailnet; Windows needs the embedded wintun).
//   <exe> userspace  -> run userspace (no driver; use `<exe> ping` / SOCKS5).
//   <exe> install    -> register autostart.
//   <exe> uninstall  -> bring the node down and remove autostart.
//   <exe> stop       -> bring the node down.
//   <exe> <cli...>    -> pass through to the tailscale CLI (status, ping, ...).
//
// For normal (non-node) builds nodeMode is "" and maybeRunNode is a no-op, so
// tailscaled behaves exactly as upstream.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"tailscale.com/hostinfo"
	"tailscale.com/tailcfg"
)

// Set at build time via -ldflags "-X main.nodeMode=proxy" etc.
var (
	nodeMode        = ""                                // "", "proxy", "portable"
	nodeLoginServer = "https://vpn2.hangocthanh.io.vn"  // headscale control
	nodeMetricsURL  = "https://vpn2.hangocthanh.io.vn/app"
	nodeLANRoutes   = "10.0.0.0/8" // advertised in proxy mode
	// nodeDefaultMode is the network mode used when the user passes no mode
	// argument: "userspace" (SOCKS5, no driver) or "tun" (real VPN interface).
	// The wintun-embedded "vpn" build bakes "tun"; others default to userspace.
	nodeDefaultMode = "userspace"
	// nodeAcceptRoutes controls whether the node accepts subnet routes advertised
	// by other nodes (e.g. the itop/proxy node's LAN). ON by default — like the
	// stock client — so advertised subnets are reachable; the OS keeps the local
	// /24 more specific than a broad advertised /8, so internet still works.
	// Override per-machine with `accept_routes = off` in node.conf if a machine's
	// own network genuinely overlaps an advertised route.
	nodeAcceptRoutes = true
)

const (
	nodeSocksAddr   = "127.0.0.1:7654"
	nodePeerProxy   = "7655"
	nodeDERPKeepSec = "25"
	nodeUpRetries   = 15
)

// Report this node's primary MAC via Hostinfo.WoLMACs so headscale can, at
// registration time, look up a reserved/historical tailnet IP for this
// hardware (see CMS GET /api/internal/reserved-ip) — nodeKey/MachineKey
// change on every reinstall, MAC does not. WoLMACs is a real upstream
// tailcfg field (Wake-on-LAN targets) that headscale never reads; reusing it
// avoids forking tailcfg.Hostinfo itself, which would force headscale's
// go.mod to replace its pinned upstream tailscale.com dependency — a much
// bigger, riskier change. Gated on nodeMode != "" (node-launcher builds
// only) — a normal/default tailscaled build never sets this, so a stock
// client talking to the same headscale is completely unaffected.
func init() {
	hostinfo.RegisterHostinfoNewHook(func(hi *tailcfg.Hostinfo) {
		if nodeMode == "" {
			return
		}
		if mac := primaryMAC(); mac != "" {
			hi.WoLMACs = []string{mac}
		}
	})

	// Run the dashboard integration (folder-share reconcile+report, folder
	// browse, runtime config, device-register) INSIDE the always-on daemon
	// process, not the launcher. The launcher is a foreground wrapper a user
	// can close (its window); the daemon (tailscaled) keeps running and is what
	// stays "on". Before, these pollers lived only in the launcher, so closing
	// it silently stopped folder-sharing while the daemon looked online. Only
	// the daemon invocation of a node build runs them (see nodeIsDaemonProc);
	// the launcher no longer starts them, and short-lived CLI subprocesses
	// (`<exe> status`, `<exe> drive share`, spawned BY these loops) skip it, so
	// there's no duplication or recursion.
	if nodeMode != "" && nodeMetricsURL != "" && nodeIsDaemonProc() {
		go nodeDaemonDashboardLoops()
	}
}

// nodeIsDaemonProc reports whether THIS process is the tailscaled daemon
// invocation — the launcher runs the same binary with tailscaled flags
// (`--tun`, `--statedir`, ...), so the first arg starts with '-'. The launcher
// itself (no args or a mode verb like "vpn"/"portable"/"run") and CLI
// subprocesses ("status", "drive", ...) do not, and thus return false.
func nodeIsDaemonProc() bool {
	a := os.Args[1:]
	return len(a) > 0 && strings.HasPrefix(a[0], "-")
}

// nodeDaemonDashboardLoops waits for the daemon's LocalAPI to accept commands,
// then starts the dashboard pollers in-process so they live exactly as long as
// the daemon (surviving the launcher being closed). The pollers drive the local
// `<exe>` CLI (drive share, status, net use), which needs the LocalAPI up —
// hence the readiness wait. Best-effort and capped; each poller is itself
// idempotent and retry-safe on its own ticker.
func nodeDaemonDashboardLoops() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("node: daemon dashboard: cannot find own path: %v", err)
		return
	}
	// Wait (capped ~3min) until `<exe> status` succeeds — i.e. the LocalAPI is
	// serving — so the pollers' CLI calls don't all fail on the first ticks.
	ready := false
	for i := 0; i < 90; i++ {
		time.Sleep(2 * time.Second)
		c := exec.Command(exe, "status")
		c.Env = append(os.Environ(), "TS_BE_CLI=1")
		if c.Run() == nil {
			ready = true
			break
		}
	}
	if !ready {
		log.Printf("node: daemon dashboard: LocalAPI not ready after wait; starting pollers anyway (they self-retry)")
	}
	log.Printf("node: daemon-side dashboard pollers starting (dashboard=%s)", nodeMetricsURL)
	go nodeRuntimePollLoop(exe, nodeLANRoutes)
	go nodeBrowsePollLoop()
	go nodeRegisterDeviceIdentity(exe)
}

// maybeRunNode handles the node-launcher subcommands for node-mode builds.
// Returns true if it handled the invocation (caller should return).
func maybeRunNode() bool {
	if nodeMode == "" {
		return false // normal build: untouched
	}
	args := os.Args[1:]
	// Daemon re-exec: the launcher runs this same binary with tailscaled flags
	// (--tun, --statedir, ...). Anything starting with '-' is a daemon
	// invocation -> hand back to the normal tailscaled path, do NOT treat as CLI.
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return false
	}
	if len(args) == 0 || args[0] == "run" || args[0] == "node" {
		runNodeLauncher(nodeWantsTun("")) // default mode (baked / env)
		return true
	}
	switch args[0] {
	case "install":
		nodeInstall()
		return true
	case "uninstall", "remove":
		nodeUninstall()
		return true
	case "tun", "vpn", "full":
		// Real VPN interface: OS ping + all apps route via the tailnet.
		runNodeLauncher(true)
		return true
	case "userspace", "user":
		// No driver: connectivity via `<exe> ping` / SOCKS5 127.0.0.1:7654.
		runNodeLauncher(false)
		return true
	case "stop":
		// Note: only "stop" — "down" falls through to the CLI below so nodeStop
		// (which runs `<exe> down`) does not recurse back into itself.
		nodeStop()
		return true
	}
	switch args[0] {
	case "serve-taildrive", "be-child":
		// tailscaled-internal subcommands: the Taildrive owner file-server
		// backend (spawned as `<exe> serve-taildrive <name> <path>...`) and
		// childproc. These are neither node verbs nor tailscale CLI verbs —
		// hand them back to tailscaled's normal subcommand dispatch, or the
		// owner's WebDAV backend never starts and accessors get HTTP 500 on
		// mount (surfacing as Windows "System error 59"). Note: we do NOT
		// blanket-forward every subCommands key here — "debug" is also a CLI
		// verb (the node uses `<exe> debug prefs` with TS_BE_CLI=1), so an
		// explicit allowlist avoids changing its routing.
		return false
	}
	// Any other verb (status, ping, up, down, ...) -> tailscale CLI in this binary.
	if beCLI != nil {
		beCLI()
		return true
	}
	return false
}

// nodeWantsTun resolves the network mode. explicit is "" for the default
// invocation (use the baked nodeDefaultMode, overridable by TS_NODE_TUN=full),
// or a mode word from the command line.
func nodeWantsTun(explicit string) bool {
	m := explicit
	if m == "" {
		m = nodeDefaultMode
		if os.Getenv("TS_NODE_TUN") == "full" {
			m = "tun"
		}
	}
	switch m {
	case "tun", "full", "vpn":
		return true
	default:
		return false
	}
}

func runNodeLauncher(tun bool) {
	// The Windows node exe carries a requireAdministrator manifest, so Windows
	// elevates it at launch (one UAC prompt) and the daemon child inherits admin
	// — needed to create the LocalAPI named pipe. No runtime self-relaunch.
	// Free the pipe from any previously-running tailscaled first (Windows only).
	nodeKillConflicting()

	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("node: cannot find own path: %v", err)
	}
	dir := filepath.Dir(exe)
	logDir := filepath.Join(dir, "state", "logs")

	// This launcher runs unattended via Task Scheduler (nodeInstall), with no
	// console to see — without this, every "node: ..." line below (self-update,
	// folder-share, browse-poll, runtime poll) went to a stdout nobody captured.
	// tailscaled.log next to it is the CHILD daemon's own networking log; this
	// process never wrote to it. MultiWriter keeps console output too, for
	// interactive/manual runs.
	if err := os.MkdirAll(logDir, 0o700); err == nil {
		if lf, lerr := os.OpenFile(filepath.Join(logDir, "node-launcher.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); lerr == nil {
			log.SetOutput(io.MultiWriter(os.Stderr, lf))
		}
	}

	nodeLoadConfig(dir) // node.conf next to the exe can override the control host

	// Log the running build so testers can see which version a machine is on
	// (empty on non-versioned/dev builds).
	if nodeBuild != "" || nodeVersion != "" {
		log.Printf("node: running build %s (%s), variant %s", nodeBuild, nodeVersion, nodeVariant)
	}

	// Auto-update: before starting the daemon, pull a newer/admin-pinned build
	// (if enabled) and re-exec into it. No-op on non-versioned/dev builds or when
	// the dashboard has auto-update off (globally or for this machine specifically
	// — see checkAndSelfUpdate); any failure just proceeds with this exe.
	nodeCleanupOldExe(exe)
	if checkAndSelfUpdate(nodeMetricsURL, metricsReportSecret(), exe, primaryMAC()) {
		nodeRestartSelf(exe) // does not return
	}

	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		log.Fatalf("node: mkdir state: %v", err)
	}

	// TUN mode on Windows needs wintun.dll next to the exe. The "vpn" build
	// embeds it (extracted here); other builds error out with a clear message.
	if tun && runtime.GOOS == "windows" {
		if err := nodeEnsureWintun(dir); err != nil {
			log.Fatalf("node: TUN mode needs wintun.dll: %v", err)
		}
	}

	// Daemon environment (baked per variant).
	env := append(os.Environ(), "TS_METRICS_REPORT="+nodeMetricsURL)

	// Deterministic machine identity: seed the daemon's machine key from this
	// PC's stable hardware serial so it keeps the SAME headscale node (and pinned
	// tailnet IP) across state wipes, exe-dir moves and reinstalls — the
	// random-per-install machine key was the root cause of IP drift (itop
	// .19→.21, votam .20→.22). Applies to BOTH modes. The daemon only honors this
	// when it has no stored key yet (existing state always wins — see
	// ipnlocal.initMachineKeyLocked), so it never fights a machine that's already
	// registered. Best-effort: unreadable/empty serial → no seed → upstream random
	// key (drift accepted, logged). We log only the serial's length, never the
	// serial itself — it can reproduce the private machine key.
	if serial, herr := machineHardwareID(); herr != nil {
		log.Printf("node: machine-key seed: could not read hardware serial (%v); using random machine key (identity may drift)", herr)
	} else if seed := machineKeySeed(serial); seed != "" {
		env = append(env, "TS_MACHINE_KEY_SEED="+seed)
		log.Printf("node: machine-key seed: deterministic identity from hardware serial (serial len=%d)", len(serial))
	} else {
		log.Printf("node: machine-key seed: hardware serial empty; using random machine key (identity may drift)")
	}

	if nodeMode == "proxy" {
		env = append(env,
			"TS_PEER_HTTP_PROXY="+nodePeerProxy,
			"TS_DEBUG_ALWAYS_USE_DERP=1",
			"TS_DERP_KEEPALIVE_SECS="+nodeDERPKeepSec,
		)
		if pc := filepath.Join(dir, "proxy.conf"); nodeFileExists(pc) {
			env = append(env, "TS_PROXY_CONF="+pc)
		}
	}

	// Self-update pollers stay in the launcher: applying an update means
	// re-exec'ing a new binary, which is the launcher's job (it supervises and
	// restarts the daemon). The dashboard pollers that must survive the launcher
	// being closed — runtime config, folder-share reconcile+report, folder
	// browse, device-register — now run INSIDE the daemon instead (see init's
	// nodeDaemonDashboardLoops), so folder-sharing keeps working headless even
	// after this launcher window is closed.
	go nodeUpdateLoop(exe)
	go nodeUpdateSignalPollLoop(exe)

	nodeRunDaemonSupervised(exe, stateDir, logDir, env, tun)
}

// nodeRunDaemonSupervised starts the daemon and blocks forever, restarting it
// with backoff on any unexpected exit instead of letting the whole launcher
// (and every poller started above) die with it. Before this, a daemon crash
// — self-update race, transient OS/network issue, anything — silently killed
// the entire node with nothing left running to notice or recover; an admin
// had to find the dead process and relaunch it by hand.
func nodeRunDaemonSupervised(exe, stateDir, logDir string, env []string, tun bool) {
	const base, max = 2 * time.Second, 30 * time.Second
	delay := time.Duration(0)
	for {
		start := time.Now()
		err := nodeRunDaemonOnce(exe, stateDir, logDir, env, tun)
		ranFor := time.Since(start)
		if err == nil {
			log.Printf("node: daemon exited cleanly after %s", ranFor.Round(time.Second))
		} else {
			log.Printf("node: daemon exited unexpectedly after %s: %v", ranFor.Round(time.Second), err)
		}
		delay = daemonRestartDelay(delay, ranFor, base, max)
		log.Printf("node: restarting daemon in %s…", delay)
		time.Sleep(delay)
		// NOTE: no nodeKillConflicting() here — the daemon we just waited on has
		// already exited (it can't still hold the LocalAPI pipe), and killing by
		// image name on every restart risks taking out a legitimate concurrent
		// instance. Cross-instance contention is handled once at launcher start
		// (nodeKillConflicting in runNodeLauncher), not on the hot restart path.
	}
}

// daemonRestartDelay decides how long to wait before restarting the daemon,
// given the previous delay and how long the just-exited run lasted. A run that
// survived at least `max` is treated as healthy and resets to `base` (a lone
// crash shouldn't leave every future restart stuck at the ceiling); otherwise
// the delay doubles from `base` up to `max`. Pure — unit-tested.
func daemonRestartDelay(prev, ranFor, base, max time.Duration) time.Duration {
	if ranFor >= max {
		return base
	}
	next := prev * 2
	if next < base {
		next = base
	}
	if next > max {
		next = max
	}
	return next
}

// nodeRunDaemonOnce starts one daemon instance, brings it up, and blocks until
// it exits. Returns the daemon's own exit error (nil on a clean/expected
// shutdown, e.g. an explicit `<exe> stop` or `down` from outside this process).
func nodeRunDaemonOnce(exe, stateDir, logDir string, env []string, tun bool) error {
	// Start the daemon: this same binary. tun=false → userspace (no driver,
	// SOCKS5 at nodeSocksAddr, works everywhere); tun=true → a real VPN
	// interface (Windows: wintun; Linux: kernel TUN, needs root/CAP_NET_ADMIN)
	// so the OS routes the tailnet and normal `ping`/apps work.
	//
	// --no-logs-no-support disables logtail: the node never uploads logs to
	// log.tailscale.com. Combined with the headscale control server (vpn2), the
	// node talks only to your own host — no phone-home to tailscale.com. (The
	// derp*.tailscale.com bootstrap-DNS lines only appear as a fallback when the
	// OS DNS is down; with normal connectivity they never fire.)
	dArgs := []string{"--statedir=" + stateDir, "--verbose=1", "--no-logs-no-support"}
	if tun {
		dArgs = append(dArgs, "--tun=tailscale0")
	} else {
		dArgs = append(dArgs, "--tun=userspace-networking", "--socks5-server="+nodeSocksAddr)
	}
	modeName := "userspace"
	if tun {
		modeName = "tun"
	}
	d := exec.Command(exe, dArgs...)
	d.Env = env
	// Daemon logs to a file so the node can run windowless; the interactive
	// OIDC login URL still prints to this launcher's console (up child, below).
	if lf, lerr := os.OpenFile(filepath.Join(logDir, "tailscaled.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); lerr == nil {
		d.Stdout, d.Stderr = lf, lf
	} else {
		d.Stdout, d.Stderr = os.Stdout, os.Stderr
	}
	nodeHideChildWindow(d)
	if err := d.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	log.Printf("node[%s/%s]: daemon started (pid %d); bringing up against %s", nodeMode, modeName, d.Process.Pid, nodeLoginServer)

	// Bring the node up (retry until the daemon is ready). OIDC login prints a
	// URL on the console; --unattended keeps it connected across restarts.
	//
	// accept-routes ON (default) makes advertised subnets (itop/proxy LAN) usable
	// like the stock client — the OS keeps the local /24 more specific than a
	// broad advertised /8, so internet stays up. Set `accept_routes = off` in
	// node.conf for a machine whose own network truly overlaps an advertised route.
	acceptFlag := "--accept-routes=false"
	if nodeAcceptRoutes {
		acceptFlag = "--accept-routes"
	}
	upArgs := []string{"up", acceptFlag, "--login-server=" + nodeLoginServer}
	if runtime.GOOS == "windows" {
		upArgs = append(upArgs, "--unattended") // keep connected when logged out
	}
	if nodeMode == "proxy" {
		upArgs = append(upArgs, "--advertise-routes="+nodeLANRoutes)
	}
	var upErr error
	for i := 0; i < nodeUpRetries; i++ {
		time.Sleep(2 * time.Second)
		c := exec.Command(exe, upArgs...)
		c.Env = append(os.Environ(), "TS_BE_CLI=1")
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if upErr = c.Run(); upErr == nil {
			break
		}
	}
	if upErr != nil {
		log.Printf("node: 'up' did not complete: %v (daemon still running)", upErr)
	} else {
		log.Printf("node: connected.")
	}

	return d.Wait()
}

// nodeInstall registers the launcher to run at login/boot.
func nodeInstall() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("install: cannot find own path: %v", err)
	}
	switch runtime.GOOS {
	case "windows":
		// Scheduled task at logon, highest privileges (no UAC prompt at start).
		c := exec.Command("schtasks", "/Create", "/TN", "TailscaleNode",
			"/TR", `"`+exe+`"`, "/SC", "ONLOGON", "/RL", "HIGHEST", "/F")
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			log.Fatalf("install: schtasks failed: %v", err)
		}
		log.Printf("node: autostart installed (Task Scheduler task 'TailscaleNode').")
		// Make the Taildrive network drives that the elevated daemon mounts via
		// WebDAV (Z:, M:, ...) visible in the interactive (non-elevated) user's
		// Explorer. Without EnableLinkedConnections Windows keeps the elevated
		// and non-elevated logon tokens' drive maps separate, so the user never
		// sees the shares the daemon mapped — the exact "net use shows Z: but
		// Explorer doesn't" confusion. Takes effect on the next reboot.
		// Best-effort: a failure never blocks autostart, and the share is still
		// reachable via its \\100.100.100.100@8080\... UNC regardless.
		rc := exec.Command("reg", "add",
			`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`,
			"/v", "EnableLinkedConnections", "/t", "REG_DWORD", "/d", "1", "/f")
		rc.Stdout, rc.Stderr = os.Stdout, os.Stderr
		if err := rc.Run(); err != nil {
			log.Printf("node: could not set EnableLinkedConnections (%v); mapped Taildrive drives may only appear in an elevated Explorer until it is set manually", err)
		} else {
			log.Printf("node: EnableLinkedConnections=1 set — reboot once so mapped Taildrive drives (Z:, M:, ...) show in normal Explorer.")
		}
	case "linux":
		unit := "[Unit]\n" +
			"Description=Tailscale (mod) node\n" +
			"After=network-online.target\nWants=network-online.target\n\n" +
			"[Service]\nType=simple\nExecStart=" + exe + "\nRestart=always\nRestartSec=5\n\n" +
			"[Install]\nWantedBy=default.target\n"
		home, _ := os.UserHomeDir()
		udir := filepath.Join(home, ".config", "systemd", "user")
		if err := os.MkdirAll(udir, 0o755); err != nil {
			log.Fatalf("install: mkdir %s: %v", udir, err)
		}
		path := filepath.Join(udir, "tailscale-node.service")
		if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
			log.Fatalf("install: write unit: %v", err)
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		if err := exec.Command("systemctl", "--user", "enable", "--now", "tailscale-node.service").Run(); err != nil {
			log.Printf("node: unit written to %s but enable failed (%v); enable manually: systemctl --user enable --now tailscale-node.service", path, err)
			return
		}
		log.Printf("node: autostart installed (systemd --user unit %s).", path)
	default:
		log.Printf("node: autostart not supported on %s; schedule the exe at boot yourself.", runtime.GOOS)
	}
}

// nodeUninstall reverses nodeInstall: brings the node down and removes the
// autostart entry. Node state (login/keys under <exe>/state) is left in place;
// delete that folder to fully wipe. Best-effort — missing entries are ignored.
func nodeUninstall() {
	nodeStop() // bring the node down first (best-effort)
	switch runtime.GOOS {
	case "windows":
		c := exec.Command("schtasks", "/Delete", "/TN", "TailscaleNode", "/F")
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			log.Printf("node: no autostart task to remove (or delete failed: %v).", err)
		} else {
			log.Printf("node: autostart removed (Task Scheduler task 'TailscaleNode' deleted).")
		}
		// Best-effort: stop any daemon still holding the LocalAPI pipe.
		nodeKillConflicting()
	case "linux":
		_ = exec.Command("systemctl", "--user", "disable", "--now", "tailscale-node.service").Run()
		home, _ := os.UserHomeDir()
		path := filepath.Join(home, ".config", "systemd", "user", "tailscale-node.service")
		if err := os.Remove(path); err != nil {
			log.Printf("node: no unit file to remove (or remove failed: %v).", err)
		} else {
			log.Printf("node: autostart removed (systemd --user unit deleted).")
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	default:
		log.Printf("node: nothing to uninstall on %s.", runtime.GOOS)
	}
	exe, _ := os.Executable()
	log.Printf("node: uninstalled. State kept at %s; delete it to fully reset.", filepath.Join(filepath.Dir(exe), "state"))
}

// nodeStop brings the node down via the built-in CLI.
func nodeStop() {
	exe, _ := os.Executable()
	c := exec.Command(exe, "down")
	c.Env = append(os.Environ(), "TS_BE_CLI=1")
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	_ = c.Run()
}

// nodeLoadConfig reads an optional node.conf next to the exe and overrides the
// baked control host / metrics URL / advertised routes. Format: simple
// key=value lines, '#' or ';' comments. Missing file → keep baked defaults, so
// the single exe still works with nothing alongside it. Recognised keys:
//
//	login_server = https://your-host        # headscale control URL
//	metrics_url  = https://your-host/app    # dashboard base (blank = disable)
//	advertise_routes = 10.0.0.0/8           # proxy mode only
func nodeLoadConfig(dir string) {
	for _, name := range []string{"node.conf", "node.config", "config.txt"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k = strings.ToLower(strings.TrimSpace(k))
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			switch k {
			case "login_server", "login-server", "control", "server", "host":
				if v != "" {
					nodeLoginServer = v
				}
			case "metrics_url", "metrics-url", "metrics":
				nodeMetricsURL = v // blank disables the reporter
			case "advertise_routes", "advertise-routes", "lan_routes":
				if v != "" {
					nodeLANRoutes = v
				}
			case "accept_routes", "accept-routes":
				switch strings.ToLower(v) {
				case "off", "false", "0", "no", "n":
					nodeAcceptRoutes = false
				case "on", "true", "1", "yes", "y":
					nodeAcceptRoutes = true
				}
			}
		}
		log.Printf("node: loaded config %s (control=%s)", name, nodeLoginServer)
		return
	}
}

func nodeFileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

const nodeRuntimePollInterval = 20 * time.Second

// nodeRuntimeResponse is the subset of GET /api/client/runtime this launcher
// acts on. advertise_routes lets the CMS change a proxy node's advertised LAN
// without a rebuild/restart; reload_at is bumped by the dashboard's "Reload"
// button to force a re-apply even when advertise_routes itself is unchanged.
// shares/mounts drive the per-PC folder-share feature — see foldershare.go.
type nodeRuntimeResponse struct {
	AdvertiseRoutes string             `json:"advertise_routes"`
	ReloadAt        string             `json:"reload_at"`
	// UpdateCheckAt is bumped by the dashboard "Cập nhật ngay" button; when it
	// changes to a value we haven't acted on, the launcher runs the self-update
	// check immediately (instead of waiting for the 6h periodic loop).
	UpdateCheckAt string             `json:"update_check_at"`
	Shares        []nodeShareDesired `json:"shares"`
	Mounts        []nodeMountDesired `json:"mounts"`
}

// nodeRuntimePollLoop polls the dashboard for runtime overrides and applies
// changes live via `<exe> set --advertise-routes=...` — no daemon/launcher
// restart needed. Fail-open: a dashboard that is down or unreachable just
// means the node keeps running with whatever it already applied. Failures are
// NOT silent, though — logged once immediately and then rate-limited (1, 5,
// 20, 100, ... consecutive failures), so "poll never succeeds" (e.g. wrong
// URL, 401 from a configured dashboard secret) is diagnosable from the log
// without spamming it every 20s.
func nodeRuntimePollLoop(exe, initialRoutes string) {
	if nodeMetricsURL == "" {
		log.Printf("node: runtime poll: disabled (no dashboard URL baked in)")
		return
	}
	mac := primaryMAC()
	if mac == "" {
		log.Printf("node: runtime poll: disabled (could not determine primary MAC)")
		return
	}
	hostname, _ := os.Hostname() // display hint for the folder-share status report
	log.Printf("node: runtime poll: starting (dashboard=%s mac=%s, every %s)", nodeMetricsURL, mac, nodeRuntimePollInterval)

	client := &http.Client{Timeout: 5 * time.Second}
	lastRoutes := initialRoutes
	lastReloadAt := ""
	consecutiveFails := 0
	loggedFirstSuccess := false

	t := time.NewTicker(nodeRuntimePollInterval)
	defer t.Stop()
	for {
		<-t.C
		resp, err := nodeFetchRuntime(client, mac)
		if err != nil {
			consecutiveFails++
			if consecutiveFails == 1 || consecutiveFails == 5 || consecutiveFails%20 == 0 {
				log.Printf("node: runtime poll: failed (%d in a row): %v", consecutiveFails, err)
			}
			continue
		}
		if consecutiveFails > 0 {
			log.Printf("node: runtime poll: recovered after %d failed attempts", consecutiveFails)
		}
		consecutiveFails = 0
		if !loggedFirstSuccess {
			log.Printf("node: runtime poll: dashboard reachable (advertise_routes=%q)", resp.AdvertiseRoutes)
			loggedFirstSuccess = true
		}

		// Folder-share (Taildrive): reconcile every tick, not gated behind
		// nodeShouldReapply — these are independently idempotent (each call
		// re-derives current state and only acts on a diff) and must
		// self-heal if a share/mount was removed by something other than us.
		// nodeBrowsePoll runs on its own faster ticker (nodeBrowsePollLoop),
		// not here — see that function for why.
		shareSt := nodeReconcileShares(exe, resp.Shares)
		mountSt := nodeReconcileMounts(exe, resp.Mounts)
		// Report serve/mount result (incl. errors like "System error 67") so
		// the dashboard shows per-PC folder-share health. Best-effort — a
		// report failure never affects reconcile or connectivity.
		if err := nodeReportFolderShareStatus(nodeMetricsURL, mac, hostname, shareSt, mountSt); err != nil {
			log.Printf("node: folder-share: report status failed: %v", err)
		}

		// Update-now push (dashboard "Cập nhật ngay") is handled by its own
		// fast poll — nodeUpdateSignalPollLoop, felt in ~3s instead of waiting
		// for this 20s tick.

		if !nodeShouldReapply(resp, lastRoutes, lastReloadAt) {
			continue
		}
		if err := nodeApplyAdvertiseRoutes(exe, resp.AdvertiseRoutes); err != nil {
			log.Printf("node: runtime reload: apply advertise-routes failed: %v", err)
			continue
		}
		log.Printf("node: runtime reload: advertise-routes=%q applied", resp.AdvertiseRoutes)
		lastRoutes = resp.AdvertiseRoutes
		lastReloadAt = resp.ReloadAt
	}
}

// nodeShouldReapply decides whether a poll result warrants re-applying
// config: either advertise_routes actually changed, or the dashboard's
// "Reload" button bumped reload_at to a value we haven't acted on yet (an
// explicit force, even when advertise_routes itself is unchanged). Pure —
// unit-tested without a network.
func nodeShouldReapply(resp nodeRuntimeResponse, lastRoutes, lastReloadAt string) bool {
	changed := resp.AdvertiseRoutes != lastRoutes
	reloaded := resp.ReloadAt != "" && resp.ReloadAt != lastReloadAt
	return changed || reloaded
}

func nodeFetchRuntime(client *http.Client, mac string) (nodeRuntimeResponse, error) {
	url := nodeMetricsURL + "/api/client/runtime?mac=" + mac
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nodeRuntimeResponse{}, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nodeRuntimeResponse{}, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nodeRuntimeResponse{}, fmt.Errorf("status %d from %s: %s", resp.StatusCode, url, string(body))
	}
	var out nodeRuntimeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nodeRuntimeResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// nodeApplyAdvertiseRoutes changes the running node's advertised routes
// without a full `up`/restart. Proxy variant only — a no-op empty value on
// other variants is harmless (they never advertise routes anyway).
func nodeApplyAdvertiseRoutes(exe, routes string) error {
	c := exec.Command(exe, "set", "--advertise-routes="+routes)
	c.Env = append(os.Environ(), "TS_BE_CLI=1")
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

// nodeStatusSelf is the subset of `tailscale status --json` this launcher
// reads to learn its own NodeKey + current tailnet IPv4 once `up` has
// completed.
type nodeStatusSelf struct {
	Self struct {
		PublicKey    string   `json:"PublicKey"`
		TailscaleIPs []string `json:"TailscaleIPs"`
	} `json:"Self"`
}

// nodeOwnStatus shells out to `<exe> status --json` and returns
// (Self.PublicKey, first IPv4 in Self.TailscaleIPs). Retries briefly because
// the daemon may not have these published in the first second or two right
// after `up` returns.
func nodeOwnStatus(exe string) (nodeKey, ipv4 string, err error) {
	var lastErr error
	for i := 0; i < 5; i++ {
		if i > 0 {
			time.Sleep(2 * time.Second)
		}
		c := exec.Command(exe, "status", "--json")
		c.Env = append(os.Environ(), "TS_BE_CLI=1")
		out, cmdErr := c.Output()
		if cmdErr != nil {
			lastErr = fmt.Errorf("status --json: %w", cmdErr)
			continue
		}
		var st nodeStatusSelf
		if decodeErr := json.Unmarshal(out, &st); decodeErr != nil {
			lastErr = fmt.Errorf("decode status: %w", decodeErr)
			continue
		}
		if st.Self.PublicKey != "" {
			var ip string
			for _, a := range st.Self.TailscaleIPs {
				if !strings.Contains(a, ":") { // skip IPv6
					ip = a
					break
				}
			}
			return st.Self.PublicKey, ip, nil
		}
		lastErr = fmt.Errorf("empty Self.PublicKey")
	}
	return "", "", lastErr
}

// nodeRegisterDeviceIdentity reports (mac, hostname, nodeKey) to the dashboard
// once per launcher run so it can dedup/rename by stable MAC instead of the
// legacy (user, hostname) text match. Best-effort/fire-and-forget: logged on
// failure but never blocks or retries the node's own connectivity.
func nodeRegisterDeviceIdentity(exe string) {
	if nodeMetricsURL == "" {
		return
	}
	mac := primaryMAC()
	if mac == "" {
		log.Printf("node: device-register: skipped (could not determine primary MAC)")
		return
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		log.Printf("node: device-register: skipped (could not determine hostname: %v)", err)
		return
	}
	nodeKey, ipv4, err := nodeOwnStatus(exe)
	if err != nil {
		log.Printf("node: device-register: could not determine node key: %v", err)
	}

	body, _ := json.Marshal(struct {
		Mac      string `json:"mac"`
		Hostname string `json:"hostname"`
		NodeKey  string `json:"node_key"`
		IPv4     string `json:"ipv4"`
		Version  string `json:"version,omitempty"`
		Build    int    `json:"build,omitempty"`
		Variant  string `json:"variant,omitempty"`
	}{
		Mac:      mac,
		Hostname: hostname,
		NodeKey:  nodeKey,
		IPv4:     ipv4,
		Version:  nodeVersion,
		Build:    nodeCurrentBuild(),
		Variant:  nodeVariant,
	})
	url := nodeMetricsURL + "/api/internal/device-register"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		log.Printf("node: device-register: build request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("node: device-register: request failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		log.Printf("node: device-register: status %d from %s: %s", resp.StatusCode, url, string(respBody))
		return
	}
	log.Printf("node: device-register: reported mac=%s hostname=%s", mac, hostname)
}
