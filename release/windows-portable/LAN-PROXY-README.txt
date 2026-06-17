========================================================================
 LAN-PROXY: truy cap mang noi bo cua may KHAC (vd 10.120.x.x) qua tailnet
 (ca 2 may deu chay ban PORTABLE userspace - khong cai gi)
========================================================================

VI SAO: ban portable userspace KHONG lam subnet router duoc. Nen ta di vong:
may o trong mang noi bo chay 1 proxy nho (gost) + "he cong" qua tailnet bang
`tailscale serve`; may kia noi vao qua SOCKS5 cua tailscale. Chi 10.120.x.x di
duong nay (theo PAC), con lai van di internet binh thuong.

------------------------------------------------------------------------
 CHI CAN 1 SCRIPT: start-tailscale.bat
------------------------------------------------------------------------
Mo start-tailscale.bat, sua dong cau hinh o dau file:

  Tren may CHIA SE (o trong mang 10.120.x.x):   set "LAN_PROXY_MODE=itop"
  Tren may MUON DUNG:                            set "LAN_PROXY_MODE=votam"
  May binh thuong (chi VPN):                     set "LAN_PROXY_MODE="

Roi double-click start-tailscale.bat. No tu lam HET: chay tailscaled, dang nhap
headscale (HS_SERVER), va bat phan LAN-proxy theo vai tro. KHONG can chay 2 script.

------------------------------------------------------------------------
 SO DO
------------------------------------------------------------------------
  may-dung browser --(PAC: chi 10.120.x.x)--> gost(:18888)
     --(SOCKS5 127.0.0.1:1055 = vao tailnet)--> may-chia-se 100.64.0.1:18080
     --> gost --> 10.120.x.x

------------------------------------------------------------------------
 BAT PAC tren may DUNG (votam)
------------------------------------------------------------------------
Windows: Settings > Network & Internet > Proxy > Use setup script =
  file:///C:/<duong-dan-thu-muc>/tailscale-proxy.pac
Chi 10.120.x.x di qua tunnel; con lai DIRECT. (Sua dai IP trong file .pac neu can.)

------------------------------------------------------------------------
 KIEM TRA / LUU Y
------------------------------------------------------------------------
- Tren may CHIA SE, khi chay phai thay "serve status" liet ke TCP 18080.
  Neu bao "unknown command" => ban build khong ho tro `tailscale serve`.
- Truy cap bang IP 10.120.x.x thi PAC khop ngay. Neu dung TEN MIEN noi bo,
  mo tailscale-proxy.pac va them dong dnsDomainIs (co san vi du).
- Trang HTTPS van ma hoa dau-cuoi. Go chia se: tailscale.exe serve reset
- HS_SERVER mac dinh tro ve headscale tu host (vpn2.hangocthanh.io.vn),
  KHONG phai Tailscale Inc.
