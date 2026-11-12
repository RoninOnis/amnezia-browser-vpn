@echo off
setlocal enabledelayedexpansion
rem  awgbv helper uninstaller for Windows - pure batch, no PowerShell, no admin
rem
rem  Removes:
rem    - native messaging registry key (HKCU, no admin needed)
rem    - the helper binary and its manifest in %LOCALAPPDATA%\AwgBrowserVpn
rem    - the helper state (log, control file, token) in the same folder
rem
rem  Keeps: nothing. The extension itself is removed from Firefox by the user.
rem
rem  Usage: double-click, or run "uninstall-windows.cmd /nopause" from a console.

set "HOST_NAME=awgbv"
set "TARGET_DIR=%LOCALAPPDATA%\AwgBrowserVpn"
set "TARGET_EXE=%TARGET_DIR%\awgbv.exe"

echo.
echo  Amnezia Browser VPN - helper uninstall
echo.

if exist "%TARGET_EXE%" (
  echo  [1/3] stopping the helper ...
  "%TARGET_EXE%" --stop >nul 2>&1
  if exist "%TARGET_EXE%" ping -n 3 127.0.0.1 >nul
) else (
  echo  [1/3] helper binary not found, skipping stop
)

echo  [2/3] removing native messaging registration ...
reg delete "HKCU\Software\Mozilla\NativeMessagingHosts\%HOST_NAME%" /f >nul 2>&1
if errorlevel 1 (
  echo        nothing to remove in the registry
) else (
  echo        registry key removed
)

echo  [3/3] removing files ...
if not exist "%TARGET_DIR%" goto :done
del /f /q "%TARGET_EXE%" >nul 2>&1
if exist "%TARGET_EXE%" (
  rem still mapped - rename is allowed even for a running image
  move /y "%TARGET_EXE%" "%TARGET_EXE%.old" >nul 2>&1
)
rmdir /s /q "%TARGET_DIR%" >nul 2>&1
if exist "%TARGET_DIR%" (
  echo.
  echo  ERROR: some files in %TARGET_DIR% could not be removed.
  echo         Close Firefox completely and run this file again.
  goto :fail
)

:done
echo.
echo  Done. Restart Firefox to drop the native host from the list.
echo  The extension can be removed in Firefox: about:addons -^> Amnezia Browser VPN -^> Remove.
echo.
if /i not "%~1"=="/nopause" pause
exit /b 0

:fail
if /i not "%~1"=="/nopause" pause
exit /b 1
