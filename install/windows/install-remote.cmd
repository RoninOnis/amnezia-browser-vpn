@echo off
setlocal
set "HERE=%~dp0"
if not exist "%HERE%install-windows.ps1" (
  echo install-windows.ps1 not found next to this file.
  echo Download both files into the same folder and run this again.
  pause
  exit /b 1
)
echo Installing the awgbv helper for the current user (no admin rights needed)...
powershell -NoProfile -ExecutionPolicy Bypass -File "%HERE%install-windows.ps1" %*
set "RC=%ERRORLEVEL%"
echo.
if not "%RC%"=="0" echo Finished with errors, code %RC%.
pause
exit /b %RC%
