========================================================================
 BO LACH: cho votam-pc truy cap 10.120.x.x QUA itop-thanhhn5
 (ca 2 may deu chay Tailscale ban PORTABLE userspace - khong cai gi)
========================================================================

VI SAO CAN: ban portable userspace KHONG lam subnet router duoc. Nen ta di
vong bang 1 proxy nho (gost) chay tren itop, "he cong" qua tailnet bang
tailscale serve; votam-pc noi vao do qua SOCKS5 cua tailscale. Chi 10.120.x.x
di duong nay (theo PAC), con lai van di internet binh thuong.

SO DO:
  votam-pc browser --(PAC: chi 10.120.x.x)--> gost(votam :18888)
     --(SOCKS5 1055 cua tailscale = vao tailnet)--> itop 100.64.0.1:18080
     --> gost(itop) --> 10.120.x.x   (itop o trong mang nay nen toi duoc)

------------------------------------------------------------------------
 TRONG BO NAY
------------------------------------------------------------------------
  gost.exe               - cong cu proxy (portable, khong cai)
  itop-share-proxy.bat   - CHAY TREN itop  (chia se)
  votam-use-proxy.bat    - CHAY TREN votam-pc (su dung)
  tailscale-proxy.pac    - cau hinh trinh duyet votam-pc (chi 10.120.x.x)
  README.txt             - file nay

------------------------------------------------------------------------
 CAI DAT (copy file vao thu muc portable)
------------------------------------------------------------------------
Tren CA HAI may: chep TAT CA file trong bo nay vao dung thu muc portable
cua Tailscale (cho co tailscale.exe + tailscaled.exe). Phai dang chay
tailscaled portable + da dang nhap headscale truoc.

------------------------------------------------------------------------
 CHAY (dung thu tu)
------------------------------------------------------------------------
1) TREN itop:  chay  itop-share-proxy.bat
   - Phai thay dong "serve status" liet ke TCP 18080 -> 127.0.0.1:18080.
   - Neu bao "unknown command" / khong ho tro serve  => bao lai, ban
     portable cua ban khong co serve, phai dung cach khac.

2) TREN votam-pc:  chay  votam-use-proxy.bat
   - De cua so gost-votam chay.

3) TREN votam-pc:  bat PAC cho trinh duyet HOAC Windows:
   - Windows: Settings > Network & Internet > Proxy > Use setup script =
     file:///C:/.../tailscale-proxy.pac  (duong dan toi file trong thu muc portable)

4) Thu mo  http://10.120.x.x  trong trinh duyet votam-pc -> phai vao duoc.
   Cac trang khac van di thang (khong qua itop).

------------------------------------------------------------------------
 LUU Y
------------------------------------------------------------------------
- IP tailnet itop mac dinh 100.64.0.1; SOCKS5 votam 127.0.0.1:1055.
  Neu khac, sua trong votam-use-proxy.bat.
- Truy cap bang IP 10.120.x.x thi PAC khop ngay. Neu dung TEN MIEN noi bo,
  mo tailscale-proxy.pac va them dong dnsDomainIs (co san vi du).
- Trang HTTPS van ma hoa dau-cuoi (itop chi tunnel CONNECT, khong doc duoc).
  Trang HTTP thi noi dung di qua gost itop dang ro - chap nhan duoc trong LAN.
- Go chia se tren itop:  tailscale.exe serve reset
