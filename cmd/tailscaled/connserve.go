// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_netstack

package main

import (
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// serveHTTPOnConn serves HTTP on a single already-accepted netstack conn using
// h, then cleans up. The conn is handled with keep-alive in a goroutine spawned
// by http.Server and lives until the peer closes it or it idles out.
//
// It lets the integrated peer features (HTTP proxy, file share) run a standard
// http.Handler over a raw tailnet conn handed out by netstack's
// GetTCPHandlerForFlow, without needing a real OS listener or `tailscale serve`.
func serveHTTPOnConn(c net.Conn, h http.Handler) {
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
var errOneConnDone = errors.New("connserve: one-shot listener drained")

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
