// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package magicsock

import (
	"context"
	"errors"
	"testing"
	"time"

	"tailscale.com/tailcfg"
)

// TestDerpPingOnce validates the DERP fast-ping decision across every outcome:
// a fast pong, a slow-but-in-time pong (the case the flapping fix protects), a
// timed-out ping, and a transport error. derpPingOnce reports an error iff the
// node did not answer within the timeout, which is exactly what runDerpFastPing
// uses to declare a relay dead and switch away.
func TestDerpPingOnce(t *testing.T) {
	blockUntilDeadline := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	sleepThenOK := func(d time.Duration) func(context.Context) error {
		return func(ctx context.Context) error {
			select {
			case <-time.After(d):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	tests := []struct {
		name      string
		timeout   time.Duration
		ping      func(context.Context) error
		wantAlive bool
	}{
		{
			name:      "fast pong is alive",
			timeout:   200 * time.Millisecond,
			ping:      func(context.Context) error { return nil },
			wantAlive: true,
		},
		{
			// A pong that arrives well after the old 3s cutoff but within the
			// timeout must be treated as ALIVE — this is the flapping fix.
			name:      "slow pong within timeout is alive",
			timeout:   300 * time.Millisecond,
			ping:      sleepThenOK(60 * time.Millisecond),
			wantAlive: true,
		},
		{
			name:      "no pong before timeout is dead",
			timeout:   50 * time.Millisecond,
			ping:      blockUntilDeadline,
			wantAlive: false,
		},
		{
			name:      "transport error is dead",
			timeout:   200 * time.Millisecond,
			ping:      func(context.Context) error { return errors.New("connection reset") },
			wantAlive: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rtt, err := derpPingOnce(context.Background(), tt.timeout, tt.ping)
			gotAlive := err == nil
			if gotAlive != tt.wantAlive {
				t.Fatalf("alive=%v (err=%v), want alive=%v", gotAlive, err, tt.wantAlive)
			}
			if rtt < 0 {
				t.Fatalf("negative rtt %v", rtt)
			}
			// A ping killed by the deadline must not report success sooner than
			// the timeout window (guards against measuring/timeout mistakes).
			if errors.Is(err, context.DeadlineExceeded) && rtt < tt.timeout {
				t.Fatalf("timed-out ping rtt %v < timeout %v", rtt, tt.timeout)
			}
		})
	}
}

// TestDerpFastPingTimeoutPolicy guards the flapping fix at the constant level so
// the value cannot silently regress (e.g. back to the old 3s). A corp node with
// no UDP tunnels DERP over an HTTP proxy where round-trips are high and jittery;
// a too-small timeout falsely marks the preferred relay dead and causes it to
// flap (observed: itop bouncing vpn4<->vpn2). The timeout must comfortably
// tolerate a representative proxied RTT and must exceed the ping interval.
func TestDerpFastPingTimeoutPolicy(t *testing.T) {
	const representativeProxyRTT = 5 * time.Second
	if derpFastPingTimeout <= representativeProxyRTT {
		t.Fatalf("derpFastPingTimeout=%v is too small; must tolerate a proxied RTT of %v to avoid false-dead flapping",
			derpFastPingTimeout, representativeProxyRTT)
	}
	if derpFastPingTimeout <= derpFastPingInterval {
		t.Fatalf("derpFastPingTimeout(%v) must exceed derpFastPingInterval(%v)",
			derpFastPingTimeout, derpFastPingInterval)
	}
}

func mkDERPMap(scores map[int]float64, regionIDs ...int) *tailcfg.DERPMap {
	dm := &tailcfg.DERPMap{Regions: map[int]*tailcfg.DERPRegion{}}
	for _, id := range regionIDs {
		dm.Regions[id] = &tailcfg.DERPRegion{RegionID: id}
	}
	if len(scores) > 0 {
		dm.HomeParams = &tailcfg.DERPHomeParams{RegionScore: scores}
	}
	return dm
}

// TestDerpForcedHomeRegion validates detection of an explicitly-assigned ("ép")
// home region across every case. The dashboard encodes an assignment by leaving
// the assigned region at a normal score and penalising all other regions with a
// huge HomeParams score; the client treats "there is a penalised region AND
// exactly one non-penalised region" as a hard force. Everything else (no
// penalties, multiple assigned, maintenance-only, nil/empty) is not a force and
// falls through to normal auto-selection.
func TestDerpForcedHomeRegion(t *testing.T) {
	const pen = 1e33 // union penalty score for a non-assigned region

	tests := []struct {
		name   string
		dm     *tailcfg.DERPMap
		wantID int
		wantOK bool
	}{
		{
			name:   "assigned: one preferred, others penalised",
			dm:     mkDERPMap(map[int]float64{1001: 0.0001, 1000: pen, 1003: 1e30, 2000: pen}, 1000, 1001, 1003, 2000),
			wantID: 1001, wantOK: true,
		},
		{
			name:   "assigned with default-priority home (score omitted == 1)",
			dm:     mkDERPMap(map[int]float64{2000: pen}, 1001, 2000),
			wantID: 1001, wantOK: true,
		},
		{
			name:   "unassigned base map (globally tuned, no penalty) is not forced",
			dm:     mkDERPMap(map[int]float64{1001: 0.0001, 1003: 0.000464}, 1000, 1001, 1003, 2000),
			wantOK: false,
		},
		{
			name:   "maintenance score is not a penalty",
			dm:     mkDERPMap(map[int]float64{2000: 9999}, 1001, 2000),
			wantOK: false,
		},
		{
			name:   "multiple assigned (two non-penalised) is not a single force",
			dm:     mkDERPMap(map[int]float64{1001: 0.0001, 1003: 0.000464, 2000: pen}, 1001, 1003, 2000),
			wantOK: false,
		},
		{name: "nil map", dm: nil, wantOK: false},
		{name: "empty regions", dm: &tailcfg.DERPMap{}, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := derpForcedHomeRegion(tt.dm)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v want %v", ok, tt.wantOK)
			}
			if ok && id != tt.wantID {
				t.Fatalf("regionID=%d want %d", id, tt.wantID)
			}
		})
	}
}

// TestDerpForcedShouldStick validates the "stick to the forced home, then give
// up after 30s dead" boundary (derpForcedDeadGrace).
func TestDerpForcedShouldStick(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	cases := []struct {
		elapsed time.Duration
		want    bool
	}{
		{0, true},
		{10 * time.Second, true},
		{derpForcedDeadGrace - time.Second, true},
		{derpForcedDeadGrace, false},
		{derpForcedDeadGrace + 10*time.Second, false},
	}
	for _, c := range cases {
		if got := derpForcedShouldStick(base, base.Add(c.elapsed)); got != c.want {
			t.Fatalf("shouldStick(elapsed=%v)=%v want %v (grace=%v)", c.elapsed, got, c.want, derpForcedDeadGrace)
		}
	}
	if derpForcedDeadGrace != 30*time.Second {
		t.Fatalf("derpForcedDeadGrace=%v, expected 30s per design", derpForcedDeadGrace)
	}
}
