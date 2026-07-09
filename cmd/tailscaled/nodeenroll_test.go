// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// enrollTestServer serves a canned reply and captures the request the client sent.
func enrollTestServer(t *testing.T, status int, body string, got *nodeEnrollRequest) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/internal/enroll" {
			t.Errorf("enroll posted to %q, want /api/internal/enroll", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got != nil {
			if err := json.NewDecoder(r.Body).Decode(got); err != nil {
				t.Errorf("decode request: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNodeEnrollOnce(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		want     nodeEnrollOutcome
		wantErr  bool
		validate func(t *testing.T, r nodeEnrollResponse)
	}{
		{
			name: "202 pending -> retry later",
			// The device row exists but an admin has not approved it yet.
			status: http.StatusAccepted,
			body:   `{"status":"pending"}`,
			want:   nodeEnrollPending,
		},
		{
			name:   "200 first enroll issues a device token",
			status: http.StatusOK,
			body:   `{"authKey":"ak1","deviceToken":"dt1","loginServer":"https://hs.test","pinnedIp":"100.64.0.19"}`,
			want:   nodeEnrollOK,
			validate: func(t *testing.T, r nodeEnrollResponse) {
				if r.AuthKey != "ak1" {
					t.Errorf("authKey = %q", r.AuthKey)
				}
				if r.DeviceToken != "dt1" {
					t.Errorf("deviceToken = %q", r.DeviceToken)
				}
				if r.LoginServer != "https://hs.test" {
					t.Errorf("loginServer = %q", r.LoginServer)
				}
				if r.PinnedIP != "100.64.0.19" {
					t.Errorf("pinnedIp = %q", r.PinnedIP)
				}
			},
		},
		{
			name: "200 re-enroll: key but no new device token",
			// Device already holds a token; the server must not mint another.
			status: http.StatusOK,
			body:   `{"authKey":"ak2","loginServer":"https://hs.test"}`,
			want:   nodeEnrollOK,
			validate: func(t *testing.T, r nodeEnrollResponse) {
				if r.DeviceToken != "" {
					t.Errorf("re-enroll must not issue a new device token, got %q", r.DeviceToken)
				}
			},
		},
		{
			name: "200 without authKey is a server bug -> retry, do not bring up",
			// Guards against bringing the node up with no credential.
			status:  http.StatusOK,
			body:    `{"loginServer":"https://hs.test"}`,
			want:    nodeEnrollRetry,
			wantErr: true,
		},
		{
			name:   "403 revoked -> denied, never retry",
			status: http.StatusForbidden,
			body:   `{"reason":"revoked"}`,
			want:   nodeEnrollDenied,
			validate: func(t *testing.T, r nodeEnrollResponse) {
				if r.Reason != "revoked" {
					t.Errorf("reason = %q", r.Reason)
				}
			},
		},
		{
			name:   "404 -> not enrolled (probe found no adopted row)",
			status: http.StatusNotFound,
			body:   `{"status":"not_enrolled"}`,
			want:   nodeEnrollNotFound,
		},
		{
			name:    "500 -> transient, retry",
			status:  http.StatusInternalServerError,
			body:    `boom`,
			want:    nodeEnrollRetry,
			wantErr: true,
		},
		{
			name:    "malformed 200 body -> retry",
			status:  http.StatusOK,
			body:    `{not json`,
			want:    nodeEnrollRetry,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := enrollTestServer(t, tc.status, tc.body, nil)
			got, resp, err := nodeEnrollOnce(srv.Client(), srv.URL,
				nodeEnrollRequest{Mac: "aa:bb", Salt: "SER1", Hostname: "h"})
			if got != tc.want {
				t.Errorf("outcome = %v, want %v (err=%v)", got, tc.want, err)
			}
			if tc.wantErr && err == nil {
				t.Error("expected an error explaining the retry")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tc.validate != nil {
				tc.validate(t, resp)
			}
		})
	}
}

func TestNodeEnrollOnceSendsIdentity(t *testing.T) {
	var got nodeEnrollRequest
	srv := enrollTestServer(t, http.StatusAccepted, `{"status":"pending"}`, &got)

	want := nodeEnrollRequest{Mac: "f8:cf:00:11", Salt: "WD-WCC4", Hostname: "itop", Token: "dt9"}
	if _, _, err := nodeEnrollOnce(srv.Client(), srv.URL, want); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if got != want {
		t.Errorf("server received %+v, want %+v", got, want)
	}
}

func TestNodeEnrollOnceOmitsEmptyToken(t *testing.T) {
	// A device that has never enrolled must not send an empty "token" field —
	// the api-center distinguishes "no token yet" (first-enroll-wins) from a
	// supplied-but-wrong token (403).
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"pending"}`))
	}))
	defer srv.Close()

	if _, _, err := nodeEnrollOnce(srv.Client(), srv.URL,
		nodeEnrollRequest{Mac: "aa", Salt: "S", Hostname: "h"}); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, body)
	}
	if _, present := m["token"]; present {
		t.Errorf("empty token must be omitted from the request, got %s", body)
	}
}

func TestNodeProbeEnrollWith(t *testing.T) {
	writeCfg := func(t *testing.T) (string, nodeXMLConfig) {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "node.xml")
		// Start from the default an un-adopted machine has: autologin OFF.
		cfg := nodeXMLConfig{Autologin: false, APICenter: "PLACEHOLDER"}
		if err := nodeWriteXML(path, cfg); err != nil {
			t.Fatal(err)
		}
		return path, cfg
	}

	t.Run("adopted: writes key, flips node.xml to autologin=true + token", func(t *testing.T) {
		path, cfg := writeCfg(t)
		srv := enrollTestServer(t, http.StatusOK,
			`{"authKey":"ak-adopt","deviceToken":"dt-new","loginServer":"https://hs.test"}`, nil)
		cfg.APICenter = srv.URL

		key, ls := nodeProbeEnrollWith(srv.Client(), path, cfg, "aa:bb", "SER1", "host1")
		if key != "ak-adopt" {
			t.Fatalf("authKey = %q, want ak-adopt", key)
		}
		if ls != "https://hs.test" {
			t.Errorf("loginServer = %q", ls)
		}
		// node.xml must now be self-configured for unattended future starts.
		got := nodeLoadOrCreateXML(path, "fallback")
		if !got.Autologin {
			t.Error("node.xml autologin should be flipped to true after adopt")
		}
		if got.DeviceToken != "dt-new" {
			t.Errorf("deviceToken = %q, want dt-new", got.DeviceToken)
		}
	})

	t.Run("not adopted (404): no key, node.xml untouched (stays autologin=false)", func(t *testing.T) {
		path, cfg := writeCfg(t)
		srv := enrollTestServer(t, http.StatusNotFound, `{"status":"not_enrolled"}`, nil)
		cfg.APICenter = srv.URL

		key, _ := nodeProbeEnrollWith(srv.Client(), path, cfg, "aa:bb", "SER1", "host1")
		if key != "" {
			t.Fatalf("authKey = %q, want empty (fall back to interactive login)", key)
		}
		if got := nodeLoadOrCreateXML(path, "fallback"); got.Autologin {
			t.Error("node.xml must stay autologin=false when not adopted")
		}
	})
}

func TestNodeEnrollBackoff(t *testing.T) {
	const base, max = 60 * time.Second, 5 * time.Minute
	// First attempt starts at base, then doubles, then saturates at max.
	steps := []time.Duration{base, 2 * base, 4 * base, max, max}
	prev := time.Duration(0)
	for i, want := range steps {
		prev = nodeEnrollBackoff(prev, base, max)
		if prev != want {
			t.Fatalf("step %d: backoff = %v, want %v", i, prev, want)
		}
	}
	// Never below base, even from a bogus negative previous value.
	if got := nodeEnrollBackoff(-time.Second, base, max); got != base {
		t.Errorf("backoff(negative) = %v, want %v", got, base)
	}
}
