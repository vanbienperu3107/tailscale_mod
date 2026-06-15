#!/usr/bin/env bash
# setup-derp.sh — install a Tailscale DERP relay (cmd/derper) on this VPS.
#
# Purpose: give your tailnet a relay close to you (e.g. a Vultr "Santiago" VPS)
# for networks where direct UDP is blocked and everything must go over DERP/TCP
# through an HTTP proxy (corporate Squid). The relay listens on TCP/443, so the
# proxy tunnels to it via CONNECT exactly like it does for Tailscale's own DERP.
#
# Prerequisites (do these FIRST):
#   1. A fresh Ubuntu/Debian VPS with a PUBLIC IP, near your users.
#   2. A DNS A-record for a hostname pointing to this VPS's public IP, e.g.
#         derp-lima.example.com  ->  1.2.3.4
#      Let's Encrypt needs this to issue the TLS certificate.
#   3. (Recommended) A reusable Tailscale auth key from the admin console
#      (Settings -> Keys). With it, the relay only serves YOUR tailnet
#      (--verify-clients); without it the relay is OPEN to anyone.
#
# Usage (run as root):
#   sudo DERP_HOSTNAME=derp-lima.example.com \
#        TS_AUTHKEY=tskey-auth-xxxxxxxx \
#        bash setup-derp.sh
#
# After it finishes, open https://<DERP_HOSTNAME>/ — you should see a DERP page.
# Then add the relay to your tailnet ACL (see README.md in this folder).

set -euo pipefail

DERP_HOSTNAME="${DERP_HOSTNAME:?set DERP_HOSTNAME=your.derp.domain (must already point to this server)}"
TS_AUTHKEY="${TS_AUTHKEY:-}"
GO_VERSION="${GO_VERSION:-1.24.5}"   # bootstrap Go; auto-fetches the toolchain derper needs

if [ "$(id -u)" -ne 0 ]; then
  echo "Please run as root (sudo)." >&2
  exit 1
fi

echo ">> DERP hostname : ${DERP_HOSTNAME}"
echo ">> verify-clients: $([ -n "${TS_AUTHKEY}" ] && echo yes || echo 'NO (open relay!)')"

export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y curl ca-certificates ufw

# --- 1. Install Go (only used to build derper) ---
if ! /usr/local/go/bin/go version >/dev/null 2>&1; then
  echo ">> Installing Go ${GO_VERSION}..."
  arch="$(dpkg --print-architecture)"   # amd64 | arm64
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${arch}.tar.gz" -o /tmp/go.tgz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tgz
fi
export PATH="/usr/local/go/bin:${PATH}"

# --- 2. Build derper (pinned to the same release as the clients) ---
echo ">> Building derper (this can take a minute)..."
GOBIN=/usr/local/bin GOTOOLCHAIN=auto go install tailscale.com/cmd/derper@v1.98.4

# --- 3. (Optional) join the tailnet so the relay can verify clients ---
VERIFY_FLAG=""
if [ -n "${TS_AUTHKEY}" ]; then
  echo ">> Installing tailscale and joining your tailnet (for --verify-clients)..."
  curl -fsSL https://tailscale.com/install.sh | sh
  tailscale up --authkey="${TS_AUTHKEY}" --hostname="derper-${DERP_HOSTNAME%%.*}" --accept-dns=false
  VERIFY_FLAG="--verify-clients"
  echo "   (Tip: in the admin console, disable key expiry for this node so the"
  echo "    relay keeps verifying clients after 6 months.)"
else
  echo "!! No TS_AUTHKEY: the relay will be OPEN (anyone who learns the hostname"
  echo "!! can relay through it). Re-run with TS_AUTHKEY=tskey-... to restrict it."
fi

# --- 4. Firewall ---
ufw allow 22/tcp   || true   # keep SSH open
ufw allow 80/tcp   || true   # Let's Encrypt HTTP-01 challenge
ufw allow 443/tcp  || true   # DERP over HTTPS  <-- the one Squid CONNECTs to
ufw allow 3478/udp || true   # STUN (useless for UDP-blocked clients, harmless otherwise)
yes | ufw enable   || true

# --- 5. systemd service ---
cat >/etc/systemd/system/derper.service <<EOF
[Unit]
Description=Tailscale DERP relay
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/derper --hostname=${DERP_HOSTNAME} --certmode=letsencrypt -a :443 --http-port=80 --stun ${VERIFY_FLAG}
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now derper

echo
echo ">> Done. Status:    systemctl status derper --no-pager"
echo ">> Live logs:       journalctl -u derper -f"
echo ">> Verify (wait ~30s for the cert, then):"
echo "     curl -sI https://${DERP_HOSTNAME}/    # expect HTTP 200 and a DERP page"
echo
echo ">> Next: add this relay to your tailnet ACL (see README.md), then run"
echo "   'tailscale netcheck' on a client — your region should appear and win."
