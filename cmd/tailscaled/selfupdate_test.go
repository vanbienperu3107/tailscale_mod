// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package main

import "testing"

// TestNodeShouldSwapBuild khoá quy tắc "chỉ tiến lên" của tự-cập-nhật.
//
// Ca quan trọng nhất là "hạ cấp": trước khi có nodeShouldSwapBuild, điều kiện
// là `latest != current` nên một build cũ hơn cũng kích hoạt đổi binary. Hai
// build khác nhau thay nhau được công bố sẽ tạo vòng lặp hạ-rồi-nâng, mỗi vòng
// restart launcher và rớt kết nối. Test này phải fail nếu ai đó đổi lại về `!=`.
func TestNodeShouldSwapBuild(t *testing.T) {
	tests := []struct {
		name            string
		latest, current int
		want            bool
	}{
		{"build mới hơn thì cập nhật", 112, 111, true},
		{"nhảy nhiều build vẫn cập nhật", 130, 100, true},
		{"cùng build thì đứng yên", 111, 111, false},
		{"build cũ hơn KHÔNG được hạ cấp", 108, 111, false},
		{"ca thực tế 2026-09-04: 111 đang chạy, dashboard trả 108", 108, 111, false},
		{"dashboard không trả build", 0, 111, false},
		{"binary không nhúng build", 112, 0, false},
		{"cả hai đều trống", 0, 0, false},
		{"giá trị âm bị loại", -1, 111, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeShouldSwapBuild(tt.latest, tt.current); got != tt.want {
				t.Errorf("nodeShouldSwapBuild(latest=%d, current=%d) = %v, muốn %v",
					tt.latest, tt.current, got, tt.want)
			}
		})
	}
}

// TestNodeShouldSwapBuildNoFlapLoop mô phỏng đúng vòng lặp đã quan sát được:
// hai build thay nhau được công bố. Với quy tắc "chỉ tiến lên", client dừng ở
// build cao nhất và không đổi nữa, thay vì đổi qua lại vô hạn.
func TestNodeShouldSwapBuildNoFlapLoop(t *testing.T) {
	const high, low = 111, 108
	current := high
	swaps := 0
	// Dashboard công bố xen kẽ low/high nhiều lần.
	for i := 0; i < 10; i++ {
		announced := low
		if i%2 == 1 {
			announced = high
		}
		if nodeShouldSwapBuild(announced, current) {
			current = announced
			swaps++
		}
	}
	if swaps != 0 {
		t.Errorf("đã đổi build %d lần; phải đứng yên ở build cao nhất", swaps)
	}
	if current != high {
		t.Errorf("build hiện tại = %d, muốn %d", current, high)
	}
}
