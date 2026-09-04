// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"slices"
	"strings"
	"testing"
)

// TestNodeAutostartArgs khoá hình dạng lệnh schtasks mà autostart phụ thuộc vào.
//
// Ba tính chất phải giữ:
//   - /F: đăng ký lại đè lên task cũ. Thiếu nó, một task trỏ bản exe đã bị xóa
//     sẽ tồn tại mãi và máy im lặng không khởi động gì — tệ hơn là không có task.
//   - ONLOGON + HIGHEST: chạy khi đăng nhập, đủ quyền tạo LocalAPI pipe.
//   - Đường dẫn exe bọc trong ngoặc kép: đường dẫn Windows hay có dấu cách
//     (C:\Program Files\..., C:\Users\Ha Ngoc\...), không bọc là schtasks hiểu sai.
func TestNodeAutostartArgs(t *testing.T) {
	const exe = `C:\Users\Ha Ngoc Thanh\Downloads\tailscale-node-vpn.exe`
	args := nodeAutostartArgs(exe)

	for _, want := range []string{"/Create", "/F", "ONLOGON", "HIGHEST", nodeAutostartTaskName} {
		if !slices.Contains(args, want) {
			t.Errorf("thiếu %q trong đối số schtasks: %v", want, args)
		}
	}

	i := slices.Index(args, "/TR")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("không có /TR kèm giá trị: %v", args)
	}
	target := args[i+1]
	if !strings.HasPrefix(target, `"`) || !strings.HasSuffix(target, `"`) {
		t.Errorf("đường dẫn exe phải bọc ngoặc kép (đường dẫn có dấu cách), có: %s", target)
	}
	if !strings.Contains(target, exe) {
		t.Errorf("đối số /TR = %s, phải chứa đường dẫn exe %s", target, exe)
	}
}

// TestNodeAutostartArgsFollowsExePath: mỗi lần chạy phải trỏ về ĐÚNG file đang
// chạy, vì bản cập nhật thường nằm ở thư mục khác. Nếu đối số không đổi theo
// exe thì autostart sẽ mắc kẹt ở bản cũ.
func TestNodeAutostartArgsFollowsExePath(t *testing.T) {
	a := nodeAutostartArgs(`C:\old\tailscale-node-vpn.exe`)
	b := nodeAutostartArgs(`C:\new\tailscale-node-vpn.exe`)
	if slices.Equal(a, b) {
		t.Fatal("đối số không đổi theo đường dẫn exe; autostart sẽ kẹt ở bản cũ")
	}
	ia, ib := slices.Index(a, "/TR"), slices.Index(b, "/TR")
	if a[ia+1] == b[ib+1] {
		t.Errorf("đích của task không đổi: %s", a[ia+1])
	}
}
