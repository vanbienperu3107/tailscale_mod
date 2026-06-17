@echo off
setlocal EnableExtensions
title TEST LAN-proxy (tu dong)

REM ===================== CAU HINH =====================
set "HS_SERVER=https://vpn2.hangocthanh.io.vn"
REM MODE: itop = may CHIA SE (o trong mang noi bo) ; votam = may DUNG
set "MODE=itop"
REM (chi cho votam) 1 dia chi noi bo de thu truy cap:
set "TARGET=http://10.121.20.152:8888/WebTool/query.xhtml"
REM IP tailnet cua may itop (may chia se):
set "ITOP_TS_IP=100.64.0.1"
REM ===================================================

REM Tu nang quyen (Tailscale can admin). Lan UAC nay chay bang tai khoan admin
REM (vd vtp.ins1) -> tailscaled + serve cung 1 user -> khong bi loi lech user.
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo Xin quyen admin (chon dung tai khoan admin cua may, vd vtp.ins1)...
    powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
    exit /b
)
cd /d "%~dp0"
if not exist "%~dp0state" mkdir "%~dp0state"

echo ============================================================
echo   TEST LAN-proxy   MODE=%MODE%   Server=%HS_SERVER%
echo   Thu muc: %~dp0
echo ============================================================
echo.
echo [1/4] Khoi dong tailscaled (userspace + socks5 1055)...
start "tailscaled-test" /D "%~dp0" tailscaled.exe --tun=userspace-networking --socks5-server=127.0.0.1:1055 --statedir="%~dp0state" --verbose=1
echo      doi 6 giay...
timeout /t 6 /nobreak >nul

echo [2/4] Dang nhap headscale (neu hien link -^> dang nhap Google)...
tailscale.exe up --reset --login-server=%HS_SERVER% --accept-routes
echo      --- status ---
tailscale.exe status
echo.

if /I "%MODE%"=="itop"  goto test_itop
if /I "%MODE%"=="votam" goto test_votam
echo [!] MODE sai - phai la itop hoac votam.
goto end

:test_itop
echo [3/4] Khoi dong gost (HTTP proxy noi bo 127.0.0.1:18080)...
start "gost-itop" /D "%~dp0" gost.exe -L "http://127.0.0.1:18080"
timeout /t 3 /nobreak >nul
echo [4/4] tailscale serve dua cong 18080 len tailnet...
tailscale.exe serve --bg --tcp 18080 tcp://127.0.0.1:18080
tailscale.exe serve status > "%~dp0_serve.txt" 2>&1
echo      --- serve status ---
type "%~dp0_serve.txt"
echo      --- netstat 18080 ---
netstat -ano | findstr 18080
echo.
echo ============================================================
findstr /C:"18080" "%~dp0_serve.txt" >nul 2>&1
if %errorlevel% equ 0 (
  echo   KET QUA: PASS  ^>^>  serve da dua 18080 len tailnet.
  echo   itop SAN SANG chia se. Sang may votam dat MODE=votam roi chay test-lan.bat.
) else (
  echo   KET QUA: FAIL  ^>^>  serve KHONG dua duoc 18080.
  echo   Chup TOAN BO cua so nay gui lai (nhat la phan serve status o tren^).
)
echo ============================================================
goto end

:test_votam
echo [3/4] Khoi dong gost bridge (18888 -^> socks5 -^> itop %ITOP_TS_IP%:18080)...
start "gost-votam" /D "%~dp0" gost.exe -L "http://127.0.0.1:18888" -F "socks5://127.0.0.1:1055" -F "http://%ITOP_TS_IP%:18080"
timeout /t 3 /nobreak >nul
echo [4/4] Thu truy cap site noi bo qua chuoi:
echo      %TARGET%
curl.exe -x http://127.0.0.1:18888 -s -o NUL -w "HTTP_CODE=%%{http_code}" --max-time 25 "%TARGET%" > "%~dp0_curl.txt" 2>&1
type "%~dp0_curl.txt"
echo.
echo ============================================================
findstr /C:"HTTP_CODE=000" "%~dp0_curl.txt" >nul 2>&1
if %errorlevel% equ 0 (
  echo   KET QUA: FAIL ^>^> khong nhan duoc phan hoi (HTTP 000^).
  echo   Kiem: may itop da chay test-lan.bat MODE=itop ^(PASS^) chua?
) else (
  echo   KET QUA: PASS ^>^> da vao duoc site noi bo QUA itop.
  echo   Bat PAC cho trinh duyet: file:///%~dp0tailscale-proxy.pac
)
echo ============================================================
goto end

:end
echo.
echo (Giu cua so tailscaled / gost dang mo neu muon dung tiep.)
echo (Dung het: chay stop-tailscale.bat hoac dong cac cua so do.)
pause
