#!/bin/sh
# collect-kit 完整构建：前端（ui → ui/dist）→ 双平台编译 linux/amd64 + windows/amd64 → bin/（CGO 关闭纯静态）
# 前置：node/npm（前端）+ go。分发形态 = 二进制 + recipes/ 目录（见 README「内置 recipe 定位」）。
set -e
cd "$(dirname "$0")"

echo "==> [1/2] 前端构建（ui → ui/dist，Vite + Vue3 + Element Plus）"
(cd ui && npm install --no-audit --no-fund && npm run build)

echo "==> [2/2] 双平台编译 → bin/"
mkdir -p bin
rm -f bin/collect-kit-linux bin/collect-kit-windows.exe
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/collect-kit-linux .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/collect-kit-windows.exe
echo "完成：bin/collect-kit-linux、bin/collect-kit-windows.exe"
