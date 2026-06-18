# metrics-report.ps1 - Reporter MAC + latency cho node Tailscale.
#
# Moi INTERVAL giay: doc MAC card chinh + `tailscale ping` cac peer (RTT, di
# thang/DERP) roi POST ve VPS /metrics/report (xac thuc Bearer token).
#
# Token doc tu metrics.conf (canh file nay). KHONG co token -> thoat ngay
# (khong gui gi). Token PHAI khop METRICS_TOKEN tren server.
#
# Chay 1 ban duy nhat (mutex). Duoc start-tailscale.bat goi an khi co metrics.conf;
# may ban cai day du (votam) co the chay tay: powershell -File metrics-report.ps1
#
# Tu kiem tra logic parse:  powershell -File metrics-report.ps1 -SelfTest
param([switch]$SelfTest)

$ErrorActionPreference = 'SilentlyContinue'
$ProgressPreference = 'SilentlyContinue'

$base = Split-Path -Parent $MyInvocation.MyCommand.Path

function Parse-Ping([string]$out) {
    # Tra ve hashtable { ok; rtt; path }. path = 'direct' hoac 'derp:<region>'.
    $line = ($out -split "`n" | Where-Object { $_ -match 'pong from' } | Select-Object -Last 1)
    if (-not $line) { return @{ ok = $false; rtt = $null; path = '' } }
    $rtt = $null
    if ($line -match 'in\s+([\d.]+)ms') { $rtt = [double]$Matches[1] }
    $path = 'direct'
    if ($line -match 'via\s+DERP\(([^)]+)\)') { $path = 'derp:' + $Matches[1] }
    return @{ ok = $true; rtt = $rtt; path = $path }
}

function Get-PrimaryMac {
    # MAC card co default route (NIC chinh). Fallback: NIC vat ly dau tien Up.
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

if ($SelfTest) {
    $a = Parse-Ping "pong from votam (100.64.0.3) via 1.2.3.4:41641 in 12ms"
    $b = Parse-Ping "pong from votam (100.64.0.3) via DERP(myderp) in 45.5ms"
    $c = Parse-Ping "no pong; timed out"
    if (-not $a.ok -or $a.path -ne 'direct' -or [double]$a.rtt -ne 12) { throw "SELFTEST direct FAIL" }
    if (-not $b.ok -or $b.path -ne 'derp:myderp' -or [double]$b.rtt -ne 45.5) { throw "SELFTEST derp FAIL" }
    if ($c.ok) { throw "SELFTEST timeout FAIL" }
    Write-Host "SELFTEST OK"
    exit 0
}

# --- chi chay 1 ban (mutex) ---
$mutex = New-Object System.Threading.Mutex($false, 'Global\TailscaleMetricsReporter')
if (-not $mutex.WaitOne(0)) { exit 0 }

# --- doc config metrics.conf (key=value) ---
$conf = Join-Path $base 'metrics.conf'
if (-not (Test-Path $conf)) { exit 0 }
$cfg = @{}
foreach ($line in Get-Content $conf) {
    $t = $line.Trim()
    if ($t -eq '' -or $t.StartsWith('#')) { continue }
    $i = $t.IndexOf('=')
    if ($i -lt 1) { continue }
    $cfg[$t.Substring(0, $i).Trim()] = $t.Substring($i + 1).Trim()
}
$token = $cfg['TOKEN']
if ([string]::IsNullOrWhiteSpace($token)) { exit 0 }
$server = if ($cfg['SERVER']) { $cfg['SERVER'].TrimEnd('/') } else { 'https://vpn2.hangocthanh.io.vn' }
$interval = 60; if ($cfg['INTERVAL'] -match '^\d+$') { $interval = [int]$cfg['INTERVAL'] }
$count = 2; if ($cfg['PINGCOUNT'] -match '^\d+$') { $count = [int]$cfg['PINGCOUNT'] }

# tailscale.exe: uu tien canh script (ban portable), neu khong co thi dung PATH (ban cai day du)
$ts = Join-Path $base 'tailscale.exe'
if (-not (Test-Path $ts)) { $ts = 'tailscale.exe' }

[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

while ($true) {
    try {
        $st = (& $ts status --json 2>$null | Out-String | ConvertFrom-Json)
        if ($st -and $st.Self) {
            $selfHost = $st.Self.HostName
            $selfIp = ($st.Self.TailscaleIPs | Where-Object { $_ -notmatch ':' } | Select-Object -First 1)
            $mac = Get-PrimaryMac
            $samples = @()
            if ($st.Peer) {
                foreach ($p in $st.Peer.PSObject.Properties.Value) {
                    $dstIp = ($p.TailscaleIPs | Where-Object { $_ -notmatch ':' } | Select-Object -First 1)
                    if (-not $dstIp) { continue }
                    $r = Parse-Ping ((& $ts ping -c $count --timeout 3s $dstIp 2>$null) | Out-String)
                    $samples += [pscustomobject]@{
                        dst = $p.HostName; dst_ip = $dstIp
                        rtt_ms = $r.rtt; path = $r.path; ok = $r.ok
                    }
                }
            }
            $payload = @{ hostname = $selfHost; ipv4 = $selfIp; mac = $mac; samples = @($samples) }
            $body = $payload | ConvertTo-Json -Depth 6
            Invoke-RestMethod -Uri "$server/metrics/report" -Method Post -TimeoutSec 20 `
                -Headers @{ Authorization = "Bearer $token" } `
                -ContentType 'application/json' -Body $body | Out-Null
        }
    } catch {}
    Start-Sleep -Seconds $interval
}
