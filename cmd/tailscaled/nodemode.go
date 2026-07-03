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
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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
	nodeLoadConfig(dir) // node.conf next to the exe can override the control host
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
	logDir := filepath.Join(stateDir, "logs")
	_ = os.MkdirAll(logDir, 0o700)
	if lf, lerr := os.OpenFile(filepath.Join(logDir, "tailscaled.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); lerr == nil {
		d.Stdout, d.Stderr = lf, lf
	} else {
		d.Stdout, d.Stderr = os.Stdout, os.Stderr
	}
	nodeHideChildWindow(d)
	if err := d.Start(); err != nil {
		log.Fatalf("node: start daemon: %v", err)
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

	// Poll the dashboard for runtime overrides (advertise_routes) + "Reload"
	// requests from the CMS, so an admin change takes effect without a node
	// restart. Only meaningful for the proxy variant (advertise-routes is a
	// no-op elsewhere), but harmless to run always — keeps "Reload" working
	// for future fields too.
	go nodeRuntimePollLoop(exe, nodeLANRoutes)

	// Keep the launcher alive holding the daemon; exiting would leave the daemon
	// orphaned but running — waiting keeps logs and lifecycle together.
	if err := d.Wait(); err != nil {
		log.Printf("node: daemon exited: %v", err)
	}
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
type nodeRuntimeResponse struct {
	AdvertiseRoutes string `json:"advertise_routes"`
	ReloadAt        string `json:"reload_at"`
}

// nodeRuntimePollLoop polls the dashboard for runtime overrides and applies
// changes live via `<exe> set --advertise-routes=...` — no daemon/launcher
// restart needed. Fail-open: a dashboard that is down or unreachable just
// means the node keeps running with whatever it already applied; the loop
// quietly retries next tick instead of logging on every failure.
func nodeRuntimePollLoop(exe, initialRoutes string) {
	if nodeMetricsURL == "" {
		return
	}
	mac := primaryMAC()
	if mac == "" {
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	lastRoutes := initialRoutes
	lastReloadAt := ""

	t := time.NewTicker(nodeRuntimePollInterval)
	defer t.Stop()
	for {
		<-t.C
		resp, ok := nodeFetchRuntime(client, mac)
		if !ok {
			continue
		}
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

func nodeFetchRuntime(client *http.Client, mac string) (nodeRuntimeResponse, bool) {
	url := nodeMetricsURL + "/api/client/runtime?mac=" + mac
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nodeRuntimeResponse{}, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return nodeRuntimeResponse{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nodeRuntimeResponse{}, false
	}
	var out nodeRuntimeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nodeRuntimeResponse{}, false
	}
	return out, true
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
