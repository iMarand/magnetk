# Install Magnetk on Windows (per user, no admin rights needed).
#
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1            client: magnetk on PATH + magnetk:// links
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Relay     also open the relay ports (needs admin) and
#                                                                          print how to run it at logon
#   ... -Uninstall    remove everything this script added
#
# What gets installed:
#   %LOCALAPPDATA%\Programs\Magnetk\magnetk.exe (+ msquic.dll)
#   that folder on the user PATH
#   magnetk:// and magnetk:? links -> "magnetk.exe open -pause <link>" (HKCU, via "magnetk install")
#   %USERPROFILE%\.magnetk\settings.conf (if missing)
param(
    [switch]$Relay,
    [switch]$NoLinks,
    [switch]$Uninstall,
    [string]$Bin = "$PSScriptRoot\..\build\bin\magnetk.exe"
)
$ErrorActionPreference = "Stop"
$dest = Join-Path $env:LOCALAPPDATA "Programs\Magnetk"
$exe = Join-Path $dest "magnetk.exe"

function Set-UserPath([string]$dir, [bool]$add) {
    $p = [Environment]::GetEnvironmentVariable("Path", "User")
    $parts = @($p -split ";" | Where-Object { $_ -and ($_.TrimEnd("\") -ne $dir.TrimEnd("\")) })
    if ($add) { $parts += $dir }
    [Environment]::SetEnvironmentVariable("Path", ($parts -join ";"), "User")
}

if ($Uninstall) {
    if (Test-Path $exe) { & $exe install -uninstall }
    Set-UserPath $dest $false
    if (Test-Path $dest) { Remove-Item -Recurse -Force $dest }
    Write-Host "Magnetk removed (settings in $env:USERPROFILE\.magnetk were kept)."
    exit 0
}

if (-not (Test-Path $Bin)) {
    throw "magnetk.exe not found at $Bin - build it first: scripts\build.bat"
}
$src = Split-Path (Resolve-Path $Bin)
Write-Host "=== installing magnetk into $dest ==="
New-Item -ItemType Directory -Force $dest | Out-Null
Copy-Item (Resolve-Path $Bin) $exe -Force
if (Test-Path "$src\msquic.dll") { Copy-Item "$src\msquic.dll" $dest -Force }
Set-UserPath $dest $true
$env:Path = "$dest;$env:Path"
& $exe version

if (-not $NoLinks) {
    Write-Host "=== magnetk:// link handler ==="
    & $exe install
}

$cfgDir = Join-Path $env:USERPROFILE ".magnetk"
if (-not (Test-Path "$cfgDir\settings.conf")) {
    New-Item -ItemType Directory -Force $cfgDir | Out-Null
    & $exe config init "$cfgDir\settings.conf"
}

if ($Relay) {
    Write-Host "=== relay ==="
    $admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    if ($admin) {
        foreach ($proto in "UDP", "TCP") {
            if (-not (Get-NetFirewallRule -DisplayName "Magnetk relay $proto" -ErrorAction SilentlyContinue)) {
                New-NetFirewallRule -DisplayName "Magnetk relay $proto" -Direction Inbound -Protocol $proto -LocalPort 4002 -Action Allow | Out-Null
            }
        }
        Write-Host "firewall: inbound UDP+TCP 4002 allowed"
    } else {
        Write-Host "run as administrator to open UDP+TCP 4002 in the firewall (or allow magnetk.exe when Windows asks)"
    }
    Write-Host "start it:            magnetk relay -config `"$cfgDir\settings.conf`""
    Write-Host "start it at logon:   schtasks /Create /SC ONLOGON /TN MagnetkRelay /TR `"\`"$exe\`" relay -config \`"$cfgDir\settings.conf\`"`""
}

Write-Host ""
Write-Host "Done. Open a new terminal, set RELAYS (and TOKEN) in $cfgDir\settings.conf, then e.g.:"
Write-Host "  magnetk chat <node>        or click/open  magnetk://<node>@<relay>/chat"
