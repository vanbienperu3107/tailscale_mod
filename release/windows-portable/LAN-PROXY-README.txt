========================================================================
 LAN-PROXY: truy cap mang noi bo cua may KHAC (vd 10.121.x.x) qua tailnet
 (ca 2 may deu chay ban PORTABLE userspace - khong cai gi)
========================================================================

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
     -> app/trinh duyet tro vao SOCKS5 127.0.0.1:1055; tailscale dinh
        tuyen 10.x QUA itop trong netstack.

  May binh thuong (chi VPN):                  set "LAN_PROXY_MODE="

BAT PROXY tren may DUNG (votam) - 1 trong 2:
  a) PAC (KHUYEN NGHI - chi 10.x di qua, con lai DIRECT):
       Windows: Settings > Network & Internet > Proxy > Use setup script =
         file:///C:/<duong-dan-thu-muc>/tailscale-proxy.pac
       (Chrome / Edge / Firefox deu hieu SOCKS5 trong PAC.)
  b) Hoac dat SOCKS5 thang cho trinh duyet: host 127.0.0.1  port 1055

SO DO (native):
  votam browser --(PAC: chi 10.x)--> SOCKS5 127.0.0.1:1055 (tailscale)
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
     -> chay gost bridge 18888 -> SOCKS5 1055 -> itop:18080.
     -> trong tailscale-proxy.pac, doi dong proxy thanh:
          return "PROXY 127.0.0.1:18888";

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
