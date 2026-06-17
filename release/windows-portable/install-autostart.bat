@echo off
setlocal EnableExtensions
title Cai tu dong chay Tailscale Portable khi dang nhap Windows

REM ============================================================
REM  Tao Scheduled Task chay start-tailscale.bat luc DANG NHAP
REM  Windows, quyen cao (KHONG hoi UAC) -> tu bat khi mo may.
REM  Mode (itop/votam) TU NHAN DIEN theo IP may, khoi nhap.
REM
REM  LAN DAU TIEN: nen chay start-tailscale.bat 1 lan de dang
REM  nhap Google; cac lan sau tu ket noi lai (khong can trinh duyet).
REM ============================================================

REM Can quyen admin de tao task /RL HIGHEST.
net session >nul 2>&1
if not errorlevel 1 goto admin_ok
echo Requesting administrator privileges...
powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
exit /b
:admin_ok
cd /d "%~dp0"

set "TASKNAME=TailscalePortable"
set "TARGET=%~dp0start-tailscale.bat"

echo.
echo Tao Scheduled Task "%TASKNAME%":
echo   chay  : "%TARGET%" auto   (mode tu nhan dien)
echo   khi   : dang nhap Windows
echo   quyen : cao nhat (khong hoi UAC)
echo.
schtasks /Create /TN "%TASKNAME%" /TR "\"%TARGET%\" auto" /SC ONLOGON /RL HIGHEST /F
if errorlevel 1 goto fail

echo.
echo XONG. Tu gio Tailscale tu chay moi khi ban dang nhap Windows (mode tu nhan dien).
echo Chay thu ngay bay gio...
schtasks /Run /TN "%TASKNAME%" >nul 2>&1
echo.
echo Go cai sau nay: chay uninstall-autostart.bat
echo.
pause
exit /b 0

:fail
echo.
echo LOI: khong tao duoc Scheduled Task.
echo - Tai khoan dang nhap PHAI la admin (de dung /RL HIGHEST).
echo - Neu may dung tai khoan admin khac, can cai bang tai khoan do.
echo Chup man hinh nay gui lai.
pause
exit /b 1
