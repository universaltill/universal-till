@echo off
REM Universal Till POS - uninstaller (portable Windows build).
REM Double-click this file, or run it from the extracted release folder.
REM It stops a running till, then asks before deleting your shop data
REM (database, plugins, backups). It cannot delete the folder it runs from -
REM remove that last. If you used the installer (.exe), uninstall from
REM "Add or remove programs" instead.
setlocal
cd /d "%~dp0"

set "DATA_DIR=%LOCALAPPDATA%\UniversalTill"

echo Universal Till - uninstall
echo.

REM 1) Stop a running till so nothing is holding the database open.
taskkill /IM unitill-pos.exe /F >nul 2>&1

REM 2) Shop data is precious (sales history, receipts) - never delete silently.
if exist "%DATA_DIR%" (
  echo Your shop data is stored at:
  echo   %DATA_DIR%
  echo This holds your database, installed plugins, and backups.
  echo.
  set /p "REPLY=Delete ALL shop data too? This cannot be undone. [y/N] "
  if /I "%REPLY%"=="y" (
    rmdir /s /q "%DATA_DIR%"
    echo Shop data deleted.
  ) else (
    echo Kept your shop data at %DATA_DIR%
    echo ^(A future install will pick it up again.^)
  )
) else (
  echo No shop data found at %DATA_DIR% - nothing to delete.
)

echo.
echo Almost done. To finish removing the app, delete this folder:
echo   %CD%
echo.
echo Done.
pause
