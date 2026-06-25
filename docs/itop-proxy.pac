// ============================================================================
//  itop-proxy.pac  -  DAT TREN MAY VOTAM (ban Tailscale day du / installer)
// ----------------------------------------------------------------------------
//  Cac trang chi may itop (o Peru) vao duoc -> di QUA itop.
//  Con lai -> di thang internet binh thuong.
//
//  Proxy TICH HOP trong tailscaled cua itop (port 7655). itop chay:
//    start-tailscale.bat itop   (da tu bat TS_PEER_HTTP_PROXY=7655).
//
//  CACH DUNG:
//    1. Sua so 100.64.0.11 ben duoi thanh IP "100.x" THAT cua may itop
//       (tren may itop chay: tailscale status  -> lay dong IP bat dau bang 100).
//    2. Windows: Settings > Network & Internet > Proxy > Use setup script = ON
//       Script address = file:///C:/duong-dan/itop-proxy.pac
//       (Hoac dung proxy-switcher: HTTP + HTTPS = <ip-itop>:7655)
//    3. Mo trinh duyet -> vao bitel.com.pe / viettel.com.vn.
//
//  THEM TRANG: copy 1 dong shExpMatch va doi ten mien.
// ============================================================================
function FindProxyForURL(url, host) {
    var ITOP = "PROXY 100.64.0.11:7655";   // <-- SUA IP NAY = IP 100.x cua may itop

    if (shExpMatch(host, "bitel.com.pe")   || shExpMatch(host, "*.bitel.com.pe"))   return ITOP;
    if (shExpMatch(host, "viettel.com.vn") || shExpMatch(host, "*.viettel.com.vn")) return ITOP;
    // Them trang moi o day, vi du:
    // if (shExpMatch(host, "abc.com") || shExpMatch(host, "*.abc.com")) return ITOP;

    return "DIRECT";   // moi thu khac di thang
}
