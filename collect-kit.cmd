@echo off
setlocal
REM ============================================================================
REM collect-kit v2 - generic configurable collector (three-stage deliver
REM pipeline, stage 1; recipe plugins carry all repo knowledge)
REM  - no args : compile collect-kit.exe and start the local web wizard
REM              (browser form rendered from the recipe params; actions:
REM              collect / push-images / repush). The web UI (ui/, Vite +
REM              Vue3 + Element Plus) is built automatically on first run
REM              when npm is available; without npm the wizard serves a
REM              built-in hint page (run build.cmd once to build the UI).
REM  - args    : pass straight through to collect-kit.exe subcommands
REM              (collect / repush / push-images / upload-preset /
REM              zip-dir / list-recipes). e.g.
REM              collect-kit.cmd collect --recipe ai-mse --name tenant-a --modules oa,fs --studio http://127.0.0.1:7860 --studio-user admin --studio-pass xxx --package
REM ============================================================================
REM WARNING: keep this file ASCII-only (comments included). With codepage 65001
REM active, cmd.exe mis-tracks line offsets on multi-byte UTF-8 lines and chops
REM later commands mid-line, so any Chinese text here breaks the whole script.
REM chcp 65001 below is ONLY for child processes (collect-kit.exe / powershell)
REM that emit UTF-8 Chinese console output.
set "SCRIPT_DIR=%~dp0"

chcp 65001 >nul

where go >nul 2>nul
if errorlevel 1 (
    if not exist "%SCRIPT_DIR%collect-kit.exe" (
        echo Go is required to compile collect-kit. Install Go and retry.
        goto :done
    )
) else (
    cd /d "%SCRIPT_DIR%"
    rem Build web UI once when missing; rebuild after UI changes via build.cmd
    if not exist "ui\dist\index.html" (
        where npm >nul 2>nul
        if errorlevel 1 (
            echo Web UI not built and npm not found - wizard will show a hint page.
            echo Install node/npm and run again, or run build.cmd once.
        ) else (
            echo Building web UI ^(first run, needs npm^)...
            pushd ui
            call npm install --no-audit --no-fund
            if errorlevel 1 (popd & echo npm install failed & goto :done)
            call npm run build
            if errorlevel 1 (popd & echo UI build failed & goto :done)
            popd
        )
    )
    go build -o collect-kit.exe . || (echo collect-kit build failed & goto :done)
)

if not "%~1"=="" (
    "%SCRIPT_DIR%collect-kit.exe" %*
    goto :done
)

echo Starting the local wizard. A browser page will open ^(via collect-kit.exe^).
echo Close this window to stop the wizard.
"%SCRIPT_DIR%collect-kit.exe" %*

:done
echo.
pause
endlocal
