// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package tshttpproxy

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/net/http/httpproxy"
	"tailscale.com/paths"
)

// proxyConfigFileName is the name of the on-disk proxy configuration file,
// stored alongside tailscaled's state (see paths.DefaultTailscaledStateDir).
const proxyConfigFileName = "proxy.conf"

// ProxyConfig is the on-disk proxy.conf file structure. It lets an operator
// force Tailscale's outbound HTTP/HTTPS proxying on or off independently of the
// process environment variables.
type ProxyConfig struct {
	// Enabled controls whether the file overrides the environment.
	//
	//   - nil   (field omitted): treat the file as enabled and use its values.
	//   - true:  use the proxy values from this file, overriding env vars.
	//   - false: disable proxying entirely, ignoring env vars.
	Enabled    *bool            `json:"enabled"`
	HTTPProxy  string           `json:"httpProxy"`
	HTTPSProxy string           `json:"httpsProxy"`
	NoProxy    string           `json:"noProxy"`
	ProxyAuth  *ProxyAuthConfig `json:"proxyAuth,omitempty"`
}

// ProxyAuthConfig holds optional basic-auth credentials sent to the proxy.
type ProxyAuthConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

var (
	configFileMu   sync.Mutex
	cachedFileConf *ProxyConfig // last parsed config; nil if absent/unreadable
	configFileRead bool         // whether we've attempted a read since the last reset
)

// proxyConfigEnv is the environment variable that, when set, overrides the
// location of proxy.conf. It lets portable/standalone builds keep proxy.conf
// next to the binaries instead of in the default state directory.
const proxyConfigEnv = "TS_PROXY_CONF"

// defaultProxyConfigPath returns the absolute path to proxy.conf:
//   - $TS_PROXY_CONF, if set (used by portable builds), else
//   - <DefaultTailscaledStateDir>/proxy.conf, else
//   - "" if there's no reasonable default state directory on this platform.
func defaultProxyConfigPath() string {
	if p := os.Getenv(proxyConfigEnv); p != "" {
		return p
	}
	dir := paths.DefaultTailscaledStateDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, proxyConfigFileName)
}

// proxyConfigPath returns the path to proxy.conf. It's a var so tests can point
// it at a temporary file.
var proxyConfigPath = defaultProxyConfigPath

// loadProxyConfig reads and caches proxy.conf from disk.
//
// It returns nil when the file is absent or cannot be read/parsed, which the
// callers treat as "fall back to environment variables". The result is cached
// until ReloadProxyConfig (or InvalidateCache) clears it.
func loadProxyConfig() *ProxyConfig {
	configFileMu.Lock()
	defer configFileMu.Unlock()

	if configFileRead {
		return cachedFileConf
	}
	configFileRead = true

	path := proxyConfigPath()
	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("tshttpproxy: error reading %s: %v", path, err)
		}
		return nil
	}

	var conf ProxyConfig
	if err := json.Unmarshal(data, &conf); err != nil {
		log.Printf("tshttpproxy: error parsing %s: %v", path, err)
		return nil
	}

	log.Printf("tshttpproxy: loaded proxy config from %s (enabled=%v)", path, conf.enabledString())
	cachedFileConf = &conf
	return cachedFileConf
}

// enabledString returns a human-readable form of the Enabled tri-state for logs
// (avoids logging a *bool pointer address).
func (c *ProxyConfig) enabledString() string {
	switch {
	case c.Enabled == nil:
		return "unset"
	case *c.Enabled:
		return "true"
	default:
		return "false"
	}
}

// configFromFile returns an httpproxy.Config derived from proxy.conf, or nil if
// no file is present (meaning: use the environment).
//
// When the file explicitly disables proxying (enabled=false), it returns an
// empty, non-nil Config so that env vars are ignored and no proxy is used.
func configFromFile() *httpproxy.Config {
	fc := loadProxyConfig()
	if fc == nil {
		return nil // no file → use env vars
	}
	if fc.Enabled != nil && !*fc.Enabled {
		// Explicitly disabled → empty config (no proxy, env ignored).
		return &httpproxy.Config{}
	}
	// Enabled explicitly or implicitly (field omitted): use the file's values.
	return &httpproxy.Config{
		HTTPProxy:  fc.HTTPProxy,
		HTTPSProxy: fc.HTTPSProxy,
		NoProxy:    fc.NoProxy,
	}
}

// ReloadProxyConfig forces proxy.conf and the derived proxy state to be re-read
// on the next use. It's safe to call at runtime (e.g. wired to a signal or the
// LocalAPI) to pick up an edited proxy.conf without restarting.
func ReloadProxyConfig() {
	configFileMu.Lock()
	configFileRead = false
	cachedFileConf = nil
	configFileMu.Unlock()

	mu.Lock()
	config = nil
	proxyFunc = nil
	mu.Unlock()

	// Allow the new proxy URL to be logged again.
	logMessageMu.Lock()
	logMessagePrinted = nil
	logMessageMu.Unlock()
}
