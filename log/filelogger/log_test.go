// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package filelogger

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestNewHonorsTSLogsDir(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("filelogger is only supported on Windows")
	}
	dir := t.TempDir()
	t.Setenv("TS_LOGS_DIR", dir)

	// New logs "local disk logdir: <dir>"; capture it to confirm the directory
	// was taken from TS_LOGS_DIR (without opening a log file, which would keep a
	// handle open and break TempDir cleanup on Windows).
	var gotDir string
	New("tailscale-test", "TESTLOGID", func(format string, a ...any) {
		if msg := fmt.Sprintf(format, a...); strings.HasPrefix(msg, "local disk logdir: ") {
			gotDir = strings.TrimPrefix(msg, "local disk logdir: ")
		}
	})
	if gotDir != dir {
		t.Fatalf("filelogger dir = %q; want TS_LOGS_DIR %q", gotDir, dir)
	}
}

func TestRemoveDatePrefix(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"\n", "\n"},
		{"2009/01/23 01:23:23", "2009/01/23 01:23:23"},
		{"2009/01/23 01:23:23 \n", "\n"},
		{"2009/01/23 01:23:23 foo\n", "foo\n"},
		{"9999/01/23 01:23:23 foo\n", "foo\n"},
		{"2009_01/23 01:23:23 had an underscore\n", "2009_01/23 01:23:23 had an underscore\n"},
	}
	for i, tt := range tests {
		got := removeDatePrefix([]byte(tt.in))
		if string(got) != tt.want {
			t.Logf("[%d] removeDatePrefix(%q) = %q; want %q", i, tt.in, got, tt.want)
		}
	}

}
