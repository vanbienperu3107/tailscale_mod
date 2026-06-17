// PAC (NATIVE): moi dia chi 10.x.x.x di QUA itop bang SOCKS5 cua tailscale
// (127.0.0.1:1055), con lai di THANG internet. Dung kem start-tailscale.bat
// MODE=votam (native, KHONG gost). Hieu luc tot tren Chrome / Edge / Firefox.
function FindProxyForURL(url, host) {
    // 10.0.0.0/8 = moi IP bat dau bang "10." -> qua itop (subnet route da accept)
    if (isInNet(host, "10.0.0.0", "255.0.0.0")) return "SOCKS5 127.0.0.1:1055";

    // (Tuy chon) neu truy cap bang TEN MIEN noi bo, bo comment + sua duoi:
    // if (dnsDomainIs(host, ".corp.local")) return "SOCKS5 127.0.0.1:1055";

    // === DU PHONG (mode votam-gost) ===
    // Neu dung che do gost cu, doi dong tren thanh: return "PROXY 127.0.0.1:18888";

    return "DIRECT";
}
