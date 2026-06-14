# Tailscale (mod) installer for Windows.
# Installs the binaries, registers tailscaled as a Windows service, and drops a
# default proxy.conf into the daemon's state directory.

[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $env:ProgramFiles "Tailscale")
)

$ErrorActionPreference = "Stop"

# --- Self-elevate to Administrator if needed ---
$principal = New-Object Security.Principal.WindowsPrincipal(
    [Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Host "Requesting administrator privileges..."
    Start-Process -FilePath "powershell.exe" -Verb RunAs -ArgumentList @(
        "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "`"$PSCommandPath`"")
    return
}

$src = $PSScriptRoot

Write-Host "Installing Tailscale to: $InstallDir"
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Copy-Item -Force (Join-Path $src "tailscale.exe")  $InstallDir
Copy-Item -Force (Join-Path $src "tailscaled.exe") $InstallDir

# --- Default proxy.conf in the daemon's default state dir (don't overwrite) ---
$dataDir  = Join-Path $env:ProgramData "Tailscale"
New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
$proxyDst = Join-Path $dataDir "proxy.conf"
if (Test-Path $proxyDst) {
    Write-Host "Keeping existing proxy.conf at $proxyDst"
} else {
    Copy-Item -Force (Join-Path $src "proxy.conf") $proxyDst
    Write-Host "Installed default proxy.conf at $proxyDst"
}

# --- Register and start the Windows service ---
$tailscaled = Join-Path $InstallDir "tailscaled.exe"
Write-Host "Registering the Tailscale service..."
& $tailscaled uninstall-system-daemon 2>$null   # ignore if not installed yet
$global:LASTEXITCODE = 0
& $tailscaled install-system-daemon
if ($LASTEXITCODE -ne 0) { throw "install-system-daemon failed (exit $LASTEXITCODE)" }
Start-Service -Name "Tailscale"

# --- Add install dir to the system PATH (so `tailscale` works in new shells) ---
$machPath = [Environment]::GetEnvironmentVariable("Path", "Machine")
if ($machPath -notlike "*$InstallDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$machPath;$InstallDir", "Machine")
    Write-Host "Added $InstallDir to the system PATH (restart your terminal to use it)."
}

Write-Host ""
Write-Host "Done. Next steps:"
Write-Host "  1. Run:  `"$tailscaled`" ..  ->  log in with:  `"$(Join-Path $InstallDir 'tailscale.exe')`" up"
Write-Host "  2. To force traffic through a proxy, edit $proxyDst"
Write-Host "     (set \"enabled\": true and your proxy URLs), then:  Restart-Service Tailscale"
Write-Host ""
if ($Host.Name -eq "ConsoleHost") { Read-Host "Press Enter to close" }
