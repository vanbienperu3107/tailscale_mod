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

REM Tat cac PS agent nen (pac-agent, metrics-report) theo command-line, roi go AutoConfigURL.
powershell -NoProfile -Command "Get-CimInstance Win32_Process -Filter \"Name='powershell.exe'\" | Where-Object { $_.CommandLine -match 'pac-agent.ps1|metrics-report.ps1' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }" >nul 2>&1
reg delete "HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings" /v AutoConfigURL /f >nul 2>&1

echo Stopped.
echo.
pause
