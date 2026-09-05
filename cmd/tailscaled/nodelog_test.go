// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNodeLogArchiveName(t *testing.T) {
	tests := []struct {
		base, day, want string
	}{
		{"tailscaled.log", "2026-09-04", "tailscaled-2026-09-04.log"},
		{"node-launcher.log", "2026-01-31", "node-launcher-2026-01-31.log"},
		{"noext", "2026-09-04", "noext-2026-09-04"},
	}
	for _, tt := range tests {
		if got := nodeLogArchiveName(tt.base, tt.day); got != tt.want {
			t.Errorf("nodeLogArchiveName(%q, %q) = %q, muốn %q", tt.base, tt.day, got, tt.want)
		}
	}
}

func TestNodeLogShouldRotate(t *testing.T) {
	day := func(s string) time.Time {
		t2, err := time.ParseInLocation(nodeLogDayFormat, s, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return t2
	}
	tests := []struct {
		name   string
		curDay string
		now    time.Time
		want   bool
	}{
		{"cùng ngày", "2026-09-04", day("2026-09-04").Add(23 * time.Hour), false},
		{"sang ngày mới", "2026-09-04", day("2026-09-05"), true},
		{"máy tắt vài ngày rồi bật lại", "2026-09-01", day("2026-09-05"), true},
		{"chưa có nhãn ngày", "", day("2026-09-05"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeLogShouldRotate(tt.curDay, tt.now); got != tt.want {
				t.Errorf("nodeLogShouldRotate(%q, %v) = %v, muốn %v", tt.curDay, tt.now, got, tt.want)
			}
		})
	}
}

// TestNodeLogExpiredFiles khoá quy tắc dọn file: chỉ đụng đúng file lưu trữ của
// chính base, đúng dạng ngày, và chỉ khi đã quá hạn giữ.
func TestNodeLogExpiredFiles(t *testing.T) {
	now, err := time.ParseInLocation(nodeLogDayFormat, "2026-09-20", time.Local)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{
		"tailscaled.log",              // file hiện hành, không bao giờ xoá
		"tailscaled-2026-09-19.log",   // hôm qua
		"tailscaled-2026-09-07.log",   // đúng 13 ngày, còn trong hạn
		"tailscaled-2026-09-05.log",   // 15 ngày, quá hạn
		"tailscaled-2026-08-01.log",   // rất cũ, quá hạn
		"node-launcher-2026-08-01.log", // file của base khác, không được đụng
		"tailscaled-backup.log",        // không phải dạng ngày
		"important-notes.txt",          // file lạ
	}
	got := nodeLogExpiredFiles(names, "tailscaled.log", now, nodeLogKeepDays)
	want := []string{"tailscaled-2026-08-01.log", "tailscaled-2026-09-05.log"}
	if len(got) != len(want) {
		t.Fatalf("nodeLogExpiredFiles = %v, muốn %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("nodeLogExpiredFiles = %v, muốn %v", got, want)
		}
	}
}

// TestNodeDailyLogRotatesOnDayChange kiểm tra vòng đời thật trên đĩa: ghi hôm
// nay, sang ngày mới thì nội dung cũ nằm trong file có nhãn ngày và file hiện
// hành bắt đầu lại từ rỗng.
func TestNodeDailyLogRotatesOnDayChange(t *testing.T) {
	dir := t.TempDir()
	cur := time.Date(2026, 9, 4, 10, 0, 0, 0, time.Local)

	l, err := nodeOpenDailyLog(dir, "tailscaled.log", nodeLogKeepDays)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.now = func() time.Time { return cur }
	l.day = nodeLogDayOf(cur)

	if _, err := l.Write([]byte("dòng của ngày 4\n")); err != nil {
		t.Fatal(err)
	}

	cur = cur.AddDate(0, 0, 1) // sang ngày mới
	if _, err := l.Write([]byte("dòng của ngày 5\n")); err != nil {
		t.Fatal(err)
	}

	archived, err := os.ReadFile(filepath.Join(dir, "tailscaled-2026-09-04.log"))
	if err != nil {
		t.Fatalf("không tìm thấy file đã xoay: %v", err)
	}
	if string(archived) != "dòng của ngày 4\n" {
		t.Errorf("file đã xoay chứa %q", archived)
	}

	current, err := os.ReadFile(filepath.Join(dir, "tailscaled.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "dòng của ngày 5\n" {
		t.Errorf("file hiện hành chứa %q, muốn chỉ có dòng của ngày mới", current)
	}
}

// TestNodeDailyLogRotatesStaleFileOnOpen mô phỏng đúng tình trạng máy votam:
// một file khổng lồ tích từ những ngày trước. Lần mở đầu tiên sau khi cập nhật
// phải cắt nó ra, không cần ai thao tác tay.
func TestNodeDailyLogRotatesStaleFileOnOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tailscaled.log")
	if err := os.WriteFile(path, []byte("log tích từ hôm trước\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yesterday := time.Now().AddDate(0, 0, -1)
	if err := os.Chtimes(path, yesterday, yesterday); err != nil {
		t.Fatal(err)
	}

	l, err := nodeOpenDailyLog(dir, "tailscaled.log", nodeLogKeepDays)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	archive := filepath.Join(dir, nodeLogArchiveName("tailscaled.log", nodeLogDayOf(yesterday)))
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("file cũ chưa được xoay khi mở: %v", err)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cur) != 0 {
		t.Errorf("file hiện hành phải rỗng sau khi xoay, đang có %d byte", len(cur))
	}
}

// TestNodeDailyLogPrunesOldArchives kiểm tra việc dọn file quá hạn khi mở.
func TestNodeDailyLogPrunesOldArchives(t *testing.T) {
	dir := t.TempDir()
	old := nodeLogArchiveName("tailscaled.log", nodeLogDayOf(time.Now().AddDate(0, 0, -nodeLogKeepDays-5)))
	keep := nodeLogArchiveName("tailscaled.log", nodeLogDayOf(time.Now().AddDate(0, 0, -2)))
	for _, n := range []string{old, keep} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	l, err := nodeOpenDailyLog(dir, "tailscaled.log", nodeLogKeepDays)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
		t.Errorf("file quá hạn %s chưa bị xoá", old)
	}
	if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
		t.Errorf("file còn trong hạn %s bị xoá nhầm", keep)
	}
}

// TestNodeDailyLogConcurrentWrites: os/exec sao chép stdout và stderr bằng hai
// goroutine riêng vào cùng writer này, nên nó phải chịu được ghi song song.
func TestNodeDailyLogConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	l, err := nodeOpenDailyLog(dir, "tailscaled.log", nodeLogKeepDays)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	done := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				l.Write([]byte("dòng log\n"))
			}
		}()
	}
	<-done
	<-done

	b, err := os.ReadFile(filepath.Join(dir, "tailscaled.log"))
	if err != nil {
		t.Fatal(err)
	}
	if want := 400 * len("dòng log\n"); len(b) != want {
		t.Errorf("ghi được %d byte, muốn %d", len(b), want)
	}
}
