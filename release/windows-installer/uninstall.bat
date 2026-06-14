@echo off
REM Double-click launcher for uninstall.ps1 (bypasses PowerShell execution policy).
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0uninstall.ps1"
