@echo off
setlocal EnableExtensions
title Tailscale Portable

REM Tailscale needs Administrator rights (TUN adapter + control named pipe).
REM Self-elevate if not already running as admin.
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo Requesting administrator privileges...
    powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
    exit /b
)

cd /d "%~dp0"

REM Portable: keep state next to the binaries and read proxy.conf from here.
set "TS_PROXY_CONF=%~dp0proxy.conf"
if not exist "%~dp0state" mkdir "%~dp0state"

echo ============================================================
echo  Tailscale Portable
echo   State dir  : %~dp0state
echo   Proxy conf : %TS_PROXY_CONF%
echo ============================================================
echo.
echo Starting tailscaled in a separate window...
start "tailscaled - Tailscale Portable" /D "%~dp0" cmd /c "set TS_PROXY_CONF=%~dp0proxy.conf&& tailscaled.exe --statedir=\"%~dp0state\" --verbose=0"

echo Waiting for the daemon to come up...
timeout /t 4 /nobreak >nul

echo.
echo Logging in (a browser window will open if needed)...
tailscale.exe up

echo.
echo Current status:
tailscale.exe status
echo.
echo Keep the "tailscaled" window open while using Tailscale.
echo To stop, run stop-tailscale.bat (or close that window).
echo.
pause
