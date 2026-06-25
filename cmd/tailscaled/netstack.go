// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_netstack

package main

import (
	"context"
	"expvar"
	"net"
	"net/netip"

	"tailscale.com/net/tsdial"
	"tailscale.com/tsd"
	"tailscale.com/types/logger"
	"tailscale.com/wgengine/netstack"
)

// hookSetupPeerHTTPProxy, when non-nil, wires the integrated peer-facing HTTP
// proxy onto the freshly created netstack Impl. It is set by peerproxy.go's
// init when that file is compiled in (default builds); it stays nil when the
// outbound-proxy or netstack features are omitted.
var hookSetupPeerHTTPProxy func(logf logger.Logf, ns *netstack.Impl, dialer *tsdial.Dialer)

func init() {
	hookNewNetstack.Set(newNetstack)
}

func newNetstack(logf logger.Logf, sys *tsd.System, onlyNetstack bool) (tsd.NetstackImpl, error) {
	ns, err := netstack.Create(logf,
		sys.Tun.Get(),
		sys.Engine.Get(),
		sys.MagicSock.Get(),
		sys.Dialer.Get(),
		sys.DNSManager.Get(),
		sys.ProxyMapper(),
	)
	if err != nil {
		return nil, err
	}
	// Only register debug info if we have a debug mux
	if debugMux != nil {
		expvar.Publish("netstack", ns.ExpVar())
	}

	sys.Set(ns)
	ns.ProcessLocalIPs = onlyNetstack
	ns.ProcessSubnets = onlyNetstack || handleSubnetsInNetstack()

	dialer := sys.Dialer.Get() // must be set by caller already

	// Integrated peer-facing HTTP proxy (TS_PEER_HTTP_PROXY). No-op unless the
	// env knob is set; only effective in userspace-networking mode.
	if hookSetupPeerHTTPProxy != nil {
		hookSetupPeerHTTPProxy(logf, ns, dialer)
	}

	if onlyNetstack {
		e := sys.Engine.Get()
		dialer.UseNetstackForIP = func(ip netip.Addr) bool {
			_, ok := e.PeerForIP(ip)
			return ok
		}
		dialer.NetstackDialTCP = func(ctx context.Context, dst netip.AddrPort) (net.Conn, error) {
			// Note: don't just return ns.DialContextTCP or we'll return
			// *gonet.TCPConn(nil) instead of a nil interface which trips up
			// callers.
			tcpConn, err := ns.DialContextTCP(ctx, dst)
			if err != nil {
				return nil, err
			}
			return tcpConn, nil
		}
		dialer.NetstackDialUDP = func(ctx context.Context, dst netip.AddrPort) (net.Conn, error) {
			// Note: don't just return ns.DialContextUDP or we'll return
			// *gonet.UDPConn(nil) instead of a nil interface which trips up
			// callers.
			udpConn, err := ns.DialContextUDP(ctx, dst)
			if err != nil {
				return nil, err
			}
			return udpConn, nil
		}
	}

	return ns, nil
}
