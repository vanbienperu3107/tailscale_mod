// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Xoay log theo ngày cho node launcher.
//
// Trước đây tailscaled.log và node-launcher.log mở bằng O_APPEND rồi ghi mãi:
// máy votam-pc tích 116 MB trong 5 ngày, mỗi lần đọc để chẩn đoán phải nuốt cả
// file. Ở đây launcher tự cầm đầu ghi (thay vì đưa thẳng fd cho tiến trình con),
// nhờ đó có thể đóng - đổi tên - mở lại tại thời khắc sang ngày, và dọn các file
// quá hạn. Tiến trình con không biết gì về việc này: nó vẫn ghi vào một pipe.

const (
	// nodeLogKeepDays: số ngày lưu file log đã xoay. Đủ dài để truy một sự cố
	// cuối tuần, đủ ngắn để không phình đĩa máy người dùng.
	nodeLogKeepDays = 14

	// nodeLogDayFormat là hậu tố ngày trong tên file đã xoay.
	nodeLogDayFormat = "2006-01-02"
)

// nodeLogArchiveName trả về tên file lưu trữ cho một ngày, ví dụ
// ("tailscaled.log", "2026-09-04") -> "tailscaled-2026-09-04.log".
func nodeLogArchiveName(base, day string) string {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return fmt.Sprintf("%s-%s%s", stem, day, ext)
}

// nodeLogDayOf trả về nhãn ngày (giờ địa phương) của một thời điểm.
func nodeLogDayOf(t time.Time) string {
	return t.Format(nodeLogDayFormat)
}

// nodeLogShouldRotate cho biết đã sang ngày mới so với nhãn đang ghi hay chưa.
func nodeLogShouldRotate(curDay string, now time.Time) bool {
	return curDay != "" && curDay != nodeLogDayOf(now)
}

// nodeLogExpiredFiles lọc ra các file lưu trữ của base đã quá hạn giữ.
//
// Chỉ nhận đúng dạng "<stem>-<YYYY-MM-DD><ext>"; tên lạ hoặc ngày không phân
// tích được thì bỏ qua, để không bao giờ xoá nhầm file của người khác.
func nodeLogExpiredFiles(names []string, base string, now time.Time, keepDays int) []string {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	prefix := stem + "-"
	cutoff := now.AddDate(0, 0, -keepDays)

	var out []string
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ext) {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ext)
		t, err := time.ParseInLocation(nodeLogDayFormat, day, now.Location())
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// nodeDailyLog là io.WriteCloser ghi vào <dir>/<base>, tự xoay sang
// <base>-<ngày hôm qua> khi sang ngày mới, và dọn file quá hạn.
//
// An toàn khi gọi từ nhiều goroutine: os/exec sao chép stdout và stderr của
// tiến trình con bằng hai goroutine riêng vào cùng writer này.
type nodeDailyLog struct {
	dir      string
	base     string
	keepDays int
	now      func() time.Time // thay được trong test

	mu  sync.Mutex
	f   *os.File
	day string
}

// nodeOpenDailyLog mở file log hiện hành trong dir.
//
// Nếu file sẵn có là của một ngày trước (theo thời gian sửa đổi), nó được xoay
// ngay — nhờ vậy một file khổng lồ tích từ trước cũng được cắt ở lần chạy đầu
// tiên sau khi cập nhật, không cần thao tác tay.
func nodeOpenDailyLog(dir, base string, keepDays int) (*nodeDailyLog, error) {
	l := &nodeDailyLog{dir: dir, base: base, keepDays: keepDays, now: time.Now}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, base)
	if st, err := os.Stat(path); err == nil {
		if day := nodeLogDayOf(st.ModTime()); day != nodeLogDayOf(l.now()) {
			os.Rename(path, filepath.Join(dir, nodeLogArchiveName(base, day)))
		}
	}
	if err := l.openLocked(); err != nil {
		return nil, err
	}
	l.prune()
	return l, nil
}

func (l *nodeDailyLog) openLocked() error {
	f, err := os.OpenFile(filepath.Join(l.dir, l.base), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	l.f = f
	l.day = nodeLogDayOf(l.now())
	return nil
}

// rotateLocked đóng file hiện tại, đổi tên theo ngày đang ghi rồi mở file mới.
func (l *nodeDailyLog) rotateLocked() error {
	oldDay := l.day
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
	path := filepath.Join(l.dir, l.base)
	if err := os.Rename(path, filepath.Join(l.dir, nodeLogArchiveName(l.base, oldDay))); err != nil && !os.IsNotExist(err) {
		// Không đổi tên được (file đang bị giữ, đĩa lỗi): vẫn mở lại để không
		// mất log, chỉ là chưa xoay được lần này.
		if oerr := l.openLocked(); oerr != nil {
			return oerr
		}
		return err
	}
	return l.openLocked()
}

func (l *nodeDailyLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if nodeLogShouldRotate(l.day, l.now()) {
		if err := l.rotateLocked(); err == nil {
			go l.prune()
		}
	}
	if l.f == nil {
		return len(p), nil // nuốt log còn hơn làm nghẽn tiến trình con
	}
	return l.f.Write(p)
}

func (l *nodeDailyLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// prune xoá các file lưu trữ quá hạn giữ.
func (l *nodeDailyLog) prune() {
	ents, err := os.ReadDir(l.dir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	for _, name := range nodeLogExpiredFiles(names, l.base, l.now(), l.keepDays) {
		os.Remove(filepath.Join(l.dir, name))
	}
}

// nodeLogWriter mở một writer xoay theo ngày, trả về io.Discard khi không mở
// được để chỗ gọi không phải xử lý lỗi (mất log tốt hơn là không chạy được).
func nodeLogWriter(dir, base string) io.WriteCloser {
	l, err := nodeOpenDailyLog(dir, base, nodeLogKeepDays)
	if err != nil {
		return nopWriteCloser{io.Discard}
	}
	return l
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
