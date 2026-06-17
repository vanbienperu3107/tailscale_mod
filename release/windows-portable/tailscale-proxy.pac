// PAC cho votam-pc: CHI 10.120.x.x di qua itop (gost bridge cuc bo 127.0.0.1:18888),
// moi thu khac di THANG (DIRECT). Dung kem votam-use-proxy.bat.
function FindProxyForURL(url, host) {
    // Truy cap bang IP noi bo 10.120.x.x -> qua itop
    if (isInNet(host, "10.120.0.0", "255.255.0.0")) return "PROXY 127.0.0.1:18888";

    // (Tuy chon) neu truy cap bang TEN MIEN noi bo, bo comment + sua duoi:
    // if (dnsDomainIs(host, ".corp.local")) return "PROXY 127.0.0.1:18888";
    // if (dnsDomainIs(host, ".intranet"))   return "PROXY 127.0.0.1:18888";

    return "DIRECT";
}
