#!/usr/bin/env bash
# 交叉编译 cf-yxipd（N1 = aarch64）并打出可安装的 tar.gz
set -euo pipefail
cd "$(dirname "$0")"
export PATH=/vol1/dev/tools/go/bin:$PATH
VER=$(grep -oP 'Version\s+=\s+"\K[^"]+' core/config.go)
echo "== 构建 cf-yxipd $VER (linux/arm64) =="
cd core && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.Version=$VER" -o ../luci-app-cf-yxip/root/usr/bin/cf-yxipd . && cd ..
chmod 755 luci-app-cf-yxip/root/usr/bin/cf-yxipd
echo "== 打包 =="
mkdir -p dist
tar -C luci-app-cf-yxip/root -czf "dist/cf-yxip-${VER}.tar.gz" .
ls -lh dist/
