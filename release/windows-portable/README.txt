Tailscale Portable (Windows, with proxy.conf support)
=====================================================

This is a portable build of Tailscale. No installer/service is required:
just unzip the folder anywhere and run the scripts below. All state is kept
inside this folder (the "state" subfolder), so it leaves no trace elsewhere.

It also supports a "proxy.conf" file (next to the .exe files) that forces ALL
of Tailscale's outbound HTTP/HTTPS connections (DERP, control plane, logs,
DNS fallback) through an HTTP/HTTPS proxy, independently of environment
variables.

Contents
--------
  tailscale.exe        - the CLI
  tailscaled.exe       - the daemon
  proxy.conf           - proxy configuration (see below)
  start-tailscale.bat  - start the daemon and log in
  stop-tailscale.bat   - disconnect and stop the daemon
  README.txt           - this file

Quick start
-----------
  1. Unzip this folder anywhere (e.g. your Desktop or a USB drive).
  2. Double-click "start-tailscale.bat".
     It will ask for Administrator rights (Tailscale needs them for the
     virtual network adapter), start the daemon, and open the login page.
  3. To stop, double-click "stop-tailscale.bat" (or close the tailscaled
     window).

Note: Administrator rights are required (Windows needs them to create the
Tailscale control pipe). The build is "portable" in the sense that nothing is
installed and all state lives in this folder - but it still elevates on start.

Networking mode: the portable build runs in "userspace-networking" mode, so it
needs no TUN driver (no wintun.dll) and starts reliably from any folder. You
can reach your tailnet from this app. To route ALL of this PC's traffic through
Tailscale (a full system VPN), use the installer build instead.

Using the proxy (proxy.conf)
----------------------------
The daemon reads "proxy.conf" from this folder (via the TS_PROXY_CONF
environment variable set by start-tailscale.bat).

Format (JSON):

  {
    "enabled": true,
    "httpProxy":  "http://proxy.example.com:8080",
    "httpsProxy": "http://proxy.example.com:8080",
    "noProxy":    "127.0.0.1,localhost,*.local",
    "proxyAuth": { "username": "user", "password": "pass" }
  }

Behavior:
  * "enabled": true   -> use these proxy settings (they override any HTTP_PROXY
                         / HTTPS_PROXY environment variables).
  * "enabled": false  -> proxying is fully OFF, even if environment proxy
                         variables are set. (This is the default shipped here.)
  * delete proxy.conf -> fall back to the standard HTTP_PROXY / HTTPS_PROXY
                         environment variables.

"proxyAuth" is optional; use it for proxies that require Basic authentication.
Note: the password is stored in plain text, so protect this folder.

To turn the proxy ON:
  1. Edit proxy.conf: set "enabled": true and fill in your proxy URLs.
  2. Restart with stop-tailscale.bat then start-tailscale.bat.

You can confirm it is in effect in the tailscaled window; you will see lines
like:
  tshttpproxy: using proxy config from proxy.conf
  tshttpproxy: using proxy "http://proxy.example.com:8080" for URL: ...

Logs (for debugging)
--------------------
Besides the live tailscaled window, full logs are written to the "logs"
subfolder next to the binaries (start-tailscale.bat points TS_LOGS_DIR there):
  logs\tailscale-service-YYYYMMDDThhmmss-*.txt
Open the newest .txt file and look for "tshttpproxy:" and "control:" lines when
diagnosing proxy/connectivity problems.
