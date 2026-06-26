@echo off
setlocal EnableExtensions
title Tailscale Portable (self-host)

REM ===================== CAU HINH (sua o day) =====================
REM Headscale server tu host (KHONG phai Tailscale Inc):
set "HS_SERVER=https://vpn2.hangocthanh.io.vn"

REM (Tuy chon) pre-auth key. De TRONG = dang nhap bang Google (OIDC).
set "HS_AUTHKEY="

REM LAN-proxy mode. DE TRONG = TU NHAN DIEN:
REM   may co IP trong dai corp (ITOP_LAN_PREFIX) -> itop (CHIA SE)
REM   nguoc lai                                  -> votam (DUNG)
REM Co the dat tay: itop | votam | itop-gost | votam-gost
set "LAN_PROXY_MODE="

REM Dai IP corp de TU NHAN DIEN may itop (may co IP bat dau bang day nay -> itop).
set "ITOP_LAN_PREFIX=10.121."
REM Dai IP noi bo se di qua tailnet.
set "LAN_ROUTES=10.0.0.0/8"

REM Thu muc CHIA SE cho peer khac (vao http://^<ip-itop^>:7656/ tren trinh duyet,
REM hoac map o dia mang WebDAV). Mac dinh: thu muc "shared" canh binaries.
REM DE TRONG ("") = TAT chia se thu muc.
set "SHARE_DIR=%~dp0shared"

REM SOCKS5 cuc bo (userspace + LAN-proxy). Trinh duyet votam tro vao day.
set "SOCKS_ADDR=127.0.0.1:7654"
REM (chi cho cac mode -gost) IP tailnet + port cua may itop chia se:
set "ITOP_TS_IP=100.64.0.1"
set "PROXY_PORT=18080"
REM ===============================================================

REM Tham so (tuy chon):
REM   start-tailscale.bat <mode> [auto]   mode = itop|votam|itop-gost|votam-gost
REM   start-tailscale.bat auto            tu nhan dien mode + chay nen (cho Task)
set "AUTORUN="
if /I not "%~1"=="auto" goto chk_mode
set "AUTORUN=1"
goto args_done
:chk_mode
if not "%~1"=="" set "LAN_PROXY_MODE=%~1"
if /I "%~2"=="auto" set "AUTORUN=1"
:args_done

REM Tailscale can quyen admin (tao named pipe). Tu nang quyen, giu nguyen tham so.
net session >nul 2>&1
if not errorlevel 1 goto admin_ok
echo Requesting administrator privileges...
if "%~1"=="" goto elevate_plain
powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -ArgumentList '%*' -Verb RunAs"
exit /b
:elevate_plain
powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
exit /b
:admin_ok
cd /d "%~dp0"

REM ====== TU NHAN DIEN VAI TRO (chi khi LAN_PROXY_MODE con trong) ======
REM May co IP bat dau bang %ITOP_LAN_PREFIX% -> itop ; nguoc lai -> votam.
if not "%LAN_PROXY_MODE%"=="" goto after_detect
for /f "usebackq delims=" %%I in (`powershell -NoProfile -Command "$ips=(Get-NetIPAddress -AddressFamily IPv4 -EA SilentlyContinue).IPAddress; if ($ips -like '%ITOP_LAN_PREFIX%*') {'itop'} else {'votam'}"`) do set "LAN_PROXY_MODE=%%I"
echo [auto] Tu nhan dien vai tro: LAN_PROXY_MODE=%LAN_PROXY_MODE%
:after_detect

REM Portable: state + proxy.conf + logs nam canh binaries.
set "TS_PROXY_CONF=%~dp0proxy.conf"
set "TS_LOGS_DIR=%~dp0logs"
REM Sau HTTP proxy, UDP thuong bi chan -> ep di DERP (TCP) + giu tunnel song.
set "TS_DEBUG_ALWAYS_USE_DERP=1"
set "TS_DERP_KEEPALIVE_SECS=25"
REM HTTP proxy TICH HOP (built-in tailscaled): peer khac mo trinh duyet/PAC tro vao
REM <ip-tailnet-node>:7655 -> node nay tu resolve DNS + ket noi (vao ten mien noi bo
REM nhu bitel.com.pe). KHONG can gost.exe / `tailscale serve` / quyen user Windows.
REM Dat VO DIEU KIEN cho chac (KHONG phu thuoc auto-detect mode). Tat: de trong "".
set "TS_PEER_HTTP_PROXY=7655"
REM CHIA SE THU MUC tich hop (built-in tailscaled): peer khac vao
REM http://<ip-tailnet-node>:7656/ de duyet/tai, hoac map o dia mang (WebDAV, doc-ghi).
set "TS_PEER_FILE_SHARE=%SHARE_DIR%"
if not "%SHARE_DIR%"=="" if not exist "%SHARE_DIR%" mkdir "%SHARE_DIR%"
if not exist "%~dp0state" mkdir "%~dp0state"
if not exist "%~dp0logs" mkdir "%~dp0logs"

REM ====== OVERRIDE CAU HINH TU DASHBOARD DB (per-node, fail-open) ======
REM bootstrap-config.ps1 lay MAC -> GET /api/client/runtime -> sinh env.generated.cmd.
REM Loi/timeout/khong co script -> giu nguyen gia tri hardcode mac dinh o tren.
REM env.generated.cmd da resolve san (default+override) o server: HS_SERVER, LAN_PROXY_MODE,
REM LAN_ROUTES, SOCKS_ADDR, TS_PEER_HTTP_PROXY, TS_DERP_KEEPALIVE_SECS, TS_DEBUG_ALWAYS_USE_DERP
REM (rong = cho phep UDP/direct), PAC_SERVER_PORT, PAC_URL.
if not defined PAC_SERVER_PORT set "PAC_SERVER_PORT=7658"
if exist "%~dp0bootstrap-config.ps1" (
  powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0bootstrap-config.ps1" >nul 2>&1
  if exist "%~dp0env.generated.cmd" call "%~dp0env.generated.cmd"
)

REM Mode itop NATIVE: quang ba route LAN vao tailnet (server tu duyet - auto-approve).
set "LANARG="
if /I "%LAN_PROXY_MODE%"=="itop" set "LANARG=--advertise-routes=%LAN_ROUTES%"

REM (HTTP proxy tich hop da bat VO DIEU KIEN o tren - TS_PEER_HTTP_PROXY=7655.)

echo ============================================================
echo  Tailscale Portable (userspace) - self-host
echo   Server     : %HS_SERVER%
echo   SOCKS5     : %SOCKS_ADDR%
echo   LAN proxy  : %LAN_PROXY_MODE%
echo   HTTP proxy : tich hop port 7655 (peer vao ^<ip-node^>:7655)
if not "%SHARE_DIR%"=="" echo   File share : %SHARE_DIR% -^> http://^<ip-node^>:7656/
echo ============================================================
echo.
echo Starting tailscaled (chay AN/hidden - dong cua so KHONG lam chet daemon)...
REM Chi giu 1 instance: kill cai cu (neu co) roi chay lai sach.
taskkill /IM tailscaled.exe /F >nul 2>&1
REM userspace-networking: khong can wintun/driver. Bat SOCKS5 de app/LAN-proxy dung.
REM Chay HIDDEN + detached: khong co cua so de dong nham; log ghi vao thu muc logs\.
REM Muon dung han: chay stop-tailscale.bat.
powershell -NoProfile -Command "Start-Process -WindowStyle Hidden -WorkingDirectory '%~dp0' -FilePath '%~dp0tailscaled.exe' -ArgumentList '--tun=userspace-networking','--socks5-server=%SOCKS_ADDR%','--statedir=%~dp0state','--verbose=1'"

echo Waiting for the daemon...
if defined HS_AUTHKEY (set "AUTHARG=--authkey=%HS_AUTHKEY%") else (set "AUTHARG=")
set /a n=0
:trylogin
timeout /t 2 /nobreak >nul
REM --login-server -> headscale tu host; --unattended giu ket noi; --accept-routes
REM de nhan route LAN do itop quang ba; %LANARG% = advertise (chi mode itop).
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

REM ===== Reporter MAC + latency (ZERO-CONFIG, chay AN + tach tien trinh) =====
REM Tu tim peer 'collector' trong tailnet roi gui (khong token, khong cau hinh).
REM Mutex trong .ps1 dam bao 1 ban. Khong thay collector -> tu cho, khong loi.
REM Tat: xoa metrics-report.ps1.
if exist "%~dp0metrics-report.ps1" (
  echo Bat reporter MAC/latency ^(an^)...
  powershell -NoProfile -Command "Start-Process -WindowStyle Hidden -FilePath 'powershell.exe' -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-WindowStyle','Hidden','-File','%~dp0metrics-report.ps1'"
)

REM ===== PAC agent: serve PAC tu RAM tai 127.0.0.1:%PAC_SERVER_PORT%, refresh 30s tu DB =====
REM Mutex trong .ps1 dam bao 1 ban. Tat: xoa pac-agent.ps1. Tro browser tu dong: PAC_AUTOSET=1.
REM (Dat default NGOAI khoi if de %PAC_AUTOSET% expand dung - tranh bug delayed-expansion.)
if not defined PAC_AUTOSET set "PAC_AUTOSET=1"
if exist "%~dp0pac-agent.ps1" (
  echo Bat PAC agent ^(an, port %PAC_SERVER_PORT%^)...
  powershell -NoProfile -Command "Start-Process -WindowStyle Hidden -FilePath 'powershell.exe' -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-WindowStyle','Hidden','-File','%~dp0pac-agent.ps1'"
)
if exist "%~dp0pac-agent.ps1" if "%PAC_AUTOSET%"=="1" (
  reg add "HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings" /v AutoConfigURL /t REG_SZ /d "http://127.0.0.1:%PAC_SERVER_PORT%/proxy.pac" /f >nul 2>&1
  echo   AutoConfigURL -^> http://127.0.0.1:%PAC_SERVER_PORT%/proxy.pac
)

REM ============ LAN-proxy (gop vao day - KHOI chay script thu 2) ============
if /I "%LAN_PROXY_MODE%"=="itop"       goto lan_itop
if /I "%LAN_PROXY_MODE%"=="votam"      goto lan_votam
if /I "%LAN_PROXY_MODE%"=="itop-gost"  goto lan_itop_gost
if /I "%LAN_PROXY_MODE%"=="votam-gost" goto lan_votam_gost
goto done

:lan_itop
echo.
echo [LAN/itop - NATIVE] Da quang ba route %LAN_ROUTES% vao tailnet.
echo   Server tu duyet (auto-approve). May votam se di qua itop nay.
echo   HTTP proxy TICH HOP da bat: peer goi vao ^<ip-tailnet-itop^>:7655
echo   de vao ten mien noi bo (vd bitel.com.pe). KHONG gost, KHONG serve.
goto done

:lan_votam
echo.
echo [LAN/votam - NATIVE] Da accept-routes. Truy cap %LAN_ROUTES% QUA itop bang SOCKS5.
echo   Bat proxy cho trinh duyet (1 trong 2 cach):
echo     - PAC (chi %LAN_ROUTES% di qua, con lai DIRECT):
echo         file:///%~dp0tailscale-proxy.pac
echo     - Hoac dat SOCKS5 thang: host 127.0.0.1  port 7654
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
if defined AUTORUN goto finish
echo.
echo Giu cua so "tailscaled" mo trong khi dung. Dung: chay stop-tailscale.bat.
echo.
pause
:finish
