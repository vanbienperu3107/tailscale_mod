@echo off
setlocal EnableExtensions
title Stop Tailscale Portable

net session >nul 2>&1
if %errorlevel% neq 0 (
    powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
    exit /b
)

cd /d "%~dp0"
echo Disconnecting and stopping Tailscale...
tailscale.exe down >nul 2>&1
taskkill /IM tailscaled.exe /F >nul 2>&1
echo Stopped.
echo.
pause
