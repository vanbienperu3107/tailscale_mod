// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package magicsock

import (
	"context"
	"errors"
	"testing"
	"time"
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
