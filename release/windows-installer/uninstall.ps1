# Tailscale (mod) uninstaller for Windows.
# Stops and removes the service and deletes the installed binaries.
# Your configuration under %ProgramData%\Tailscale (including proxy.conf and
# tailscaled.state) is left in place.

[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $env:ProgramFiles "Tailscale")
)

$ErrorActionPreference = "Stop"

$principal = New-Object Security.Principal.WindowsPrincipal(
    [Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Host "Requesting administrator privileges..."
    Start-Process -FilePath "powershell.exe" -Verb RunAs -ArgumentList @(
        "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "`"$PSCommandPath`"")
    return
}

$tailscaled = Join-Path $InstallDir "tailscaled.exe"
Write-Host "Removing the Tailscale service..."
if (Test-Path $tailscaled) {
    & $tailscaled uninstall-system-daemon 2>$null
} else {
    sc.exe stop Tailscale   | Out-Null
    sc.exe delete Tailscale | Out-Null
}

Write-Host "Removing $InstallDir ..."
Remove-Item -Recurse -Force $InstallDir -ErrorAction SilentlyContinue

# Remove install dir from system PATH.
$machPath = [Environment]::GetEnvironmentVariable("Path", "Machine")
if ($machPath -like "*$InstallDir*") {
    $newPath = ($machPath -split ';' | Where-Object { $_ -and $_ -ne $InstallDir }) -join ';'
    [Environment]::SetEnvironmentVariable("Path", $newPath, "Machine")
}

Write-Host ""
Write-Host "Uninstalled. Config under $(Join-Path $env:ProgramData 'Tailscale') was left in place."
if ($Host.Name -eq "ConsoleHost") { Read-Host "Press Enter to close" }
