// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Autostart trên macOS: một LaunchDaemon hệ thống (chạy root, vì chế độ VPN
// cần tạo giao diện utun). Cùng triết lý "một file lo trọn" như Windows:
// chạy binary bằng sudo là tự đăng ký lại LaunchDaemon trỏ đúng file đó.
const (
	nodeLaunchdLabel = "io.hangocthanh.tailscale-node"
	nodeLaunchdPath  = "/Library/LaunchDaemons/" + nodeLaunchdLabel + ".plist"
)

// nodeTunName là tên giao diện TUN mà daemon được yêu cầu tạo. macOS chỉ cho
// phép tên dạng utun (kernel tự cấp số: utun3, utun4...); Linux/Windows dùng
// tailscale0 như trước. Tách riêng để test.
func nodeTunName(goos string) string {
	if goos == "darwin" {
		return "utun"
	}
	return "tailscale0"
}

// nodeLaunchdPlist dựng nội dung plist cho LaunchDaemon: chạy exe khi boot,
// giữ sống (KeepAlive) để launchd tự khởi động lại nếu launcher chết, ghi
// stdout/stderr vào thư mục logs cạnh exe. Thuần hàm — unit test khoá cấu
// trúc (Label, ProgramArguments, RunAtLoad, KeepAlive, đường dẫn exe).
//
// TS_NODE_NO_AUTOSTART=1 trong môi trường của daemon: launcher do launchd
// khởi động không được tự ghi đè plist rồi bootout chính mình.
func nodeLaunchdPlist(exe string) string {
	logDir := filepath.Join(filepath.Dir(exe), "state", "logs")
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>WorkingDirectory</key>
	<string>%s</string>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>TS_NODE_NO_AUTOSTART</key>
		<string>1</string>
	</dict>
</dict>
</plist>
`, nodeLaunchdLabel, xmlEscape(exe), xmlEscape(filepath.Dir(exe)),
		xmlEscape(filepath.Join(logDir, "launchd.out.log")),
		xmlEscape(filepath.Join(logDir, "launchd.err.log")))
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// nodeDarwinIsRoot: chế độ VPN trên macOS bắt buộc root (utun + socket
// /var/run/tailscaled.sock). Launcher kiểm tra sớm để báo đúng lệnh sudo thay
// vì để daemon chết với lỗi khó hiểu.
func nodeDarwinIsRoot() bool { return runtime.GOOS == "darwin" && os.Geteuid() == 0 }

// nodeInstallLaunchd ghi plist và nạp vào launchd (bootout trước nếu đã có để
// "xoá tạo lại" giống /F của schtasks). Chỉ chạy được với root.
func nodeInstallLaunchd(exe string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("cần quyền root: chạy lại bằng `sudo %s install`", exe)
	}
	if err := os.WriteFile(nodeLaunchdPath, []byte(nodeLaunchdPlist(exe)), 0o644); err != nil {
		return fmt.Errorf("ghi %s: %w", nodeLaunchdPath, err)
	}
	// bootout cũ (bỏ qua lỗi "không có") rồi bootstrap mới.
	_ = exec.Command("launchctl", "bootout", "system/"+nodeLaunchdLabel).Run()
	out, err := exec.Command("launchctl", "bootstrap", "system", nodeLaunchdPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// nodeUninstallLaunchd gỡ LaunchDaemon (best-effort).
func nodeUninstallLaunchd() error {
	_ = exec.Command("launchctl", "bootout", "system/"+nodeLaunchdLabel).Run()
	if err := os.Remove(nodeLaunchdPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
