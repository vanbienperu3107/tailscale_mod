@echo off
REM Khoi dong nhanh cho may DUNG (votam) - native, KHONG gost.
REM Chi can double-click file nay; sau do bat PAC tailscale-proxy.pac cho trinh duyet
REM (hoac dat SOCKS5 127.0.0.1:7654).
"%~dp0start-tailscale.bat" votam
