// PAC cho che do VOTAM-GOST: cac trang "chi itop vao duoc" (ten mien noi bo /
// chan theo vung) va dai IP noi bo di QUA itop bang HTTP proxy (gost chain:
// 127.0.0.1:18888 -> tailnet -> gost tren itop -> itop TU resolve DNS + ket noi).
// Con lai di THANG internet. Dung kem:
//   may itop  : start-tailscale.bat itop-gost
//   may votam : start-tailscale.bat votam-gost
// Hieu luc tot tren Chrome / Edge / Firefox.
//
// >>> THEM TRANG: chi can them 1 dong shExpMatch trong khoi "DOMAIN" duoi day. <<<
function FindProxyForURL(url, host) {
    var ITOP = "PROXY 127.0.0.1:18888";  // gost chain toi itop

    // ===== DOMAIN: cac trang chi itop vao duoc (resolve TAI itop) =====
    if (shExpMatch(host, "bitel.com.pe")   || shExpMatch(host, "*.bitel.com.pe"))   return ITOP;
    if (shExpMatch(host, "viettel.com.vn") || shExpMatch(host, "*.viettel.com.vn")) return ITOP;
    // Them domain moi o day, vi du:
    // if (shExpMatch(host, "intranet.itop") || shExpMatch(host, "*.intranet.itop")) return ITOP;

    // ===== DAI IP noi bo (neu truy cap bang IP, khong phai ten mien) =====
    if (isInNet(host, "10.0.0.0",    "255.0.0.0"))   return ITOP;
    if (isInNet(host, "172.16.0.0",  "255.240.0.0")) return ITOP;
    if (isInNet(host, "192.168.0.0", "255.255.0.0")) return ITOP;

    // ===== DU PHONG (che do votam NATIVE, khong gost) =====
    // Neu chay 'votam' (khong gost) va chi can dai IP noi bo, doi cac dong tren
    // thanh: return "SOCKS5 127.0.0.1:7654";  (luc do ten mien noi bo se KHONG vao duoc).

    return "DIRECT";  // moi thu khac di thang internet
}
