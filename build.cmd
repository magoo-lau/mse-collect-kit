@echo off
rem collect-kit double-click build entry (pure ASCII: cmd mis-tracks multi-byte UTF-8 lines under chcp 65001)
powershell -ExecutionPolicy Bypass -File "%~dp0build.ps1"
pause
