@echo off
setlocal EnableExtensions
title votam-pc - di toi 10.120.x.x qua itop
REM ============================================================
REM  CHAY TREN votam-pc. Dat file nay + gost.exe + tailscale-proxy.pac
REM  vao CUNG thu muc portable (canh tailscale.exe). Phai dang chay
REM  tailscaled portable (SOCKS5 127.0.0.1:1055) va da ket noi headscale.
REM ============================================================
cd /d "%~dp0"

REM ==== Cau hinh (sua neu can) ====
set "ITOP_TS_IP=100.64.0.1"      REM IP tailnet cua itop-thanhhn5
set "ITOP_PROXY_PORT=18080"      REM port itop he qua tailscale serve
set "VOTAM_SOCKS=127.0.0.1:1055" REM SOCKS5 cua tailscaled portable tren votam-pc
set "LOCAL_HTTP=127.0.0.1:18888" REM HTTP proxy cuc bo cho trinh duyet

echo Chay gost bridge: %LOCAL_HTTP%  --(SOCKS5 tailnet)-->  %ITOP_TS_IP%:%ITOP_PROXY_PORT%
REM Chuoi: trinh duyet -> gost(18888) -> SOCKS5 tailscale -> proxy itop -> 10.120.x.x
start "gost-votam" /D "%~dp0" gost.exe -L "http://%LOCAL_HTTP%" -F "socks5://%VOTAM_SOCKS%" -F "http://%ITOP_TS_IP%:%ITOP_PROXY_PORT%"

echo.
echo ===== XONG =====
echo 1) De cua so "gost-votam" chay.
echo 2) Cau hinh trinh duyet/Windows dung PAC sau (chi 10.120.x.x di qua itop):
echo      file:///%~dp0tailscale-proxy.pac
echo    (Windows: Settings ^> Network ^& Internet ^> Proxy ^> Use setup script)
echo 3) Thu mo http://10.120.x.x trong trinh duyet.
echo.
pause
