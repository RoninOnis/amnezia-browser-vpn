# Remove the Amnezia Browser VPN helper (Windows, per-user).
$ErrorActionPreference = "SilentlyContinue"

$HostName  = "awgbv"
$TargetDir = Join-Path $env:LOCALAPPDATA "AwgBrowserVpn"
$TargetExe = Join-Path $TargetDir "awgbv.exe"

if (Test-Path $TargetExe) {
  & $TargetExe --stop | Out-Null
  Start-Sleep -Milliseconds 500
}

Remove-Item -Recurse -Force $TargetDir
Remove-Item -Recurse -Force "HKCU:\Software\Mozilla\NativeMessagingHosts\$HostName"

Write-Host "helper removed. Расширение удаляется в about:addons."
