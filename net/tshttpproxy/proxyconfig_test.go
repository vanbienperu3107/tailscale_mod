// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package tshttpproxy

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// resetProxyState clears all package-level proxy caches so each test starts
// from a clean slate.
func resetProxyState() {
	mu.Lock()
	config = nil
	proxyFunc = nil
	noProxyUntil = time.Time{}
	mu.Unlock()

	configFileMu.Lock()
	cachedFileConf = nil
	configFileRead = false
	configFileMu.Unlock()

	logMessageMu.Lock()
	logMessagePrinted = nil
	logMessageMu.Unlock()
}

// useProxyConfig points proxyConfigPath at a temp file containing contents
// (or at a non-existent path when contents is empty) and resets proxy state.
// It returns the file path so callers can rewrite it. All overrides are undone
// via t.Cleanup.
func useProxyConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), proxyConfigFileName)
	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := proxyConfigPath
	proxyConfigPath = func() string { return path }
	resetProxyState()
	t.Cleanup(func() {
		proxyConfigPath = old
		resetProxyState()
	})
	return path
}

func TestConfigFromFileEnabled(t *testing.T) {
	useProxyConfig(t, `{
		"enabled": true,
		"httpProxy": "http://p.example:8080",
		"httpsProxy": "http://p.example:8443",
		"noProxy": "localhost,*.local"
	}`)
	c := configFromFile()
	if c == nil {
		t.Fatal("configFromFile() = nil; want config")
	}
	if c.HTTPProxy != "http://p.example:8080" {
		t.Errorf("HTTPProxy = %q", c.HTTPProxy)
	}
	if c.HTTPSProxy != "http://p.example:8443" {
		t.Errorf("HTTPSProxy = %q", c.HTTPSProxy)
	}
	if c.NoProxy != "localhost,*.local" {
		t.Errorf("NoProxy = %q", c.NoProxy)
	}
}

func TestConfigFromFileEnabledOmitted(t *testing.T) {
	// No "enabled" field → treated as enabled, use file values.
	useProxyConfig(t, `{"httpsProxy": "http://implicit.example:8443"}`)
	c := configFromFile()
	if c == nil || c.HTTPSProxy != "http://implicit.example:8443" {
		t.Fatalf("configFromFile() = %+v; want implicit-enabled config", c)
	}
}

func TestConfigFromFileDisabled(t *testing.T) {
	useProxyConfig(t, `{"enabled": false, "httpProxy": "http://should.ignore:8080"}`)
	c := configFromFile()
	if c == nil {
		t.Fatal("configFromFile() = nil; want empty non-nil config")
	}
	if c.HTTPProxy != "" || c.HTTPSProxy != "" {
		t.Errorf("want empty config, got %+v", c)
	}
	// And the derived ProxyFunc returns no proxy for any URL.
	got, err := c.ProxyFunc()(mustURL(t, "https://example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("ProxyFunc returned %v; want nil (no proxy)", got)
	}
}

func TestConfigFromFileAbsent(t *testing.T) {
	useProxyConfig(t, "") // path points to a non-existent file
	if c := configFromFile(); c != nil {
		t.Errorf("configFromFile() = %+v; want nil (fall back to env)", c)
	}
}

func TestConfigFromFileInvalidJSON(t *testing.T) {
	useProxyConfig(t, `{ this is not valid json`)
	if c := configFromFile(); c != nil {
		t.Errorf("configFromFile() = %+v; want nil on parse error", c)
	}
}

func TestGetProxyFuncFileOverridesEnv(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://env.example:9999")
	t.Setenv("HTTPS_PROXY", "http://env.example:9999")
	useProxyConfig(t, `{"enabled": true, "httpsProxy": "http://file.example:8443"}`)

	got, err := getProxyFunc()(mustURL(t, "https://example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.String() != "http://file.example:8443" {
		t.Errorf("proxy = %v; want http://file.example:8443 (file overrides env)", got)
	}
}

func TestGetProxyFuncDisabledIgnoresEnv(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://env.example:9999")
	t.Setenv("HTTPS_PROXY", "http://env.example:9999")
	useProxyConfig(t, `{"enabled": false}`)

	got, err := getProxyFunc()(mustURL(t, "https://example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("proxy = %v; want nil (disabled file ignores env)", got)
	}
}

func TestGetProxyFuncNoFileUsesEnv(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://env.example:9999")
	useProxyConfig(t, "") // no file → env vars

	got, err := getProxyFunc()(mustURL(t, "https://example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.String() != "http://env.example:9999" {
		t.Errorf("proxy = %v; want env proxy", got)
	}
}

func TestGetAuthHeaderFromFile(t *testing.T) {
	useProxyConfig(t, `{
		"enabled": true,
		"httpsProxy": "http://p.example:8443",
		"proxyAuth": {"username": "user", "password": "password"}
	}`)
	const want = "Basic dXNlcjpwYXNzd29yZA==" // base64("user:password")

	got, err := GetAuthHeader(mustURL(t, "http://p.example:8443")) // no userinfo in URL
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("GetAuthHeader = %q; want %q", got, want)
	}
}

func TestGetAuthHeaderURLUserinfoBeatsFile(t *testing.T) {
	useProxyConfig(t, `{
		"enabled": true,
		"proxyAuth": {"username": "fileuser", "password": "filepass"}
	}`)
	// URL carries its own credentials; those win over the file.
	const want = "Basic dXNlcjpwYXNzd29yZA==" // base64("user:password")

	got, err := GetAuthHeader(mustURL(t, "http://user:password@p.example:8443"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("GetAuthHeader = %q; want %q (URL userinfo)", got, want)
	}
}

func TestReloadProxyConfig(t *testing.T) {
	path := useProxyConfig(t, `{"enabled": false}`)

	// Initially disabled.
	if c := configFromFile(); c == nil || c.HTTPSProxy != "" {
		t.Fatalf("initial configFromFile = %+v; want disabled", c)
	}

	// Rewrite the file to enable a proxy.
	if err := os.WriteFile(path, []byte(`{"enabled": true, "httpsProxy": "http://new.example:8443"}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Without a reload the cached (disabled) value is still returned.
	if c := configFromFile(); c == nil || c.HTTPSProxy != "" {
		t.Fatalf("cached configFromFile = %+v; want still-disabled before reload", c)
	}

	ReloadProxyConfig()

	if c := configFromFile(); c == nil || c.HTTPSProxy != "http://new.example:8443" {
		t.Fatalf("post-reload configFromFile = %+v; want new proxy", c)
	}
}

func TestInvalidateCacheRereadsAuth(t *testing.T) {
	path := useProxyConfig(t, `{"enabled": true, "proxyAuth": {"username": "a", "password": "b"}}`)

	if _, err := GetAuthHeader(mustURL(t, "http://p.example:8443")); err != nil {
		t.Fatal(err)
	}

	// Update credentials on disk, then invalidate so the next read re-reads.
	if err := os.WriteFile(path, []byte(`{"enabled": true, "proxyAuth": {"username": "user", "password": "password"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	InvalidateCache()

	const want = "Basic dXNlcjpwYXNzd29yZA==" // base64("user:password")
	got, err := GetAuthHeader(mustURL(t, "http://p.example:8443"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("after InvalidateCache, GetAuthHeader = %q; want %q", got, want)
	}
}

func TestDefaultProxyConfigPathEnvOverride(t *testing.T) {
	const want = "/custom/dir/proxy.conf"
	t.Setenv(proxyConfigEnv, want)
	if got := defaultProxyConfigPath(); got != want {
		t.Errorf("defaultProxyConfigPath() = %q; want %q (from %s)", got, want, proxyConfigEnv)
	}
}

func TestProxyConfigViaEnvPath(t *testing.T) {
	// End-to-end: set TS_PROXY_CONF to a real file and confirm it's loaded
	// through the default path resolver (not via the test override var).
	path := filepath.Join(t.TempDir(), "myproxy.conf")
	if err := os.WriteFile(path, []byte(`{"enabled": true, "httpsProxy": "http://portable.example:8443"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(proxyConfigEnv, path)
	resetProxyState()
	t.Cleanup(resetProxyState)

	c := configFromFile()
	if c == nil || c.HTTPSProxy != "http://portable.example:8443" {
		t.Fatalf("configFromFile() = %+v; want proxy from %s", c, proxyConfigEnv)
	}
}

// mustURL parses rawURL or fails the test.
func mustURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", rawURL, err)
	}
	return u
}
