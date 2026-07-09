// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

// Regression tests for the two reporter bugs described in reporterutil.go:
// a MAC snapshotted as "" for the life of the process, and non-2xx responses
// (notably the dashboard's 400 on an empty MAC) being discarded silently.
//
// Test names start with "Metrics" so they match the -run filter the release
// workflow uses (see .github/workflows/build-windows-portable.yml).

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsMacResolverRetriesUntilNonEmpty(t *testing.T) {
	// The exact failure on VOTAM-PC: the NIC isn't up for the first few ticks,
	// so primaryMAC() returns "". The old code cached that "" forever.
	calls := 0
	m := macResolver{fn: func() string {
		calls++
		if calls < 3 {
			return ""
		}
		return "f8:cf:52:6f:84:70"
	}}

	if got := m.get(); got != "" {
		t.Fatalf("tick 1: got %q, want empty", got)
	}
	if got := m.get(); got != "" {
		t.Fatalf("tick 2: got %q, want empty", got)
	}
	if got := m.get(); got != "f8:cf:52:6f:84:70" {
		t.Fatalf("tick 3: got %q, want the real MAC", got)
	}
	if calls != 3 {
		t.Fatalf("resolver called %d times, want 3", calls)
	}

	// Once resolved it must be cached, not re-resolved every tick.
	if got := m.get(); got != "f8:cf:52:6f:84:70" {
		t.Fatalf("tick 4: got %q", got)
	}
	if calls != 3 {
		t.Fatalf("resolver called %d times after caching, want 3", calls)
	}
}

func TestMetricsCheckPostResponse(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr bool
		wantIn  string
	}{
		{name: "200 ok", status: 200, body: `{"ok":true}`},
		{name: "204 ok", status: 204},
		// This is the response the dashboard actually returns for mac:"" —
		// previously swallowed, leaving the node silently unreported for 32h.
		{
			name:    "400 empty mac surfaces body",
			status:  400,
			body:    `{"error":{"fieldErrors":{"mac":["String must contain at least 1 character(s)"]}}}`,
			wantErr: true,
			wantIn:  "at least 1 character",
		},
		{name: "401 unauthorized", status: 401, body: `unauthorized`, wantErr: true, wantIn: "status 401"},
		{name: "500 server error", status: 500, body: `boom`, wantErr: true, wantIn: "status 500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				if tt.body != "" {
					w.Write([]byte(tt.body))
				}
			}))
			defer srv.Close()

			resp, err := srv.Client().Get(srv.URL)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			err = checkPostResponse(resp, "home-derp")
			if tt.wantErr != (err != nil) {
				t.Fatalf("err=%v, wantErr=%v", err, tt.wantErr)
			}
			if err == nil {
				return
			}
			if !strings.Contains(err.Error(), "home-derp") {
				t.Errorf("error %q missing reporter name", err)
			}
			if tt.wantIn != "" && !strings.Contains(err.Error(), tt.wantIn) {
				t.Errorf("error %q missing %q", err, tt.wantIn)
			}
		})
	}
}

func TestMetricsReportPostStateLogging(t *testing.T) {
	var s reportPostState
	var logged []string
	logf := func(format string, args ...any) {
		logged = append(logged, format)
	}
	errBoom := http.ErrHandlerTimeout

	// First failure is logged; the next 98 are not (a 3s reporter would
	// otherwise write ~28k lines/day for one permanent 400).
	s.note(logf, "homederpreport", errBoom)
	if len(logged) != 1 {
		t.Fatalf("after 1 failure: logged %d, want 1", len(logged))
	}
	for i := 2; i <= 99; i++ {
		s.note(logf, "homederpreport", errBoom)
	}
	if len(logged) != 1 {
		t.Fatalf("after 99 failures: logged %d, want 1", len(logged))
	}
	// The 100th is.
	s.note(logf, "homederpreport", errBoom)
	if len(logged) != 2 {
		t.Fatalf("after 100 failures: logged %d, want 2", len(logged))
	}

	// Recovery is always logged, and resets the counter.
	s.note(logf, "homederpreport", nil)
	if len(logged) != 3 {
		t.Fatalf("after recovery: logged %d, want 3", len(logged))
	}
	if !strings.Contains(logged[2], "recovered") {
		t.Errorf("recovery line = %q", logged[2])
	}
	if s.fails != 0 {
		t.Errorf("fails=%d after recovery, want 0", s.fails)
	}

	// A steady-state success must not log at all.
	s.note(logf, "homederpreport", nil)
	if len(logged) != 3 {
		t.Fatalf("success after recovery logged again: %d", len(logged))
	}
}
