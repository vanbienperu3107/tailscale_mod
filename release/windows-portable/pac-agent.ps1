# pac-agent.ps1 - Serve PAC tu RAM tai http://127.0.0.1:<port>/proxy.pac,
#                 tu cap nhat tu dashboard DB moi 30 giay.
#
# Browser/he thong tro AutoConfigURL vao URL co dinh nay -> doi domain tren
# dashboard la <=30s sau moi may tu cap nhat, KHONG can sua tay tung may.
#
#   - Tai PAC: GET $DashboardUrl/api/client/pac?mac=  (header X-Headscale-Secret)
#   - Giu PAC trong RAM ($script:pac); ghi proxy.cache.pac de cold-start offline
#   - FAIL-OPEN: loi mang -> giu ban tot cuoi; cold-start khong mang -> cache -> default DIRECT
#
# Override: file 'runtime-config-override.ps1' canh script (dot-sourced) - dung chung bootstrap.
#   $DashboardUrl, $DashSecret
param([switch]$SelfTest)

$ErrorActionPreference = 'SilentlyContinue'
$ProgressPreference    = 'SilentlyContinue'
$base = Split-Path -Parent $MyInvocation.MyCommand.Path

# ---- Cau hinh mac dinh ----
$DashboardUrl = 'https://vpn2.hangocthanh.io.vn/app'
$DashSecret   = ''
$RefreshSec   = 30
$Port         = if ($env:PAC_SERVER_PORT) { [int]$env:PAC_SERVER_PORT } else { 7658 }
# ---------------------------
$cfgFile = Join-Path $base 'runtime-config-override.ps1'
if (Test-Path $cfgFile) { . $cfgFile }

$cacheFile   = Join-Path $base 'proxy.cache.pac'
$defaultPac  = "function FindProxyForURL(url, host) { return `"DIRECT`"; }`n"

function Get-PrimaryMac {
    try {
        $idx = (Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction Stop |
            Sort-Object RouteMetric | Select-Object -First 1).InterfaceIndex
        $a = Get-NetAdapter -InterfaceIndex $idx -ErrorAction Stop
        if ($a -and $a.MacAddress) { return $a.MacAddress }
    } catch {}
    return ''
}

$script:mac = (Get-PrimaryMac) -replace '["\r\n]', ''
$script:pac = $null

function Fetch-Pac {
    $q = if ($script:mac) { "?mac=$([uri]::EscapeDataString($script:mac))" } else { '' }
    $url = "$DashboardUrl/api/client/pac$q"
    try {
        $args = @('-s', '-m', '5', $url)
        if ($DashSecret) { $args += @('-H', "X-Headscale-Secret: $DashSecret") }
        $raw = & curl.exe @args | Out-String
        if ($raw -and $raw -match 'FindProxyForURL') {
            $script:pac = $raw
            [System.IO.File]::WriteAllText($cacheFile, $raw, (New-Object System.Text.UTF8Encoding($false)))
            return $true
        }
    } catch {}
    return $false
}

# Cold-start: thu fetch; that bai -> cache -> default.
function Init-Pac {
    if (Fetch-Pac) { return }
    if (Test-Path $cacheFile) { $script:pac = Get-Content $cacheFile -Raw }
    if (-not $script:pac) { $script:pac = $defaultPac }
}

if ($SelfTest) {
    $script:pac = $defaultPac
    if ($script:pac -notmatch 'FindProxyForURL') { throw 'SELFTEST: default PAC FAIL' }
    Write-Host 'SELFTEST OK'
    exit 0
}

# --- Chi chay 1 ban (mutex) ---
$mutex = New-Object System.Threading.Mutex($false, 'Global\TailscalePacAgent')
if (-not $mutex.WaitOne(0)) { exit 0 }

Init-Pac

$listener = New-Object System.Net.HttpListener
$listener.Prefixes.Add("http://127.0.0.1:$Port/")
try { $listener.Start() } catch { exit 1 }

$sw = [System.Diagnostics.Stopwatch]::StartNew()
while ($listener.IsListening) {
    # GetContextAsync: vua cho request, vua refresh moi 30s (1 luong, khong runspace).
    $task = $listener.GetContextAsync()
    $ar   = [System.IAsyncResult]$task
    while (-not $ar.AsyncWaitHandle.WaitOne(1000)) {
        if ($sw.Elapsed.TotalSeconds -ge $RefreshSec) { [void](Fetch-Pac); $sw.Restart() }
    }
    $ctx  = $task.Result
    $resp = $ctx.Response
    try {
        $bytes = [System.Text.Encoding]::UTF8.GetBytes([string]$script:pac)
        $resp.ContentType = 'application/x-ns-proxy-autoconfig'
        $resp.Headers['Cache-Control'] = 'no-cache, no-store, must-revalidate'
        $resp.ContentLength64 = $bytes.Length
        $resp.OutputStream.Write($bytes, 0, $bytes.Length)
    } catch {}
    finally { try { $resp.OutputStream.Close() } catch {} }
}
