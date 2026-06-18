@echo off
setlocal EnableExtensions
title TEST LAN-proxy

set "HS_SERVER=https://vpn2.hangocthanh.io.vn"
set "MODE=itop"
set "TARGET=http://10.121.20.152:8888/WebTool/query.xhtml"
set "ITOP_TS_IP=100.64.0.1"

net session >nul 2>&1
if not errorlevel 1 goto admin_ok
echo Xin quyen admin - chon tai khoan admin cua may [vd vtp.ins1] ...
powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
exit /b
:admin_ok

cd /d "%~dp0"
if not exist "%~dp0state" mkdir "%~dp0state"

echo ============================================================
echo   TEST LAN-proxy   MODE=%MODE%
echo   Server : %HS_SERVER%
echo   Folder : %~dp0
echo ============================================================
echo.
echo [1/4] Khoi dong tailscaled ...
start "tailscaled-test" /D "%~dp0" tailscaled.exe --tun=userspace-networking --socks5-server=127.0.0.1:7654 --statedir="%~dp0state" --verbose=1
timeout /t 6 /nobreak >nul

echo [2/4] Dang nhap headscale [neu hien link thi dang nhap Google] ...
tailscale.exe up --reset --login-server=%HS_SERVER% --accept-routes
echo --- status ---
tailscale.exe status
echo.

if /i "%MODE%"=="votam" goto mode_votam

echo [3/4] Khoi dong gost HTTP proxy 127.0.0.1:18080 ...
start "gost-itop" /D "%~dp0" gost.exe -L "http://127.0.0.1:18080"
timeout /t 3 /nobreak >nul
echo [4/4] tailscale serve dua cong 18080 len tailnet ...
tailscale.exe serve --bg --tcp 18080 tcp://127.0.0.1:18080
tailscale.exe serve status > "%~dp0_serve.txt" 2>&1
echo --- serve status ---
type "%~dp0_serve.txt"
echo --- netstat 18080 ---
netstat -ano | findstr 18080
echo.
echo ============================================================
findstr /C:"18080" "%~dp0_serve.txt" >nul 2>&1
if errorlevel 1 goto itop_fail
echo   KET QUA: PASS - serve da dua 18080 len tailnet.
echo   itop SAN SANG. Sang may votam: sua MODE=votam roi chay test-lan.bat.
goto done
:itop_fail
echo   KET QUA: FAIL - serve khong dua duoc 18080.
echo   Chup toan bo cua so nay gui lai.
goto done

:mode_votam
echo [3/4] Khoi dong gost bridge 18888 qua socks5 toi itop %ITOP_TS_IP%:18080 ...
start "gost-votam" /D "%~dp0" gost.exe -L "http://127.0.0.1:18888" -F "socks5://127.0.0.1:7654" -F "http://%ITOP_TS_IP%:18080"
timeout /t 3 /nobreak >nul
echo [4/4] Thu truy cap site noi bo: %TARGET%
curl.exe -x http://127.0.0.1:18888 -s -o NUL -w "HTTP_CODE=%%{http_code}" --max-time 25 "%TARGET%" > "%~dp0_curl.txt" 2>&1
type "%~dp0_curl.txt"
echo.
echo ============================================================
findstr /C:"HTTP_CODE=000" "%~dp0_curl.txt" >nul 2>&1
if not errorlevel 1 goto votam_fail
echo   KET QUA: PASS - vao duoc site noi bo qua itop.
echo   Bat PAC cho trinh duyet: %~dp0tailscale-proxy.pac
goto done
:votam_fail
echo   KET QUA: FAIL - khong nhan phan hoi [HTTP 000].
echo   Kiem may itop da chay test-lan.bat MODE=itop PASS chua.
goto done

:done
echo ============================================================
echo.
echo Giu cua so tailscaled / gost mo neu muon dung tiep.
pause
