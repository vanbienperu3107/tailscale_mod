========================================================================
 LAN-PROXY: truy cap mang noi bo cua may KHAC (vd 10.121.x.x) qua tailnet
 (ca 2 may deu chay ban PORTABLE userspace - khong cai gi)
========================================================================

**** NHANH NHAT (tu dong) ****
  1. Double-click start-tailscale.bat -> no TU NHAN DIEN vai tro:
       - May co IP 10.121.x.x (trong mang corp) -> tu bat itop (CHIA SE)
       - May khac                                -> tu bat votam (DUNG)
  2. (Tuy chon) Double-click install-autostart.bat 1 lan -> Tailscale TU CHAY
     moi khi dang nhap Windows, KHONG hoi UAC. Go: uninstall-autostart.bat.
  3. Tren may votam, bat PAC tailscale-proxy.pac cho trinh duyet (hoac SOCKS5
     127.0.0.1:7654). Xong.
  Muon ep vai tro thay vi tu nhan dien: dung start-itop.bat / start-votam.bat.

CO 2 CACH. MAC DINH dung CACH 1 (native) - gon, KHONG can gost.

------------------------------------------------------------------------
 CACH 1 (MAC DINH - NATIVE subnet routing, KHONG gost)
------------------------------------------------------------------------
Ban userspace VAN lam subnet router duoc (giong container Docker cua
Tailscale). Khong can gost, khong can `tailscale serve`.

  Tren may CHIA SE (o trong mang 10.x.x.x):  set "LAN_PROXY_MODE=itop"
     -> tu chay: tailscale up ... --advertise-routes=10.0.0.0/8
     -> server da bat AUTO-APPROVE nen route duoc duyet tu dong.

  Tren may MUON DUNG:                         set "LAN_PROXY_MODE=votam"
     -> tu chay: tailscale up ... --accept-routes  (da co san)
     -> app/trinh duyet tro vao SOCKS5 127.0.0.1:7654; tailscale dinh
        tuyen 10.x QUA itop trong netstack.

  May binh thuong (chi VPN):                  set "LAN_PROXY_MODE="

BAT PROXY tren may DUNG (votam) - 1 trong 2:
  a) PAC (KHUYEN NGHI - chi 10.x di qua, con lai DIRECT):
       Windows: Settings > Network & Internet > Proxy > Use setup script =
         file:///C:/<duong-dan-thu-muc>/tailscale-proxy.pac
       (Chrome / Edge / Firefox deu hieu SOCKS5 trong PAC.)
  b) Hoac dat SOCKS5 thang cho trinh duyet: host 127.0.0.1  port 7654

SO DO (native):
  votam browser --(PAC: chi 10.x)--> SOCKS5 127.0.0.1:7654 (tailscale)
     --(subnet route da accept)--> itop --> 10.x.x.x

KIEM TRA:
  - Tren may itop: `tailscale status` thay dong route; tren server
    route 10.0.0.0/8 = Approved (da bat auto-approve san).
  - Tren may votam: mo http://10.121.20.152:8888 (vi du) -> vao duoc.

------------------------------------------------------------------------
 CACH 2 (DU PHONG - gost, neu vi ly do nao do native chua thong)
------------------------------------------------------------------------
Van bundle san gost.exe + test-lan.bat. Dung khi can:

  Tren may CHIA SE:   set "LAN_PROXY_MODE=itop-gost"
     -> chay gost(:18080) + `tailscale serve --tcp 18080`.
  Tren may MUON DUNG: set "LAN_PROXY_MODE=votam-gost"
     -> chay gost bridge 18888 -> SOCKS5 7654 -> itop:18080.
     -> tailscale-proxy.pac DA san dinh tuyen qua gost (PROXY 127.0.0.1:18888),
        KHONG can sua tay nua.

  ** VAO TRANG CHI ITOP MO DUOC (ten mien noi bo / chan theo vung) **
  Vi du bitel.com.pe, viettel.com.vn: cac trang nay PHAI resolve DNS + ket noi
  TAI itop.

  >> CACH MOI (KHUYEN DUNG) - HTTP proxy TICH HOP trong tailscaled:
     - May itop: chay  start-tailscale.bat itop   (mode itop tu dat
       TS_PEER_HTTP_PROXY=18080 -> tailscaled mo proxy tren IP tailnet cua no).
       KHONG can gost.exe, KHONG can `tailscale serve` -> het loi quyen user.
     - May votam: tro PAC toi  PROXY <ip-100.x-cua-itop>:18080
       Vi du file PAC (votam ban day du):
         function FindProxyForURL(url, host) {
           var ITOP = "PROXY 100.64.0.10:18080";   // doi = IP 100.x cua itop
           if (shExpMatch(host,"bitel.com.pe")||shExpMatch(host,"*.bitel.com.pe")) return ITOP;
           if (shExpMatch(host,"viettel.com.vn")||shExpMatch(host,"*.viettel.com.vn")) return ITOP;
           return "DIRECT";
         }
     - Test tu votam: curl -x http://<ip-itop>:18080 -I http://bitel.com.pe

  (Cach gost itop-gost/votam-gost ben tren chi con la DU PHONG.)

TEST NHANH (che do gost): chay test-lan.bat
  - tren itop: MODE=itop  -> tu in PASS/FAIL (serve da dua 18080 len tailnet)
  - tren votam: MODE=votam -> thu curl site noi bo qua chuoi, in PASS/FAIL

------------------------------------------------------------------------
 LUU Y CHUNG
------------------------------------------------------------------------
- HS_SERVER mac dinh tro ve headscale tu host (vpn2.hangocthanh.io.vn),
  KHONG phai Tailscale Inc.
- Trang HTTPS van ma hoa dau-cuoi.
- Go chia se (mode gost): tailscale.exe serve reset
- Bo route (mode native, tren itop): tailscale.exe up ... (bo --advertise-routes).
