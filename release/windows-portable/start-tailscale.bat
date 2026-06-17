@echo off
setlocal EnableExtensions
title Tailscale Portable (self-host)

REM ===================== CAU HINH (sua o day) =====================
REM Headscale server tu host (KHONG phai Tailscale Inc):
set "HS_SERVER=https://vpn2.hangocthanh.io.vn"

REM (Tuy chon) pre-auth key. De TRONG = dang nhap bang Google (OIDC).
set "HS_AUTHKEY="

REM LAN-proxy (truy cap mang noi bo cua may KHAC, vd 10.x.x.x):
REM   itop       = may o trong mang do, CHIA SE  (NATIVE: advertise-routes, KHONG gost)
REM   votam      = may muon DUNG                 (NATIVE: accept-routes + SOCKS5, KHONG gost)
REM   itop-gost  = du phong: chia se kieu cu bang gost + tailscale serve
REM   votam-gost = du phong: dung kieu cu bang gost bridge
REM   (de trong) = tat, chi chay VPN binh thuong
set "LAN_PROXY_MODE="

REM Dai IP noi bo se di qua tailnet (mac dinh ca 10.0.0.0/8).
set "LAN_ROUTES=10.0.0.0/8"

REM SOCKS5 cuc bo (userspace + LAN-proxy). Trinh duyet votam tro vao day.
set "SOCKS_ADDR=127.0.0.1:1055"
REM (chi cho cac mode -gost) IP tailnet + port cua may itop chia se:
set "ITOP_TS_IP=100.64.0.1"
set "PROXY_PORT=18080"
REM ===============================================================

REM Tailscale can quyen admin (tao named pipe). Tu nang quyen neu chua co.
net session >nul 2>&1
if not errorlevel 1 goto admin_ok
echo Requesting administrator privileges...
powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
exit /b
:admin_ok
cd /d "%~dp0"

REM Portable: state + proxy.conf + logs nam canh binaries.
set "TS_PROXY_CONF=%~dp0proxy.conf"
set "TS_LOGS_DIR=%~dp0logs"
REM Sau HTTP proxy, UDP thuong bi chan -> ep di DERP (TCP) + giu tunnel song.
set "TS_DEBUG_ALWAYS_USE_DERP=1"
set "TS_DERP_KEEPALIVE_SECS=25"
if not exist "%~dp0state" mkdir "%~dp0state"
if not exist "%~dp0logs" mkdir "%~dp0logs"

REM Mode itop NATIVE: quang ba route LAN vao tailnet (server tu duyet - auto-approve).
set "LANARG="
if /I "%LAN_PROXY_MODE%"=="itop" set "LANARG=--advertise-routes=%LAN_ROUTES%"

echo ============================================================
echo  Tailscale Portable (userspace) - self-host
echo   Server     : %HS_SERVER%
echo   SOCKS5     : %SOCKS_ADDR%
echo   LAN proxy  : %LAN_PROXY_MODE%
echo ============================================================
echo.
echo Starting tailscaled...
REM userspace-networking: khong can wintun/driver. Bat SOCKS5 de app/LAN-proxy dung.
start "tailscaled - Tailscale Portable" /D "%~dp0" tailscaled.exe --tun=userspace-networking --socks5-server=%SOCKS_ADDR% --statedir="%~dp0state" --verbose=1

echo Waiting for the daemon...
if defined HS_AUTHKEY (set "AUTHARG=--authkey=%HS_AUTHKEY%") else (set "AUTHARG=")
set /a n=0
:trylogin
timeout /t 2 /nobreak >nul
REM --login-server tro ve headscale tu host; --unattended giu ket noi sau khi CLI thoat;
REM --accept-routes de nhan route LAN do itop quang ba; %LANARG% = advertise (mode itop).
tailscale.exe up --unattended --login-server=%HS_SERVER% %AUTHARG% --accept-routes %LANARG%
if %errorlevel% equ 0 goto loggedin
set /a n+=1
if %n% lss 10 goto trylogin
echo.
echo [!] Khong ket noi duoc tailscaled / dang nhap. Xem cua so "tailscaled".
goto done

:loggedin
echo.
echo Connected. Status:
tailscale.exe status

REM ============ LAN-proxy (gop vao day - KHOI chay script thu 2) ============
if /I "%LAN_PROXY_MODE%"=="itop"       goto lan_itop
if /I "%LAN_PROXY_MODE%"=="votam"      goto lan_votam
if /I "%LAN_PROXY_MODE%"=="itop-gost"  goto lan_itop_gost
if /I "%LAN_PROXY_MODE%"=="votam-gost" goto lan_votam_gost
goto done

:lan_itop
echo.
echo [LAN/itop - NATIVE] Da quang ba route %LAN_ROUTES% vao tailnet.
echo   Server tu duyet (auto-approve). May votam (MODE=votam) se di qua itop nay.
echo   KHONG can gost, KHONG can tailscale serve.
goto done

:lan_votam
echo.
echo [LAN/votam - NATIVE] Da accept-routes. Truy cap %LAN_ROUTES% QUA itop bang SOCKS5.
echo   Bat proxy cho trinh duyet (1 trong 2 cach):
echo     - PAC (chi %LAN_ROUTES% di qua, con lai DIRECT):
echo         file:///%~dp0tailscale-proxy.pac
echo     - Hoac dat SOCKS5 thang: host 127.0.0.1  port 1055
goto done

:lan_itop_gost
echo.
echo [LAN/itop - GOST du phong] Chia se bang gost + tailscale serve ...
start "gost-itop" /D "%~dp0" gost.exe -L "http://127.0.0.1:%PROXY_PORT%"
tailscale.exe serve --bg --tcp %PROXY_PORT% tcp://127.0.0.1:%PROXY_PORT%
echo [LAN/itop] serve status - phai thay dong TCP %PROXY_PORT%:
tailscale.exe serve status
goto done

:lan_votam_gost
echo.
echo [LAN/votam - GOST du phong] Bac cau toi itop qua tailnet ...
start "gost-votam" /D "%~dp0" gost.exe -L "http://127.0.0.1:18888" -F "socks5://%SOCKS_ADDR%" -F "http://%ITOP_TS_IP%:%PROXY_PORT%"
echo [LAN/votam] Bat PAC, sua dong proxy ve PROXY 127.0.0.1:18888:
echo     file:///%~dp0tailscale-proxy.pac
goto done

:done
echo.
echo Giu cua so "tailscaled" mo trong khi dung. Dung: chay stop-tailscale.bat.
echo.
pause
