@echo off
setlocal EnableExtensions
title Cai tu dong chay Tailscale Portable khi dang nhap Windows

REM ============================================================
REM  Tao Scheduled Task chay Tailscale luc DANG NHAP Windows,
REM  quyen cao (KHONG hoi UAC) -> tu bat khi mo may.
REM  Chay AN HOAN TOAN qua wscript+run-hidden.vbs: KHONG cua so,
REM  KHONG nhay den. Mode (itop/votam) TU NHAN DIEN theo IP may.
REM
REM  LAN DAU TIEN: PHAI chay start-itop.bat (hoac start-tailscale.bat)
REM  1 lan de THAY URL dang nhap Google; cac lan sau task tu ket noi
REM  lai (khong can trinh duyet) -> dung "chi lan dau can login".
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
set "VBS=%~dp0run-hidden.vbs"

if not exist "%VBS%" (
  echo LOI: khong tim thay run-hidden.vbs canh file nay.
  echo Hay giai nen DAY DU goi portable roi chay lai.
  pause
  exit /b 1
)

echo.
echo Tao Scheduled Task "%TASKNAME%":
echo   chay  : wscript "%VBS%"   (chay AN start-tailscale.bat auto)
echo   khi   : dang nhap Windows
echo   quyen : cao nhat (khong hoi UAC)
echo   hien  : KHONG cua so, KHONG nhay den
echo.
schtasks /Create /TN "%TASKNAME%" /TR "wscript.exe \"%VBS%\"" /SC ONLOGON /RL HIGHEST /F
if errorlevel 1 goto fail

echo.
echo XONG. Tu gio Tailscale tu chay AN moi khi ban dang nhap Windows (mode tu nhan dien).

REM URL ACL cho PAC agent (HttpListener 127.0.0.1:<port>) - de bind duoc ca khi KHONG admin.
REM Mac dinh port 7658; doi PAC_SERVER_PORT neu dashboard cap port khac.
if not defined PAC_SERVER_PORT set "PAC_SERVER_PORT=7658"
echo Cap URL ACL cho PAC agent port %PAC_SERVER_PORT% ...
netsh http add urlacl url=http://127.0.0.1:%PAC_SERVER_PORT%/ user=Everyone >nul 2>&1

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
