// PAC: MOI dia chi 10.x.x.x di QUA itop (gost bridge cuc bo 127.0.0.1:18888),
// con lai di THANG internet. Dung kem start-tailscale.bat (LAN_PROXY_MODE=votam).
function FindProxyForURL(url, host) {
    // 10.0.0.0/8 = moi IP bat dau bang "10." -> qua itop
    if (isInNet(host, "10.0.0.0", "255.0.0.0")) return "PROXY 127.0.0.1:18888";

    // (Tuy chon) neu truy cap bang TEN MIEN noi bo, bo comment + sua duoi:
    // if (dnsDomainIs(host, ".corp.local")) return "PROXY 127.0.0.1:18888";

    return "DIRECT";
}
