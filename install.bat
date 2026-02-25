@echo off
:: onWatch Windows Installer
:: Double-click this file or run from Command Prompt
::
:: This batch file launches the PowerShell installer script.
:: For direct PowerShell installation, run:
::   irm https://raw.githubusercontent.com/onllm-dev/onwatch/main/install.ps1 | iex

echo.
echo   onWatch Installer for Windows
echo   ==============================
echo.
echo   Launching PowerShell installer...
echo.

:: Check if PowerShell is available
where powershell >nul 2>&1
if %ERRORLEVEL% neq 0 (
    echo   ERROR: PowerShell is required but not found.
    echo   Please install PowerShell or Windows Management Framework.
    echo.
    pause
    exit /b 1
)

:: Run the PowerShell installer
:: -ExecutionPolicy Bypass allows running the script without changing system policy
:: -NoProfile skips loading the user's PowerShell profile for faster startup
powershell -ExecutionPolicy Bypass -NoProfile -Command "& { irm https://raw.githubusercontent.com/onllm-dev/onwatch/main/install.ps1 | iex }"

:: If PowerShell exits with an error, show a message
if %ERRORLEVEL% neq 0 (
    echo.
    echo   Installation encountered an error.
    echo.
)

pause
