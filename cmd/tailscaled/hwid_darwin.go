// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build darwin

package main

import (
	"errors"
	"os/exec"
	"strings"
)

// machineHardwareID trên macOS đọc IOPlatformSerialNumber từ ioreg — số serial
// phần cứng của máy (giống "About This Mac"), ổn định qua cài lại và đổi thư
// mục, dùng làm hạt giống cho machine key để Mac giữ nguyên node/IP trên
// headscale. Không bao giờ ghi serial ra log (xem runNodeLauncher).
func machineHardwareID() (string, error) {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return "", err
	}
	return parseIOPlatformSerial(string(out))
}

// parseIOPlatformSerial tách giá trị "IOPlatformSerialNumber" = "XXXX" khỏi
// output của ioreg. Tách riêng để test được.
func parseIOPlatformSerial(ioregOut string) (string, error) {
	for _, line := range strings.Split(ioregOut, "\n") {
		if !strings.Contains(line, `"IOPlatformSerialNumber"`) {
			continue
		}
		_, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		serial := strings.Trim(strings.TrimSpace(val), `"`)
		if serial != "" {
			return serial, nil
		}
	}
	return "", errors.New("IOPlatformSerialNumber not found in ioreg output")
}
