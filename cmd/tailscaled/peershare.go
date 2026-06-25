// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_netstack

// Integrated peer-facing file share.
//
// When TS_PEER_FILE_SHARE points at a directory, this node serves that folder
// to other tailnet peers, directly inside netstack on its own Tailscale IP at
// TS_PEER_FILE_SHARE_PORT (default 7656). A peer can:
//   - browse + download via a normal browser at http://<this-node-ip>:<port>/
//     (served by http.FileServer), or
//   - map it as a read-write network drive over WebDAV (PROPFIND/PUT/DELETE/...).
//
// Like the integrated HTTP proxy, it needs no `tailscale serve` LocalAPI call,
// so it is not blocked by Windows multi-user "server mode" ownership checks, and
// it only applies in userspace-networking mode (netstack handles inbound).

package main

import (
	"net"
	"net/http"
	"net/netip"
	"os"

	"golang.org/x/net/webdav"
	"tailscale.com/envknob"
	"tailscale.com/net/tsaddr"
	"tailscale.com/types/logger"
	"tailscale.com/wgengine/netstack"
)

var (
	// peerFileSharePath is the directory to share; empty disables the feature.
	peerFileSharePath = envknob.RegisterString("TS_PEER_FILE_SHARE")
	// peerFileSharePort overrides the tailnet-facing port; 0 uses the default.
	peerFileSharePort = envknob.RegisterInt("TS_PEER_FILE_SHARE_PORT")
)

const defaultPeerFileSharePort = 7656

func init() {
	hookSetupPeerFileShare = setupPeerFileShare
}

// setupPeerFileShare wires ns.GetTCPHandlerForFlow so that inbound tailnet TCP
// to this node's Tailscale IP on the share port is served by a hybrid
// browser+WebDAV file server for the configured directory. No-op when unset.
func setupPeerFileShare(logf logger.Logf, ns *netstack.Impl) {
	dir := peerFileSharePath()
	if dir == "" {
		return
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		logf("peershare: TS_PEER_FILE_SHARE=%q is not a usable directory: %v", dir, err)
		return
	}
	port := peerFileSharePort()
	if port <= 0 || port > 65535 {
		port = defaultPeerFileSharePort
	}
	pp := uint16(port)
	handler := peerFileShareHandler(dir)
	logf("peershare: integrated file share (browser + read-write WebDAV) for %q enabled on port %d", dir, pp)

	prev := ns.GetTCPHandlerForFlow
	ns.GetTCPHandlerForFlow = func(src, dst netip.AddrPort) (func(net.Conn), bool) {
		// Only claim connections to THIS node's own Tailscale IP on the share
		// port; everything else falls through to any previous handler.
		if dst.Port() == pp && tsaddr.IsTailscaleIP(dst.Addr()) {
			return func(c net.Conn) {
				go serveHTTPOnConn(c, handler)
			}, true
		}
		if prev != nil {
			return prev(src, dst)
		}
		return nil, false
	}
}

// peerFileShareHandler serves dir to tailnet peers. GET/HEAD go to
// http.FileServer (browser directory listing + downloads); all other methods
// (OPTIONS, PROPFIND, PROPPATCH, PUT, DELETE, MKCOL, MOVE, COPY, LOCK, UNLOCK)
// go to a read-write WebDAV handler so the share can also be mapped as a
// Windows network drive.
func peerFileShareHandler(dir string) http.Handler {
	fileServer := http.FileServer(http.Dir(dir))
	dav := &webdav.Handler{
		FileSystem: webdav.Dir(dir),
		LockSystem: webdav.NewMemLS(),
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			fileServer.ServeHTTP(w, r)
		default:
			dav.ServeHTTP(w, r)
		}
	})
}
