@echo off
setlocal EnableExtensions
title Go cai tu dong chay Tailscale Portable

REM Can quyen admin de xoa Scheduled Task.
net session >nul 2>&1
if not errorlevel 1 goto admin_ok
echo Requesting administrator privileges...
powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
exit /b
:admin_ok

set "TASKNAME=TailscalePortable"
echo Xoa Scheduled Task "%TASKNAME%" ...
schtasks /Delete /TN "%TASKNAME%" /F
echo.
echo Da go (neu co). Tailscale van chay den khi ban tat (stop-tailscale.bat).
echo.
pause
