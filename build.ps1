# collect-kit 完整构建（Windows 原生入口）：前端（ui → ui/dist）→ 双平台编译 linux/amd64 + windows/amd64 → bin/
# 用法：powershell -ExecutionPolicy Bypass -File build.ps1（或双击 build.cmd）
# 前置：node/npm（前端）+ go
# 本文件必须保存为「UTF-8 带 BOM」：Windows PowerShell 5.1 对无 BOM 文件按 ANSI/GBK 解析，中文输出即乱码
$ErrorActionPreference = "Stop"
Set-Location (Split-Path $MyInvocation.MyCommand.Path -Parent)
# 控制台切 UTF-8：go 等原生命令的 UTF-8 输出在默认 GBK 控制台才不乱码；无控制台宿主下静默跳过
try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch {}

Write-Host "==> [1/2] 前端构建（ui → ui/dist，Vite + Vue3 + Element Plus）"
Push-Location ui
npm install --no-audit --no-fund
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "npm install 失败" }
npm run build
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "npm run build 失败" }
Pop-Location

Write-Host "==> [2/2] 双平台编译 → bin/"
New-Item -ItemType Directory -Force -Path bin | Out-Null
Remove-Item bin/collect-kit-linux, bin/collect-kit-windows.exe -ErrorAction SilentlyContinue

$env:CGO_ENABLED = "0"
$env:GOOS = "linux";   $env:GOARCH = "amd64"; go build -trimpath -ldflags="-s -w" -o bin/collect-kit-linux .
$env:GOOS = "windows"; $env:GOARCH = "amd64"; go build -trimpath -ldflags="-s -w" -o bin/collect-kit-windows.exe
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue

Write-Host "完成：bin\collect-kit-linux、bin\collect-kit-windows.exe"
