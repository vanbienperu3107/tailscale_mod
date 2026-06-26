# bootstrap-config.ps1 - Load cau hinh runtime tu dashboard DB roi sinh env.generated.cmd.
#
# Chay 1 LAN luc khoi dong (start-tailscale.bat goi TRUOC khi launch tailscaled).
#   - Lay MAC (card chinh) + hostname
#   - GET $DashboardUrl/api/client/runtime?mac=&host=  (header X-Headscale-Secret)
#   - Ghi env.generated.cmd (cac dong `set "VAR=..."`) cho .bat `call`
#   - FAIL-OPEN: loi/timeout -> dung config.cache.json lan truoc; chua co -> KHONG ghi
#     (de .bat giu nguyen gia tri hardcode mac dinh)
#
# Override cau hinh: tao file 'runtime-config-override.ps1' canh script (dot-sourced):
#   $DashboardUrl = 'https://vpn2.hangocthanh.io.vn/app'
#   $DashSecret   = 'your-secret'
param([switch]$SelfTest)

$ErrorActionPreference = 'SilentlyContinue'
$ProgressPreference    = 'SilentlyContinue'
$base = Split-Path -Parent $MyInvocation.MyCommand.Path

# ---- Cau hinh mac dinh (khop metrics-report.ps1) ----
$DashboardUrl = 'https://vpn2.hangocthanh.io.vn/app'   # khong co dau / cuoi
$DashSecret   = ''                                      # X-Headscale-Secret (de trong = khong gui)
$TimeoutSec   = 3
# -----------------------------------------------------
$cfgFile = Join-Path $base 'runtime-config-override.ps1'
if (Test-Path $cfgFile) { . $cfgFile }

$cacheFile = Join-Path $base 'config.cache.json'
$envFile   = Join-Path $base 'env.generated.cmd'

function Get-PrimaryMac {
    try {
        $idx = (Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction Stop |
            Sort-Object RouteMetric | Select-Object -First 1).InterfaceIndex
        $a = Get-NetAdapter -InterfaceIndex $idx -ErrorAction Stop
        if ($a -and $a.MacAddress) { return $a.MacAddress }
    } catch {}
    $a = Get-NetAdapter -Physical -ErrorAction SilentlyContinue |
        Where-Object { $_.Status -eq 'Up' -and $_.MacAddress } |
        Sort-Object ifIndex | Select-Object -First 1
    if ($a) { return $a.MacAddress }
    return ''
}

# Bo dau " va ky tu xuong dong khoi gia tri (chong chen lenh trong .cmd).
function San([string]$v) { return ($v -replace '["\r\n]', '') }

# Sinh env.generated.cmd tu object cau hinh (da resolve san o server).
function Write-EnvCmd($cfg) {
    $lines = @('@echo off', 'REM Sinh tu dong boi bootstrap-config.ps1 - KHONG sua tay.')
    if ($cfg.login_server)    { $lines += ('set "HS_SERVER={0}"'              -f (San $cfg.login_server)) }
    if ($cfg.mode)            { $lines += ('set "LAN_PROXY_MODE={0}"'         -f (San $cfg.mode)) }
    if ($cfg.lan_routes)      { $lines += ('set "LAN_ROUTES={0}"'            -f (San $cfg.lan_routes)) }
    if ($cfg.socks_addr)      { $lines += ('set "SOCKS_ADDR={0}"'           -f (San $cfg.socks_addr)) }
    $lines += ('set "TS_PEER_HTTP_PROXY={0}"'   -f (San [string]$cfg.peer_http_proxy))
    $lines += ('set "TS_DERP_KEEPALIVE_SECS={0}"' -f (San [string]$cfg.derp_keepalive_secs))
    # always_use_derp=false -> clear bien (cho phep UDP/direct). true -> =1.
    if ($cfg.always_use_derp) { $lines += 'set "TS_DEBUG_ALWAYS_USE_DERP=1"' }
    else                      { $lines += 'set "TS_DEBUG_ALWAYS_USE_DERP="' }
    if ($cfg.pac_server_port) { $lines += ('set "PAC_SERVER_PORT={0}"' -f (San [string]$cfg.pac_server_port)) }
    if ($cfg.pac_url)         { $lines += ('set "PAC_URL={0}"' -f (San $cfg.pac_url)) }
    [System.IO.File]::WriteAllText($envFile, ($lines -join "`r`n") + "`r`n",
        (New-Object System.Text.UTF8Encoding($false)))
}

if ($SelfTest) {
    $fake = [pscustomobject]@{ login_server='https://x'; mode='itop'; lan_routes='10.0.0.0/8';
        socks_addr='127.0.0.1:7654'; peer_http_proxy='7655'; derp_keepalive_secs=25;
        always_use_derp=$false; pac_server_port=7658; pac_url='https://x/api/client/pac' }
    Write-EnvCmd $fake
    $txt = Get-Content $envFile -Raw
    if ($txt -notmatch 'TS_DEBUG_ALWAYS_USE_DERP=\"' ) { throw 'SELFTEST: always_use_derp=false phai clear bien' }
    if ($txt -notmatch 'LAN_PROXY_MODE=itop')         { throw 'SELFTEST: mode FAIL' }
    Remove-Item $envFile -ErrorAction SilentlyContinue
    Write-Host 'SELFTEST OK'
    exit 0
}

$mac   = San (Get-PrimaryMac)
$hname = San $env:COMPUTERNAME
$url   = "$DashboardUrl/api/client/runtime?mac=$([uri]::EscapeDataString($mac))&host=$([uri]::EscapeDataString($hname))"

$json = $null
try {
    $args = @('-s', '-m', [string]$TimeoutSec, $url)
    if ($DashSecret) { $args += @('-H', "X-Headscale-Secret: $DashSecret") }
    $raw = & curl.exe @args
    if ($raw) { $json = $raw | ConvertFrom-Json }
} catch { $json = $null }

if ($json -and $json.login_server) {
    # Thanh cong -> ghi env + cache.
    Write-EnvCmd $json
    [System.IO.File]::WriteAllText($cacheFile, ($json | ConvertTo-Json -Depth 6),
        (New-Object System.Text.UTF8Encoding($false)))
    exit 0
}

# Fail-open: dung cache neu co.
if (Test-Path $cacheFile) {
    try {
        $cached = Get-Content $cacheFile -Raw | ConvertFrom-Json
        if ($cached) { Write-EnvCmd $cached; exit 0 }
    } catch {}
}
# Khong co gi -> KHONG ghi env.generated.cmd; .bat giu nguyen hardcode mac dinh.
exit 0
