// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_netstack && !ts_omit_outboundproxy

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// TestOneConnListener verifies the listener yields its conn exactly once and
// then reports done, so http.Server.Serve stops after the single connection.
func TestOneConnListener(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	l := &oneConnListener{conn: c2}
	got, err := l.Accept()
	if err != nil {
		t.Fatalf("first Accept: %v", err)
	}
	if got != c2 {
		t.Fatalf("first Accept returned wrong conn")
	}
	if _, err := l.Accept(); err != errOneConnDone {
		t.Fatalf("second Accept = %v, want errOneConnDone", err)
	}
}

// TestServePeerHTTPProxyConn_HTTP exercises the plain-HTTP (absolute-URL) proxy
// path: a client configured to use our conn as an HTTP proxy fetches a URL, and
// the request is dialed out via the (stubbed) user dialer to a backend origin.
func TestServePeerHTTPProxyConn_HTTP(t *testing.T) {
	const want = "hello-from-origin"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, want)
	}))
	defer backend.Close()
	backendAddr := backend.Listener.Addr().String()

	// User dialer stub: ignore the requested host, always reach the backend.
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", backendAddr)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		serveHTTPOnConn(c, httpProxyHandler(dial))
	}()

	proxyURL, _ := url.Parse("http://" + ln.Addr().String())
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   5 * time.Second,
	}
	resp, err := client.Get("http://origin.invalid/")
	if err != nil {
		t.Fatalf("proxied GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

// TestServePeerHTTPProxyConn_CONNECT exercises the CONNECT tunnel path used by
// HTTPS: the client issues CONNECT, receives 200, then exchanges raw bytes that
// are proxied to a TCP echo backend reached via the (stubbed) user dialer.
func TestServePeerHTTPProxyConn_CONNECT(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go io.Copy(c, c)
		}
	}()
	echoAddr := echoLn.Addr().String()

	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", echoAddr)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		serveHTTPOnConn(c, httpProxyHandler(dial))
	}()

	pc, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	pc.SetDeadline(time.Now().Add(5 * time.Second))

	fmt.Fprintf(pc, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echoAddr, echoAddr)
	br := bufio.NewReader(pc)
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil {
		t.Fatalf("reading CONNECT response: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
	}

	const ping = "ping123"
	if _, err := io.WriteString(pc, ping); err != nil {
		t.Fatalf("write through tunnel: %v", err)
	}
	buf := make([]byte, len(ping))
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("read echo through tunnel: %v", err)
	}
	if string(buf) != ping {
		t.Fatalf("echo = %q, want %q", buf, ping)
	}
}
