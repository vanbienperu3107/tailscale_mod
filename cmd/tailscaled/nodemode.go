// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

// Combined single-file "node" launcher.
//
// When this binary is built with -tags ts_include_cli AND -ldflags sets
// main.nodeMode ("proxy" or "portable"), the SAME executable is daemon + CLI +
// launcher in one file:
//
//   <exe>            -> node launcher: start the daemon (this binary, userspace)
//                       with the baked env, then bring the node up against the
//                       self-hosted control server. Just double-click to run.
//   <exe> install    -> register autostart (Stage 3).
//   <exe> stop       -> bring the node down.
//   <exe> <cli...>    -> pass through to the tailscale CLI (status, ping, ...).
//
// For normal (non-node) builds nodeMode is "" and maybeRunNode is a no-op, so
// tailscaled behaves exactly as upstream.

package main

import (
	"log"
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
		runNodeLauncher()
		return true
	}
	switch args[0] {
	case "install":
		nodeInstall()
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

func runNodeLauncher() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("node: cannot find own path: %v", err)
	}
	dir := filepath.Dir(exe)
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		log.Fatalf("node: mkdir state: %v", err)
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

	// Start the daemon: this same binary. TUN mode: userspace by default (no
	// root, works everywhere); TS_NODE_TUN=full uses a real TUN interface
	// (Linux, needs root/CAP_NET_ADMIN — a real VPN interface).
	dArgs := []string{"--statedir=" + stateDir, "--verbose=1"}
	if os.Getenv("TS_NODE_TUN") == "full" {
		dArgs = append(dArgs, "--tun=tailscale0")
	} else {
		dArgs = append(dArgs, "--tun=userspace-networking", "--socks5-server="+nodeSocksAddr)
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
	log.Printf("node[%s]: daemon started (pid %d); bringing up against %s", nodeMode, d.Process.Pid, nodeLoginServer)

	// Bring the node up (retry until the daemon is ready). OIDC login prints a
	// URL on the console; --unattended keeps it connected across restarts.
	upArgs := []string{"up", "--accept-routes", "--login-server=" + nodeLoginServer}
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

// nodeStop brings the node down via the built-in CLI.
func nodeStop() {
	exe, _ := os.Executable()
	c := exec.Command(exe, "down")
	c.Env = append(os.Environ(), "TS_BE_CLI=1")
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	_ = c.Run()
}

func nodeFileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
