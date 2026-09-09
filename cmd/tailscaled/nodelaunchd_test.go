// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"strings"
	"testing"
)

func TestNodeTunName(t *testing.T) {
	if got := nodeTunName("darwin"); got != "utun" {
		t.Errorf("darwin: got %q, want utun (macOS chỉ nhận tên utun)", got)
	}
	for _, goos := range []string{"linux", "windows"} {
		if got := nodeTunName(goos); got != "tailscale0" {
			t.Errorf("%s: got %q, want tailscale0", goos, got)
		}
	}
}

// TestNodeLaunchdPlist khoá cấu trúc plist: đúng label, trỏ đúng exe, chạy khi
// boot, tự hồi sinh, và tắt nodeEnsureAutostart bên trong (tránh launcher do
// launchd khởi động lại tự ghi đè plist rồi bootout chính mình).
func TestNodeLaunchdPlist(t *testing.T) {
	exe := "/Users/thanh/tailscale-node/tailscale-node-vpn-darwin-arm64"
	p := nodeLaunchdPlist(exe)
	for _, want := range []string{
		"<string>" + nodeLaunchdLabel + "</string>",
		"<string>" + exe + "</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>KeepAlive</key>\n\t<true/>",
		"<key>TS_NODE_NO_AUTOSTART</key>",
		"/Users/thanh/tailscale-node/state/logs/launchd.err.log",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist thiếu %q:\n%s", want, p)
		}
	}
	if !strings.HasPrefix(p, "<?xml") || !strings.HasSuffix(strings.TrimSpace(p), "</plist>") {
		t.Errorf("plist không phải XML hoàn chỉnh")
	}
}

func TestNodeLaunchdPlistEscapesPath(t *testing.T) {
	p := nodeLaunchdPlist("/Volumes/Data & Apps/<node>/tailscale-node")
	if strings.Contains(p, "Data & Apps") || strings.Contains(p, "<node>") {
		t.Errorf("đường dẫn có ký tự đặc biệt phải được escape XML:\n%s", p)
	}
	if !strings.Contains(p, "Data &amp; Apps/&lt;node&gt;/tailscale-node") {
		t.Errorf("escape sai:\n%s", p)
	}
}

func TestNodeLaunchdPathIsSystemDaemon(t *testing.T) {
	if !strings.HasPrefix(nodeLaunchdPath, "/Library/LaunchDaemons/") {
		t.Errorf("VPN cần root nên phải là LaunchDaemon hệ thống, got %s", nodeLaunchdPath)
	}
}
