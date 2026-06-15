@echo off
setlocal EnableExtensions
title Tailscale Portable

REM Tailscale needs Administrator rights (to create its control named pipe).
REM Self-elevate if not already running as admin.
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo Requesting administrator privileges...
    powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
    exit /b
)

cd /d "%~dp0"

REM Portable: keep state next to the binaries and read proxy.conf from here.
REM TS_PROXY_CONF is exported here and inherited by the tailscaled child below.
set "TS_PROXY_CONF=%~dp0proxy.conf"
set "TS_LOGS_DIR=%~dp0logs"
if not exist "%~dp0state" mkdir "%~dp0state"
if not exist "%~dp0logs" mkdir "%~dp0logs"

echo ============================================================
echo  Tailscale Portable (userspace-networking mode)
echo   State dir  : %~dp0state
echo   Proxy conf : %TS_PROXY_CONF%
echo   Logs dir   : %TS_LOGS_DIR%
echo ============================================================
echo.
echo Starting tailscaled in a separate window...
REM userspace-networking needs no TUN driver, so this works without wintun.dll.
start "tailscaled - Tailscale Portable" /D "%~dp0" tailscaled.exe --tun=userspace-networking --statedir="%~dp0state" --verbose=1

echo Waiting for the daemon to come up...
set /a n=0
:trylogin
timeout /t 2 /nobreak >nul
tailscale.exe up
if %errorlevel% equ 0 goto loggedin
set /a n+=1
if %n% lss 10 goto trylogin
echo.
echo [!] Could not reach tailscaled after several tries.
echo     Look at the "tailscaled" window for the error message.
goto done

:loggedin
echo.
echo Connected. Current status:
tailscale.exe status

:done
echo.
echo Keep the "tailscaled" window open while using Tailscale.
echo To stop, run stop-tailscale.bat (or close that window).
echo.
echo NOTE: portable runs in userspace mode (access the tailnet from this app).
echo To route ALL of this PC's traffic, use the installer build instead.
echo.
pause
