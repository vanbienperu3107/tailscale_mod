# Custom DERP relay (for proxy / UDP-blocked networks)

When your machines sit behind a corporate HTTP proxy (Squid) that blocks UDP,
Tailscale can't make a direct connection and must relay everything through a
**DERP** server over TCP/443 (the proxy tunnels to it with `CONNECT`, exactly
like any HTTPS site). Tailscale's nearest public DERP may be far away
(e.g. Miami ~83 ms from Peru). Running your **own DERP close to you**
(e.g. a VPS in Santiago/Lima) makes the relay leg much faster.

This works through Squid as long as you can browse arbitrary HTTPS sites at work
(that proves Squid allows `CONNECT` to any `:443`). The relay **must** listen on
**port 443**.

## 1. Get a VPS near you

- Easiest + close to Peru: **Vultr – Santiago (Chile)** (~30 ms, simple UI, cheap).
- Lowest latency: a **Lima** local provider (~1–10 ms) if you can buy one.
- Test before committing: `ping <vps-ip>` from a client — lower than Miami's ~83 ms is the goal.

A small instance (1 vCPU / 1 GB) is plenty; DERP is light on CPU but **relays
bytes**, so watch the VPS's bandwidth/egress allowance.

## 2. Point a DNS name at it

Create an A-record, e.g. `derp-lima.example.com -> <vps-public-ip>`.
Let's Encrypt needs it to issue the TLS certificate.

## 3. Run the installer

Copy `setup-derp.sh` to the VPS and run it as root:

```bash
sudo DERP_HOSTNAME=derp-lima.example.com \
     TS_AUTHKEY=tskey-auth-xxxxxxxx \
     bash setup-derp.sh
```

- `TS_AUTHKEY` is a **reusable auth key** from the admin console
  (Settings → Keys). With it the relay only serves *your* tailnet
  (`--verify-clients`). Omit it only if you accept an open relay.
- After ~30 s, `curl -sI https://derp-lima.example.com/` should return HTTP 200.

## 4. Tell your tailnet to use it (admin console → Access Controls)

Add a `derpMap` block to your ACL policy. Custom regions must use
**`RegionID` ≥ 900**:

```jsonc
"derpMap": {
  // Keep Tailscale's built-in relays as fallback at first (do NOT set
  // OmitDefaultRegions until you've confirmed yours works).
  "Regions": {
    "900": {
      "RegionID":   900,
      "RegionCode": "lima",
      "RegionName": "Lima (custom)",
      "Nodes": [
        {
          "Name":     "1",
          "RegionID": 900,
          "HostName": "derp-lima.example.com",
          "DERPPort": 443
        }
      ]
    }
  }
}
```

Notes:
- Use **`HostName`** (a public DNS name) — the proxy resolves it during `CONNECT`.
  Don't bother with `IPv4`/`STUNPort`: STUN is UDP and useless when UDP is blocked.
- Once you've verified it for a few days, you can add `"OmitDefaultRegions": true`
  to force *all* relay traffic through your DERP — but then the VPS becomes a
  single point of failure, so leave the default regions until you're confident.

## 5. Verify from a client

```
tailscale netcheck
```

Your region (e.g. "Lima (custom)") should appear in the DERP latency list and,
if it's the lowest, become the **Nearest DERP**. After that, relayed traffic
takes the short path through your VPS instead of Miami.

## Reality check

Traffic still goes **client → Squid → your DERP → peer**, and it's still
TCP-inside-TCP through the proxy. A nearby DERP removes the long relay detour
(Miami → ~Santiago/Lima) but not the proxy/TCP overhead. Expect a clear
improvement in latency and stability, not direct-UDP speed.
