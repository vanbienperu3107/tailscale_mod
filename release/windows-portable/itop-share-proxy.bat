@echo off
setlocal EnableExtensions
title itop - chia se mang 10.120.x.x qua tailnet
REM ============================================================
REM  CHAY TREN itop-thanhhn5 (may o trong mang 10.120.x.x).
REM  Dat file nay + gost.exe vao CUNG thu muc portable (canh tailscale.exe).
REM  Phai dang chay tailscaled portable + da dang nhap headscale truoc.
REM ============================================================
cd /d "%~dp0"

set "PROXY_PORT=18080"

echo [1/2] Chay gost HTTP proxy cuc bo tai 127.0.0.1:%PROXY_PORT% ...
REM gost dung mang binh thuong cua itop -> toi duoc 10.120.x.x
start "gost-itop" /D "%~dp0" gost.exe -L "http://127.0.0.1:%PROXY_PORT%"

echo [2/2] He cong %PROXY_PORT% len tailnet (de votam-pc toi 100.64.0.1:%PROXY_PORT%) ...
REM Can tailscale serve. Neu bao loi "unknown command"/khong ho tro -> bao lai.
tailscale.exe serve --bg --tcp %PROXY_PORT% tcp://127.0.0.1:%PROXY_PORT%

echo.
echo ===== Trang thai serve (phai thay TCP %PROXY_PORT% -> 127.0.0.1:%PROXY_PORT%) =====
tailscale.exe serve status
echo.
echo Neu thay dong serve o tren = OK. De cua so gost-itop chay.
echo De go chia se sau nay: tailscale.exe serve reset
echo.
pause
