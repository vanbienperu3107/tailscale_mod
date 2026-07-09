// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

// Shared plumbing for the built-in dashboard reporters (metricsreport.go,
// homederpreport.go, derppingreport.go).
//
// Two bugs motivated this file, both found on a node that looked offline on the
// dashboard for 32h while headscale showed it happily Connected:
//
//  1. The reporters ran `mac := primaryMAC()` ONCE, at goroutine start. They are
//     started from NewLocalBackend, very early in daemon boot — early enough
//     that on some machines no interface is up yet and primaryMAC() returns "".
//     That empty string was then reused for the entire life of the process. The
//     later-starting loops (nodeRuntimePollLoop, device-register) call
//     primaryMAC() after the LocalAPI is ready and get the real MAC, which is
//     why only *some* of a node's reports went missing.
//
//  2. The post helpers never looked at the response status. The dashboard
//     rejects an empty MAC with HTTP 400, so every report was dropped on the
//     floor and the client logged nothing at all — not even at -v 1.
//
// macResolver fixes (1) by re-resolving until it gets a real MAC, then caching.
// checkPostResponse fixes (2). reportPostState keeps the resulting logs from
// spamming a line every 3s forever.

package main

import (
	"fmt"
	"io"
	"net/http"

	"tailscale.com/types/logger"
)

// macResolver lazily resolves this machine's primary MAC, retrying on each call
// until it gets a non-empty value and caching it from then on.
//
// The zero value is ready to use. Not safe for concurrent use; each reporter
// goroutine owns its own.
type macResolver struct {
	mac string
	fn  func() string // nil -> primaryMAC; injectable for tests
}

// get returns the primary MAC, or "" if it still cannot be determined.
func (m *macResolver) get() string {
	if m.mac != "" {
		return m.mac
	}
	fn := m.fn
	if fn == nil {
		fn = primaryMAC
	}
	m.mac = fn()
	return m.mac
}

// checkPostResponse closes resp.Body and reports a non-2xx status as an error,
// including a snippet of the body so a validation failure ("mac: Required") is
// visible instead of being silently discarded.
func checkPostResponse(resp *http.Response, what string) error {
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return fmt.Errorf("%s: status %d: %s", what, resp.StatusCode, string(body))
}

// reportPostState throttles reporter logging. A reporter posting every 3s that
// hits a permanent 400 would otherwise write 28k log lines a day; staying
// completely silent (the old behaviour) is worse. Log the first failure, then
// every logEveryNFailures-th, and always log the recovery.
type reportPostState struct {
	fails int
}

const logEveryNFailures = 100

func (s *reportPostState) note(logf logger.Logf, what string, err error) {
	if err != nil {
		s.fails++
		if s.shouldLogFailure() {
			logf("%s: post failed (%d consecutive): %v", what, s.fails, err)
		}
		return
	}
	if s.fails > 0 {
		logf("%s: post recovered after %d consecutive failures", what, s.fails)
		s.fails = 0
	}
}

// shouldLogFailure reports whether the current consecutive-failure count is one
// we want on disk. Split out so it can be unit-tested without a logger.
func (s *reportPostState) shouldLogFailure() bool {
	return s.fails == 1 || s.fails%logEveryNFailures == 0
}
