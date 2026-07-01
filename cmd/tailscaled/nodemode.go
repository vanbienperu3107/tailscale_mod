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
	if len(args) == 0 || args[0] == "run" || args[0] == "node" {
		runNodeLauncher()
		return true
	}
	switch args[0] {
	case "install":
		nodeInstall()
		return true
	case "stop", "down":
		nodeStop()
		return true
	}
	// Any other verb (status, ping, up, ...) -> tailscale CLI in this binary.
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

	// Start the daemon: this same binary, in userspace mode.
	d := exec.Command(exe,
		"--tun=userspace-networking",
		"--statedir="+stateDir,
		"--socks5-server="+nodeSocksAddr,
		"--verbose=1",
	)
	d.Env = env
	d.Stdout, d.Stderr = os.Stdout, os.Stderr
	nodeHideChildWindow(d)
	if err := d.Start(); err != nil {
		log.Fatalf("node: start daemon: %v", err)
	}
	log.Printf("node[%s]: daemon started (pid %d); bringing up against %s", nodeMode, d.Process.Pid, nodeLoginServer)

	// Bring the node up (retry until the daemon is ready). OIDC login prints a
	// URL on the console; --unattended keeps it connected across restarts.
	upArgs := []string{"up", "--unattended", "--accept-routes", "--login-server=" + nodeLoginServer}
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

// nodeInstall registers the launcher to run at login/boot. Fleshed out in
// Stage 3 (Task Scheduler on Windows, systemd/user unit on Linux).
func nodeInstall() {
	log.Printf("node: autostart install not implemented yet (Stage 3). Run the exe directly for now.")
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
