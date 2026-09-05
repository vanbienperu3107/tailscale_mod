// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package magicsock

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestDerpPingErrIsNotConnected khoá việc nhận diện lỗi "chưa nối xong".
//
// derphttp.Client.SendPing trả về đúng chuỗi này tức thì khi client == nil, tức
// bước connect (timeout 10s) vẫn đang chạy. Nếu nhận nhầm lỗi này là "node
// chết" thì fast-ping (2s/lần) sẽ giết mọi kết nối đang dở.
func TestDerpPingErrIsNotConnected(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"chưa nối xong", errors.New("client not connected"), true},
		{"chưa nối xong, có bọc thêm", errors.New("derp-1001: client not connected"), true},
		{"pong quá hạn", context.DeadlineExceeded, false},
		{"kết nối bị reset", errors.New("read: connection reset by peer"), false},
		{"EOF", errors.New("readFrameHeader: unexpected EOF"), false},
		{"không lỗi", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := derpPingErrIsNotConnected(tt.err); got != tt.want {
				t.Errorf("derpPingErrIsNotConnected(%v) = %v, muốn %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestDerpShouldCloseOnPingFail mô phỏng đúng vòng lặp đã quan sát trên
// votam-pc: connect tới derp-1001 mất hơn 2 giây trên đường Bitel chậm, trong
// khi fast-ping hỏi mỗi 2 giây và nhận "client not connected".
//
// Trước khi có derpConnectGrace, mỗi lần hỏi như vậy đóng luôn kết nối đang
// thiết lập (log ghi "closing connection to derp-1001 (fast-ping-fail), age
// 5s"), nên connect không bao giờ kịp hoàn tất. Test này phải fail nếu ai đó bỏ
// ân hạn hoặc đặt nó ngắn hơn timeout connect 10s của derphttp.
func TestDerpShouldCloseOnPingFail(t *testing.T) {
	notConnected := errors.New("client not connected")
	base := time.Unix(1_700_000_000, 0)

	tests := []struct {
		name      string
		err       error
		elapsed   time.Duration
		wantClose bool
	}{
		{"đang nối, mới 2s -> chờ", notConnected, 2 * time.Second, false},
		{"đang nối, 8s -> vẫn chờ (connect timeout 10s chưa hết)", notConnected, 8 * time.Second, false},
		{"đang nối, vừa chạm ân hạn -> đóng", notConnected, derpConnectGrace, true},
		{"đang nối, quá ân hạn -> đóng", notConnected, 30 * time.Second, true},
		{"pong quá hạn -> đóng ngay", context.DeadlineExceeded, 0, true},
		{"kết nối reset -> đóng ngay", errors.New("connection reset by peer"), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := derpShouldCloseOnPingFail(tt.err, base, base.Add(tt.elapsed))
			if got != tt.wantClose {
				t.Errorf("derpShouldCloseOnPingFail(%v, sau %v) = %v, muốn %v",
					tt.err, tt.elapsed, got, tt.wantClose)
			}
		})
	}
}

// TestDerpConnectGraceExceedsConnectTimeout khoá bất biến quan trọng: ân hạn
// phải dài hơn timeout connect của derphttp (10s), nếu không kết nối vẫn bị
// giết trước khi kịp xong và bản vá thành vô nghĩa.
func TestDerpConnectGraceExceedsConnectTimeout(t *testing.T) {
	const derphttpConnectTimeout = 10 * time.Second
	if derpConnectGrace <= derphttpConnectTimeout {
		t.Fatalf("derpConnectGrace = %v, phải lớn hơn timeout connect của derphttp (%v)",
			derpConnectGrace, derphttpConnectTimeout)
	}
}

// TestDerpSlowConnectSurvivesFastPing chạy mô phỏng theo thời gian: connect mất
// 6 giây, fast-ping hỏi mỗi 2 giây. Kết nối phải sống tới lúc hoàn tất.
func TestDerpSlowConnectSurvivesFastPing(t *testing.T) {
	const connectDuration = 6 * time.Second
	notConnected := errors.New("client not connected")
	base := time.Unix(1_700_000_000, 0)

	var failSince time.Time
	var hasFail bool
	for elapsed := derpFastPingInterval; elapsed <= 20*time.Second; elapsed += derpFastPingInterval {
		now := base.Add(elapsed)
		if elapsed >= connectDuration {
			// Connect đã xong, ping thành công từ đây.
			return
		}
		if !hasFail {
			failSince, hasFail = now, true
		}
		if derpShouldCloseOnPingFail(notConnected, failSince, now) {
			t.Fatalf("kết nối bị đóng ở giây thứ %v, trong khi connect cần %v", elapsed, connectDuration)
		}
	}
	t.Fatal("vòng mô phỏng kết thúc mà connect chưa hoàn tất")
}

// TestDerpRegionIsDead khoá ngưỡng loại region khỏi fallback.
func TestDerpRegionIsDead(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name       string
		hasFail    bool
		elapsed    time.Duration
		wantIsDead bool
	}{
		{"đang khoẻ", false, 0, false},
		{"vừa fail vài giây", true, 5 * time.Second, false},
		{"fail 30s, vẫn trong hạn", true, 30 * time.Second, false},
		{"fail đúng ngưỡng", true, derpHomeDeadGrace, true},
		{"fail nhiều phút (ca votam 21:18-22:39)", true, 80 * time.Minute, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := derpRegionIsDead(base, tt.hasFail, base.Add(tt.elapsed))
			if got != tt.wantIsDead {
				t.Errorf("derpRegionIsDead(fail=%v, sau %v) = %v, muốn %v",
					tt.hasFail, tt.elapsed, got, tt.wantIsDead)
			}
		})
	}
}

// TestPickDERPFallbackID khoá hành vi chọn region dự phòng.
//
// Ca quan trọng nhất: region home đã chết hẳn thì PHẢI chọn region khác. Trước
// bản vá, pickDERPFallback luôn trả về c.myDerp nên votam-pc bám derp-1001 suốt
// hơn một giờ dù mọi probe tới nó đều timeout.
func TestPickDERPFallbackID(t *testing.T) {
	alwaysFirst := func(n int) int { return 0 }

	tests := []struct {
		name     string
		ids      []int
		myDerp   int
		dead     bool
		want     int
		wantNot  int
		hasNotWa bool
	}{
		{name: "không có region nào", ids: nil, myDerp: 1001, want: 0},
		{name: "home còn sống thì giữ nguyên", ids: []int{1001, 1003}, myDerp: 1001, dead: false, want: 1001},
		{name: "chưa có home thì chọn từ danh sách", ids: []int{1001, 1003}, myDerp: 0, want: 1001},
		{name: "home chết -> chọn region khác", ids: []int{1001, 1003}, myDerp: 1001, dead: true, want: 1003},
		{name: "home chết, chỉ còn chính nó -> đành giữ", ids: []int{1001}, myDerp: 1001, dead: true, want: 1001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickDERPFallbackID(tt.ids, tt.myDerp, tt.dead, alwaysFirst)
			if got != tt.want {
				t.Errorf("pickDERPFallbackID(%v, myDerp=%d, dead=%v) = %d, muốn %d",
					tt.ids, tt.myDerp, tt.dead, got, tt.want)
			}
		})
	}
}

// TestPickDERPFallbackIDNeverReturnsDeadWhenAlternativesExist quét mọi vị trí
// của region chết trong danh sách nhiều region, đảm bảo không có thứ tự nào làm
// lọt region chết ra ngoài.
func TestPickDERPFallbackIDNeverReturnsDeadWhenAlternativesExist(t *testing.T) {
	ids := []int{999, 1001, 1003, 2000}
	const dead = 1003
	for pick := 0; pick < len(ids); pick++ {
		got := pickDERPFallbackID(ids, dead, true, func(n int) int { return pick % n })
		if got == dead {
			t.Errorf("với pickRandom=%d, đã chọn lại region chết %d", pick, dead)
		}
	}
}
