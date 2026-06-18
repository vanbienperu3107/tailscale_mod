# metrics-report.ps1 - Reporter MAC + latency cho node Tailscale (ZERO-CONFIG).
#
# Khong can token, khong can file cau hinh. Moi INTERVAL giay:
#   - doc MAC card chinh
#   - `tailscale ping` TAT CA peer (cac node khac + server) -> RTT, di thang/DERP
#   - tu tim peer ten 'collector' (VPS trong tailnet) roi POST ket qua THANG toi
#     http://<ip-tailnet-collector>:8090/metrics/report  (trong tailnet, khong token)
#
# Node userspace (ban portable, vd itop): POST di qua SOCKS 127.0.0.1:7654.
# Node TUN (ban cai day du): POST di thang. Tu phat hien.
#
# Chay 1 ban duy nhat (mutex). Duoc start-tailscale.bat goi an. May cai day du
# co the chay tay:  powershell -File metrics-report.ps1
# Tu kiem tra parser:  powershell -File metrics-report.ps1 -SelfTest
param([switch]$SelfTest)

$ErrorActionPreference = 'SilentlyContinue'
$ProgressPreference = 'SilentlyContinue'

$base = Split-Path -Parent $MyInvocation.MyCommand.Path

# Cau hinh mac dinh (zero-config). Doi o day neu can.
$CollectorHost = 'collector'      # ten node VPS trong tailnet
$CollectorPort = 8090
$Interval      = 60
$PingCount     = 2
$SocksAddr     = '127.0.0.1:7654' # SOCKS cua ban portable (userspace)

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

# tailscale.exe: uu tien canh script (ban portable), neu khong co thi PATH (ban cai day du)
$ts = Join-Path $base 'tailscale.exe'
if (-not (Test-Path $ts)) { $ts = 'tailscale.exe' }

# Node userspace (khong co card TUN 'Tailscale') -> POST phai qua SOCKS.
$hasTun = [bool](Get-NetAdapter -ErrorAction SilentlyContinue |
    Where-Object { $_.InterfaceDescription -match 'Tailscale' -or $_.Name -match 'Tailscale' })
$useSocks = -not $hasTun

function Send-Report([string]$collectorIp, [string]$body) {
    $tmp = Join-Path $env:TEMP ("tsmetrics_{0}.json" -f $PID)
    [System.IO.File]::WriteAllText($tmp, $body, (New-Object System.Text.UTF8Encoding($false)))
    try {
        $a = @('-s', '-m', '20', '-X', 'POST', "http://${collectorIp}:${CollectorPort}/metrics/report",
            '-H', 'Content-Type: application/json', '--data-binary', "@$tmp")
        if ($useSocks) { $a = @('--socks5-hostname', $SocksAddr) + $a }
        & curl.exe @a | Out-Null
    } finally { Remove-Item $tmp -ErrorAction SilentlyContinue }
}

while ($true) {
    try {
        $st = (& $ts status --json 2>$null | Out-String | ConvertFrom-Json)
        if ($st -and $st.Self -and $st.Peer) {
            # tim peer 'collector' (VPS trong tailnet)
            $collectorIp = $null
            foreach ($p in $st.Peer.PSObject.Properties.Value) {
                if ($p.HostName -eq $CollectorHost) {
                    $collectorIp = ($p.TailscaleIPs | Where-Object { $_ -notmatch ':' } | Select-Object -First 1)
                    break
                }
            }
            if ($collectorIp) {
                $selfHost = $st.Self.HostName
                $selfIp = ($st.Self.TailscaleIPs | Where-Object { $_ -notmatch ':' } | Select-Object -First 1)
                $mac = Get-PrimaryMac
                $samples = @()
                foreach ($p in $st.Peer.PSObject.Properties.Value) {
                    $dstIp = ($p.TailscaleIPs | Where-Object { $_ -notmatch ':' } | Select-Object -First 1)
                    if (-not $dstIp) { continue }
                    $r = Parse-Ping ((& $ts ping -c $PingCount --timeout 3s $dstIp 2>$null) | Out-String)
                    $samples += [pscustomobject]@{
                        dst = $p.HostName; dst_ip = $dstIp
                        rtt_ms = $r.rtt; path = $r.path; ok = $r.ok
                    }
                }
                $body = @{ hostname = $selfHost; ipv4 = $selfIp; mac = $mac; samples = @($samples) } |
                    ConvertTo-Json -Depth 6
                Send-Report $collectorIp $body
            }
        }
    } catch {}
    Start-Sleep -Seconds $Interval
}
