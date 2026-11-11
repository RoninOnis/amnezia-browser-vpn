@echo off
rem ============================================================================
rem  awgbv helper installer for Windows - pure batch, no PowerShell, no admin
rem
rem  Downloads the helper matching this machine, verifies its sha256 against
rem  SHA256SUMS from the same release, installs it into the user profile and
rem  registers the native-messaging manifest under HKCU.
rem
rem  Messages are ASCII on purpose: the console code page cannot be relied on,
rem  and a mis-decoded byte used to break script parsing.
rem ============================================================================
setlocal enableextensions enabledelayedexpansion

set "REPO=RoninOnis/amnezia-browser-vpn"
set "BASE=https://github.com/%REPO%/releases/latest/download"
set "HOST_NAME=awgbv"
set "EXT_ID=awgbv@0xoni.ru"
set "TARGET_DIR=%LOCALAPPDATA%\AwgBrowserVpn"
set "TARGET_EXE=%TARGET_DIR%\awgbv.exe"
set "TARGET_JSON=%TARGET_DIR%\awgbv.json"
set "TMP=%TEMP%\awgbv-install"

echo.
echo  awgbv helper installer
echo  target: %TARGET_DIR%
echo.

rem ---------------------------------------------------------------- 1. platform
set "ARCH="
if /i "%PROCESSOR_ARCHITECTURE%"=="AMD64" set "ARCH=amd64"
if /i "%PROCESSOR_ARCHITECTURE%"=="ARM64" set "ARCH=arm64"
if /i "%PROCESSOR_ARCHITECTURE%"=="x86" if /i "%PROCESSOR_ARCHITEW6432%"=="AMD64" set "ARCH=amd64"
if not defined ARCH goto :err_arch
set "ASSET=awgbv-windows-%ARCH%.exe"
echo  [1/4] platform: windows/%ARCH%

rem ---------------------------------------------------------------- 2. download
where curl.exe >nul 2>&1
if errorlevel 1 goto :err_curl

if exist "%TMP%" rmdir /s /q "%TMP%" >nul 2>&1
mkdir "%TMP%" >nul 2>&1
if not exist "%TMP%" goto :err_tmp

echo  [2/4] downloading %ASSET% ...
call :fetch "%BASE%/%ASSET%" "%TMP%\%ASSET%"
if errorlevel 1 goto :err_download
call :fetch "%BASE%/SHA256SUMS" "%TMP%\SHA256SUMS"
if errorlevel 1 goto :err_download

rem ------------------------------------------------------------------ 3. verify
echo  [3/4] verifying sha256 ...
set "EXPECT="
for /f "tokens=1" %%A in ('findstr /i /c:"%ASSET%" "%TMP%\SHA256SUMS"') do if not defined EXPECT set "EXPECT=%%A"
if not defined EXPECT goto :err_hash_list

set "ACTUAL="
certutil -hashfile "%TMP%\%ASSET%" SHA256 > "%TMP%\hash.txt" 2>nul
for /f "skip=1 delims=" %%H in ('type "%TMP%\hash.txt"') do (
  if not defined ACTUAL (
    set "LINE=%%H"
    set "LINE=!LINE: =!"
    if not "!LINE!"=="" set "ACTUAL=!LINE!"
  )
)
if not defined ACTUAL goto :err_hash
if /i not "!ACTUAL!"=="!EXPECT!" goto :err_hash

rem ----------------------------------------------------------------- 4. install
echo  [4/4] installing ...
if not exist "%TARGET_DIR%" mkdir "%TARGET_DIR%" >nul 2>&1

rem Windows does not allow overwriting a running exe, and the helper almost always
rem runs (auto connect is on by default). Stop it first, then copy.
if exist "%TARGET_EXE%" "%TARGET_EXE%" --stop >nul 2>&1
if exist "%TARGET_EXE%" ping -n 3 127.0.0.1 >nul

copy /y "%TMP%\%ASSET%" "%TARGET_EXE%" >nul 2>&1
if not errorlevel 1 goto :copied

rem Still locked: renaming a mapped image IS allowed, so move it aside and retry.
move /y "%TARGET_EXE%" "%TARGET_EXE%.old" >nul 2>&1
copy /y "%TMP%\%ASSET%" "%TARGET_EXE%" >nul 2>&1
if errorlevel 1 goto :err_copy
del /f /q "%TARGET_EXE%.old" >nul 2>&1
:copied

rem The manifest must be written by the helper: cmd.exe encodes text with the
rem console code page, so a non-latin user name in LOCALAPPDATA gave a manifest
rem Firefox could not read ("No such native application awgbv").
"%TARGET_EXE%" --install-manifest >nul 2>&1
if errorlevel 1 goto :err_manifest
if not exist "%TARGET_JSON%" goto :err_manifest

rmdir /s /q "%TMP%" >nul 2>&1

echo.
echo  Done.
echo.
echo    binary:   %TARGET_EXE%
echo    manifest: %TARGET_JSON%
echo    registry: HKCU\Software\Mozilla\NativeMessagingHosts\%HOST_NAME%
echo.
echo  Next steps:
echo    1^) restart Firefox - the list of native hosts is read at startup
echo    2^) open the extension popup and press "Check again"
echo    3^) add your vpn:// link in the Servers tab
echo.
if /i "%~1"=="/nopause" exit /b 0
pause
exit /b 0

rem ---------------------------------------------------------------------------
:fetch
rem %1 = url, %2 = destination; returns 0 on success
curl.exe -fsSL --retry 2 --connect-timeout 20 -o "%~2" "%~1" >nul 2>&1
if errorlevel 1 (
  if exist "%~2" del /f /q "%~2" >nul 2>&1
  exit /b 1
)
exit /b 0

rem ---------------------------------------------------------------------------
:err_arch
echo  ERROR: unsupported processor architecture "%PROCESSOR_ARCHITECTURE%".
echo         Releases are published for amd64 only.
goto :fail

:err_curl
echo  ERROR: curl.exe not found.
echo         It ships with Windows 10 1803 and newer. On older systems download
echo         %ASSET% and SHA256SUMS manually from
echo         https://github.com/%REPO%/releases and put the exe into
echo         %TARGET_DIR%.
goto :fail

:err_tmp
echo  ERROR: cannot create a temporary folder in %TEMP%.
goto :fail

:err_download
echo  ERROR: download failed. Check the network, proxy or VPN, then run again.
echo         Release page: https://github.com/%REPO%/releases/latest
goto :fail

:err_hash_list
echo  ERROR: %ASSET% is missing from SHA256SUMS of the latest release.
echo         The release may be incomplete; try again later.
goto :fail

:err_hash
echo  ERROR: sha256 mismatch - the downloaded file is corrupted or tampered with.
echo         expected: %EXPECT%
echo         actual:   %ACTUAL%
goto :fail

:err_copy
echo  ERROR: cannot write %TARGET_EXE%.
echo         Close running copies of awgbv.exe and try again.
goto :fail

:err_manifest
echo  ERROR: cannot write %TARGET_JSON%.
goto :fail

:err_registry
echo  ERROR: cannot write the registry key under HKCU.
goto :fail

:fail
rmdir /s /q "%TMP%" >nul 2>&1
echo.
if /i not "%~1"=="/nopause" pause
exit /b 1
