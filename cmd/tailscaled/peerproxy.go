// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_netstack && !ts_omit_outboundproxy

// Integrated peer-facing HTTP proxy.
//
// This is the built-in replacement for running an external gost.exe behind
// `tailscale serve`. When TS_PEER_HTTP_PROXY is set to a port, this node serves
// an HTTP/HTTPS forward proxy (CONNECT + absolute-URL) directly inside netstack
// on its own Tailscale IP at that port. Another tailnet peer points its browser
// (or PAC) at <this-node-tailscale-ip>:<port>; this node resolves the requested
// host with its OWN DNS/network and dials out via the user dialer. That makes it
// possible to reach intranet/split-horizon/geo-restricted sites (e.g.
// bitel.com.pe) that only this node can resolve and reach.
//
// Unlike `tailscale serve`, it needs no LocalAPI call and is therefore not
// blocked by Windows multi-user "server mode" ownership checks. It only applies
// in userspace-networking mode, where inbound tailnet connections to this node
// are handled by netstack.

package main

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"tailscale.com/envknob"
	"tailscale.com/net/tsaddr"
	"tailscale.com/net/tsdial"
	"tailscale.com/types/logger"
	"tailscale.com/wgengine/netstack"
)

// peerHTTPProxyPort, when >0, is the tailnet-facing port on which this node
// serves a built-in HTTP forward proxy for other peers. 0 (unset) disables it.
var peerHTTPProxyPort = envknob.RegisterInt("TS_PEER_HTTP_PROXY")

func init() {
	hookSetupPeerHTTPProxy = setupPeerHTTPProxy
}

// setupPeerHTTPProxy wires ns.GetTCPHandlerForFlow so that inbound tailnet TCP
// to this node's Tailscale IP on the configured port is served by the built-in
// HTTP proxy. It is a no-op when the port is unset/invalid.
func setupPeerHTTPProxy(logf logger.Logf, ns *netstack.Impl, dialer *tsdial.Dialer) {
	port := peerHTTPProxyPort()
	if port <= 0 || port > 65535 {
		return
	}
	pp := uint16(port)
	handler := httpProxyHandler(dialer.UserDial)
	logf("peerproxy: integrated HTTP proxy for tailnet peers enabled on port %d", pp)

	prev := ns.GetTCPHandlerForFlow
	ns.GetTCPHandlerForFlow = func(src, dst netip.AddrPort) (func(net.Conn), bool) {
		// Only claim connections addressed to THIS node's own Tailscale IP on
		// the proxy port. Subnet-routed traffic (private dst IPs) and every
		// other port fall through to any previous handler / default behavior.
		if dst.Port() == pp && tsaddr.IsTailscaleIP(dst.Addr()) {
			return func(c net.Conn) {
				go servePeerHTTPProxyConn(c, handler)
			}, true
		}
		if prev != nil {
			return prev(src, dst)
		}
		return nil, false
	}
}

// servePeerHTTPProxyConn serves HTTP (including CONNECT) on a single already
// accepted netstack conn using h. The conn is handled with keep-alive in a
// goroutine spawned by http.Server and lives until the peer closes it or it
// idles out.
func servePeerHTTPProxyConn(c net.Conn, h http.Handler) {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	_ = srv.Serve(&oneConnListener{conn: c})
}

// errOneConnDone is returned by oneConnListener.Accept after its single conn has
// been handed out, which makes http.Server.Serve return (its already-accepted
// conn keeps being served in its own goroutine).
var errOneConnDone = errors.New("peerproxy: one-shot listener drained")

// oneConnListener is a net.Listener that yields a single pre-accepted conn once
// and then reports done, so http.Server can serve an existing net.Conn.
type oneConnListener struct {
	mu   sync.Mutex
	conn net.Conn
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return nil, errOneConnDone
	}
	c := l.conn
	l.conn = nil
	return c, nil
}

func (l *oneConnListener) Close() error { return nil }

func (l *oneConnListener) Addr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn != nil {
		return l.conn.LocalAddr()
	}
	return &net.TCPAddr{}
}
