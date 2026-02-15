@echo off
cd /d "%~dp0"
if exist open-zone.exe (open-zone.exe) else (go run ./cmd/open-zone)
echo.
pause
