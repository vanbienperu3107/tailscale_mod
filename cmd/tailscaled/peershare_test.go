// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_netstack

package main

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPeerFileShareHandler exercises the hybrid file share: GET downloads an
// existing file (http.FileServer), PUT uploads a new file (read-write WebDAV)
// that lands on disk, and OPTIONS advertises WebDAV so the share can be mapped
// as a network drive.
func TestPeerFileShareHandler(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("xin chao"), 0644); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	h := peerFileShareHandler(dir)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveHTTPOnConn(c, h)
		}
	}()
	base := "http://" + ln.Addr().String()
	client := &http.Client{Timeout: 5 * time.Second}

	// GET: download an existing file.
	resp, err := client.Get(base + "/hello.txt")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "xin chao" {
		t.Fatalf("GET body = %q, want %q", body, "xin chao")
	}

	// PUT: upload a new file via WebDAV; it must land on disk.
	req, _ := http.NewRequest(http.MethodPut, base+"/up.txt", strings.NewReader("data-moi"))
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 201/204", resp.StatusCode)
	}
	got, err := os.ReadFile(filepath.Join(dir, "up.txt"))
	if err != nil || string(got) != "data-moi" {
		t.Fatalf("uploaded file = %q, err = %v", got, err)
	}

	// OPTIONS: WebDAV detection must advertise the DAV header (for drive mapping).
	req, _ = http.NewRequest(http.MethodOptions, base+"/", nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS: %v", err)
	}
	resp.Body.Close()
	if resp.Header.Get("DAV") == "" {
		t.Fatalf("OPTIONS response missing DAV header (WebDAV not advertised)")
	}
}
