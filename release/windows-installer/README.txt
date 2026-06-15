Tailscale (mod) - Windows Installer (with proxy.conf support)
============================================================

This installs Tailscale as a Windows service (starts on boot) and sets up a
default proxy.conf, so the daemon can force ALL outbound HTTP/HTTPS traffic
(DERP, control plane, logs, DNS fallback) through an HTTP/HTTPS proxy.

If you prefer no installation, use the "portable" zip instead.

Install
-------
  1. Unzip this folder.
  2. Double-click "install.bat" (it will ask for Administrator rights).
     This will:
       - copy tailscale.exe / tailscaled.exe / wintun.dll to
         "%ProgramFiles%\Tailscale" (wintun.dll powers the VPN adapter),
       - register and start the "Tailscale" Windows service,
       - create %ProgramData%\Tailscale\proxy.conf (only if not already there),
       - add the install folder to the system PATH.
  3. Open a new terminal and run:  tailscale up

Uninstall
---------
  Double-click "uninstall.bat" (Administrator). It stops and removes the
  service and deletes the program files. Your configuration under
  %ProgramData%\Tailscale (proxy.conf, tailscaled.state) is kept.

Configuring the proxy (proxy.conf)
----------------------------------
The service reads:  %ProgramData%\Tailscale\proxy.conf

Format (JSON):

  {
    "enabled": true,
    "httpProxy":  "http://proxy.example.com:8080",
    "httpsProxy": "http://proxy.example.com:8080",
    "noProxy":    "127.0.0.1,localhost,*.local",
    "proxyAuth": { "username": "user", "password": "pass" }
  }

Behavior:
  * "enabled": true   -> use these settings (override HTTP_PROXY/HTTPS_PROXY).
  * "enabled": false  -> proxying fully OFF, even if env vars are set (default).
  * delete the file   -> fall back to HTTP_PROXY / HTTPS_PROXY env vars.

"proxyAuth" is optional (Basic auth). The password is stored in plain text;
the file is created under %ProgramData% which is writable only by admins.

After editing proxy.conf, apply it with:   Restart-Service Tailscale

Logs (for debugging)
--------------------
The service writes full logs to:
  %ProgramData%\Tailscale\Logs\tailscale-service-*.txt
Open the newest .txt file and look for "tshttpproxy:" and "control:" lines when
diagnosing proxy/connectivity problems.
