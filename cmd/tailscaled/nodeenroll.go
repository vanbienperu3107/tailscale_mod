// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Zero-touch enrollment.
//
// A machine with <autologin>true</autologin> in node.xml reports itself to the
// api-center (dashboard) as {mac, salt, hostname}. The api-center creates a
// PENDING row an admin must approve; once approved it mints a SHORT-LIVED
// headscale pre-auth key and hands it back, and the node brings itself up
// unattended. No admin ever types an auth URL on the machine.
//
// Authorization after approval is a per-device token issued on the FIRST
// successful enroll (first-enroll-wins) and cached in node.xml, so merely
// knowing (mac, salt) is not enough to claim a device later.
//
// The auth key is used immediately and never persisted.

const (
	// Retry cadence while waiting for an admin to approve this device. Zero-touch
	// means "plug the machine in and let it wait", so the loop never gives up.
	nodeEnrollBaseDelay = 60 * time.Second
	nodeEnrollMaxDelay  = 5 * time.Minute
)

// nodeEnrollRequest is the body of POST {apiCenter}/api/internal/enroll.
// Salt is the normalized hardware serial (also the machine-key seed input);
// Token is empty until this device has completed its first enroll.
type nodeEnrollRequest struct {
	Mac      string `json:"mac"`
	Salt     string `json:"salt"`
	Hostname string `json:"hostname"`
	Token    string `json:"token,omitempty"`
	// Probe = "chỉ hỏi đã được adopt/duyệt chưa, đừng tạo dòng pending". Dùng bởi
	// máy CHƯA bật autologin: nếu server trả 404 (chưa adopt) thì im lặng quay về
	// đăng nhập tay; nếu 200 thì tự chuyển sang autologin.
	Probe bool `json:"probe,omitempty"`
}

// nodeEnrollResponse is the api-center's reply. AuthKey/LoginServer are set on
// 200; DeviceToken only on the first successful enroll; Reason on 403.
type nodeEnrollResponse struct {
	Status      string `json:"status"`
	AuthKey     string `json:"authKey"`
	DeviceToken string `json:"deviceToken"`
	LoginServer string `json:"loginServer"`
	PinnedIP    string `json:"pinnedIp"`
	Reason      string `json:"reason"`
}

// nodeEnrollOutcome classifies one enrollment attempt.
type nodeEnrollOutcome int

const (
	// nodeEnrollRetry: transient failure (network, 5xx, malformed 200) — retry.
	nodeEnrollRetry nodeEnrollOutcome = iota
	// nodeEnrollPending: 202, the device row exists but is awaiting approval.
	nodeEnrollPending
	// nodeEnrollOK: 200, an auth key was issued.
	nodeEnrollOK
	// nodeEnrollDenied: 403, revoked or wrong device token — STOP, never retry.
	nodeEnrollDenied
	// nodeEnrollNotFound: 404, a probe found no adopted/approved row — not enrolled.
	nodeEnrollNotFound
)

func (o nodeEnrollOutcome) String() string {
	switch o {
	case nodeEnrollPending:
		return "pending"
	case nodeEnrollOK:
		return "ok"
	case nodeEnrollDenied:
		return "denied"
	case nodeEnrollNotFound:
		return "not-enrolled"
	default:
		return "retry"
	}
}

// nodeEnrollBackoff returns the delay before the next enrollment attempt:
// starts at base, doubles each time, capped at max. Pure — unit-tested.
func nodeEnrollBackoff(prev, base, max time.Duration) time.Duration {
	if prev <= 0 {
		return base
	}
	next := prev * 2
	if next > max {
		return max
	}
	return next
}

// nodeEnrollOnce performs a single enrollment request and classifies the reply.
// It never retries; the caller drives the loop. Errors are returned only for the
// nodeEnrollRetry outcome (they explain what to log).
func nodeEnrollOnce(client *http.Client, apiCenter string, req nodeEnrollRequest) (nodeEnrollOutcome, nodeEnrollResponse, error) {
	var out nodeEnrollResponse

	body, err := json.Marshal(req)
	if err != nil {
		return nodeEnrollRetry, out, fmt.Errorf("marshal request: %w", err)
	}
	url := strings.TrimRight(apiCenter, "/") + "/api/internal/enroll"
	hreq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nodeEnrollRetry, out, fmt.Errorf("build request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(hreq)
	if err != nil {
		return nodeEnrollRetry, out, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.Unmarshal(raw, &out); err != nil {
			return nodeEnrollRetry, out, fmt.Errorf("decode 200 body: %w", err)
		}
		if out.AuthKey == "" {
			// An approved device MUST come with a key; treat a keyless 200 as a
			// server bug and retry rather than bringing the node up keyless.
			return nodeEnrollRetry, out, errors.New("200 response carried no authKey")
		}
		return nodeEnrollOK, out, nil
	case http.StatusAccepted:
		_ = json.Unmarshal(raw, &out) // body is advisory ({"status":"pending"})
		return nodeEnrollPending, out, nil
	case http.StatusForbidden:
		_ = json.Unmarshal(raw, &out) // {"reason":"revoked"} etc.
		return nodeEnrollDenied, out, nil
	case http.StatusNotFound:
		// Only a probe gets 404 ("not adopted yet"); a normal enroll never does.
		_ = json.Unmarshal(raw, &out)
		return nodeEnrollNotFound, out, nil
	default:
		snippet := string(raw)
		if len(snippet) > 256 {
			snippet = snippet[:256]
		}
		return nodeEnrollRetry, out, fmt.Errorf("status %d: %s", resp.StatusCode, snippet)
	}
}

// nodeDeviceIdentity gathers the values that identify this machine to the
// api-center. salt is the NORMALIZED serial — the same value that seeds the
// machine key — so the dashboard and the key derivation always agree.
func nodeDeviceIdentity() (mac, salt, hostname string, err error) {
	mac = primaryMAC()
	if mac == "" {
		return "", "", "", errors.New("could not determine primary MAC")
	}
	serial, herr := machineHardwareID()
	if herr != nil {
		return "", "", "", fmt.Errorf("read hardware serial: %w", herr)
	}
	salt = normalizeSerial(serial)
	if salt == "" {
		return "", "", "", errors.New("hardware serial is empty")
	}
	hostname, _ = os.Hostname()
	return mac, salt, hostname, nil
}

// nodeEnroll blocks until this device is approved (returning a fresh auth key +
// login server) or definitively denied. A 202 (awaiting admin approval) retries
// forever with backoff — that is the zero-touch contract: plug the machine in,
// approve it whenever, it joins by itself.
//
// On the FIRST successful enroll the api-center returns a device token, which we
// persist to node.xml so later re-enrolls (after a state wipe) prove identity.
func nodeEnroll(client *http.Client, cfgPath string, cfg nodeXMLConfig) (authKey, loginServer string, err error) {
	mac, salt, hostname, err := nodeDeviceIdentity()
	if err != nil {
		return "", "", err
	}
	req := nodeEnrollRequest{Mac: mac, Salt: salt, Hostname: hostname, Token: cfg.DeviceToken}
	log.Printf("node: enroll: reporting to %s (mac=%s hostname=%s, token=%v)",
		cfg.APICenter, mac, hostname, cfg.DeviceToken != "")

	var delay time.Duration
	for attempt := 1; ; attempt++ {
		outcome, resp, oerr := nodeEnrollOnce(client, cfg.APICenter, req)
		switch outcome {
		case nodeEnrollOK:
			if resp.DeviceToken != "" {
				if werr := nodePersistDeviceToken(cfgPath, cfg, resp.DeviceToken); werr != nil {
					log.Printf("node: enroll: could not save device token to %s: %v (a state wipe will need admin reset-token)", cfgPath, werr)
				} else {
					log.Printf("node: enroll: device token saved to %s", cfgPath)
				}
			}
			ls := resp.LoginServer
			if ls == "" {
				ls = nodeLoginServer
			}
			if resp.PinnedIP != "" {
				log.Printf("node: enroll: approved (pinned IP %s)", resp.PinnedIP)
			} else {
				log.Printf("node: enroll: approved")
			}
			return resp.AuthKey, ls, nil

		case nodeEnrollDenied:
			reason := resp.Reason
			if reason == "" {
				reason = "forbidden"
			}
			return "", "", fmt.Errorf("enrollment denied: %s", reason)

		case nodeEnrollPending:
			log.Printf("node: enroll: awaiting admin approval (attempt %d)", attempt)

		default:
			log.Printf("node: enroll: attempt %d failed: %v", attempt, oerr)
		}

		delay = nodeEnrollBackoff(delay, nodeEnrollBaseDelay, nodeEnrollMaxDelay)
		time.Sleep(delay)
	}
}

// nodeCLIOutput runs this same binary as the built-in tailscale CLI (TS_BE_CLI=1
// re-enters the CLI rather than the node launcher) and returns its stdout.
func nodeCLIOutput(exe string, args ...string) ([]byte, error) {
	c := exec.Command(exe, args...)
	c.Env = append(os.Environ(), "TS_BE_CLI=1")
	return c.Output()
}

// nodeBackendStatus is the subset of `tailscale status --json` needed to decide
// whether this daemon still needs a login.
type nodeBackendStatus struct {
	BackendState string `json:"BackendState"`
}

// nodeBackendState returns the daemon's BackendState ("NeedsLogin", "Running",
// "Stopped", "NoState", ...), retrying briefly while the LocalAPI comes up.
// Returns "" if it never answers.
func nodeBackendState(exe string) string {
	for i := 0; i < 15; i++ {
		if i > 0 {
			time.Sleep(2 * time.Second)
		}
		out, err := nodeCLIOutput(exe, "status", "--json")
		if err != nil {
			continue
		}
		var st nodeBackendStatus
		if json.Unmarshal(out, &st) == nil && st.BackendState != "" {
			return st.BackendState
		}
	}
	return ""
}

// nodeAutoEnroll returns an auth key for `up` when this machine is configured
// for zero-touch AND actually needs a login. An already-logged-in daemon (its
// state survived) is left alone: enrollment exists only to obtain an identity,
// never to re-register a working node.
//
// Returns ("", "", nil) to mean "no key needed, use the normal up path".
func nodeAutoEnroll(exe string, cfgPath string, cfg nodeXMLConfig) (authKey, loginServer string, err error) {
	if !cfg.Autologin {
		return "", "", nil
	}
	if cfg.APICenter == "" {
		return "", "", errors.New("autologin=true but no apiCenter configured")
	}
	state := nodeBackendState(exe)
	switch state {
	case "NeedsLogin", "NoState":
		// fall through: this node has no control identity yet.
	case "":
		return "", "", errors.New("daemon LocalAPI never reported a backend state")
	default:
		log.Printf("node: autologin: backend state %q — already logged in, skipping enrollment", state)
		return "", "", nil
	}
	client := &http.Client{Timeout: 15 * time.Second}
	return nodeEnroll(client, cfgPath, cfg)
}

// nodeProbeEnroll runs ONE opportunistic enrollment probe for a machine that is
// NOT configured for autologin. If this machine was already adopted — it logged
// in via OIDC before, so the dashboard holds an approved row keyed by (mac,salt)
// — the api-center returns an auth key and we SELF-CONFIGURE: flip node.xml to
// autologin=true and cache the device token, so every future start joins the
// tailnet unattended with no manual step.
//
// Any other outcome (404 not adopted / pending / denied / error) returns no key,
// and the caller falls back to the normal interactive OIDC login. Never blocks
// in a loop — a single request with a short timeout.
func nodeProbeEnroll(cfgPath string, cfg nodeXMLConfig) (authKey, loginServer string) {
	if cfg.APICenter == "" {
		return "", ""
	}
	mac, salt, hostname, err := nodeDeviceIdentity()
	if err != nil {
		log.Printf("node: probe-enroll: skipped (%v)", err)
		return "", ""
	}
	client := &http.Client{Timeout: 15 * time.Second}
	return nodeProbeEnrollWith(client, cfgPath, cfg, mac, salt, hostname)
}

// nodeProbeEnrollWith is the testable core of nodeProbeEnroll with the device
// identity injected (the real serial read is Windows-only, so tests supply it).
func nodeProbeEnrollWith(client *http.Client, cfgPath string, cfg nodeXMLConfig, mac, salt, hostname string) (authKey, loginServer string) {
	outcome, resp, oerr := nodeEnrollOnce(client, cfg.APICenter, nodeEnrollRequest{
		Mac: mac, Salt: salt, Hostname: hostname, Token: cfg.DeviceToken, Probe: true,
	})
	if outcome != nodeEnrollOK {
		switch outcome {
		case nodeEnrollNotFound:
			log.Printf("node: probe-enroll: not adopted yet — using interactive login")
		case nodeEnrollDenied:
			log.Printf("node: probe-enroll: denied (%s) — using interactive login", resp.Reason)
		default:
			log.Printf("node: probe-enroll: %s (%v) — using interactive login", outcome, oerr)
		}
		return "", ""
	}

	// Adopted: persist autologin + device token so future starts are unattended.
	newCfg := cfg
	newCfg.Autologin = true
	if resp.DeviceToken != "" {
		newCfg.DeviceToken = resp.DeviceToken
	}
	if werr := nodeWriteXML(cfgPath, newCfg); werr != nil {
		log.Printf("node: probe-enroll: adopted but could not write %s: %v (still joining now)", cfgPath, werr)
	} else {
		log.Printf("node: probe-enroll: adopted — node.xml switched to autologin=true")
	}
	ls := resp.LoginServer
	if ls == "" {
		ls = nodeLoginServer
	}
	log.Printf("node: probe-enroll: joining unattended via adopted enrollment")
	return resp.AuthKey, ls
}

// nodePrintID prints this machine's enrollment identity as JSON so an admin can
// PRE-APPROVE it on the dashboard before the machine is ever plugged in:
//
//	{"mac":"f8:cf:...","salt":"WD-WCC4E5PZ","hostname":"itop"}
//
// salt is the normalized disk serial. Treat it as sensitive: it reproduces the
// private machine key (see hwid.go).
func nodePrintID() {
	mac, salt, hostname, err := nodeDeviceIdentity()
	if err != nil {
		fmt.Fprintf(os.Stderr, "id: %v\n", err)
		os.Exit(1)
	}
	out, _ := json.Marshal(struct {
		Mac      string `json:"mac"`
		Salt     string `json:"salt"`
		Hostname string `json:"hostname"`
	}{mac, salt, hostname})
	fmt.Println(string(out))
}

// nodeEnrollDebug runs ONE enrollment attempt and prints the outcome — a manual
// probe for admins (`<exe> enroll`), distinct from the launcher's blocking loop.
func nodeEnrollDebug(dir string) {
	cfgPath := nodeConfigPath(dir)
	cfg := nodeLoadOrCreateXML(cfgPath, nodeMetricsURL)
	mac, salt, hostname, err := nodeDeviceIdentity()
	if err != nil {
		fmt.Fprintf(os.Stderr, "enroll: %v\n", err)
		os.Exit(1)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	outcome, resp, oerr := nodeEnrollOnce(client, cfg.APICenter,
		nodeEnrollRequest{Mac: mac, Salt: salt, Hostname: hostname, Token: cfg.DeviceToken})
	fmt.Printf("api-center: %s\noutcome:    %s\n", cfg.APICenter, outcome)
	if oerr != nil {
		fmt.Printf("error:      %v\n", oerr)
	}
	if resp.Reason != "" {
		fmt.Printf("reason:     %s\n", resp.Reason)
	}
	if outcome == nodeEnrollOK {
		// Never print the auth key itself.
		fmt.Printf("approved:   yes (auth key issued, pinnedIp=%q)\n", resp.PinnedIP)
	}
}
