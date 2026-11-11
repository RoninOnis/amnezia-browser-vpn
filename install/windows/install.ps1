# Install the Amnezia Browser VPN helper on Windows (Firefox native messaging).
#
#   powershell -ExecutionPolicy Bypass -File install\windows\install.ps1 [-Binary <path-to-awgbv.exe>]
#
# Installs:
#   %LOCALAPPDATA%\AwgBrowserVpn\awgbv.exe     the helper binary
#   %LOCALAPPDATA%\AwgBrowserVpn\awgbv.json    native-messaging manifest
#   HKCU\Software\Mozilla\NativeMessagingHosts\awgbv  -> manifest path
# No administrator rights required: everything is per-user.

param(
  [string]$Binary = ""
)

$ErrorActionPreference = "Stop"

$ExtId    = "awgbv@0xoni.ru"
$HostName = "awgbv"
$TargetDir = Join-Path $env:LOCALAPPDATA "AwgBrowserVpn"
$TargetExe = Join-Path $TargetDir "awgbv.exe"
$Manifest  = Join-Path $TargetDir "awgbv.json"

if ([string]::IsNullOrWhiteSpace($Binary)) {
  $here = Split-Path -Parent $MyInvocation.MyCommand.Path
  $Binary = Join-Path $here "..\..\dist\awgbv-windows-amd64.exe"
}
if (-not (Test-Path $Binary)) {
  Write-Error "helper binary not found: $Binary`n  build it first: .\install\build.ps1"
  exit 1
}

New-Item -ItemType Directory -Force -Path $TargetDir | Out-Null
Copy-Item -Force $Binary $TargetExe
Write-Host "installed binary: $TargetExe"

$manifestObj = [ordered]@{
  name               = $HostName
  description        = "Amnezia Browser VPN helper (AmneziaWG userspace tunnel + local SOCKS5)"
  path               = $TargetExe
  type               = "stdio"
  allowed_extensions = @($ExtId)
}
$manifestObj | ConvertTo-Json -Depth 4 | Set-Content -Encoding UTF8 $Manifest
Write-Host "installed manifest: $Manifest"

$regPath = "HKCU:\Software\Mozilla\NativeMessagingHosts\$HostName"
New-Item -Path $regPath -Force | Out-Null
Set-Item -Path $regPath -Value $Manifest
Write-Host "registered: $regPath"

Write-Host ""
Write-Host "Готово. Дальше:"
Write-Host "1) Firefox: about:debugging -> «Это Firefox» -> «Загрузить временное дополнение» -> extension\manifest.json"
Write-Host "   (или установите подписанный .xpi)"
Write-Host "2) В настройках расширения вставьте ключ vpn://... из приложения Amnezia"
Write-Host "3) Проверка без браузера:"
Write-Host "   & '$TargetExe' --status"
Write-Host "   & '$TargetExe' --connect C:\path\to\config.conf --listen 127.0.0.1:1081"
Write-Host "   curl.exe --socks5-hostname 127.0.0.1:1081 https://api.ipify.org"
Write-Host "   & '$TargetExe' --stop"
Write-Host ""
Write-Host "Лог хелпера: $TargetDir\helper.log"
