# One-file installer for Windows: downloads the helper from GitHub Releases,
# verifies the checksum and registers the Firefox native-messaging host.
# Per-user only — no administrator rights required.
#
#   powershell -ExecutionPolicy Bypass -File install-windows.ps1
#
# Optional:
#   -Version v0.1.1   release tag
#   -Binary <path>    use a local exe instead of downloading

param(
  [string]$Version = "latest",
  [string]$Binary = ""
)

$ErrorActionPreference = "Stop"

$Repo      = "RoninOnis/amnezia-browser-vpn"
$HostName  = "awgbv"
$ExtId     = "awgbv@0xoni.ru"
$TargetDir = Join-Path $env:LOCALAPPDATA "AwgBrowserVpn"
$TargetExe = Join-Path $TargetDir "awgbv.exe"
$Manifest  = Join-Path $TargetDir "awgbv.json"
$Base      = if ($Version -eq "latest") { "https://github.com/$Repo/releases/latest/download" }
             else { "https://github.com/$Repo/releases/download/$Version" }

New-Item -ItemType Directory -Force -Path $TargetDir | Out-Null

if ([string]::IsNullOrWhiteSpace($Binary)) {
  $arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
  $asset = "awgbv-windows-$arch.exe"
  $tmp = Join-Path $env:TEMP $asset
  Write-Host "→ платформа: windows/$arch, версия $Version"
  Write-Host "→ скачиваю $asset"
  Invoke-WebRequest -UseBasicParsing -Uri "$Base/$asset" -OutFile $tmp

  try {
    $sumsPath = Join-Path $env:TEMP "SHA256SUMS"
    Invoke-WebRequest -UseBasicParsing -Uri "$Base/SHA256SUMS" -OutFile $sumsPath
    $line = (Get-Content $sumsPath | Where-Object { $_ -match " $([regex]::Escape($asset))$" })
    if ($line) {
      $expected = ($line -split '\s+')[0].ToUpper()
      $actual = (Get-FileHash -Algorithm SHA256 -Path $tmp).Hash.ToUpper()
      if ($expected -ne $actual) {
        throw "sha256 mismatch: expected $expected, got $actual"
      }
      Write-Host "→ sha256 совпал"
    } else {
      Write-Warning "asset отсутствует в SHA256SUMS"
    }
  } catch {
    Write-Warning "проверка sha256 не удалась: $_"
  }

  $Binary = $tmp
}

Copy-Item -Force $Binary $TargetExe
Write-Host "→ бинарь: $TargetExe"

$manifestObj = [ordered]@{
  name               = $HostName
  description        = "Amnezia Browser VPN helper (AmneziaWG userspace tunnel + local SOCKS5)"
  path               = $TargetExe
  type               = "stdio"
  allowed_extensions = @($ExtId)
}
$manifestObj | ConvertTo-Json -Depth 4 | Set-Content -Encoding UTF8 $Manifest
Write-Host "→ манифест: $Manifest"

$regPath = "HKCU:\Software\Mozilla\NativeMessagingHosts\$HostName"
New-Item -Path $regPath -Force | Out-Null
Set-Item -Path $regPath -Value $Manifest
Write-Host "→ реестр: $regPath"

Write-Host ""
Write-Host "Готово. Дальше:"
Write-Host "  1) установить подписанный .xpi: about:addons -> шестерёнка -> «Установить дополнение из файла»"
Write-Host "  2) в настройках расширения вставить ключ vpn:// из приложения Amnezia"
Write-Host ""
Write-Host "Проверка без браузера:"
Write-Host "  & '$TargetExe' --status"
Write-Host "  curl.exe --socks5-hostname 127.0.0.1:1081 https://api.ipify.org"
